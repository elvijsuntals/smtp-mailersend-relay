package config

import "testing"

func TestLoadFromEnv_DefaultMailerSendPlanIsStarter(t *testing.T) {
	t.Setenv("MAILERSEND_ACCOUNT_PLAN", "")
	t.Setenv("MAILERSEND_BULK_API_SUPPORTED", "")
	t.Setenv("MAILERSEND_CUSTOM_HEADERS_SUPPORTED", "")
	t.Setenv("MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST", "")
	t.Setenv("MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if cfg.MailersendAccountPlan != MailerSendPlanStarter {
		t.Fatalf("default plan = %q, want %q", cfg.MailersendAccountPlan, MailerSendPlanStarter)
	}
	if !cfg.MailersendBulkAPISupported {
		t.Fatalf("starter plan should support bulk api")
	}
	if cfg.MailersendCustomHeadersSupported {
		t.Fatalf("starter plan should not support custom headers")
	}
	if cfg.MailersendBulkAPIMaxMessagesPerRequest != 500 {
		t.Fatalf("starter max messages = %d, want 500", cfg.MailersendBulkAPIMaxMessagesPerRequest)
	}
	if cfg.MailersendBulkAPIMaxRequestsPerMin != 10 {
		t.Fatalf("starter max requests per minute = %d, want 10", cfg.MailersendBulkAPIMaxRequestsPerMin)
	}
}

func TestValidateServe_RejectsUnsupportedBulkPlans(t *testing.T) {
	for _, plan := range []string{MailerSendPlanFree, MailerSendPlanTrial} {
		t.Run(plan, func(t *testing.T) {
			t.Setenv("MAILERSEND_ACCOUNT_PLAN", plan)
			cfg := mustLoadServeConfig(t)

			if err := cfg.ValidateServe(); err == nil {
				t.Fatalf("ValidateServe() succeeded for unsupported plan %q", plan)
			}
		})
	}
}

func TestValidateServe_CustomHeadersSupportDependsOnPlan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plan    string
		wantErr bool
	}{
		{name: "starter rejects", plan: MailerSendPlanStarter, wantErr: true},
		{name: "hobby rejects", plan: MailerSendPlanHobby, wantErr: true},
		{name: "professional allows", plan: MailerSendPlanProfessional, wantErr: false},
		{name: "enterprise allows", plan: MailerSendPlanEnterprise, wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAILERSEND_ACCOUNT_PLAN", tc.plan)
			t.Setenv("MAILERSEND_ENABLE_CUSTOM_HEADERS", "true")
			cfg := mustLoadServeConfig(t)

			err := cfg.ValidateServe()
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateServe() succeeded for plan %q", tc.plan)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateServe() error for plan %q: %v", tc.plan, err)
			}
		})
	}
}

func TestLoadFromEnv_MailerSendOverridesTakePrecedence(t *testing.T) {
	t.Setenv("MAILERSEND_ACCOUNT_PLAN", MailerSendPlanStarter)
	t.Setenv("MAILERSEND_BULK_API_SUPPORTED", "true")
	t.Setenv("MAILERSEND_CUSTOM_HEADERS_SUPPORTED", "true")
	t.Setenv("MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST", "123")
	t.Setenv("MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN", "7")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if !cfg.MailersendCustomHeadersSupported {
		t.Fatalf("custom header override was not applied")
	}
	if cfg.MailersendBulkAPIMaxMessagesPerRequest != 123 {
		t.Fatalf("max messages override = %d, want 123", cfg.MailersendBulkAPIMaxMessagesPerRequest)
	}
	if cfg.MailersendBulkAPIMaxRequestsPerMin != 7 {
		t.Fatalf("max requests override = %d, want 7", cfg.MailersendBulkAPIMaxRequestsPerMin)
	}
}

func TestLoadFromEnv_InvalidMailerSendOverrideRangesFail(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "messages too high", key: "MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST", value: "501"},
		{name: "requests too low", key: "MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN", value: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadFromEnv(); err == nil {
				t.Fatalf("LoadFromEnv() succeeded for %s=%s", tc.key, tc.value)
			}
		})
	}
}

func TestEffectiveBatchMaxCount_UsesMailerSendLimit(t *testing.T) {
	cfg := Config{
		BatchMaxCount:                          500,
		MailersendBulkAPIMaxMessagesPerRequest: 250,
	}

	if got := cfg.EffectiveBatchMaxCount(); got != 250 {
		t.Fatalf("EffectiveBatchMaxCount() = %d, want 250", got)
	}
}

func mustLoadServeConfig(t *testing.T) Config {
	t.Helper()
	t.Setenv("SMTP_AUTH_USERNAME", "relay-user")
	t.Setenv("SMTP_AUTH_PASSWORD", "relay-pass")
	t.Setenv("SMTP_ALLOWED_SENDER_DOMAINS", "example.com")
	t.Setenv("SMTP_REQUIRE_STARTTLS", "false")
	t.Setenv("MAILERSEND_API_KEY", "key")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	return cfg
}
