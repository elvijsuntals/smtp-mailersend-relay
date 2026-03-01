# Failure Playbook

## Symptom: SMTP rejects with `530 Must issue STARTTLS first`

- Cause: client did not upgrade with STARTTLS.
- Action: enable TLS in sender app; verify relay cert/key paths.

## Symptom: SMTP rejects with `550 Sender domain not allowed`

- Cause: `MAIL FROM` domain not in `SMTP_ALLOWED_SENDER_DOMAINS`.
- Action: add verified sender domain to allowlist and restart.

## Symptom: Queue depth grows continuously

- Cause: MailerSend API failures, quota/rate limits, invalid API key.
- Action:
  1. Check `relay_dispatch_api_calls_total` and response codes.
  2. Validate `MAILERSEND_API_KEY`.
  3. Inspect `relay_dispatch_retries_total` and `relay_dispatch_dlq_total`.

## Symptom: High DLQ volume

- Cause: permanent validation errors (e.g. 422).
- Action:
  1. Export DLQ records: `relay dlq export --out=./dlq-export.jsonl`
  2. Fix root cause (sender, content, recipient issues).
  3. Replay selected IDs: `relay dlq replay --ids=...`

## Symptom: Stale processing jobs after crash

- Cause: worker died after claiming jobs.
- Action: relay auto-recovers via stale lease requeue; confirm with `relay_requeue_stale_recoveries_total`.

