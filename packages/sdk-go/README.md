# Mailat Go SDK

Requires Go 1.21+. Run `go test -race ./...` in this package for HTTP and signature tests.

Version 0.2.0 targets API contract `2026-10-07` (the `info.version` of your instance's `/api/v1/openapi.json`). Instances on `f696e55` serve the same paths and schemas but still label them `2026-10-04`; the date-based version starts with the M6 release.

The base URL is required: Mailat is self-hosted, so there is no default host. `NewClient(baseURL, apiKey string, opts ...ClientOption) (*Client, error)` takes your instance origin or its API root; `https://mail.example.com`, `https://mail.example.com/`, `https://mail.example.com/api/v1` and `https://mail.example.com/api/v1/` all resolve to `https://mail.example.com/api/v1`. It returns an error for an empty or invalid base URL or an empty API key. This is a breaking change from 0.1.x: `DefaultBaseURL` and `WithBaseURL` are removed, and `NewClient` now returns an error.

```go
import "github.com/dublyo/mailat-go/mailat"
client, err := mailat.NewClient("https://mail.example.com", apiKey)
// Handle err before using client.
sent, err := client.Emails.Send(ctx, &mailat.SendEmailRequest{
    From: "hello@yourdomain.com", To: []string{"recipient@example.com"},
    Subject: "Hello", Text: "Hello",
    Attachments: []mailat.Attachment{{Name:"hello.txt", Content:"SGVsbG8=", Type:"text/plain"}},
}, &mailat.SendOptions{IdempotencyKey:"order-123-confirmation"})
// Handle err before using sent.
status, err := client.Emails.Get(ctx, sent.ID)
batch, err := client.Emails.SendBatch(ctx, requests, &mailat.SendOptions{IdempotencyKey:"batch-order-123"})
page, err := client.Inbox.List(ctx, url.Values{"folder":{"inbox"},"page":{"1"},"pageSize":{"20"}})
err = client.Inbox.Mark(ctx, []string{"message-uuid"}, true)
changes, err := client.Inbox.Changes(ctx, savedCursor, 100)
attachmentBytes, err := client.Inbox.Attachment(ctx, "message-uuid", "attachment-uuid")
outcome, err := client.Webhooks.TestDelivery(ctx, "webhook-uuid")
event, err := mailat.ParseWebhookPayload(rawBody, signature, secret, atomicallyClaim)
```

`atomicallyClaim` has signature `func(string) (bool, error)`. `VerifyWebhookSignature` accepts an explicit `time.Duration`; zero uses five minutes and a negative value is rejected.

Namespaces: `Emails`, `Inbox` (including `Labels` and `Filters`), `Compose`, `Domains`, `Identities`, `Templates`, `Webhooks`, `Triggers`, `Deliveries`. Core request/response objects use `mailat.Object` with camelCase keys. Core `List` methods return `json.RawMessage` because some resources return arrays and others return paginated objects. `Inbox.AssignLabels` accepts label names. Compose context uses `ReplyContext` / `ForwardContext`; `Compose.Send` expects `fromEmail` and a stable key. `APIError` exposes `StatusCode`, `Code`, and `RetryAfter`.

Use `Webhooks.TestDelivery` to inspect the actual attempt; legacy `Webhooks.Test` only returns API errors. `Template.ID` and `Webhook.ID` are numeric database IDs; their `UUID` fields address routes.

Every send needs a stable 8–128 character idempotency key. Reuse the same key and unchanged payload after a timeout. A changed payload under the same key returns 409. An `unknown` result must be reconciled using email status; changing the key can cause a duplicate. Batch retries retain the complete original body and order, including per-item keys. The SDK does not retry automatically. `sent` means provider acceptance, while `delivered` means recipient-server delivery.

API keys are scoped and limited per minute. 403 means a missing permission; 429 includes `Retry-After`. Use `email:send`, `email:read`, and `email:manage` for mail automation, with domain/identity/webhook scopes only when needed. API keys cannot create more keys or change account security.

Webhooks use `{version:"1", id, type, createdAt, data}` and `X-Webhook-Signature: t=<unix>,v1=<HMAC-SHA256(timestamp.rawBody)>`, with a dot between timestamp and exact raw body. Verification enforces a five-minute window. Do not parse and reserialize before verifying. Atomically deduplicate `id` in durable storage; retries retain the event ID. Optional claim callbacks support that check, but your application must ensure failed processing is retried safely. Save a job and the claim in one transaction before acknowledging.

Events: `email.received`, `email.sent`, `email.delivered`, `email.failed`, `email.unknown`, `email.bounced`, `email.complained`, and `webhook.test`. Fetch content through the inbox API using `data.messageUuid`. A webhook test returning `status: retry` and `httpStatus: 500` means the receiver failed, even when Mailat's API returned HTTP 200.

Domain creation and `setupSending` / `setup_sending` / `SetupSending` keep receiving opt-in and do not replace root MX. Existing valid DMARC is preserved. Core resource bodies retain documented camelCase keys. See your instance's `/api-docs` and `/api/v1/openapi.json` for field definitions.

DMARC reports use `mailat.FolderDMARCReports` (`dmarc-reports`). Generic methods are unchanged; `FolderCounts` adds a typed `InboxCounts` result:

```go
reports, err := client.Inbox.List(ctx, url.Values{"folder": {mailat.FolderDMARCReports}})
counts, err := client.Inbox.FolderCounts(ctx, 0) // Zero means all owned identities.
// Handle err, then use counts.InboxUnread, counts.DMARCReports, counts.DMARCReportsUnread.
err = client.Inbox.Move(ctx, []string{"message-uuid"}, "inbox")
```

`Unread` retains the global non-trash count, including Spam and unread reports. Explicit filter destinations override automatic sorting. Received events include final `data.folder`; historical moves appear in the change feed without another received event.

`DMARCReportsSettings` and `UpdateDMARCReportsSettings` describe the human-session-only `autoOrganizeDmarcReports` preference, default true. A nil update pointer is omitted; a pointer to false opts out for future arrivals. Existing reports remain in place, and DNS/receiving setup is unaffected. API keys cannot change account settings.
