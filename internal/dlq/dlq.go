package dlq

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"smtp-mailersend-relay/internal/queue"
)

type Service struct {
	store queue.QueueStore
}

func New(store queue.QueueStore) *Service {
	return &Service{store: store}
}

type ExportRecord struct {
	ID             string    `json:"id"`
	EnvelopeFrom   string    `json:"envelope_from"`
	RecipientEmail string    `json:"recipient_email"`
	AttemptCount   int       `json:"attempt_count"`
	LastError      string    `json:"last_error"`
	LastHTTPStatus int       `json:"last_http_status"`
	UpdatedAt      time.Time `json:"updated_at"`
	PayloadJSON    string    `json:"payload_json"`
}

func (s *Service) ExportJSONL(ctx context.Context, outPath string, limit int) (int, error) {
	rows, err := s.store.ListDLQ(ctx, limit)
	if err != nil {
		return 0, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("create export file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	for _, j := range rows {
		record := ExportRecord{
			ID:             j.ID,
			EnvelopeFrom:   j.EnvelopeFrom,
			RecipientEmail: j.RecipientEmail,
			AttemptCount:   j.AttemptCount,
			LastError:      j.LastError,
			LastHTTPStatus: j.LastHTTPStatus,
			UpdatedAt:      j.UpdatedAt,
			PayloadJSON:    j.PayloadJSON,
		}
		b, err := json.Marshal(record)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (s *Service) ReplayIDs(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return s.store.RequeueDLQIDs(ctx, ids, time.Now().UTC())
}

func ReadIDsFromFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var ids []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "{") {
			var rec ExportRecord
			if err := json.Unmarshal([]byte(line), &rec); err == nil && strings.TrimSpace(rec.ID) != "" {
				ids = append(ids, strings.TrimSpace(rec.ID))
				continue
			}
		}
		ids = append(ids, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

