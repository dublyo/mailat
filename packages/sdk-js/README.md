# Mailat JavaScript / TypeScript SDK

Requires Node 20+ or a browser with Web Crypto and Fetch. Build from this package with `npm install && npm run build`; run `npm test` for signature and HTTP contract tests.

Version 0.2.0 targets API contract `2026-10-07` (the `info.version` of your instance's `/api/v1/openapi.json`). Instances on `f696e55` serve the same paths and schemas but still label them `2026-10-04`; the date-based version starts with the M6 release.

`baseUrl` is required: Mailat is self-hosted, so there is no default host. Pass your instance origin or its API root; `https://mail.example.com`, `https://mail.example.com/`, `https://mail.example.com/api/v1` and `https://mail.example.com/api/v1/` all resolve to `https://mail.example.com/api/v1`. A missing or invalid `baseUrl` throws at construction (breaking change from 0.1.x, which fell back to a hosted URL).

```ts
import { Mailat } from '@mailat/sdk';
const client = new Mailat({apiKey: process.env.MAILAT_API_KEY!, baseUrl: 'https://mail.example.com'});
const sent = await client.emails.send({
  from: 'hello@yourdomain.com', to: ['recipient@example.com'], subject: 'Hello', text: 'Hello',
  attachments: [{name: 'hello.txt', content: 'SGVsbG8=', type: 'text/plain'}]
}, {idempotencyKey: 'order-123-confirmation'});
const status = await client.emails.get(sent.id);
const batch = await client.emails.sendBatch([
  {from:'hello@yourdomain.com', to:['recipient@example.com'], subject:'Hello', text:'Hello'}
], {idempotencyKey:'batch-order-123'});
const inbox = await client.inbox.list({folder:'inbox', page:1, pageSize:20});
const detail = await client.inbox.get('message-uuid');
await client.inbox.mark(['message-uuid'], true);
const changes = await client.inbox.changes(savedCursor, 100); // Persist the returned cursor only after processing.
const bytes = await client.inbox.attachment('message-uuid', 'attachment-uuid');
```

Namespaces: `emails`, `inbox` (including `labels` and `filters`), `compose`, `domains`, `identities`, `templates`, `webhooks`, `triggers`, and `deliveries`. Inbox actions include `mark`, `star`, `move`, `trash`, `assignLabels` (label names), and `testFilter`. Reply and forward context use `compose.replyContext` / `forwardContext`; construct `compose.send` with `fromEmail` and a stable key. Sending context's `from` object is not the compose send request.

```ts
const outcome = await client.triggers.test('trigger-uuid');
const history = await client.deliveries.list({page:1, pageSize:20});
await client.deliveries.replay('delivery-uuid');
// Both verification helpers are async. rawBody is a string or Uint8Array.
const valid = await Mailat.verifyWebhookSignature(rawBody, signature, secret);
const event = await Mailat.parseWebhookPayload(rawBody, signature, secret, async id => atomicallyClaim(id));
```

`MailatError` exposes `status`, `code`, and `retryAfter`.

Every send needs a stable 8–128 character idempotency key. Reuse the same key and unchanged payload after a timeout. A changed payload under the same key returns 409. An `unknown` result must be reconciled using email status; changing the key can cause a duplicate. Batch retries retain the complete original body and order, including per-item keys. The SDK does not retry automatically. `sent` means provider acceptance, while `delivered` means recipient-server delivery.

API keys are scoped and limited per minute. 403 means a missing permission; 429 includes `Retry-After`. Use `email:send`, `email:read`, and `email:manage` for mail automation, with domain/identity/webhook scopes only when needed. API keys cannot create more keys or change account security.

Webhooks use `{version:"1", id, type, createdAt, data}` and `X-Webhook-Signature: t=<unix>,v1=<HMAC-SHA256(timestamp.rawBody)>`, with a dot between timestamp and exact raw body. Verification enforces a five-minute window. Do not parse and reserialize before verifying. Atomically deduplicate `id` in durable storage; retries retain the event ID. Optional claim callbacks support that check, but your application must ensure failed processing is retried safely. Save a job and the claim in one transaction before acknowledging.

Events: `email.received`, `email.sent`, `email.delivered`, `email.failed`, `email.unknown`, `email.bounced`, `email.complained`, and `webhook.test`. Fetch content through the inbox API using `data.messageUuid`. A webhook test returning `status: retry` and `httpStatus: 500` means the receiver failed, even when Mailat's API returned HTTP 200.

Domain creation and `setupSending` / `setup_sending` / `SetupSending` keep receiving opt-in and do not replace root MX. Existing valid DMARC is preserved. Core resource bodies retain documented camelCase keys. See your instance's `/api-docs` and `/api/v1/openapi.json` for field definitions.

DMARC reports use the built-in `dmarc-reports` folder. `MailboxFolder`, `InboxView`, `MailboxMoveDestination`, and `InboxCounts` are exported types; existing generic methods remain available.

```ts
import { DMARC_REPORTS_FOLDER } from '@mailat/sdk';
const reports = await client.inbox.list({folder: DMARC_REPORTS_FOLDER, isRead: false});
const counts = await client.inbox.folderCounts(); // Optional owned identityId argument.
console.log(counts.inboxUnread, counts.dmarcReports, counts.dmarcReportsUnread);
await client.inbox.move(['message-uuid'], 'inbox');
```

`unread` remains the global non-trash count (including Spam and DMARC reports); use `inboxUnread` for the Inbox badge. Explicit filter destinations override automatic report sorting. Received events include the final `data.folder`; historical moves create mailbox changes without another received event.

The human-session-only settings contract adds `autoOrganizeDmarcReports`, default `true`. `DMARCReportsSettings` and `UpdateDMARCReportsSettings` describe the field. Omit it in `PUT /settings` to preserve the preference; `false` disables sorting future arrivals without moving existing reports or altering DNS/receiving setup. API keys cannot update account settings.
