package mimeparse

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/mailersend/mailersend-go"
)

type Transformer struct {
}

func NewTransformer() *Transformer {
	return &Transformer{}
}

func (t *Transformer) Transform(envelopeFrom string, recipients []string, raw []byte) ([]*mailersend.Message, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("missing recipients")
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse rfc822 message: %w", err)
	}

	fromName, fromEmail, err := selectFromAddress(envelopeFrom, m.Header.Get("From"))
	if err != nil {
		return nil, err
	}
	replyTo := parseOptionalAddress(m.Header.Get("Reply-To"))
	subject := m.Header.Get("Subject")
	inReplyTo := strings.TrimSpace(m.Header.Get("In-Reply-To"))
	references := parseReferences(m.Header.Get("References"))
	headers := collectHeaders(m.Header)

	bodyBytes, err := io.ReadAll(m.Body)
	if err != nil {
		return nil, fmt.Errorf("read message body: %w", err)
	}

	content, err := parseContent(textproto.MIMEHeader(m.Header), bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse mime content: %w", err)
	}

	out := make([]*mailersend.Message, 0, len(recipients))
	for _, rcpt := range recipients {
		addr, err := mail.ParseAddress(rcpt)
		if err != nil {
			return nil, fmt.Errorf("invalid rcpt address %q: %w", rcpt, err)
		}

		msg := &mailersend.Message{
			From:       mailersend.From{Name: fromName, Email: fromEmail},
			Recipients: []mailersend.Recipient{{Name: addr.Name, Email: addr.Address}},
			Subject:    subject,
			Text:       content.Text,
			HTML:       content.HTML,
			Headers:    copyHeaders(headers),
			References: copyStrings(references),
		}
		if replyTo != nil {
			msg.ReplyTo = mailersend.ReplyTo{Name: replyTo.Name, Email: replyTo.Email}
		}
		if inReplyTo != "" {
			msg.InReplyTo = inReplyTo
		}
		if len(content.Attachments) > 0 {
			msg.Attachments = copyAttachments(content.Attachments)
		}
		out = append(out, msg)
	}
	return out, nil
}

type ParsedAddress struct {
	Name  string
	Email string
}

type parsedContent struct {
	Text        string
	HTML        string
	Attachments []mailersend.Attachment
}

func parseContent(header textproto.MIMEHeader, body []byte) (*parsedContent, error) {
	out := &parsedContent{}

	contentType := header.Get("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		contentType = "text/plain; charset=utf-8"
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(mediaType)

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil, fmt.Errorf("multipart part missing boundary")
		}
		mr := multipart.NewReader(bytes.NewReader(body), boundary)
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			partBytes, err := io.ReadAll(p)
			if err != nil {
				_ = p.Close()
				return nil, err
			}
			partHeader := p.Header
			_ = p.Close()

			partContent, err := parseContent(partHeader, partBytes)
			if err != nil {
				return nil, err
			}

			appendParsed(out, partContent)
		}
		return out, nil
	}

	disposition, dispParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := detectFilename(header, dispParams)
	if filename != "" || strings.EqualFold(disposition, "attachment") {
		out.Attachments = append(out.Attachments, buildAttachment(header, body, filename, disposition))
		return out, nil
	}

	switch mediaType {
	case "text/plain":
		text := strings.TrimSpace(string(body))
		if text != "" {
			out.Text = text
		}
	case "text/html":
		html := strings.TrimSpace(string(body))
		if html != "" {
			out.HTML = html
		}
	default:
		if filename != "" {
			out.Attachments = append(out.Attachments, buildAttachment(header, body, filename, disposition))
		}
	}
	return out, nil
}

func appendParsed(dst, src *parsedContent) {
	if dst.Text == "" {
		dst.Text = src.Text
	} else if src.Text != "" && src.Text != dst.Text {
		dst.Text = dst.Text + "\n" + src.Text
	}

	if dst.HTML == "" {
		dst.HTML = src.HTML
	} else if src.HTML != "" && src.HTML != dst.HTML {
		dst.HTML = dst.HTML + "\n" + src.HTML
	}

	if len(src.Attachments) > 0 {
		dst.Attachments = append(dst.Attachments, src.Attachments...)
	}
}

func buildAttachment(header textproto.MIMEHeader, body []byte, filename, disposition string) mailersend.Attachment {
	if filename == "" {
		filename = "attachment.bin"
	}
	if disposition == "" {
		disposition = mailersend.DispositionAttachment
	}

	contentID := strings.TrimSpace(header.Get("Content-ID"))
	contentID = strings.TrimPrefix(contentID, "<")
	contentID = strings.TrimSuffix(contentID, ">")

	return mailersend.Attachment{
		Content:     base64.StdEncoding.EncodeToString(body),
		Filename:    filename,
		Disposition: disposition,
		ID:          contentID,
	}
}

func detectFilename(header textproto.MIMEHeader, dispParams map[string]string) string {
	if fn := strings.TrimSpace(dispParams["filename"]); fn != "" {
		return fn
	}
	_, ctParams, _ := mime.ParseMediaType(header.Get("Content-Type"))
	if fn := strings.TrimSpace(ctParams["name"]); fn != "" {
		return fn
	}
	return ""
}

func selectFromAddress(envelopeFrom, fromHeader string) (string, string, error) {
	envAddr, err := mail.ParseAddress(envelopeFrom)
	if err != nil {
		return "", "", fmt.Errorf("invalid envelope sender: %w", err)
	}
	if strings.TrimSpace(fromHeader) == "" {
		return envAddr.Name, envAddr.Address, nil
	}
	hdrAddr, err := mail.ParseAddress(fromHeader)
	if err != nil {
		return envAddr.Name, envAddr.Address, nil
	}
	if strings.EqualFold(hdrAddr.Address, envAddr.Address) {
		return hdrAddr.Name, hdrAddr.Address, nil
	}
	return envAddr.Name, envAddr.Address, nil
}

func parseOptionalAddress(v string) *ParsedAddress {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	addr, err := mail.ParseAddress(v)
	if err != nil {
		return nil
	}
	return &ParsedAddress{Name: addr.Name, Email: addr.Address}
}

func parseReferences(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Fields(v)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func collectHeaders(h mail.Header) []mailersend.Header {
	if len(h) == 0 {
		return nil
	}
	skip := map[string]struct{}{
		"from":                      {},
		"to":                        {},
		"cc":                        {},
		"bcc":                       {},
		"subject":                   {},
		"reply-to":                  {},
		"content-type":              {},
		"content-transfer-encoding": {},
		"mime-version":              {},
		"date":                      {},
	}

	out := make([]mailersend.Header, 0, len(h))
	for k, values := range h {
		if _, ok := skip[strings.ToLower(k)]; ok {
			continue
		}
		for _, v := range values {
			out = append(out, mailersend.Header{Name: k, Value: v})
		}
	}
	return out
}

func copyHeaders(in []mailersend.Header) []mailersend.Header {
	if len(in) == 0 {
		return nil
	}
	out := make([]mailersend.Header, len(in))
	copy(out, in)
	return out
}

func copyAttachments(in []mailersend.Attachment) []mailersend.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]mailersend.Attachment, len(in))
	copy(out, in)
	return out
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
