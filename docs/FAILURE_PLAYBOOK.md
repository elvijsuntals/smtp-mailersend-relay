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
  2. Check `relay_dispatch_throttle_waits_total` for local MailerSend shaping.
  3. Validate `MAILERSEND_API_KEY`.
  4. Confirm `MAILERSEND_ACCOUNT_PLAN` and any MailerSend override env vars.
  5. Inspect `relay_dispatch_retries_total` and `relay_dispatch_dlq_total`.

## Symptom: `relay serve` exits saying the MailerSend plan does not support bulk email

- Cause: `MAILERSEND_ACCOUNT_PLAN` resolved to `free` or `trial`, or `MAILERSEND_BULK_API_SUPPORTED=false`.
- Action:
  1. Set `MAILERSEND_ACCOUNT_PLAN=hobby` or higher, or override `MAILERSEND_BULK_API_SUPPORTED=true` only if you know the account supports bulk.
  2. Restart the relay.

## Symptom: `relay serve` exits because custom headers are unsupported

- Cause: `MAILERSEND_ENABLE_CUSTOM_HEADERS=true` with a plan that does not support custom headers.
- Action:
  1. Set `MAILERSEND_ENABLE_CUSTOM_HEADERS=false`, or
  2. Use `MAILERSEND_ACCOUNT_PLAN=professional` or `enterprise`, or
  3. Override `MAILERSEND_CUSTOM_HEADERS_SUPPORTED=true` only if the account feature is actually enabled.

## Symptom: High DLQ volume

- Cause: permanent validation errors (e.g. 422).
- Action:
  1. Export DLQ records: `relay dlq export --out=./dlq-export.jsonl`
  2. Fix root cause (sender, content, recipient issues).
  3. Replay selected IDs: `relay dlq replay --ids=...`

## Symptom: Stale processing jobs after crash

- Cause: worker died after claiming jobs.
- Action: relay auto-recovers via stale lease requeue; confirm with `relay_requeue_stale_recoveries_total`.
