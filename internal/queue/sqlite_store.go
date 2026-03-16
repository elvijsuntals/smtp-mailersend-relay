package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mailersend/mailersend-go"
	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db       *sql.DB
	workerID string
}

func OpenSQLite(path string, maxOpenConns, maxIdleConns int, workerID string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	// WAL mode and busy timeout help write-heavy queue workloads.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)

	return &SQLiteStore{db: db, workerID: workerID}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *SQLiteStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	envelope_from TEXT NOT NULL,
	recipient_email TEXT NOT NULL,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	next_attempt_at DATETIME NOT NULL,
	last_error TEXT,
	last_http_status INTEGER,
	bulk_email_id TEXT,
	worker_id TEXT,
	processing_started_at DATETIME,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_status_next_attempt_at ON jobs (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_jobs_status_processing_started_at ON jobs (status, processing_started_at);
CREATE INDEX IF NOT EXISTS idx_jobs_recipient_created_at ON jobs (recipient_email, created_at);
`)
	return err
}

func (s *SQLiteStore) EnqueueTx(ctx context.Context, envelopeFrom string, messages []*mailersend.Message) ([]string, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin enqueue tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO jobs (
			id, status, payload_json, envelope_from, recipient_email, attempt_count,
			next_attempt_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare enqueue: %w", err)
	}
	defer stmt.Close()

	ids := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg == nil || len(msg.Recipients) == 0 {
			return nil, fmt.Errorf("message must have one recipient")
		}
		id := ulid.Make().String()
		payload, err := json.Marshal(msg)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		recipient := strings.ToLower(strings.TrimSpace(msg.Recipients[0].Email))
		if _, err := stmt.ExecContext(
			ctx,
			id,
			StatusQueued,
			string(payload),
			strings.ToLower(strings.TrimSpace(envelopeFrom)),
			recipient,
			now,
			now,
			now,
		); err != nil {
			return nil, fmt.Errorf("insert job: %w", err)
		}
		ids = append(ids, id)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit enqueue tx: %w", err)
	}
	return ids, nil
}

func (s *SQLiteStore) ClaimBatch(ctx context.Context, now time.Time, limit int, _ time.Duration) ([]Job, error) {
	return s.claimJobs(ctx, now, limit, func(_ int, _ int, _ int) bool {
		return false
	})
}

func (s *SQLiteStore) ClaimDispatchBatch(ctx context.Context, now time.Time, limit int, _ time.Duration, maxCount int, maxBytes int) ([]Job, error) {
	if maxCount <= 0 {
		maxCount = 500
	}
	if maxBytes <= 0 {
		maxBytes = 5 * 1024 * 1024
	}
	return s.claimJobs(ctx, now, limit, func(batchCount int, batchBytes int, nextJobBytes int) bool {
		return batchCount > 0 && (batchCount >= maxCount || batchBytes+nextJobBytes > maxBytes)
	})
}

func (s *SQLiteStore) claimJobs(ctx context.Context, now time.Time, limit int, shouldStop func(batchCount int, batchBytes int, nextJobBytes int) bool) ([]Job, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ids := make([]string, 0, limit)
	rows, err := tx.QueryContext(ctx, `
SELECT id, payload_json
FROM jobs
WHERE status IN (?, ?)
  AND next_attempt_at <= ?
ORDER BY created_at ASC
LIMIT ?`,
		StatusQueued, StatusRetry, now.UTC(), limit,
	)
	if err != nil {
		return nil, err
	}
	batchBytes := 0
	for rows.Next() {
		var id string
		var payloadJSON string
		if err := rows.Scan(&id, &payloadJSON); err != nil {
			rows.Close()
			return nil, err
		}
		nextJobBytes := len(payloadJSON) + 128
		if shouldStop(len(ids), batchBytes, nextJobBytes) {
			break
		}
		ids = append(ids, id)
		batchBytes += nextJobBytes
	}
	rows.Close()
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	updateSQL := fmt.Sprintf(`
UPDATE jobs
SET status=?, worker_id=?, processing_started_at=?, updated_at=?
WHERE status IN (?, ?)
  AND next_attempt_at <= ?
  AND id IN (%s)`, placeholders(len(ids)))
	args := make([]any, 0, 7+len(ids))
	args = append(args,
		StatusProcessing,
		s.workerID,
		now.UTC(),
		now.UTC(),
		StatusQueued,
		StatusRetry,
		now.UTC(),
	)
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, updateSQL, args...); err != nil {
		return nil, err
	}

	selectSQL := fmt.Sprintf(`
SELECT id, status, payload_json, envelope_from, recipient_email, attempt_count, next_attempt_at,
       COALESCE(last_error, ''), COALESCE(last_http_status, 0), COALESCE(bulk_email_id, ''),
       COALESCE(worker_id, ''), processing_started_at, created_at, updated_at
FROM jobs
WHERE status=?
  AND worker_id=?
  AND id IN (%s)
ORDER BY created_at ASC`, placeholders(len(ids)))
	selectArgs := make([]any, 0, 2+len(ids))
	selectArgs = append(selectArgs, StatusProcessing, s.workerID)
	for _, id := range ids {
		selectArgs = append(selectArgs, id)
	}

	claimedRows, err := tx.QueryContext(ctx, selectSQL, selectArgs...)
	if err != nil {
		return nil, err
	}
	defer claimedRows.Close()

	var out []Job
	for claimedRows.Next() {
		var j Job
		var processingStarted any
		var nextAttemptRaw any
		var createdAtRaw any
		var updatedAtRaw any
		if err := claimedRows.Scan(
			&j.ID, &j.Status, &j.PayloadJSON, &j.EnvelopeFrom, &j.RecipientEmail,
			&j.AttemptCount, &nextAttemptRaw, &j.LastError, &j.LastHTTPStatus,
			&j.BulkEmailID, &j.WorkerID, &processingStarted, &createdAtRaw, &updatedAtRaw,
		); err != nil {
			return nil, err
		}
		j.NextAttemptAt = decodeSQLTime(nextAttemptRaw)
		j.CreatedAt = decodeSQLTime(createdAtRaw)
		j.UpdatedAt = decodeSQLTime(updatedAtRaw)
		if ps := decodeOptionalSQLTime(processingStarted); ps != nil {
			j.ProcessingStarted = ps
		}
		var msg mailersend.Message
		if err := json.Unmarshal([]byte(j.PayloadJSON), &msg); err != nil {
			return nil, fmt.Errorf("unmarshal payload for job %s: %w", j.ID, err)
		}
		j.Message = &msg
		out = append(out, j)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLiteStore) MarkSent(ctx context.Context, jobs []Job, bulkEmailID string) error {
	if len(jobs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
UPDATE jobs
SET status=?, attempt_count=attempt_count+1, bulk_email_id=?, last_error=NULL, last_http_status=NULL,
    worker_id=NULL, processing_started_at=NULL, updated_at=?
WHERE id=? AND status=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, j := range jobs {
		if _, err := stmt.ExecContext(ctx, StatusSent, bulkEmailID, now, j.ID, StatusProcessing); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) MarkRetry(ctx context.Context, jobs []Job, nextAttemptAt time.Time, errMsg string, httpStatus int) error {
	if len(jobs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
UPDATE jobs
SET status=?, attempt_count=attempt_count+1, next_attempt_at=?, last_error=?, last_http_status=?,
    worker_id=NULL, processing_started_at=NULL, updated_at=?
WHERE id=? AND status=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, j := range jobs {
		if _, err := stmt.ExecContext(ctx, StatusRetry, nextAttemptAt.UTC(), errMsg, httpStatus, now, j.ID, StatusProcessing); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) MarkDLQ(ctx context.Context, jobs []Job, errMsg string, httpStatus int) error {
	if len(jobs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
UPDATE jobs
SET status=?, attempt_count=attempt_count+1, last_error=?, last_http_status=?,
    worker_id=NULL, processing_started_at=NULL, updated_at=?
WHERE id=? AND status=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, j := range jobs {
		if _, err := stmt.ExecContext(ctx, StatusDLQ, errMsg, httpStatus, now, j.ID, StatusProcessing); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) RequeueStaleProcessing(ctx context.Context, now time.Time, leaseTimeout time.Duration) (int64, error) {
	cutoff := now.UTC().Add(-leaseTimeout)
	res, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status=?, next_attempt_at=?, worker_id=NULL, processing_started_at=NULL, updated_at=?
WHERE status=? AND processing_started_at IS NOT NULL AND processing_started_at < ?`,
		StatusRetry, now.UTC(), now.UTC(), StatusProcessing, cutoff,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) CountByStatus(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{
		StatusQueued:     0,
		StatusProcessing: 0,
		StatusRetry:      0,
		StatusSent:       0,
		StatusDLQ:        0,
	}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, nil
}

func (s *SQLiteStore) ListDLQ(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, status, payload_json, envelope_from, recipient_email, attempt_count, next_attempt_at,
       COALESCE(last_error, ''), COALESCE(last_http_status, 0), COALESCE(bulk_email_id, ''),
       COALESCE(worker_id, ''), processing_started_at, created_at, updated_at
FROM jobs
WHERE status=?
ORDER BY updated_at DESC
LIMIT ?`, StatusDLQ, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *SQLiteStore) GetDLQByIDs(ctx context.Context, ids []string) ([]Job, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(`
SELECT id, status, payload_json, envelope_from, recipient_email, attempt_count, next_attempt_at,
       COALESCE(last_error, ''), COALESCE(last_http_status, 0), COALESCE(bulk_email_id, ''),
       COALESCE(worker_id, ''), processing_started_at, created_at, updated_at
FROM jobs
WHERE status=? AND id IN (%s)`, placeholders(len(ids)))
	args := make([]any, 0, 1+len(ids))
	args = append(args, StatusDLQ)
	for _, id := range ids {
		args = append(args, strings.TrimSpace(id))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *SQLiteStore) RequeueDLQIDs(ctx context.Context, ids []string, now time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q := fmt.Sprintf(`
UPDATE jobs
SET status=?, next_attempt_at=?, last_error=NULL, last_http_status=NULL,
    worker_id=NULL, processing_started_at=NULL, updated_at=?
WHERE status=? AND id IN (%s)`, placeholders(len(ids)))
	args := make([]any, 0, 4+len(ids))
	args = append(args, StatusRetry, now.UTC(), now.UTC(), StatusDLQ)
	for _, id := range ids {
		args = append(args, strings.TrimSpace(id))
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) RetryNow(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET next_attempt_at=?, updated_at=?
WHERE status=?`,
		now.UTC(), now.UTC(), StatusRetry,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	var out []Job
	for rows.Next() {
		var j Job
		var processingStarted any
		var nextAttemptRaw any
		var createdAtRaw any
		var updatedAtRaw any
		if err := rows.Scan(
			&j.ID, &j.Status, &j.PayloadJSON, &j.EnvelopeFrom, &j.RecipientEmail,
			&j.AttemptCount, &nextAttemptRaw, &j.LastError, &j.LastHTTPStatus,
			&j.BulkEmailID, &j.WorkerID, &processingStarted, &createdAtRaw, &updatedAtRaw,
		); err != nil {
			return nil, err
		}
		j.NextAttemptAt = decodeSQLTime(nextAttemptRaw)
		j.CreatedAt = decodeSQLTime(createdAtRaw)
		j.UpdatedAt = decodeSQLTime(updatedAtRaw)
		if ps := decodeOptionalSQLTime(processingStarted); ps != nil {
			j.ProcessingStarted = ps
		}
		var msg mailersend.Message
		if err := json.Unmarshal([]byte(j.PayloadJSON), &msg); err == nil {
			j.Message = &msg
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("?")
	}
	return b.String()
}

func decodeOptionalSQLTime(v any) *time.Time {
	if v == nil {
		return nil
	}
	t := decodeSQLTime(v)
	return &t
}

func decodeSQLTime(v any) time.Time {
	switch tv := v.(type) {
	case time.Time:
		return tv.UTC()
	case string:
		return parseTimeOrNow(tv)
	case []byte:
		return parseTimeOrNow(string(tv))
	case int64:
		return time.Unix(tv, 0).UTC()
	case float64:
		return time.Unix(int64(tv), 0).UTC()
	default:
		return time.Now().UTC()
	}
}

func parseTimeOrNow(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Now().UTC()
	}
	// SQLite may persist textual timestamps in different formats.
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0).UTC()
	}
	return time.Now().UTC()
}
