package dispatch

import (
	"context"
	"fmt"
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
	client *mailersend.Mailersend
}

func NewMailerSendSender(apiKey, baseURL string, timeout time.Duration) (*MailerSendSender, error) {
	ms := mailersend.NewMailersend(apiKey)

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
	ms.SetClient(httpClient)

	return &MailerSendSender{client: ms}, nil
}

func (s *MailerSendSender) Send(ctx context.Context, messages []*mailersend.Message) (SendResult, error) {
	resp, apiResp, err := s.client.BulkEmail.Send(ctx, messages)
	if err != nil {
		status := 0
		if apiResp != nil && apiResp.Response != nil {
			status = apiResp.StatusCode
		}
		return SendResult{HTTPStatus: status}, &SenderError{
			StatusCode: status,
			Err:        err,
		}
	}

	status := 0
	if apiResp != nil && apiResp.Response != nil {
		status = apiResp.StatusCode
	}
	return SendResult{
		BulkEmailID: resp.BulkEmailID,
		HTTPStatus:  status,
	}, nil
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

