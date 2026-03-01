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

