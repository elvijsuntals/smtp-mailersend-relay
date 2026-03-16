package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mailersend/mailersend-go"

	"smtp-mailersend-relay/internal/queue"
)

func TestRunQueueRetryNowMakesRetryJobsEligible(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "relay.db")
	store, err := queue.OpenSQLite(dbPath, 1, 1, "test-worker")
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
	if err := store.MarkRetry(ctx, jobs, time.Now().UTC().Add(time.Hour), "retry later", 429); err != nil {
		t.Fatalf("mark retry: %v", err)
	}

	t.Setenv("SQLITE_PATH", dbPath)
	t.Setenv("MAILERSEND_ACCOUNT_PLAN", "starter")

	if err := run([]string{"queue", "retry-now"}); err != nil {
		t.Fatalf("run queue retry-now: %v", err)
	}

	eligible, err := store.ClaimBatch(ctx, time.Now().UTC(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("claim after retry-now: %v", err)
	}
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible retry job after queue retry-now, got %d", len(eligible))
	}
}
