# smtp-mailersend-relay

SMTP relay in Go that accepts SMTP submissions (for example from listmonk), stores each recipient message durably in SQLite, and dispatches via MailerSend Bulk Email API (`/v1/bulk-email`).

This project exists to keep SMTP-compatible tools working while bypassing low SMTP relay throughput limits by using MailerSend bulk API under the hood.

## Features

- SMTP ingress (`EHLO/HELO`, `STARTTLS`, `AUTH PLAIN`, `MAIL`, `RCPT`, `DATA`, `RSET`, `NOOP`, `QUIT`)
- `250 OK` only after durable queue write
- One queued message per recipient
- RFC 5322 + MIME parsing with transfer-encoding decoding (`quoted-printable`, `base64`)
- Attachments supported
- Async batching to MailerSend (`/bulk-email`)
- Retry with backoff + jitter, DLQ support
- Health and metrics endpoints (`/healthz`, `/readyz`, `/metrics`)
- Structured logs for ingress, dispatch, retry, DLQ transitions

## Architecture (Short)

1. SMTP client submits message.
2. Relay validates/authenticates and enqueues one job per recipient in SQLite.
3. Dispatcher claims queued/retry jobs in batches.
4. Relay sends batch to MailerSend bulk API.
5. Jobs move to `sent`, `retry`, or `dlq`.

Delivery semantics are at-least-once. Duplicate sends are possible on ambiguous failures.

## Commands

- `relay serve`
- `relay migrate`
- `relay dlq export --format=jsonl --out=./dlq-export.jsonl`
- `relay dlq replay --ids=id1,id2`
- `relay dlq replay --from-file=./dlq-export.jsonl`
- `relay queue retry-now`

## Quick Start (Local)

1. Copy env template and fill values:

```bash
cp .env.example .env
```

2. Create SMTP certs (self-signed for testing):

```bash
mkdir -p certs
openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 365 \
  -keyout certs/server.key \
  -out certs/server.crt \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost"
```

3. Migrate and run:

```bash
go run ./cmd/relay migrate
go run ./cmd/relay serve
```

4. Verify:

```bash
curl -s http://localhost:8080/healthz
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/readyz
```

## Production Setup (Dokploy+Listmonk example)

### 1) Volumes

- Mount persistent Docker volume to `/data`
- Set `SQLITE_PATH=/data/relay.db`

Container runs as non-root (`uid 65532`), so volume must be writable by this user.
If needed:

```bash
docker run --rm -u 0 -v <VOLUME_NAME>:/data alpine \
  sh -c "mkdir -p /data && chown -R 65532:65532 /data && chmod 775 /data"
```

### 2) Certificates

If exposing SMTP publicly with STARTTLS:

- Mount cert file to `/certs/server.crt`
- Mount key file to `/certs/server.key`
- Set:
  - `SMTP_REQUIRE_STARTTLS=true`
  - `SMTP_ALLOW_INSECURE_AUTH=false`
  - `SMTP_TLS_CERT_FILE=/certs/server.crt`
  - `SMTP_TLS_KEY_FILE=/certs/server.key`
  - `SMTP_DOMAIN=<smtp-hostname>`

If cert is self-signed, clients must use `tls_skip_verify=true`.

### 3) Ports and DNS

- Expose TCP port `2525` publicly (or another port if you publish a different one)
- Open that `tcp` port on VPS firewall/security group
- DNS record for SMTP host must be DNS-only (no HTTP proxy in front of SMTP)

If you run multiple deployments on the same host, each deployment must publish unique host ports.
With `docker-compose.yml`, override only the published ports:

- `SMTP_PUBLISHED_PORT` (host port -> container `2525`)
- `HTTP_PUBLISHED_PORT` (host port -> container `8080`)

### 4) listmonk SMTP settings

Use:

```toml
host = "relay.yourdomain.com"
port = 2525
auth_protocol = "plain"
auth_username = "relay-user"
auth_password = "relay-pass"
tls_enabled = true
tls_skip_verify = false # true only for self-signed certs
from_email = "newsletter@yourdomain.com"
```

`from_email` domain must be in `SMTP_ALLOWED_SENDER_DOMAINS` and verified in MailerSend.

## Required Environment Variables

### Required for `serve`

- `SMTP_AUTH_USERNAME`
- `SMTP_AUTH_PASSWORD`
- `SMTP_ALLOWED_SENDER_DOMAINS`
- `MAILERSEND_API_KEY`
- `SMTP_TLS_CERT_FILE` and `SMTP_TLS_KEY_FILE` when `SMTP_REQUIRE_STARTTLS=true`

### Key defaults

- `SMTP_LISTEN_ADDR=:2525` (internal listen addr inside container)
- `HTTP_LISTEN_ADDR=:8080` (internal listen addr inside container)
- `SMTP_PUBLISHED_PORT=2525` (published host port, compose/Dokploy)
- `HTTP_PUBLISHED_PORT=8080` (published host port, compose/Dokploy)
- `SQLITE_PATH=./data/relay.db`
- `BATCH_MAX_COUNT=500`
- `BATCH_MAX_BYTES=5242880`
- `BATCH_FLUSH_INTERVAL=250ms`
- `RETRY_MAX_ATTEMPTS=8`
- `MAILERSEND_ACCOUNT_PLAN=starter`
- `MAILERSEND_ENABLE_CUSTOM_HEADERS=false`

See `.env.example` for full list.

## MailerSend Account Limits

The relay resolves MailerSend account capabilities from `.env` and shapes outbound bulk traffic locally.

- `MAILERSEND_ACCOUNT_PLAN` supports: `trial`, `free`, `hobby`, `starter`, `professional`, `enterprise`
- Default plan is `starter`
- The relay is bulk-only, so `trial` and `free` are rejected at startup
- `MAILERSEND_ENABLE_CUSTOM_HEADERS=true` is only valid when the effective plan supports custom headers
- Optional overrides:
  - `MAILERSEND_BULK_API_SUPPORTED`
  - `MAILERSEND_CUSTOM_HEADERS_SUPPORTED`
  - `MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST`
  - `MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN`

Current local assumptions:

- Bulk endpoint max messages per request: `500`
- Local bulk request rate limit: `10` requests/minute
- Daily API quota and monthly email allowance are not enforced locally

The effective bulk batch size is `min(BATCH_MAX_COUNT, MAILERSEND_BULK_API_MAX_MESSAGES_PER_REQUEST)`.

## Operations

### Health and metrics

- `GET /healthz`
- `GET /readyz`
- `GET /metrics`

### Queue status

```bash
sqlite3 /data/relay.db "select status,count(*) from jobs group by status;"
```

### Force retry queue immediately

```bash
relay queue retry-now
```

This sets `next_attempt_at` to now for all `retry` jobs so the dispatcher can pick them up immediately after restart or after a long backoff.

### Check MailerSend bulk status

```bash
curl -s -H "Authorization: Bearer $MAILERSEND_API_KEY" \
  "https://api.mailersend.com/v1/bulk-email/<bulk_email_id>"
```

## Troubleshooting

### `db migration failed: unable to open database file`

- Volume/path/permission issue.
- Use absolute DB path (`/data/relay.db`) and ensure volume writable by `uid 65532`.

### `Unsupported authentication mechanism`

- Client is not using AUTH PLAIN.
- Set listmonk `auth_protocol="plain"`.

### `unencrypted connection`

- Client refuses AUTH over plaintext.
- Enable STARTTLS on relay and `tls_enabled=true` on client.

### `configured MAILERSEND_ACCOUNT_PLAN="free" does not support bulk email`

- Cause: this relay always uses MailerSend `/v1/bulk-email`.
- Action: switch to `MAILERSEND_ACCOUNT_PLAN=hobby` or higher, or build a future non-bulk sender path.

### `certificate is not valid for any names`

- Cert SAN does not include SMTP hostname.
- Reissue cert with `DNS:<your-smtp-host>`.

### `certificate signed by unknown authority`

- Client does not trust self-signed cert.
- Use CA-signed cert, or temporarily `tls_skip_verify=true`.

### API accepts (`202`) but no delivery

- Query bulk status for `validation_errors_count` or suppressions.
- If you see `MS42233` (custom headers feature), keep:
  - `MAILERSEND_ENABLE_CUSTOM_HEADERS=false`, or use `professional` / `enterprise`

### Queue depth grows but MailerSend `429` does not

- Cause: the relay is waiting on its local MailerSend request limiter.
- Action:
  - Check `relay_dispatch_throttle_waits_total`
  - Confirm `MAILERSEND_ACCOUNT_PLAN` and any `MAILERSEND_BULK_API_MAX_REQUESTS_PER_MIN` override
  - Note that SMTP ingress continues accepting and queueing mail while dispatch is throttled

### HTML looks broken (`=3D`, image URL issues)

- Relay now decodes quoted-printable/base64 transfer encodings before forwarding.
- Deploy latest version if you still see this.

## DLQ

Export:

```bash
relay dlq export --format=jsonl --out=./dlq-export.jsonl
```

Replay by IDs:

```bash
relay dlq replay --ids=01H...,01J...
```

Replay from file:

```bash
relay dlq replay --from-file=./dlq-export.jsonl
```
