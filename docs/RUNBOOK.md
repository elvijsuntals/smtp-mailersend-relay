# Runbook

## Startup

1. Run migrations: `relay migrate`
2. Start service: `relay serve`
3. Verify:
   - `GET /healthz` returns 200
   - `GET /readyz` returns 200
   - `GET /metrics` exposes Prometheus metrics

## Graceful shutdown

Send `SIGTERM` or `SIGINT`. The relay stops accepting SMTP, waits for background workers to finish current work, and closes DB.

## Observability checks

- `relay_queue_depth{status="queued|retry|processing|sent|dlq"}`
- `relay_dispatch_api_calls_total`
- `relay_dispatch_throttle_waits_total`
- `relay_dispatch_retries_total`
- `relay_dispatch_dlq_total`

## Force retry queue now

If you need all delayed retry items to become eligible immediately:

```bash
relay queue retry-now
```

## DLQ operations

Export:

```bash
relay dlq export --format=jsonl --out=./dlq-export.jsonl
```

Replay:

```bash
relay dlq replay --ids=ID1,ID2
```

or

```bash
relay dlq replay --from-file=./dlq-export.jsonl
```

