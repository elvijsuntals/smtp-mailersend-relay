package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	MailerSendPlanTrial        = "trial"
	MailerSendPlanFree         = "free"
	MailerSendPlanHobby        = "hobby"
	MailerSendPlanStarter      = "starter"
	MailerSendPlanProfessional = "professional"
	MailerSendPlanEnterprise   = "enterprise"
)

type MailerSendAccountLimits struct {
	Plan                         string
	BulkAPISupported             bool
	CustomHeadersSupported       bool
	BulkAPIMaxMessagesPerRequest int
	BulkAPIMaxRequestsPerMin     int
}

type Config struct {
	Env      string
	LogLevel string

	SMTPListenAddr           string
	SMTPDomain               string
	SMTPAuthUsername         string
	SMTPAuthPassword         string
	SMTPTLSCertFile          string
	SMTPTLSKeyFile           string
	SMTPAllowInsecureAuth    bool
	SMTPRequireSTARTTLS      bool
	SMTPMaxMessageBytes      int64
	SMTPMaxRecipients        int
	SMTPMaxConnections       int
	SMTPMaxConnectionsPerIP  int
	SMTPRateLimitPerIPPerMin int
	SMTPAllowedSenderDomains []string

	HTTPListenAddr string

	DBPath         string
	DBMaxOpenConns int
	DBMaxIdleConns int

	DispatcherWorkers    int
	QueueClaimLimit      int
	QueueLeaseTimeout    time.Duration
	BatchMaxCount        int
	BatchMaxBytes        int
	BatchFlushInterval   time.Duration
	RetryMaxAttempts     int
	RequeueStaleInterval time.Duration

	MailersendAPIKey                       string
	MailersendBaseURL                      string
	MailersendTimeout                      time.Duration
	MailersendEnableCustomHeaders          bool
	MailersendAccountPlan                  string
	MailersendBulkAPISupported             bool
	MailersendCustomHeadersSupported       bool
	MailersendBulkAPIMaxMessagesPerRequest int
	MailersendBulkAPIMaxRequestsPerMin     int
}

func LoadFromEnv() (Config, error) {
	limits, err := loadMailerSendAccountLimitsFromEnv()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Env:      getenv("APP_ENV", "production"),
		LogLevel: getenv("LOG_LEVEL", "info"),

		SMTPListenAddr:           getenv("SMTP_LISTEN_ADDR", ":2525"),
		SMTPDomain:               getenv("SMTP_DOMAIN", "localhost"),
		SMTPAuthUsername:         getenv("SMTP_AUTH_USERNAME", ""),
		SMTPAuthPassword:         getenv("SMTP_AUTH_PASSWORD", ""),
		SMTPTLSCertFile:          getenv("SMTP_TLS_CERT_FILE", ""),
		SMTPTLSKeyFile:           getenv("SMTP_TLS_KEY_FILE", ""),
		SMTPAllowInsecureAuth:    getenvBool("SMTP_ALLOW_INSECURE_AUTH", false),
		SMTPRequireSTARTTLS:      getenvBool("SMTP_REQUIRE_STARTTLS", true),
		SMTPMaxMessageBytes:      getenvInt64("SMTP_MAX_MESSAGE_BYTES", 8*1024*1024),
		SMTPMaxRecipients:        getenvInt("SMTP_MAX_RECIPIENTS", 1000),
		SMTPMaxConnections:       getenvInt("SMTP_MAX_CONNECTIONS", 200),
		SMTPMaxConnectionsPerIP:  getenvInt("SMTP_MAX_CONNECTIONS_PER_IP", 20),
		SMTPRateLimitPerIPPerMin: getenvInt("SMTP_RATE_LIMIT_PER_IP_PER_MIN", 600),
		SMTPAllowedSenderDomains: splitCSV(getenv("SMTP_ALLOWED_SENDER_DOMAINS", "")),

		HTTPListenAddr: getenv("HTTP_LISTEN_ADDR", ":8080"),

		DBPath:         getenv("SQLITE_PATH", "./data/relay.db"),
		DBMaxOpenConns: getenvInt("SQLITE_MAX_OPEN_CONNS", 1),
		DBMaxIdleConns: getenvInt("SQLITE_MAX_IDLE_CONNS", 1),

		DispatcherWorkers:    getenvInt("DISPATCHER_WORKERS", 4),
		QueueClaimLimit:      getenvInt("QUEUE_CLAIM_LIMIT", 1000),
		QueueLeaseTimeout:    getenvDuration("QUEUE_LEASE_TIMEOUT", 30*time.Second),
		BatchMaxCount:        getenvInt("BATCH_MAX_COUNT", 500),
		BatchMaxBytes:        getenvInt("BATCH_MAX_BYTES", 5*1024*1024),
		BatchFlushInterval:   getenvDuration("BATCH_FLUSH_INTERVAL", 250*time.Millisecond),
		RetryMaxAttempts:     getenvInt("RETRY_MAX_ATTEMPTS", 8),
		RequeueStaleInterval: getenvDuration("REQUEUE_STALE_INTERVAL", 30*time.Second),

		MailersendAPIKey:                       getenv("MAILERSEND_API_KEY", ""),
		MailersendBaseURL:                      getenv("MAILERSEND_BASE_URL", "https://api.mailersend.com/v1"),
		MailersendTimeout:                      getenvDuration("MAILERSEND_TIMEOUT", 20*time.Second),
		MailersendEnableCustomHeaders:          getenvBool("MAILERSEND_ENABLE_CUSTOM_HEADERS", false),
		MailersendAccountPlan:                  limits.Plan,
		MailersendBulkAPISupported:             limits.BulkAPISupported,
		MailersendCustomHeadersSupported:       limits.CustomHeadersSupported,
		MailersendBulkAPIMaxMessagesPerRequest: limits.BulkAPIMaxMessagesPerRequest,
		MailersendBulkAPIMaxRequestsPerMin:     limits.BulkAPIMaxRequestsPerMin,
	}

	if err := cfg.ValidateCommon(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) ValidateCommon() error {
	if c.SMTPMaxMessageBytes <= 0 {
		return fmt.Errorf("SMTP_MAX_MESSAGE_BYTES must be > 0")
	}
	if c.SMTPMaxRecipients <= 0 {
		return fmt.Errorf("SMTP_MAX_RECIPIENTS must be > 0")
	}
	if c.SMTPMaxConnections < 1 {
		return fmt.Errorf("SMTP_MAX_CONNECTIONS must be >= 1")
	}
	if c.SMTPMaxConnectionsPerIP < 1 {
		return fmt.Errorf("SMTP_MAX_CONNECTIONS_PER_IP must be >= 1")
	}
	if c.SMTPRateLimitPerIPPerMin < 1 {
		return fmt.Errorf("SMTP_RATE_LIMIT_PER_IP_PER_MIN must be >= 1")
	}
	if c.DBPath == "" {
		return fmt.Errorf("SQLITE_PATH is required")
	}
	if c.DispatcherWorkers < 1 {
		return fmt.Errorf("DISPATCHER_WORKERS must be >= 1")
	}
	if c.QueueClaimLimit < 1 {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT must be >= 1")
	}
	if c.BatchMaxCount < 1 || c.BatchMaxCount > 500 {
		return fmt.Errorf("BATCH_MAX_COUNT must be in range [1,500]")
	}
	if c.BatchMaxBytes < 1024 {
		return fmt.Errorf("BATCH_MAX_BYTES must be >= 1024")
	}
	if c.BatchFlushInterval <= 0 {
		return fmt.Errorf("BATCH_FLUSH_INTERVAL must be > 0")
	}
	if c.RetryMaxAttempts < 1 {
		return fmt.Errorf("RETRY_MAX_ATTEMPTS must be >= 1")
	}
	if c.QueueLeaseTimeout <= 0 {
		return fmt.Errorf("QUEUE_LEASE_TIMEOUT must be > 0")
	}
	if c.MailersendTimeout <= 0 {
		return fmt.Errorf("MAILERSEND_TIMEOUT must be > 0")
	}
	if c.MailersendBulkAPIMaxMessagesPerRequest < 1 || c.MailersendBulkAPIMaxMessagesPerRequest > 500 {
		return fmt.Errorf("MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST must be in range [1,500]")
	}
	if c.MailersendBulkAPIMaxRequestsPerMin < 1 {
		return fmt.Errorf("MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN must be >= 1")
	}
	return nil
}

func (c Config) ValidateServe() error {
	if c.SMTPAuthUsername == "" || c.SMTPAuthPassword == "" {
		return fmt.Errorf("SMTP_AUTH_USERNAME and SMTP_AUTH_PASSWORD are required")
	}
	if len(c.SMTPAllowedSenderDomains) == 0 {
		return fmt.Errorf("SMTP_ALLOWED_SENDER_DOMAINS must not be empty")
	}
	if c.SMTPRequireSTARTTLS {
		if c.SMTPTLSCertFile == "" || c.SMTPTLSKeyFile == "" {
			return fmt.Errorf("SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE are required when SMTP_REQUIRE_STARTTLS=true")
		}
	}
	if c.MailersendAPIKey == "" {
		return fmt.Errorf("MAILERSEND_API_KEY is required")
	}
	if !c.MailersendBulkAPISupported {
		return fmt.Errorf(
			"configured MAILERSEND_ACCOUNT_PLAN=%q does not support bulk email; this relay is bulk-only and free/trial plans need a future non-bulk sender",
			c.MailersendAccountPlan,
		)
	}
	if c.MailersendEnableCustomHeaders && !c.MailersendCustomHeadersSupported {
		return fmt.Errorf(
			"MAILERSEND_ENABLE_CUSTOM_HEADERS=true requires a MailerSend plan with custom headers support; effective plan is %q",
			c.MailersendAccountPlan,
		)
	}
	return nil
}

func (c Config) EffectiveBatchMaxCount() int {
	if c.BatchMaxCount < c.MailersendBulkAPIMaxMessagesPerRequest {
		return c.BatchMaxCount
	}
	return c.MailersendBulkAPIMaxMessagesPerRequest
}

func loadMailerSendAccountLimitsFromEnv() (MailerSendAccountLimits, error) {
	plan := strings.ToLower(strings.TrimSpace(getenv("MAILERSEND_ACCOUNT_PLAN", MailerSendPlanStarter)))
	limits, err := resolveMailerSendAccountLimits(plan)
	if err != nil {
		return MailerSendAccountLimits{}, err
	}

	if override, ok, err := getenvOptionalBool("MAILERSEND_BULK_API_SUPPORTED"); err != nil {
		return MailerSendAccountLimits{}, err
	} else if ok {
		limits.BulkAPISupported = override
	}
	if override, ok, err := getenvOptionalBool("MAILERSEND_CUSTOM_HEADERS_SUPPORTED"); err != nil {
		return MailerSendAccountLimits{}, err
	} else if ok {
		limits.CustomHeadersSupported = override
	}
	if override, ok, err := getenvOptionalInt("MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST"); err != nil {
		return MailerSendAccountLimits{}, err
	} else if ok {
		limits.BulkAPIMaxMessagesPerRequest = override
	}
	if override, ok, err := getenvOptionalInt("MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN"); err != nil {
		return MailerSendAccountLimits{}, err
	} else if ok {
		limits.BulkAPIMaxRequestsPerMin = override
	}

	return limits, nil
}

func resolveMailerSendAccountLimits(plan string) (MailerSendAccountLimits, error) {
	switch plan {
	case MailerSendPlanTrial, MailerSendPlanFree:
		return MailerSendAccountLimits{
			Plan:                         plan,
			BulkAPISupported:             false,
			CustomHeadersSupported:       false,
			BulkAPIMaxMessagesPerRequest: 500,
			BulkAPIMaxRequestsPerMin:     10,
		}, nil
	case MailerSendPlanHobby, MailerSendPlanStarter:
		return MailerSendAccountLimits{
			Plan:                         plan,
			BulkAPISupported:             true,
			CustomHeadersSupported:       false,
			BulkAPIMaxMessagesPerRequest: 500,
			BulkAPIMaxRequestsPerMin:     10,
		}, nil
	case MailerSendPlanProfessional, MailerSendPlanEnterprise:
		return MailerSendAccountLimits{
			Plan:                         plan,
			BulkAPISupported:             true,
			CustomHeadersSupported:       true,
			BulkAPIMaxMessagesPerRequest: 500,
			BulkAPIMaxRequestsPerMin:     10,
		}, nil
	default:
		return MailerSendAccountLimits{}, fmt.Errorf(
			"MAILERSEND_ACCOUNT_PLAN must be one of %q, %q, %q, %q, %q, %q",
			MailerSendPlanTrial,
			MailerSendPlanFree,
			MailerSendPlanHobby,
			MailerSendPlanStarter,
			MailerSendPlanProfessional,
			MailerSendPlanEnterprise,
		)
	}
}

func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getenvInt64(key string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getenvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getenvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func getenvOptionalBool(key string) (bool, bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false, false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false, fmt.Errorf("%s must be a boolean value", key)
	}
	return b, true, nil
}

func getenvOptionalInt(key string) (int, bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false, fmt.Errorf("%s must be an integer value", key)
	}
	return n, true, nil
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	in := strings.Split(v, ",")
	out := make([]string, 0, len(in))
	for _, part := range in {
		p := strings.ToLower(strings.TrimSpace(part))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
