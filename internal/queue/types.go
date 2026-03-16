package queue

import (
	"context"
	"time"

	"github.com/mailersend/mailersend-go"
)

const (
	StatusQueued     = "queued"
	StatusProcessing = "processing"
	StatusRetry      = "retry"
	StatusSent       = "sent"
	StatusDLQ        = "dlq"
)

type Job struct {
	ID                string
	Status            string
	EnvelopeFrom      string
	RecipientEmail    string
	PayloadJSON       string
	Message           *mailersend.Message
	AttemptCount      int
	NextAttemptAt     time.Time
	LastError         string
	LastHTTPStatus    int
	BulkEmailID       string
	WorkerID          string
	ProcessingStarted *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type QueueStore interface {
	Migrate(ctx context.Context) error
	EnqueueTx(ctx context.Context, envelopeFrom string, messages []*mailersend.Message) ([]string, error)
	ClaimBatch(ctx context.Context, now time.Time, limit int, leaseTimeout time.Duration) ([]Job, error)
	ClaimDispatchBatch(ctx context.Context, now time.Time, limit int, leaseTimeout time.Duration, maxCount int, maxBytes int) ([]Job, error)
	MarkSent(ctx context.Context, jobs []Job, bulkEmailID string) error
	MarkRetry(ctx context.Context, jobs []Job, nextAttemptAt time.Time, errMsg string, httpStatus int) error
	MarkDLQ(ctx context.Context, jobs []Job, errMsg string, httpStatus int) error
	RequeueStaleProcessing(ctx context.Context, now time.Time, leaseTimeout time.Duration) (int64, error)
	CountByStatus(ctx context.Context) (map[string]int64, error)
	PingContext(ctx context.Context) error
	ListDLQ(ctx context.Context, limit int) ([]Job, error)
	GetDLQByIDs(ctx context.Context, ids []string) ([]Job, error)
	RequeueDLQIDs(ctx context.Context, ids []string, now time.Time) (int64, error)
	Close() error
}
