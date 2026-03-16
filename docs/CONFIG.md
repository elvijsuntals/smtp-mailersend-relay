# Configuration Reference

All configuration is environment-variable based.

## Required for `relay serve`

- `SMTP_AUTH_USERNAME`
- `SMTP_AUTH_PASSWORD`
- `SMTP_ALLOWED_SENDER_DOMAINS`
- `MAILERSEND_API_KEY`
- `SMTP_TLS_CERT_FILE` + `SMTP_TLS_KEY_FILE` when `SMTP_REQUIRE_STARTTLS=true`

## Defaults

See [README](../README.md) for full variable list and defaults.

## Recommended production values

- `SMTP_REQUIRE_STARTTLS=true`
- `SMTP_ALLOW_INSECURE_AUTH=false`
- `BATCH_MAX_COUNT=500`
- `BATCH_MAX_BYTES=5242880`
- `QUEUE_LEASE_TIMEOUT=30s`
- `RETRY_MAX_ATTEMPTS=8`
- `SQLITE_MAX_OPEN_CONNS=1`
- `MAILERSEND_ACCOUNT_PLAN=starter`

## MailerSend account limits

The relay resolves MailerSend bulk capabilities from `.env`.

- `MAILERSEND_ACCOUNT_PLAN` supports: `trial`, `free`, `hobby`, `starter`, `professional`, `enterprise`
- Default plan is `starter`
- `trial` and `free` are rejected by `relay serve` because this project is bulk-only
- `MAILERSEND_ENABLE_CUSTOM_HEADERS=true` requires effective custom-header support

Optional overrides:

- `MAILERSEND_BULK_API_SUPPORTED`
- `MAILERSEND_CUSTOM_HEADERS_SUPPORTED`
- `MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST`
- `MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN`

Notes:

- Effective batch size is `min(BATCH_MAX_COUNT, MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST)`
- The relay locally shapes bulk requests per minute, but does not enforce MailerSend daily API quota or monthly email allowance
