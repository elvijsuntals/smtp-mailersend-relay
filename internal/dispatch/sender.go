package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mailersend/mailersend-go"
)

type SendResult struct {
	BulkEmailID string
	HTTPStatus  int
}

type BulkSender interface {
	Send(ctx context.Context, messages []*mailersend.Message) (SendResult, error)
}

type SenderError struct {
	StatusCode int
	Err        error
}

func (e *SenderError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("status=%d: %v", e.StatusCode, e.Err)
	}
	return e.Err.Error()
}

func (e *SenderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type MailerSendSender struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
}

func NewMailerSendSender(apiKey, baseURL string, timeout time.Duration) (*MailerSendSender, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse MAILERSEND_BASE_URL: %w", err)
	}
	transport := http.DefaultTransport
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &baseURLRewriteTransport{
			baseURL: parsed,
			next:    transport,
		},
	}

	return &MailerSendSender{
		httpClient: httpClient,
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    strings.TrimRight(parsed.String(), "/"),
	}, nil
}

func (s *MailerSendSender) Send(ctx context.Context, messages []*mailersend.Message) (SendResult, error) {
	payload, err := json.Marshal(messages)
	if err != nil {
		return SendResult{}, &SenderError{
			StatusCode: 0,
			Err:        fmt.Errorf("marshal bulk payload: %w", err),
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/bulk-email", bytes.NewReader(payload))
	if err != nil {
		return SendResult{}, &SenderError{
			StatusCode: 0,
			Err:        fmt.Errorf("create bulk request: %w", err),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return SendResult{HTTPStatus: 0}, &SenderError{
			StatusCode: 0,
			Err:        err,
		}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return SendResult{HTTPStatus: resp.StatusCode}, &SenderError{
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("read bulk response: %w", readErr),
		}
	}

	result := SendResult{HTTPStatus: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		var okBody struct {
			BulkEmailID string `json:"bulk_email_id"`
		}
		// MailerSend should return JSON on success; if not, treat 2xx as accepted.
		if len(body) > 0 {
			_ = json.Unmarshal(body, &okBody)
		}
		result.BulkEmailID = okBody.BulkEmailID
		return result, nil
	}

	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	if len(msg) > 400 {
		msg = msg[:400]
	}
	return result, &SenderError{
		StatusCode: resp.StatusCode,
		Err:        fmt.Errorf("mailersend bulk api error: %s", msg),
	}
}

type baseURLRewriteTransport struct {
	baseURL *url.URL
	next    http.RoundTripper
}

func (t *baseURLRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.baseURL.Scheme
	clone.URL.Host = t.baseURL.Host
	if t.baseURL.Path != "" && !strings.HasPrefix(clone.URL.Path, t.baseURL.Path) {
		clone.URL.Path = strings.TrimSuffix(t.baseURL.Path, "/") + "/" + strings.TrimPrefix(clone.URL.Path, "/")
	}
	return t.next.RoundTrip(clone)
}
