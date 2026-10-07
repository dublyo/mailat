# Mailat Python SDK

Install this package with `pip install .`. Run `PYTHONPATH=. python -m unittest discover -s tests -v` for HTTP and signature tests.

Version 0.2.0 targets API contract `2026-10-07` (the `info.version` of your instance's `/api/v1/openapi.json`).

`base_url` is required: Mailat is self-hosted, so there is no default host. Pass your instance origin or its API root; `https://mail.example.com`, `https://mail.example.com/`, `https://mail.example.com/api/v1` and `https://mail.example.com/api/v1/` all resolve to `https://mail.example.com/api/v1`. A missing or invalid `base_url` raises `ValueError` at construction (breaking change from 0.1.x, which fell back to a hosted URL).

```python
from mailat import Mailat, SendEmailRequest
from mailat.models import Attachment
with Mailat(api_key=api_key, base_url="https://mail.example.com") as client:
    sent = client.emails.send(
        from_address="hello@yourdomain.com", to=["recipient@example.com"],
        subject="Hello", text="Hello", idempotency_key="order-123-confirmation",
        attachments=[Attachment(name="hello.txt", content="SGVsbG8=", type="text/plain")])
    status = client.emails.get(sent.id)
    batch = client.emails.send_batch([
        SendEmailRequest(from_address="hello@yourdomain.com", to=["recipient@example.com"], subject="Hello", text="Hello")
    ], idempotency_key="batch-order-123")
    page = client.inbox.list(folder="inbox", page=1, pageSize=20)
    client.inbox.mark(["message-uuid"], True)
    changes = client.inbox.changes(cursor=saved_cursor, limit=100)
    attachment_bytes = client.inbox.attachment("message-uuid", "attachment-uuid")
    result = client.triggers.test("trigger-uuid")
    client.deliveries.replay("delivery-uuid")

valid = Mailat.verify_webhook_signature(raw_body, signature, secret)
event = Mailat.parse_webhook_payload(raw_body, signature, secret, claim_event=atomically_claim)
```

Namespaces: `emails`, `inbox` (including `labels` and `filters`), `compose`, `domains`, `identities`, `templates`, `webhooks`, `triggers`, and `deliveries`. Core methods use snake_case; request dictionaries use camelCase. Typed models accept snake_case or camelCase and serialize using `model_dump(by_alias=True)`. Inbox `assign_labels` accepts label names. Compose reply/forward context uses `reply_context` / `forward_context`; send with `fromEmail` and a stable key. `MailatError` exposes `status`, `code`, and `retry_after`.

Every send needs a stable 8–128 character idempotency key. Reuse the same key and unchanged payload after a timeout. A changed payload under the same key returns 409. An `unknown` result must be reconciled using email status; changing the key can cause a duplicate. Batch retries retain the complete original body and order, including per-item keys. The SDK does not retry automatically. `sent` means provider acceptance, while `delivered` means recipient-server delivery.

API keys are scoped and limited per minute. 403 means a missing permission; 429 includes `Retry-After`. Use `email:send`, `email:read`, and `email:manage` for mail automation, with domain/identity/webhook scopes only when needed. API keys cannot create more keys or change account security.

Webhooks use `{version:"1", id, type, createdAt, data}` and `X-Webhook-Signature: t=<unix>,v1=<HMAC-SHA256(timestamp.rawBody)>`, with a dot between timestamp and exact raw body. Verification enforces a five-minute window. Do not parse and reserialize before verifying. Atomically deduplicate `id` in durable storage; retries retain the event ID. Optional claim callbacks support that check, but your application must ensure failed processing is retried safely. Save a job and the claim in one transaction before acknowledging.

Events: `email.received`, `email.sent`, `email.delivered`, `email.failed`, `email.unknown`, `email.bounced`, `email.complained`, and `webhook.test`. Fetch content through the inbox API using `data.messageUuid`. A webhook test returning `status: retry` and `httpStatus: 500` means the receiver failed, even when Mailat's API returned HTTP 200.

Domain creation and `setupSending` / `setup_sending` / `SetupSending` keep receiving opt-in and do not replace root MX. Existing valid DMARC is preserved. Core resource bodies retain documented camelCase keys. See your instance's `/api-docs` and `/api/v1/openapi.json` for field definitions.

DMARC reports use the built-in `dmarc-reports` folder. Generic methods are unchanged; `folder_counts` returns typed `InboxCounts`:

```python
from mailat import DMARC_REPORTS_FOLDER, UpdateDMARCReportsSettings
reports = client.inbox.list(folder=DMARC_REPORTS_FOLDER, isRead=False)
counts = client.inbox.folder_counts()  # Optional identity_id for one owned identity.
print(counts.inbox_unread, counts.dmarc_reports, counts.dmarc_reports_unread)
client.inbox.move(["message-uuid"], "inbox")
preference = UpdateDMARCReportsSettings(auto_organize_dmarc_reports=False)
body = preference.model_dump(by_alias=True, exclude_none=True)
# body == {"autoOrganizeDmarcReports": False}; an omitted field is not sent.
```

`unread` retains the global non-trash total, including Spam and reports. Explicit user filter destinations override automatic sorting. Received events include final `data.folder`; historical moves create change-feed updates without another received event.

The account preference defaults to true. `DMARCReportsSettings` and `UpdateDMARCReportsSettings` describe the human-session-only settings API; API keys cannot update it. False opts out of sorting future arrivals without moving existing reports, hiding the folder, or changing DNS/receiving setup. Always use `exclude_none=True` for partial preference updates.
