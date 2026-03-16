package dispatch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mailersend/mailersend-go"
	"go.uber.org/zap"

	"smtp-mailersend-relay/internal/metrics"
	"smtp-mailersend-relay/internal/queue"
)

func TestDispatcherWorker_WaitsOnLimiterBeforeClaimingBatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	job := queue.Job{
		ID:          "job-1",
		PayloadJSON: `{"subject":"hello"}`,
		Message: &mailersend.Message{
			From:       mailersend.From{Email: "sender@example.com"},
			Recipients: []mailersend.Recipient{{Email: "a@example.net"}},
			Subject:    "hello",
			Text:       "body",
		},
	}
	store := &workerTestStore{
		claimResult: []queue.Job{job},
		markSentCh:  make(chan struct{}, 1),
	}
	limiter := &blockingLimiter{releaseCh: make(chan struct{})}
	sender := &countingSender{result: SendResult{HTTPStatus: 202, BulkEmailID: "bulk-1"}}

	d := New(Config{
		Workers:             1,
		QueueClaimLimit:     10,
		QueueLeaseTimeout:   30 * time.Second,
		BatchMaxCount:       1,
		BatchMaxBytes:       5 * 1024 * 1024,
		BatchFlushInterval:  10 * time.Millisecond,
		RetryMaxAttempts:    8,
		RequeueStaleEvery:   time.Minute,
		QueueDepthPollEvery: time.Minute,
	}, zap.NewNop(), metrics.New(), store, sender, limiter)

	go d.workerLoop(ctx, 0)

	time.Sleep(100 * time.Millisecond)
	if got := store.claimCalls.Load(); got != 0 {
		t.Fatalf("ClaimDispatchBatch called %d times before limiter release, want 0", got)
	}
	if got := sender.calls.Load(); got != 0 {
		t.Fatalf("sender called %d times before limiter release, want 0", got)
	}

	close(limiter.releaseCh)

	select {
	case <-store.markSentCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker to send claimed batch")
	}
	cancel()

	if got := store.claimCalls.Load(); got == 0 {
		t.Fatal("ClaimDispatchBatch was not called after limiter release")
	}
	if got := sender.calls.Load(); got != 1 {
		t.Fatalf("sender called %d times after limiter release, want 1", got)
	}
}

type blockingLimiter struct {
	releaseCh chan struct{}
}

func (l *blockingLimiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.releaseCh:
		return nil
	}
}

type countingSender struct {
	result SendResult
	err    error
	calls  atomic.Int32
}

func (s *countingSender) Send(_ context.Context, _ []*mailersend.Message) (SendResult, error) {
	s.calls.Add(1)
	return s.result, s.err
}

type workerTestStore struct {
	claimResult []queue.Job
	markSentCh  chan struct{}
	claimCalls  atomic.Int32
}

func (s *workerTestStore) Migrate(context.Context) error { return nil }

func (s *workerTestStore) EnqueueTx(context.Context, string, []*mailersend.Message) ([]string, error) {
	return nil, nil
}

func (s *workerTestStore) ClaimBatch(context.Context, time.Time, int, time.Duration) ([]queue.Job, error) {
	return nil, nil
}

func (s *workerTestStore) ClaimDispatchBatch(context.Context, time.Time, int, time.Duration, int, int) ([]queue.Job, error) {
	s.claimCalls.Add(1)
	if len(s.claimResult) == 0 {
		return nil, nil
	}
	jobs := s.claimResult
	s.claimResult = nil
	return jobs, nil
}

func (s *workerTestStore) MarkSent(_ context.Context, _ []queue.Job, _ string) error {
	select {
	case s.markSentCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *workerTestStore) MarkRetry(context.Context, []queue.Job, time.Time, string, int) error {
	return nil
}

func (s *workerTestStore) MarkDLQ(context.Context, []queue.Job, string, int) error {
	return nil
}

func (s *workerTestStore) RequeueStaleProcessing(context.Context, time.Time, time.Duration) (int64, error) {
	return 0, nil
}

func (s *workerTestStore) CountByStatus(context.Context) (map[string]int64, error) {
	return map[string]int64{
		queue.StatusQueued:     0,
		queue.StatusProcessing: 0,
		queue.StatusRetry:      0,
		queue.StatusSent:       0,
		queue.StatusDLQ:        0,
	}, nil
}

func (s *workerTestStore) PingContext(context.Context) error { return nil }

func (s *workerTestStore) ListDLQ(context.Context, int) ([]queue.Job, error) { return nil, nil }

func (s *workerTestStore) GetDLQByIDs(context.Context, []string) ([]queue.Job, error) {
	return nil, nil
}

func (s *workerTestStore) RequeueDLQIDs(context.Context, []string, time.Time) (int64, error) {
	return 0, nil
}

func (s *workerTestStore) Close() error { return nil }
