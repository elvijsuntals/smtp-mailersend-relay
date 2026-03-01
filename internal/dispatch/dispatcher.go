package dispatch

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mailersend/mailersend-go"
	"go.uber.org/zap"

	"smtp-mailersend-relay/internal/metrics"
	"smtp-mailersend-relay/internal/queue"
)

type Config struct {
	Workers             int
	QueueClaimLimit     int
	QueueLeaseTimeout   time.Duration
	BatchMaxCount       int
	BatchMaxBytes       int
	BatchFlushInterval  time.Duration
	RetryMaxAttempts    int
	RequeueStaleEvery   time.Duration
	QueueDepthPollEvery time.Duration
}

type Dispatcher struct {
	cfg     Config
	logger  *zap.Logger
	metrics *metrics.Metrics
	store   queue.QueueStore
	sender  BulkSender

	wg            sync.WaitGroup
	lastHeartbeat atomic.Int64
}

func New(cfg Config, logger *zap.Logger, m *metrics.Metrics, store queue.QueueStore, sender BulkSender) *Dispatcher {
	return &Dispatcher{
		cfg:     cfg,
		logger:  logger,
		metrics: m,
		store:   store,
		sender:  sender,
	}
}

func (d *Dispatcher) Start(ctx context.Context) {
	for i := 0; i < d.cfg.Workers; i++ {
		d.wg.Add(1)
		go func(workerIdx int) {
			defer d.wg.Done()
			d.workerLoop(ctx, workerIdx)
		}(i)
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.requeueStaleLoop(ctx)
	}()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.queueDepthLoop(ctx)
	}()
}

func (d *Dispatcher) Wait() {
	d.wg.Wait()
}

func (d *Dispatcher) Ready() bool {
	last := d.lastHeartbeat.Load()
	if last == 0 {
		return false
	}
	maxAge := 3 * d.cfg.BatchFlushInterval
	if maxAge < 5*time.Second {
		maxAge = 5 * time.Second
	}
	return time.Since(time.Unix(last, 0)) <= maxAge
}

func (d *Dispatcher) workerLoop(ctx context.Context, workerIdx int) {
	idle := time.NewTicker(d.cfg.BatchFlushInterval)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		d.lastHeartbeat.Store(time.Now().UTC().Unix())

		now := time.Now().UTC()
		jobs, err := d.store.ClaimBatch(ctx, now, d.cfg.QueueClaimLimit, d.cfg.QueueLeaseTimeout)
		if err != nil {
			d.logger.Error("claim batch failed", zap.Int("worker", workerIdx), zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}
		if len(jobs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-idle.C:
				continue
			}
		}

		for _, batch := range splitBatches(jobs, d.cfg.BatchMaxCount, d.cfg.BatchMaxBytes) {
			if err := d.processBatch(ctx, batch); err != nil {
				d.logger.Error("process batch failed", zap.Int("worker", workerIdx), zap.Error(err))
			}
		}
	}
}

func splitBatches(jobs []queue.Job, maxCount int, maxBytes int) [][]queue.Job {
	if len(jobs) == 0 {
		return nil
	}
	if maxCount <= 0 {
		maxCount = 500
	}
	if maxBytes <= 0 {
		maxBytes = 5 * 1024 * 1024
	}

	var out [][]queue.Job
	cur := make([]queue.Job, 0, maxCount)
	curBytes := 0
	for _, j := range jobs {
		est := len(j.PayloadJSON) + 128
		if len(cur) > 0 && (len(cur) >= maxCount || curBytes+est > maxBytes) {
			out = append(out, cur)
			cur = make([]queue.Job, 0, maxCount)
			curBytes = 0
		}
		cur = append(cur, j)
		curBytes += est
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (d *Dispatcher) processBatch(ctx context.Context, jobs []queue.Job) error {
	if len(jobs) == 0 {
		return nil
	}
	messages := make([]*mailersend.Message, 0, len(jobs))
	for _, j := range jobs {
		messages = append(messages, j.Message)
	}

	start := time.Now()
	result, err := d.sender.Send(ctx, messages)
	lat := time.Since(start)
	d.metrics.DispatchBatchSize.Observe(float64(len(jobs)))
	d.metrics.DispatchLatencySeconds.Observe(lat.Seconds())

	statusCode := result.HTTPStatus
	if statusCode == 0 {
		if se, ok := err.(*SenderError); ok {
			statusCode = se.StatusCode
		}
	}
	statusLabel := strconv.Itoa(statusCode)
	if statusCode == 0 {
		statusLabel = "network"
	}

	if err == nil {
		d.metrics.DispatchAPICalls.WithLabelValues("success", statusLabel).Inc()
		d.metrics.DispatchLastSuccessUnix.Store(time.Now().UTC().Unix())
		return d.store.MarkSent(ctx, jobs, result.BulkEmailID)
	}

	class := classifyFailure(statusCode, err)
	d.metrics.DispatchAPICalls.WithLabelValues(class, statusLabel).Inc()

	switch class {
	case "retry":
		var retryJobs []queue.Job
		var dlqJobs []queue.Job
		for _, j := range jobs {
			if j.AttemptCount+1 >= d.cfg.RetryMaxAttempts {
				dlqJobs = append(dlqJobs, j)
			} else {
				retryJobs = append(retryJobs, j)
			}
		}
		if len(retryJobs) > 0 {
			next := time.Now().UTC().Add(jitteredBackoff(retryJobs[0].AttemptCount + 1))
			if markErr := d.store.MarkRetry(ctx, retryJobs, next, safeErr(err), statusCode); markErr != nil {
				return fmt.Errorf("mark retry: %w", markErr)
			}
			d.metrics.DispatchRetries.Add(float64(len(retryJobs)))
		}
		if len(dlqJobs) > 0 {
			if markErr := d.store.MarkDLQ(ctx, dlqJobs, safeErr(err), statusCode); markErr != nil {
				return fmt.Errorf("mark dlq: %w", markErr)
			}
			d.metrics.DispatchDLQ.Add(float64(len(dlqJobs)))
		}
		return nil
	case "dlq":
		if markErr := d.store.MarkDLQ(ctx, jobs, safeErr(err), statusCode); markErr != nil {
			return fmt.Errorf("mark dlq: %w", markErr)
		}
		d.metrics.DispatchDLQ.Add(float64(len(jobs)))
		return nil
	default:
		return d.store.MarkDLQ(ctx, jobs, safeErr(err), statusCode)
	}
}

func (d *Dispatcher) requeueStaleLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.RequeueStaleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := d.store.RequeueStaleProcessing(ctx, time.Now().UTC(), d.cfg.QueueLeaseTimeout)
			if err != nil {
				d.logger.Error("requeue stale processing failed", zap.Error(err))
				continue
			}
			if n > 0 {
				d.metrics.RequeueStaleRecoveries.Add(float64(n))
				d.logger.Warn("requeued stale processing jobs", zap.Int64("count", n))
			}
		}
	}
}

func (d *Dispatcher) queueDepthLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.QueueDepthPollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			counts, err := d.store.CountByStatus(ctx)
			if err != nil {
				d.logger.Error("count queue status failed", zap.Error(err))
				continue
			}
			for status, count := range counts {
				d.metrics.QueueDepth.WithLabelValues(status).Set(float64(count))
			}
		}
	}
}

func classifyFailure(statusCode int, err error) string {
	if err == nil {
		return "success"
	}
	if statusCode == 0 {
		return "retry"
	}
	if statusCode == 408 || statusCode == 425 || statusCode == 429 || statusCode >= 500 {
		return "retry"
	}

	switch statusCode {
	case 400, 401, 403, 404, 405, 409, 410, 413, 422:
		return "dlq"
	}
	if statusCode >= 400 && statusCode < 500 {
		return "dlq"
	}
	return "retry"
}

func safeErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 1024 {
		return msg[:1024]
	}
	return msg
}

var backoffSchedule = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	15 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	1 * time.Hour,
	6 * time.Hour,
}

func jitteredBackoff(attemptNumber int) time.Duration {
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	idx := attemptNumber - 1
	if idx >= len(backoffSchedule) {
		idx = len(backoffSchedule) - 1
	}
	base := backoffSchedule[idx]
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(base) * jitter)
}

