package smtpserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/mailersend/mailersend-go"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"smtp-mailersend-relay/internal/config"
	"smtp-mailersend-relay/internal/metrics"
	"smtp-mailersend-relay/internal/queue"
)

type Transformer interface {
	Transform(envelopeFrom string, recipients []string, raw []byte) ([]*mailersend.Message, error)
}

type Server struct {
	cfg      config.Config
	logger   *zap.Logger
	metrics  *metrics.Metrics
	store    queue.QueueStore
	smtpSrv  *smtp.Server
	backend  *Backend
	stopOnce sync.Once
}

func New(cfg config.Config, logger *zap.Logger, m *metrics.Metrics, store queue.QueueStore, transformer Transformer) (*Server, error) {
	backend := NewBackend(cfg, logger, m, store, transformer)

	srv := smtp.NewServer(backend)
	srv.Addr = cfg.SMTPListenAddr
	srv.Domain = cfg.SMTPDomain
	srv.MaxMessageBytes = cfg.SMTPMaxMessageBytes
	srv.MaxRecipients = cfg.SMTPMaxRecipients
	srv.AllowInsecureAuth = cfg.SMTPAllowInsecureAuth
	srv.ReadTimeout = 60 * time.Second
	srv.WriteTimeout = 60 * time.Second

	if cfg.SMTPRequireSTARTTLS {
		cert, err := tls.LoadX509KeyPair(cfg.SMTPTLSCertFile, cfg.SMTPTLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load smtp tls cert/key: %w", err)
		}
		srv.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
	}

	return &Server{
		cfg:     cfg,
		logger:  logger,
		metrics: m,
		store:   store,
		smtpSrv: srv,
		backend: backend,
	}, nil
}

func (s *Server) Start() error {
	s.logger.Info("smtp server listening", zap.String("addr", s.cfg.SMTPListenAddr))
	return s.smtpSrv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.stopOnce.Do(func() {
		err = s.smtpSrv.Shutdown(ctx)
	})
	return err
}

type Backend struct {
	cfg         config.Config
	logger      *zap.Logger
	metrics     *metrics.Metrics
	store       queue.QueueStore
	transformer Transformer

	allowedDomains map[string]struct{}

	connTokens chan struct{}

	mu           sync.Mutex
	perIPConn    map[string]int
	perIPLimiter map[string]*rate.Limiter
}

func NewBackend(cfg config.Config, logger *zap.Logger, m *metrics.Metrics, store queue.QueueStore, transformer Transformer) *Backend {
	allowed := make(map[string]struct{}, len(cfg.SMTPAllowedSenderDomains))
	for _, d := range cfg.SMTPAllowedSenderDomains {
		allowed[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
	}
	return &Backend{
		cfg:            cfg,
		logger:         logger,
		metrics:        m,
		store:          store,
		transformer:    transformer,
		allowedDomains: allowed,
		connTokens:     make(chan struct{}, cfg.SMTPMaxConnections),
		perIPConn:      make(map[string]int),
		perIPLimiter:   make(map[string]*rate.Limiter),
	}
}

func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	ip := remoteIP(c.Conn().RemoteAddr())

	if !b.acquire(ip) {
		b.logger.Warn("smtp connection rejected: capacity/rate limit", zap.String("remote_ip", ip))
		return nil, &smtp.SMTPError{
			Code:         421,
			EnhancedCode: smtp.EnhancedCode{4, 7, 0},
			Message:      "Too busy, try again later",
		}
	}

	s := &Session{
		backend: b,
		conn:    c,
		ip:      ip,
	}
	return s, nil
}

func (b *Backend) acquire(ip string) bool {
	select {
	case b.connTokens <- struct{}{}:
	default:
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	limiter, ok := b.perIPLimiter[ip]
	if !ok {
		eventsPerMin := b.cfg.SMTPRateLimitPerIPPerMin
		limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(eventsPerMin)), eventsPerMin)
		b.perIPLimiter[ip] = limiter
	}
	if !limiter.Allow() {
		<-b.connTokens
		return false
	}

	cur := b.perIPConn[ip]
	if cur >= b.cfg.SMTPMaxConnectionsPerIP {
		<-b.connTokens
		return false
	}
	b.perIPConn[ip] = cur + 1
	return true
}

func (b *Backend) release(ip string) {
	select {
	case <-b.connTokens:
	default:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := b.perIPConn[ip]; n > 1 {
		b.perIPConn[ip] = n - 1
	} else {
		delete(b.perIPConn, ip)
	}
}

type Session struct {
	backend       *Backend
	conn          *smtp.Conn
	ip            string
	authed        bool
	envelopeFrom  string
	recipients    []string
	releasedToken bool
}

func (s *Session) AuthMechanisms() []string {
	return []string{sasl.Plain}
}

func (s *Session) Auth(mech string) (sasl.Server, error) {
	if strings.ToUpper(strings.TrimSpace(mech)) != sasl.Plain {
		s.backend.logger.Warn("smtp auth rejected: unsupported mechanism",
			zap.String("remote_ip", s.ip),
			zap.String("mechanism", mech),
		)
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username != s.backend.cfg.SMTPAuthUsername || password != s.backend.cfg.SMTPAuthPassword {
			s.backend.metrics.SMTPAuthFailures.Inc()
			s.backend.metrics.SMTPRejectedMessages.WithLabelValues("auth_failed").Inc()
			s.backend.logger.Warn("smtp auth failed",
				zap.String("remote_ip", s.ip),
				zap.String("username", username),
			)
			return smtp.ErrAuthFailed
		}
		s.authed = true
		s.backend.logger.Info("smtp auth succeeded",
			zap.String("remote_ip", s.ip),
			zap.String("username", username),
		)
		return nil
	}), nil
}

func (s *Session) Mail(from string, _ *smtp.MailOptions) error {
	if s.backend.cfg.SMTPRequireSTARTTLS {
		if _, ok := s.conn.TLSConnectionState(); !ok {
			s.backend.metrics.SMTPRejectedMessages.WithLabelValues("starttls_required").Inc()
			s.backend.logger.Warn("smtp mail rejected: starttls required",
				zap.String("remote_ip", s.ip),
			)
			return &smtp.SMTPError{
				Code:         530,
				EnhancedCode: smtp.EnhancedCode{5, 7, 0},
				Message:      "Must issue STARTTLS first",
			}
		}
	}
	if !s.authed {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("auth_required").Inc()
		s.backend.logger.Warn("smtp mail rejected: auth required",
			zap.String("remote_ip", s.ip),
		)
		return &smtp.SMTPError{
			Code:         530,
			EnhancedCode: smtp.EnhancedCode{5, 7, 0},
			Message:      "Authentication required",
		}
	}

	addr, err := mail.ParseAddress(from)
	if err != nil {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("invalid_sender").Inc()
		s.backend.logger.Warn("smtp mail rejected: invalid sender",
			zap.String("remote_ip", s.ip),
			zap.String("mail_from", from),
		)
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 1, 7},
			Message:      "Invalid MAIL FROM address",
		}
	}
	domain := senderDomain(addr.Address)
	if _, ok := s.backend.allowedDomains[domain]; !ok {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("sender_domain_not_allowed").Inc()
		s.backend.logger.Warn("smtp mail rejected: sender domain not allowed",
			zap.String("remote_ip", s.ip),
			zap.String("sender_domain", domain),
		)
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 7, 1},
			Message:      "Sender domain not allowed",
		}
	}

	s.envelopeFrom = addr.Address
	s.recipients = s.recipients[:0]
	return nil
}

func (s *Session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !s.authed {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("auth_required").Inc()
		s.backend.logger.Warn("smtp rcpt rejected: auth required",
			zap.String("remote_ip", s.ip),
		)
		return smtp.ErrAuthRequired
	}
	if s.envelopeFrom == "" {
		s.backend.logger.Warn("smtp rcpt rejected: missing mail from",
			zap.String("remote_ip", s.ip),
		)
		return &smtp.SMTPError{
			Code:         503,
			EnhancedCode: smtp.EnhancedCode{5, 5, 1},
			Message:      "Need MAIL FROM first",
		}
	}
	addr, err := mail.ParseAddress(to)
	if err != nil {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("invalid_recipient").Inc()
		s.backend.logger.Warn("smtp rcpt rejected: invalid recipient",
			zap.String("remote_ip", s.ip),
			zap.String("rcpt_to", to),
		)
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 1, 3},
			Message:      "Invalid RCPT TO address",
		}
	}
	s.recipients = append(s.recipients, addr.Address)
	return nil
}

func (s *Session) Data(r io.Reader) error {
	if !s.authed {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("auth_required").Inc()
		s.backend.logger.Warn("smtp data rejected: auth required",
			zap.String("remote_ip", s.ip),
		)
		return smtp.ErrAuthRequired
	}
	if s.envelopeFrom == "" || len(s.recipients) == 0 {
		s.backend.logger.Warn("smtp data rejected: missing envelope or recipients",
			zap.String("remote_ip", s.ip),
		)
		return &smtp.SMTPError{
			Code:         503,
			EnhancedCode: smtp.EnhancedCode{5, 5, 1},
			Message:      "Need MAIL FROM and RCPT TO first",
		}
	}

	limited := io.LimitReader(r, s.backend.cfg.SMTPMaxMessageBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("read_error").Inc()
		s.backend.logger.Warn("smtp data rejected: read error",
			zap.String("remote_ip", s.ip),
			zap.Error(err),
		)
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 3, 0},
			Message:      "Read message failed",
		}
	}
	if int64(len(raw)) > s.backend.cfg.SMTPMaxMessageBytes {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("message_too_large").Inc()
		s.backend.logger.Warn("smtp data rejected: message too large",
			zap.String("remote_ip", s.ip),
			zap.Int("message_bytes", len(raw)),
			zap.Int64("max_message_bytes", s.backend.cfg.SMTPMaxMessageBytes),
		)
		return &smtp.SMTPError{
			Code:         552,
			EnhancedCode: smtp.EnhancedCode{5, 3, 4},
			Message:      "Message too large",
		}
	}

	msgs, err := s.backend.transformer.Transform(s.envelopeFrom, s.recipients, raw)
	if err != nil {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("mime_parse_error").Inc()
		s.backend.logger.Warn("smtp data rejected: mime parse error",
			zap.String("remote_ip", s.ip),
			zap.String("sender_domain", senderDomain(s.envelopeFrom)),
			zap.Int("recipient_count", len(s.recipients)),
			zap.Error(err),
		)
		return &smtp.SMTPError{
			Code:         554,
			EnhancedCode: smtp.EnhancedCode{5, 6, 0},
			Message:      "Unable to parse message",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobIDs, err := s.backend.store.EnqueueTx(ctx, s.envelopeFrom, msgs)
	if err != nil {
		s.backend.metrics.SMTPRejectedMessages.WithLabelValues("queue_error").Inc()
		s.backend.logger.Error("smtp enqueue failed",
			zap.String("remote_ip", s.ip),
			zap.String("sender_domain", senderDomain(s.envelopeFrom)),
			zap.Int("recipient_count", len(s.recipients)),
			zap.Error(err),
		)
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 3, 0},
			Message:      "Temporary queue failure",
		}
	}
	s.backend.metrics.SMTPAcceptedRecipients.Add(float64(len(msgs)))
	s.backend.logger.Info("smtp message accepted and queued",
		zap.String("remote_ip", s.ip),
		zap.String("sender_domain", senderDomain(s.envelopeFrom)),
		zap.Int("recipient_count", len(msgs)),
		zap.Int("message_bytes", len(raw)),
		zap.Int("queued_jobs", len(jobIDs)),
	)
	return nil
}

func (s *Session) Reset() {
	s.envelopeFrom = ""
	s.recipients = nil
}

func (s *Session) Logout() error {
	if !s.releasedToken {
		s.backend.release(s.ip)
		s.releasedToken = true
	}
	return nil
}

func senderDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[at+1:]))
}

func remoteIP(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
