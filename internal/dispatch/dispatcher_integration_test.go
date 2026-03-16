package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mailersend/mailersend-go"
	"go.uber.org/zap"

	"smtp-mailersend-relay/internal/metrics"
	"smtp-mailersend-relay/internal/queue"
)

func TestDispatcher_PermanentFailureToDLQ(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	defer store.Close()

	msg := &mailersend.Message{
		From:       mailersend.From{Email: "sender@example.com"},
		Recipients: []mailersend.Recipient{{Email: "a@example.net"}},
		Subject:    "subj",
		Text:       "body",
	}
	if _, err := store.EnqueueTx(context.Background(), "sender@example.com", []*mailersend.Message{msg}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	jobs, err := store.ClaimBatch(context.Background(), time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	d := New(Config{
		Workers:             1,
		QueueClaimLimit:     10,
		QueueLeaseTimeout:   30 * time.Second,
		BatchMaxCount:       500,
		BatchMaxBytes:       5 * 1024 * 1024,
		BatchFlushInterval:  100 * time.Millisecond,
		RetryMaxAttempts:    8,
		RequeueStaleEvery:   1 * time.Minute,
		QueueDepthPollEvery: 1 * time.Minute,
	}, zap.NewNop(), metrics.New(), store, &fakeSender{
		result: SendResult{HTTPStatus: 422},
		err:    &SenderError{StatusCode: 422, Err: errors.New("validation")},
	}, nil)

	if err := d.processBatch(context.Background(), jobs); err != nil {
		t.Fatalf("process batch: %v", err)
	}
	counts, err := store.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[queue.StatusDLQ] != 1 {
		t.Fatalf("expected one dlq item, got %d", counts[queue.StatusDLQ])
	}
}

func TestDispatcher_RetryThenSuccess(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	defer store.Close()

	msg := &mailersend.Message{
		From:       mailersend.From{Email: "sender@example.com"},
		Recipients: []mailersend.Recipient{{Email: "a@example.net"}},
		Subject:    "subj",
		Text:       "body",
	}
	if _, err := store.EnqueueTx(context.Background(), "sender@example.com", []*mailersend.Message{msg}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	jobs, err := store.ClaimBatch(context.Background(), time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim1: %v", err)
	}
	sender := &fakeSender{
		result: SendResult{HTTPStatus: 429},
		err:    &SenderError{StatusCode: 429, Err: errors.New("rate limited")},
	}

	d := New(Config{
		Workers:             1,
		QueueClaimLimit:     10,
		QueueLeaseTimeout:   30 * time.Second,
		BatchMaxCount:       500,
		BatchMaxBytes:       5 * 1024 * 1024,
		BatchFlushInterval:  100 * time.Millisecond,
		RetryMaxAttempts:    8,
		RequeueStaleEvery:   1 * time.Minute,
		QueueDepthPollEvery: 1 * time.Minute,
	}, zap.NewNop(), metrics.New(), store, sender, nil)

	if err := d.processBatch(context.Background(), jobs); err != nil {
		t.Fatalf("process retry batch: %v", err)
	}

	// Force claiming retries immediately by asking with a far-future "now".
	retryJobs, err := store.ClaimBatch(context.Background(), time.Now().UTC().Add(24*time.Hour), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim2: %v", err)
	}
	if len(retryJobs) != 1 {
		t.Fatalf("expected one retry job, got %d", len(retryJobs))
	}

	sender.result = SendResult{HTTPStatus: 202, BulkEmailID: "bulk-1"}
	sender.err = nil
	if err := d.processBatch(context.Background(), retryJobs); err != nil {
		t.Fatalf("process success batch: %v", err)
	}

	counts, err := store.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[queue.StatusSent] != 1 {
		t.Fatalf("expected sent=1, got %d", counts[queue.StatusSent])
	}
}

func newTestStore(t *testing.T) *queue.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	store, err := queue.OpenSQLite(filepath.Join(dir, "relay.db"), 1, 1, "dispatch-test")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

type fakeSender struct {
	result SendResult
	err    error
}

func (f *fakeSender) Send(_ context.Context, _ []*mailersend.Message) (SendResult, error) {
	return f.result, f.err
}
