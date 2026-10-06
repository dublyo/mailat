# Mailat external email automation

The supported automation path is the SES mailbox API with scoped service keys,
signed webhooks, and a durable mailbox change feed. The API guide and SDK examples
below describe the supported integration contracts. Local n8n workflow files are
kept outside Git tracking.

## Contract and credentials

`GET /api/v1/openapi.json` is the generated, embedded contract for the running
binary. `/docs/` provides the interactive reference; `/api-docs` provides the
Mailat guide, key management, examples and searchable route reference. Generate
the contract from `apps/api` with `go run ./cmd/openapi`. `--check` and the normal
Go test suite detect route/DTO/documentation drift.

For a documentation mirror such as Mailat.co, pin the generated contract to a
Mailat source revision and show that revision with the download. The contract's
relative server URL refers to the running Mailat instance, not the hosting
control panel. Examples must use `https://YOUR-MAILAT-INSTANCE/api/v1`; never
send an email API key to Mailat.co's billing or deployment APIs. The public
reference does not require a live request executor or visitor credentials.

Every operation includes `x-mailat-backend` and `x-mailat-backend-note`:

| Value | Meaning |
| --- | --- |
| `ses` | The SES sending/receiving workflow and its stored mailbox/configuration resources; provider actions still require AWS setup and owned verified domains |
| `jmap` | Legacy inbox operations that require Stalwart/JMAP accounts; these are not the SES mailbox API |
| `mailat` | Shared application/account/configuration resources; see the operation note for external-service or execution limitations |

These labels describe dependencies, not proof that a deployment is ready or that
every route has been exercised against its external provider. In particular,
`/inbox`, `/inbox/emails`, `/inbox/threads` and the older inbox mutation/search
routes use JMAP; SES integrations should use `/inbox/received`, `/inbox/changes`
and `/inbox/filters`. Stored Sieve, shared-mailbox, auto-reply or forwarding
configuration does not by itself prove that the SES receive pipeline executes
it. Legacy `/settings/aws/*` uses a different JSON envelope; prefer the current
domain sending-setup and explicit inbox receiving-setup routes.

Send `Authorization: Bearer <key>`. Keys belong to their creating user and
organization. Choose only the scopes a workflow needs:

| Work | Scopes |
| --- | --- |
| Read messages, attachment bytes, change feed, reply context | `email:read` |
| Send, upload attachments, save drafts | `email:send` |
| Mark, star, move, trash, labels and filters | `email:manage` |
| Domain status / setup | `domains:read` / `domains:manage` |
| Identity reads / changes | `identities:read` / `identities:manage` |
| Template reads / changes | `templates:read` / `templates:manage` |
| Webhook subscriptions, deliveries, rotation and replay | `webhooks:manage` |

Key expiry, active user status and a PostgreSQL-backed per-minute request counter
are checked on every call. A 429 includes `Retry-After` in seconds. API keys cannot
create stronger keys, administer sessions, change passwords or bypass MFA. The
OpenAPI reference marks human-only routes. Browser SSE uses a 60-second stream
ticket; ordinary API credentials are never accepted in a query string. Headless
SSE clients can use an `email:read` bearer key.

Most JSON responses use `{code: 0, message, data}`; failures use the HTTP status
and `{code, message}`. An operation that returns no data may omit `data`. Follow
each operation's documented status and content type: trigger creation returns
201, attachments return bytes, SSE returns `text/event-stream`, and tracking or
OAuth flows can return a pixel or redirect rather than a JSON envelope. A 200
management response is not proof that a downstream provider action succeeded.

## Sending, attachment storage and delivery status

1. Verify your owned sender domain with SES. In Domains, prepare its sending
   resources if attachments or delivery feedback are required.
2. Sending setup is independent of receiving. It does not create receipt rules or
   change root MX. Existing foreign SES feedback topics are preserved and reported
   in `reason`. Even HTTP 200 can represent partial setup: inspect `storageReady`,
   `feedbackConfigured`, `subscriptionStatus` and `feedbackReady`.
3. Persist an 8–128 character `Idempotency-Key` for each logical send. Single
   transactional sends also accept JSON `idempotencyKey`; both must match when
   present. Compose and batch submissions require the header.
4. Retries must use identical content and the same key. Changed content is 409.
   A batch binds its header key to the entire ordered payload. It can use stable
   per-item keys; otherwise they are derived from the batch key and position.
   Inspect each item's result, and retry the identical batch for incomplete work.

Transactional attachments use `name`, `type` (MIME), base64 `content`, optional
`disposition` and `cid`. Alternatively, upload private attachment data using
`POST /compose/attachments` with multipart `identityId` and `file`; compose can
reference the returned `blobId`. Content and metadata survive queued processing.
Private downloads require ownership of the parent message.

Use the public Mailat message UUID for status, mailbox detail, and reply/forward
contexts. The RFC `Message-ID`, SES provider ID and internal numeric database ID
are different identifiers. `GET /compose/reply/:id` and `/compose/forward/:id`
accept the SES mailbox UUID. Map the context's `from.email` to `/compose/send`'s
`fromEmail`; retain its `identityId`, recipient arrays, subject, bodies, threading
fields and authorized attachments. Do not submit the context object unchanged:
the response's `from` object is not the send request's scalar `fromEmail` field.
Sent mail is visible in unified Sent.

| State | Meaning |
| --- | --- |
| pending / queued | Persisted for processing; provider has not accepted it yet |
| sending | One worker has claimed submission |
| sent | SES accepted submission |
| delivered | Destination server accepted delivery |
| failed | Known failure |
| bounced / complained | Subsequent provider feedback |
| unknown | Provider acceptance may have occurred; investigate before another send |

Neither `sent` nor `delivered` guarantees inbox placement. An ambiguous send is
never blindly resubmitted with a new key. Database-backed recovery repairs the
commit/queue gap after restarts. Idempotency claims are retained for the lifetime
of their send records; there is no short automatic expiry that silently permits
a duplicate retry.

## Reading and organizing mail

Detail GETs do not mark mail read. Use `/inbox/received/mark` explicitly. Bulk
mutations validate the entire selection and ownership before changing anything.
Label resources use UUIDs; existing message label arrays and filter actions use
owned label names. Rename/delete propagates to both under a per-user transaction
lock.

Pagination is endpoint-specific. `/inbox/received?page=1&pageSize=50` returns
`data: {emails, total, unread, page, pageSize, totalPages}`. The default page size
is 50 and values above 100 are capped at 100. `/webhook-deliveries` accepts
`status`, `page` and `pageSize` and returns
`data: {deliveries, page, pageSize, total}`; page sizes outside 1–100 use 50.
The change feed instead uses `cursor`, `limit`, `nextCursor` and `hasMore`.

Use `/inbox/filters` for rules applied by the SES ingestion pipeline. The test
endpoint previews a sample without altering messages. Supported actions are
labels, folder, star, read, archive and trash. Forward/auto-reply actions belong in
your external workflow. The legacy `/rules` API remains synchronized with these
filters. Unsupported legacy rules are disabled with a `migrationWarning` instead
of silently remaining active but ineffective.

For reliable catch-up:

1. Read `/inbox/changes?cursor=now` and persist the returned high-water cursor.
2. Take the initial inbox snapshot, traversing its pages.
3. Process `/inbox/changes?cursor=<saved>&limit=100` until `hasMore` is false.
   Retrieve current message state; a deleted message has a tombstone and may
   return 404 on detail.
4. Persist `nextCursor` only after processing the page. Deduplicate by cursor and
   message UUID because snapshot overlap and retries are expected.

Cursors are opaque decimal strings scoped to the user. They are commit-ordered,
including concurrent changes to different messages. Changes are retained for 90
days. An expired cursor returns 410 and requires another initial snapshot.

## Webhook protocol and recovery

Generic `/webhooks` and filtered `/webhook-triggers` share the same outbox,
signature, delivery attempts and supported events:

```json
{
  "version": "1",
  "id": "stable-event-uuid",
  "type": "email.received",
  "createdAt": "2026-10-04T10:00:00Z",
  "data": {"messageUuid": "mailbox-uuid", "identityUuid": "identity-uuid"}
}
```

Supported types are `contact.subscribed`, `email.received`, `email.sent`, `email.delivered`,
`email.failed`, `email.unknown`, `email.bounced`, and `email.complained`.
Use these canonical dotted names for new subscriptions. Legacy underscored
aliases remain accepted for compatibility. Identity UUID is included
when the identity still exists; it can also be used in trigger filters.
The test endpoints emit a separate `webhook.test` event targeted to the tested
subscription; it is not an additional production subscription event type.

Verify `X-Webhook-Signature: t=<unix>,v1=<hex>` over the **exact raw request bytes**:
`HMAC-SHA256(secret, timestamp + "." + rawBody)`. Reject timestamps outside a
five-minute tolerance and use constant-time signature comparison. Store processed
event IDs durably, and mark them complete only after successful work. Do not
re-serialize parsed JSON before verification. The repaired SDKs implement the
same protocol. Secrets are returned once on create/rotate, never on listing.

Events are created in the same transaction as mail changes/feedback. Delivery
leases recover after a crash. There are eight total attempts, using retry delays
of 5 seconds, 30 seconds, 2 minutes, 10 minutes, 30 minutes, 1 hour and 3 hours.
After that, a delivery becomes a dead letter. Inspect `/webhook-deliveries`, its
attempt detail and replay endpoint, or use Settings → Integrations. Test delivery
shows the actual receiver HTTP outcome; HTTP 200 from the management endpoint
does not mean the receiver accepted it.

Replay preserves the event ID and body and records a new attempt cycle. Events
become eligible for hourly cleanup 30 days after their original `createdAt` when
all deliveries are terminal. A dead letter updated within the past 90 days keeps
its event; pending and retrying deliveries also prevent cleanup. Replay does not
reset the event's age, so a successful replay of an old event does not start a
new 30-day history period.
Subscriptions are user-owned, so revoking a configuring API key does not delete
them. Disable/delete the subscription separately; disabled owners cannot receive
pending deliveries. Destinations must be public HTTPS; redirects and connections
to private/reserved addresses are rejected on every attempt.

## Upgrade and release

Migrations 005–008 preserve mail and domain routing. They add authentication
lifecycle state, send payload/idempotency records, durable event delivery,
sending-only configuration and mailbox recovery/filter compatibility.

- Existing JWTs without an active persisted session require a fresh login.
- Legacy API keys without a bound active user fail closed; create a scoped
  replacement using the owner's session.
- Legacy generic webhooks are attributed to the earliest organization
  owner/admin; review ownership before enabling integrations.
- Existing webhook consumers must adopt the versioned event/signature protocol.
- Inspect migrated filter warnings and sending feedback readiness.

Release should target only the Mailat stack. Take a database backup and record
the deployed API/web image digests before migration. Keep the prior images for
rollback, but restore a pre-upgrade database backup if rolling back application
and schema together; migrations do not provide automatic down scripts. Perform
the controlled SES/Cloudflare/recipient smoke test only in the approved live
release stage.
