package queue

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mailersend/mailersend-go"
)

func TestSQLiteStore_EnqueueClaimAndMark(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := OpenSQLite(filepath.Join(dir, "relay.db"), 1, 1, "test-worker")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	msgs := []*mailersend.Message{
		{
			From:       mailersend.From{Email: "sender@example.com"},
			Recipients: []mailersend.Recipient{{Email: "a@example.net"}},
			Subject:    "hello",
			Text:       "body",
		},
		{
			From:       mailersend.From{Email: "sender@example.com"},
			Recipients: []mailersend.Recipient{{Email: "b@example.net"}},
			Subject:    "hello2",
			Text:       "body2",
		},
	}

	ids, err := store.EnqueueTx(ctx, "sender@example.com", msgs)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d", len(ids))
	}

	jobs, err := store.ClaimBatch(ctx, time.Now().UTC(), 100, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 claimed jobs, got %d", len(jobs))
	}

	if err := store.MarkSent(ctx, jobs[:1], "bulk-123"); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := store.MarkDLQ(ctx, jobs[1:], "permanent", 422); err != nil {
		t.Fatalf("mark dlq: %v", err)
	}

	counts, err := store.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[StatusSent] != 1 {
		t.Fatalf("expected sent=1, got %d", counts[StatusSent])
	}
	if counts[StatusDLQ] != 1 {
		t.Fatalf("expected dlq=1, got %d", counts[StatusDLQ])
	}
}

func TestSQLiteStore_RequeueStaleProcessing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := OpenSQLite(filepath.Join(dir, "relay.db"), 1, 1, "test-worker")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	msg := &mailersend.Message{
		From:       mailersend.From{Email: "sender@example.com"},
		Recipients: []mailersend.Recipient{{Email: "x@example.net"}},
		Subject:    "hello",
		Text:       "body",
	}
	if _, err := store.EnqueueTx(ctx, "sender@example.com", []*mailersend.Message{msg}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	jobs, err := store.ClaimBatch(ctx, time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 claimed job, got %d", len(jobs))
	}

	// Force stale timestamp.
	_, err = store.db.ExecContext(ctx, `UPDATE jobs SET processing_started_at=? WHERE id=?`, time.Now().UTC().Add(-1*time.Hour), jobs[0].ID)
	if err != nil {
		t.Fatalf("force stale: %v", err)
	}

	n, err := store.RequeueStaleProcessing(ctx, time.Now().UTC(), 30*time.Second)
	if err != nil {
		t.Fatalf("requeue stale: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected one requeued stale job, got %d", n)
	}
}

func TestSQLiteStore_ClaimDispatchBatchHonorsBatchLimits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := OpenSQLite(filepath.Join(dir, "relay.db"), 1, 1, "test-worker")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	msgs := []*mailersend.Message{
		{
			From:       mailersend.From{Email: "sender@example.com"},
			Recipients: []mailersend.Recipient{{Email: "a@example.net"}},
			Subject:    "hello",
			Text:       "body",
		},
		{
			From:       mailersend.From{Email: "sender@example.com"},
			Recipients: []mailersend.Recipient{{Email: "b@example.net"}},
			Subject:    "hello2",
			Text:       "body2",
		},
	}
	if _, err := store.EnqueueTx(ctx, "sender@example.com", msgs); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	jobs, err := store.ClaimDispatchBatch(ctx, time.Now().UTC(), 100, 30*time.Second, 1, 5*1024*1024)
	if err != nil {
		t.Fatalf("claim dispatch batch: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 claimed job, got %d", len(jobs))
	}
	if jobs[0].RecipientEmail != "a@example.net" {
		t.Fatalf("first claimed recipient = %q, want a@example.net", jobs[0].RecipientEmail)
	}

	if err := store.MarkSent(ctx, jobs, "bulk-1"); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	remaining, err := store.ClaimDispatchBatch(ctx, time.Now().UTC(), 100, 30*time.Second, 1, 5*1024*1024)
	if err != nil {
		t.Fatalf("claim remaining batch: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining claimed job, got %d", len(remaining))
	}
	if remaining[0].RecipientEmail != "b@example.net" {
		t.Fatalf("second claimed recipient = %q, want b@example.net", remaining[0].RecipientEmail)
	}
}

func TestSQLiteStore_RetryNowMakesRetryJobsEligibleImmediately(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := OpenSQLite(filepath.Join(dir, "relay.db"), 1, 1, "test-worker")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	msg := &mailersend.Message{
		From:       mailersend.From{Email: "sender@example.com"},
		Recipients: []mailersend.Recipient{{Email: "a@example.net"}},
		Subject:    "hello",
		Text:       "body",
	}
	if _, err := store.EnqueueTx(ctx, "sender@example.com", []*mailersend.Message{msg}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	jobs, err := store.ClaimBatch(ctx, time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 claimed job, got %d", len(jobs))
	}

	next := time.Now().UTC().Add(1 * time.Hour)
	if err := store.MarkRetry(ctx, jobs, next, "retry later", 429); err != nil {
		t.Fatalf("mark retry: %v", err)
	}

	eligible, err := store.ClaimBatch(ctx, time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim before retry-now: %v", err)
	}
	if len(eligible) != 0 {
		t.Fatalf("expected no eligible retry jobs before retry-now, got %d", len(eligible))
	}

	n, err := store.RetryNow(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("retry now: %v", err)
	}
	if n != 1 {
		t.Fatalf("RetryNow updated %d rows, want 1", n)
	}

	eligible, err = store.ClaimBatch(ctx, time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim after retry-now: %v", err)
	}
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible retry job after retry-now, got %d", len(eligible))
	}
}
