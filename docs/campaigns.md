# Campaigns

A campaign sends one message to every eligible contact on a list, through Amazon SES, from one of your own sending identities. Sending is durable: the API process keeps the state in PostgreSQL, so a restart resumes where it stopped and never sends a message twice.

Find **Campaigns** in Mailat's sidebar. The wizard covers the sender, the list, the content and a review step with an audience estimate and a test send.

## Before the first send

A campaign can be saved as a draft at any time. Sending or scheduling it is refused (400, with the reason) until all of these hold:

1. **SES is the provider.** `EMAIL_PROVIDER=ses` with working AWS credentials. Campaigns never send through SMTP.
2. **The From address is your own identity.** It must exactly match a sending identity you own (`can_send`) on an active, SES-verified domain in your organization. Aliases and other users' identities are rejected.
3. **Bounce and complaint feedback reaches Mailat** for that domain: open **Domains**, then **Set up sending resources** and wait until the delivery subscription is confirmed. See [delivery, bounces and complaints](self-hosting-ses.md#delivery-bounces-and-complaints).
4. **The organization has a postal address.** CAN-SPAM requires one in every commercial message. An owner or admin sets it from the wizard's review step (or `PUT /api/v1/campaign-settings`); members see a prompt to ask an admin.
5. **The list's segment rules are valid** (dynamic lists only).
6. **At least one contact is eligible** (send now only; a schedule accepts an empty list and finishes with `no_eligible_recipients`).

The sender, feedback and postal address are checked again when sending starts, when a paused campaign resumes, and every minute while it sends. If any stops holding, the campaign pauses with a reason instead of failing silently.

The review step also shows warnings: `dmarc_missing` (publish a DMARC record before bulk sending), `sandbox_mode` (the SES account can mail only verified addresses), `no_postal_address` and `feedback_not_ready`.

## Who receives it

A contact is eligible when it is in your organization, has status `active`, is on the static list or matches the dynamic list's rules, and its address is not suppressed in either the marketing `suppressions` table (which includes GDPR-erased addresses by hash) or the transactional `suppression_list` (bounces and complaints), in any letter case. Unsubscribed, bounced, complained and pending double-opt-in contacts are therefore never mailed. Addresses that differ only in case are sent once.

**The audience is a snapshot.** It is materialised into `campaign_recipients` when sending starts (for a schedule, when it fires). Contacts added to the list later are not included. The review-step figure is an estimate.

**Each recipient is rechecked just before its message.** A recipient is skipped, and counted under `skippedCount`, when the contact was unsubscribed or suppressed, is no longer active, left the list or segment, changed its email address, or was deleted or erased since the snapshot.

### Dynamic lists (segments)

A dynamic list stores rules instead of members. Segments are created and edited through the API (`POST /api/v1/lists` with `type: "dynamic"` and `segmentRules`, then `PUT /api/v1/lists/{uuid}`); the wizard shows them read-only with a **Segment** badge. Rules are validated when saved and again when a campaign starts; a list whose stored rules no longer validate pauses the campaign with `invalid_segment`.

```json
{
  "match": "all",
  "conditions": [
    { "field": "list", "op": "in_list", "value": "<static list uuid>" },
    { "field": "last_engaged_at", "op": "within_days", "value": 90 },
    { "field": "attributes.plan", "op": "eq", "value": "pro" }
  ]
}
```

| Field | Operators | Value |
| --- | --- | --- |
| `email`, `first_name`, `last_name` | `eq`, `neq`, `contains`, `starts_with`, `ends_with`, `exists`, `not_exists` | text (≤255), case-insensitive |
| `engagement_score` | `eq`, `neq`, `gt`, `gte`, `lt`, `lte` | number |
| `created_at`, `last_engaged_at` | `before`, `after`, `within_days` | RFC 3339 timestamp or `YYYY-MM-DD`; days 1–3650 |
| `list` | `in_list`, `not_in_list` | UUID of a static list in the same organization |
| `attributes.<key>` | text, number or `eq`/`neq` boolean operators, `exists`, `not_exists` | typed by the value; key matches `[A-Za-z0-9_]{1,64}` |

`match` is `all` (default) or `any`, with 1–20 conditions and at most 16 KiB of JSON. Unknown fields, unknown or mismatched operators and extra keys are rejected. Every value is bound as a query parameter.

## What every message contains

- **Personalisation.** `{{email}}`, `{{firstName}}` / `{{first_name}}`, `{{lastName}}` / `{{last_name}}` and `{{<attribute key>}}` are replaced in one pass. Values are HTML-escaped in the HTML part and inserted as-is in the text part; line breaks are removed from the subject. Unknown variables render empty and are listed as warnings when you save and in the preview.
- **A footer** with your organization name, postal address and an unsubscribe link, added to both the HTML and the text part. It cannot be turned off.
- **One-click unsubscribe headers:** `List-Unsubscribe` (an HTTPS URL on `API_URL`) and `List-Unsubscribe-Post: List-Unsubscribe=One-Click`, as Gmail and Yahoo require for bulk senders.
- **Open and click tracking**, on by default and switchable per campaign (**Track opens**, **Track clicks**). With opens on, a 1×1 pixel is added to the HTML part. With clicks on, `http`/`https` links in the HTML part are rewritten through a signed redirect; `mailto:`, `tel:`, `#` anchors, links longer than 2,048 characters, links marked `data-mailat-no-track` and the footer link are left alone. Plain-text links are never rewritten. Turning tracking off after sending stops recording, but tracked links still redirect.

Tokens in tracking and unsubscribe links are signed with a key derived from `JWT_SECRET` and contain no email address. Click targets are signed too, so the redirect only goes to the original `http`/`https` URL. Changing `JWT_SECRET` invalidates the tracking and unsubscribe links in mail already sent, so keep it stable.

Use **Preview** to render the message for a contact (tracking off, links shown as `#`), and **Send test** for a real message to up to five addresses.

## Status and reasons

| Status | Meaning |
| --- | --- |
| `draft` | Editable; nothing is sent. |
| `scheduled` | Sends at `scheduledAt` (1 minute to 365 days ahead, RFC 3339 with an offset). If the server is down at that time, it starts on the first tick after restart. |
| `sending` | Preparing the audience (`preparedAt` is empty) or sending. `throttledUntil` is set while it waits for SES. |
| `paused` | Stopped with a `statusReason`; resume when the cause is fixed. |
| `sent` | No recipient is waiting or in flight. |
| `cancelled` | Stopped for good; messages already handed to SES are not recalled. |

| `statusReason` | Cause | What to do |
| --- | --- | --- |
| `user_paused` | Someone paused it. | Resume. |
| `sender_unavailable` | The From identity or its domain is no longer usable (disabled, deleted, unverified, feedback no longer ready, creator deactivated), or SES rejected the sender. | Fix the identity or domain, then resume. |
| `no_postal_address` | The postal address was removed. | Set it, then resume. |
| `invalid_segment` / `list_unavailable` | The segment rules no longer validate, or the list is gone. | Fix the list, then resume. |
| `provider_paused` | SES reports the account suspended or sending paused. | Resolve it in AWS, then resume. |
| `provider_rejected` | SES rejected five messages in a row (often SES sandbox mode or content). | Check the failed recipients' errors, then resume. |
| `monthly_quota_exceeded` | The organization's monthly send limit is used up. | Resume next month or raise the limit. |
| `bounce_rate_high` | At least 100 sent and permanent bounces reached 5%. | Clean the list before resuming. |
| `complaint_rate_high` | At least 200 sent and complaints reached 0.3%. | Review consent and content before resuming. |
| `ses_daily_quota` / `ses_throttled` | Still `sending`: SES's 24-hour quota is used up (retry in 30 minutes) or SES throttled a request (retry in 1 minute). | Nothing; it continues by itself. |
| `no_eligible_recipients` | Finished as `sent` because nobody was eligible. | Check the list. |
| `legacy_requires_review` | A campaign that was mid-send before this version was reset to `draft`. | Review it and send again from an identity you own. |

Each recipient row moves from `pending` through `claimed` and `sending` to `sent`, `failed` or `unknown`, or ends as `skipped` (with a `skipReason`) or `cancelled`. Delivery, bounce and complaint notifications from SES set a separate `deliveryStatus`. `unknown` means SES may or may not have accepted the message (a timeout, or a crash mid-send); such a recipient is **never retried**, so nobody gets the message twice. A later SES delivery or bounce notification for it upgrades it to `sent`.

## Pause, resume, cancel, edit and delete

- **Pause** a `sending` campaign; at most one message per worker finishes after the pause. **Resume** revalidates the sender, feedback and postal address, then continues with the remaining recipients and no duplicates.
- **Cancel** a draft, scheduled, sending or paused campaign. Recipients not yet attempted become `cancelled` and their monthly-quota reservations are refunded.
- **Edit** drafts and scheduled campaigns freely. Once sending has started, content, sender, list and tracking are locked; a paused campaign whose audience was never prepared may still change content but not its list. To change a campaign that already sent to anyone, cancel it and duplicate it.
- **Delete** drafts, or cancelled campaigns that never prepared an audience. A list used by a scheduled, sending or paused campaign cannot be deleted.

Changing send state (schedule, send, pause, resume, cancel, test) is limited to the campaign's creator or, in a browser session, an organization owner or admin. An API key acts only on campaigns its user created. Only owners and admins in a browser session can change the postal address.

## Pacing, limits and safety

- **One sender per database.** Each API process runs the sender regardless of `WORKER_ENABLED`, but a PostgreSQL advisory lock lets only one of them send at a time. It works through campaigns round-robin, one batch at a time.
- **Rate.** By default 80% of the SES `MaxSendRate` (refreshed every 5 minutes), leaving the rest for compose and transactional mail, and never below 1 message per second. `CAMPAIGN_MAX_SEND_RATE` (messages per second) can lower it; `CAMPAIGN_SEND_CONCURRENCY` (1–16, default 4) sets parallel SES calls. If the quota cannot be read, the last known rate is kept, otherwise 1 per second.
- **SES 24-hour quota.** When `SentLast24Hours` reaches `Max24HourSend`, the campaign waits 30 minutes and checks again. An unlimited quota (`-1`) never waits.
- **Monthly application quota.** With `DISABLE_APP_LIMITS=false` and a positive organization limit, each attempted message reserves one send. A reservation is charged once even if the row is retried after a crash, and is refunded when a campaign is cancelled before the attempt. A limit of `0` is unlimited.
- **SES errors.** Throttling and daily-quota errors put the recipient back and defer the campaign; account-paused and sender errors put it back and pause the campaign; other definitive rejections mark the recipient `failed`; anything else (timeouts, network) marks it `unknown`.
- **Circuit breaker.** The bounce and complaint thresholds in the reasons table pause the campaign automatically.
- **Crashes and restarts.** Claimed recipients carry a 5-minute lease and return to `pending` when it expires. A recipient left in `sending` for 10 minutes becomes `unknown`, or `sent` if SES already confirmed delivery. On shutdown the sender stops claiming, lets in-flight sends finish (up to the 30-second SES timeout) and releases what it holds; give the container enough stop time (the production compose file sets `stop_grace_period: 45s`).

## Feedback and statistics

SES delivery, bounce and complaint notifications are matched to the recipient by SES message ID (or the `X-Mailat-Message-ID` header). Delivered, then permanently bounced, then complained only moves forward, and each campaign counter increases once per recipient. Transient bounces are recorded per recipient but not counted. Permanent bounces and complaints add the address to both suppression tables, so later campaigns skip it.

Rates on the campaign page use `sentCount` as the denominator. Open and click counts are unique recipients; a click without a recorded open also counts as an open. At most 50 open and 50 click events are stored per recipient. Privacy proxies such as Apple Mail Privacy Protection and link scanners inflate opens and clicks, so treat them as indicative.

An unsubscribe from a campaign email sets the contact to `unsubscribed`, adds the address to `suppressions` and counts once in the campaign's `unsubscribeCount`. It applies to the whole organization. Links in a test send accept the request but change nothing. If the contact was deleted, its snapshot address is still suppressed.

GDPR erasure (`DELETE /api/v1/contacts/:uuid/gdpr`) replaces the address on its campaign recipient rows with `erased+<id>@invalid` and clears stored tracking IPs and user agents; a recipient still waiting is skipped.

## Webhooks

The durable outbox emits `campaign.started`, `campaign.paused`, `campaign.cancelled` and `campaign.sent` to the creator's webhooks. Data: `campaignUuid`, `name`, `totalRecipients`, `sent`, `failed`, `unknown`, `skipped`, `statusReason`. Permanent bounces and complaints from campaigns emit `email.bounced` and `email.complained` with `campaignUuid`, `recipient`, `providerMessageId` and the bounce or complaint type. Deliveries are not emitted per recipient.

## API

All routes are under `/api/v1`, need a session or an API key, and are scoped to your organization. Full schemas are in `/api-docs` and `/api/v1/openapi.json`.

| Method | Path | API-key scope |
| --- | --- | --- |
| GET | `/campaigns`, `/campaigns/{uuid}`, `/campaigns/{uuid}/stats`, `/campaigns/{uuid}/progress`, `/campaigns/{uuid}/audience`, `/campaigns/{uuid}/recipients?status&page&pageSize` | `campaigns:read` |
| POST | `/campaigns/{uuid}/preview` | `campaigns:read` |
| POST | `/campaigns`, `/campaigns/{uuid}/schedule`, `/send`, `/pause`, `/resume`, `/cancel`, `/test` | `campaigns:manage` |
| PUT, DELETE | `/campaigns/{uuid}` | `campaigns:manage` |
| GET, PUT | `/campaign-settings` (`{ "postalAddress": "…" }`, ≤500 characters) | `campaigns:read` / `campaigns:manage` (PUT: session owner or admin only) |

`POST /campaigns/{uuid}/test` takes `{ "emails": [1–5 addresses] }` and an `Idempotency-Key` header (8–128 characters). Repeating a key with the same body returns the stored result without sending again; reusing it for different addresses returns 409. Test sends are limited to 10 per campaign per hour (429), refuse suppressed addresses, use the monthly quota, carry a `[Test] ` subject prefix and have tracking off.

Errors: 400 for validation and readiness problems (the message is safe to show), 403 when you may not change the campaign's send state, 404 for an unknown campaign, 409 when the status does not allow the action.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `EMAIL_PROVIDER` | `ses` | Must be `ses` for campaigns. |
| `API_URL` | – | Public origin for tracking and one-click unsubscribe URLs. |
| `WEB_URL` | – | Public origin for the unsubscribe page and invalid click links. |
| `CAMPAIGN_MAX_SEND_RATE` | `0` | Messages per second; `0` uses 80% of the SES `MaxSendRate`. A higher value has no effect. |
| `CAMPAIGN_SEND_CONCURRENCY` | `4` | Parallel SES calls, clamped to 1–16. |
| `DISABLE_APP_LIMITS`, `DEFAULT_MONTHLY_EMAIL_LIMIT` | `true`, `0` | Application monthly quota; see [limits](self-hosting-ses.md#limits-retention-upgrades-and-validation). |

Migration `013_campaign_sending.sql` adds the recipient, event and test-send tables, the campaign counters and toggles, and `organizations.postal_address`. It resets campaigns that were `sending`, `scheduled` or `paused` under the old design to `draft` with `legacy_requires_review`; nothing was sent by them before.

## Not included

A/B tests and campaign templates are stored but ignored. There is no segment editor in the UI, no SMTP sending, no IP warm-up, and plain-text links are not tracked. The audience is materialised in a single transaction, which has not been load-tested beyond about a million contacts.

## Local checks

```sh
cd apps/api
MAILAT_TEST_DATABASE_URL='postgres://USER@127.0.0.1:5432/postgres?sslmode=disable' go test -race ./...
```

The campaign tests use a fake SES provider with scripted outcomes; they cover the state machine, crashes, leases, pause and cancel, quotas, SES error handling, the circuit breaker, SNS feedback, tracking, unsubscribe, test sends and permissions. No mail leaves the machine. Real delivery, headers in a mailbox (DKIM coverage of `List-Unsubscribe`), and SES simulator bounces and complaints remain manual acceptance steps on a staging SES account.
