package mimeparse

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestTransformer_BasicMultipart(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		"From: Sender <sender@example.com>",
		"To: a@example.net",
		"Subject: Example",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=BOUNDARY",
		"",
		"--BOUNDARY",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello plain",
		"--BOUNDARY",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>hello html</p>",
		"--BOUNDARY",
		"Content-Type: text/plain; name=notes.txt",
		"Content-Disposition: attachment; filename=notes.txt",
		"",
		"attachment body",
		"--BOUNDARY--",
		"",
	}, "\r\n")

	tr := NewTransformer(false)
	msgs, err := tr.Transform("sender@example.com", []string{"rcpt1@example.net", "rcpt2@example.net"}, []byte(raw))
	if err != nil {
		t.Fatalf("transform failed: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	got := msgs[0]
	if got.Subject != "Example" {
		t.Fatalf("subject mismatch: %q", got.Subject)
	}
	if got.Text == "" || !strings.Contains(got.Text, "hello plain") {
		t.Fatalf("missing text body")
	}
	if got.HTML == "" || !strings.Contains(got.HTML, "hello html") {
		t.Fatalf("missing html body")
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(got.Attachments))
	}

	attBody, err := base64.StdEncoding.DecodeString(got.Attachments[0].Content)
	if err != nil {
		t.Fatalf("invalid attachment base64: %v", err)
	}
	if string(attBody) != "attachment body" {
		t.Fatalf("attachment content mismatch: %q", string(attBody))
	}
}

func TestTransformer_DecodesQuotedPrintableHTML(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		"From: Sender <sender@example.com>",
		"To: rcpt@example.net",
		"Subject: QP test",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"<body style=3D\"background:#fff\"><img src=3D\"https://example.com/logo.png?a=3Db\"></body>",
		"",
	}, "\r\n")

	tr := NewTransformer(false)
	msgs, err := tr.Transform("sender@example.com", []string{"rcpt@example.net"}, []byte(raw))
	if err != nil {
		t.Fatalf("transform failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}

	html := msgs[0].HTML
	if strings.Contains(html, "=3D") {
		t.Fatalf("quoted-printable artifacts still present in html: %q", html)
	}
	if !strings.Contains(html, `style="background:#fff"`) {
		t.Fatalf("missing decoded html style attribute: %q", html)
	}
	if !strings.Contains(html, "a=b") {
		t.Fatalf("missing decoded query parameter: %q", html)
	}
}
