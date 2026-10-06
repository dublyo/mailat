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
and `/inbox/filters`. Auto-replies and verified forwards run when mail
arrives through SES, and shared mailboxes deliver an independent copy to each
member who can read them (members see only shared mailboxes they belong to). Sieve scripts are no longer supported (existing rows are kept but never
run); inbox filters are the only rule engine. Use the domain sending-setup and explicit inbox receiving-setup routes for
SES configuration (the legacy `/settings/aws/*` provisioning endpoints were
removed).

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
| Contacts, lists and signup forms: read, import, export, change, delete and GDPR erasure | `contacts:manage` |
| Automation lists, stats and enrollments (includes contact emails) | `automations:read` |
| Enroll contacts, cancel and retry enrollments | `automations:enroll` |

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
labels, folder, star, read, archive and trash. Forwarding and auto-replies are
configured with `/forwards` and `/auto-replies`, not as filter actions. The legacy `/rules` API remains synchronized with these
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

## Automations (workflows)

An automation is a graph of steps drawn in the editor (Automations in the web
app). It starts from one trigger and runs each enrolled contact through its
steps on the server. Nothing runs until the automation is **activated**.

### Triggers and steps

| Trigger | Starts when |
| --- | --- |
| `contact.subscribed` | A contact joins a list by any path. You can filter by one list and by source. |
| `contact.created` | A contact is created. You can filter by source. |
| `manual` | Only the enroll endpoint or the editor enrolls contacts. |

Each list membership and contact records its source: `signup_form`, `api`,
`import`, `manual`, `preference_center`, `double_opt_in`, `automation` or
`unknown`. By default a trigger accepts `signup_form`, `api`, `manual`,
`preference_center` and `double_opt_in`. **CSV imports (`import`), contacts
added by another automation (`automation`) and `unknown` enroll only when the
automation opts in.** Only contacts who meet the trigger after activation are
enrolled. The enroll endpoint works for any active automation, whatever its
trigger.

The available steps:

- **Send email** sends an active `email_templates` template. The sender must be
  one of the publishing user's `can_send` identities on an active, SES-verified
  domain. This is checked when you publish and again at send time. Each email
  has the unsubscribe footer and `List-Unsubscribe`/`List-Unsubscribe-Post`
  headers, and you can turn open and click tracking on or off.
- **Wait** pauses for 1 minute to 365 days.
- **If/Else** and **Filter** check whether an earlier email step was opened or
  clicked, the engagement score, or a contact attribute. The check runs when
  the contact reaches the step, so put a Wait before an opened or clicked
  check. A filter continues only on Yes.
- **Add to list**, **Remove from list** and **Update contact field** change
  the contact. Static lists only. An automation cannot add contacts to its own
  trigger list, and it cannot change `email` or `status`.
- **Webhook** queues a signed `automation.webhook` event to one of the
  publishing user's active endpoints. It uses the same dispatcher, retries
  and SSRF guard as other webhooks. Endpoints cannot subscribe to
  `automation.webhook`; it reaches only the endpoint chosen in the step.
  Payload `data`: `automationUuid`, `automationName`, `enrollmentUuid`,
  `nodeId`, `contactId` (the contact UUID), `email`, `firstName` and `lastName`.
- A step with no next step ends the enrollment as `completed`.

Tags, form-submitted, opened and clicked triggers, date triggers, goals and
A/B splits are not supported. Validation rejects them.

### Versions and lifecycle

Saving changes only the draft. `POST /automations/:uuid/validate` returns
`{valid, errors:[{nodeId, field, message}]}`.

`POST /automations/:uuid/activate` validates the draft and publishes it as a
new immutable version, but only if the draft changed. It then sets the
automation `active`. The same call publishes changes to a live automation and
resumes a paused one. With `{"publishDraft": false}` it resumes the current
version and leaves the draft alone, even if the draft is invalid. A failed
validation returns 400 with the same `errors` in `data.errors`.

Each enrollment stays on the version it started on. Editing a live automation
affects only new enrollments.

| Status | Behaviour |
| --- | --- |
| `draft` | Never published; nothing runs |
| `active` | Records trigger events, enrolls contacts and runs steps |
| `paused` | No new enrollments; contacts in progress stop where they are and queued emails are held. On resume, waits that came due run right away |
| `archived` | Terminal and read-only; contacts in progress are cancelled with `exit_reason=archived` |

Only `draft` and `archived` automations can be deleted. You cannot delete a
list or template that an active or paused version uses.

### Execution and email delivery

The API process runs the executor. It polls PostgreSQL, takes leases with
`FOR UPDATE SKIP LOCKED` and needs no Redis, so more than one replica is safe.
Each step commits in one transaction together with its side effect and the
enrollment's advance.

An email step only queues the message: it writes one `automation_messages` row
in that transaction, unique per enrollment and step. A separate leased runner
sends the queued message through SES after the commit. That runner uses the
campaign pipeline: the eligibility and suppression check, sender checks,
monthly quota, the HTML-escaping renderer, the footer, tracking and SES error
classification.

A message that may have reached SES is never sent twice. After a crash
mid-send it is marked `unknown` rather than retried.

Transient step failures retry after 1 minute, 5 minutes, 15 minutes, 1 hour
and 6 hours. If the step still fails, the enrollment becomes `failed`. Errors
that a retry cannot fix fail the enrollment at once, for example a missing
template or an identity that is no longer verified.

Before each step, the executor exits the enrollment in these cases:

- the contact unsubscribed (`unsubscribed`)
- the contact is suppressed (`suppressed`)
- the contact is no longer active (`inactive`)
- for a list trigger, the email step finds the contact has left the trigger
  list (`left_list`)

Re-entry is `never` by default. With `after_exit`, a contact can enroll again
only when it has no active enrollment and none started in the last 24 hours.

### Enrollments and stats

- `GET /automations/:uuid/stats` returns live counts (`enrolled`, `active`,
  `waiting`, `completed`, `exited`, `failed`, `cancelled`) and per-step
  `nodes` counts for the published version. Pass `?version=` to choose another
  version.
- `GET /automations/:uuid/enrollments?status=` lists enrollments. `status`
  also accepts `waiting`. `GET …/enrollments/:enrollmentUuid` adds the step
  timeline.
- `POST /automations/:uuid/enroll` takes `{contactUuid}` or `{listUuid}`, with
  at most 50,000 eligible list members. It returns `{enrolled, skipped}` and
  needs an active automation.
- `POST …/enrollments/:enrollmentUuid/cancel` stops an active enrollment.
- `POST …/enrollments/:enrollmentUuid/retry` restarts a failed step. It
  returns 409 if the contact already has another active enrollment.

API keys can use only the `automations:read` and `automations:enroll` routes.
Creating, editing, validating, activating, pausing, archiving and deleting
automations need a signed-in user.

GDPR erasure of a contact removes its enrollments, step runs, trigger events
and `automation.webhook` events. The contact export includes its enrollments
and its automation emails (`automationEmails`).

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
