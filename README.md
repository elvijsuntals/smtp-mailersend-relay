# smtp-mailersend-relay

SMTP relay in Go that accepts SMTP submissions (e.g. from listmonk), stores each recipient message durably in SQLite, and dispatches using MailerSend Bulk Email API (`/v1/bulk-email`).

## What it solves

MailerSend SMTP relay has strict per-connection and per-IP transaction limits. This relay keeps SMTP compatibility for tools that only speak SMTP while sending outbound mail through MailerSend bulk API.

## Features

- SMTP ingress (`EHLO/HELO`, `STARTTLS`, `AUTH PLAIN`, `MAIL`, `RCPT`, `DATA`, `RSET`, `NOOP`, `QUIT`)
- `250 OK` only after durable SQLite enqueue
- One queued message per recipient
- RFC 5322 / MIME parsing with text+html+attachments
- Async dispatch via official `mailersend-go` SDK
- Retry with exponential backoff + jitter
- Dead-letter queue and replay/export commands
- `/healthz`, `/readyz`, `/metrics` (Prometheus)
- Structured JSON logging

## Commands

- `relay serve`
- `relay migrate`
- `relay dlq export --format=jsonl --out=./dlq-export.jsonl`
- `relay dlq replay --ids=id1,id2` or `relay dlq replay --from-file=./dlq-export.jsonl`

## Environment Variables

### Core

- `APP_ENV` (default: `production`)
- `LOG_LEVEL` (default: `info`)

### SMTP

- `SMTP_LISTEN_ADDR` (default: `:2525`)
- `SMTP_DOMAIN` (default: `localhost`)
- `SMTP_AUTH_USERNAME` (required for `serve`)
- `SMTP_AUTH_PASSWORD` (required for `serve`)
- `SMTP_REQUIRE_STARTTLS` (default: `true`)
- `SMTP_ALLOW_INSECURE_AUTH` (default: `false`)
- `SMTP_TLS_CERT_FILE` (required when `SMTP_REQUIRE_STARTTLS=true`)
- `SMTP_TLS_KEY_FILE` (required when `SMTP_REQUIRE_STARTTLS=true`)
- `SMTP_ALLOWED_SENDER_DOMAINS` (required, CSV; must be verified in MailerSend)
- `SMTP_MAX_MESSAGE_BYTES` (default: `8388608`)
- `SMTP_MAX_RECIPIENTS` (default: `1000`)
- `SMTP_MAX_CONNECTIONS` (default: `200`)
- `SMTP_MAX_CONNECTIONS_PER_IP` (default: `20`)
- `SMTP_RATE_LIMIT_PER_IP_PER_MIN` (default: `600`)

### HTTP ops

- `HTTP_LISTEN_ADDR` (default: `:8080`)

### SQLite

- `SQLITE_PATH` (default: `./data/relay.db`)
- `SQLITE_MAX_OPEN_CONNS` (default: `1`)
- `SQLITE_MAX_IDLE_CONNS` (default: `1`)

### Dispatch

- `DISPATCHER_WORKERS` (default: `4`)
- `QUEUE_CLAIM_LIMIT` (default: `1000`)
- `QUEUE_LEASE_TIMEOUT` (default: `30s`)
- `BATCH_MAX_COUNT` (default: `500`)
- `BATCH_MAX_BYTES` (default: `5242880`)
- `BATCH_FLUSH_INTERVAL` (default: `250ms`)
- `RETRY_MAX_ATTEMPTS` (default: `8`)
- `REQUEUE_STALE_INTERVAL` (default: `30s`)

### MailerSend

- `MAILERSEND_API_KEY` (required for `serve`)
- `MAILERSEND_BASE_URL` (default: `https://api.mailersend.com/v1`)
- `MAILERSEND_TIMEOUT` (default: `20s`)

## Quick Start

1. Create TLS cert/key for SMTP STARTTLS.
2. Set required env vars.
3. Run migration:

```bash
relay migrate
```

4. Run service:

```bash
relay serve
```

5. Configure listmonk SMTP to relay host/port and AUTH credentials.

## listmonk Example

```ini
host = "relay"
port = 2525
auth_username = "relay-user"
auth_password = "relay-pass"
tls_enabled = true
tls_skip_verify = false
from_email = "news@example.com"
```

`from_email` domain must be in `SMTP_ALLOWED_SENDER_DOMAINS`.

## Operational Runbook

### Health checks

- `GET /healthz`: process liveness
- `GET /readyz`: DB ping + dispatcher heartbeat
- `GET /metrics`: Prometheus metrics

### Common failure patterns

- `550 Sender domain not allowed`: sender domain missing from `SMTP_ALLOWED_SENDER_DOMAINS`
- `530 Must issue STARTTLS first`: SMTP client not using STARTTLS
- Queue growth and retries increasing: check MailerSend API key/quota/rate limits

### DLQ handling

Export:

```bash
relay dlq export --format=jsonl --out=./dlq-export.jsonl
```

Replay selected IDs:

```bash
relay dlq replay --ids=01H...,01J...
```

Replay from export file:

```bash
relay dlq replay --from-file=./dlq-export.jsonl
```

## Development Notes

- Delivery semantics are at-least-once.
- Duplicate sends are possible on ambiguous network/API failures.
- This project is single-instance optimized (SQLite queue).

