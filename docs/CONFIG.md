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

