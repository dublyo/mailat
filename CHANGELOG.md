# Changelog

Changes by milestone, newest first. Images are published as `ghcr.io/dublyo/mailat-api` and `ghcr.io/dublyo/mailat-web` tagged `sha-<full commit SHA>`; there are no version tags. Upgrade notes, backups and rollback are in [docs/self-hosting-ses.md](docs/self-hosting-ses.md).

## 2026-11-21 security update

- Login PIN and login lockout fix (migration `022_login_pin.sql`; API contract 2026-11-21). Every user now enters a 4-digit login PIN after the password (and after TOTP); the first sign-in asks to choose one, with no skip. Registration, invite and mailbox-setup acceptance and OAuth also end at the PIN step instead of issuing a session. "Remember this device for 30 days" (fixed expiry, at most 20 per user) skips the PIN; password, PIN or 2FA changes, sign-out everywhere, suspension and removal forget remembered devices. Wrong PINs: 5 per challenge, 5 per 15 minutes and 20 per 24 hours per user (429 after that; the 20th is audited). New routes: `POST /auth/pin/verify`, `POST /auth/pin/setup`, `GET|PUT /security/pin`, `GET /security/trusted-devices`, `DELETE /security/trusted-devices/:uuid`, `POST /security/trusted-devices/forget-all`, `POST /org/members/:uuid/pin/reset` (owners reset admins, members and mailbox users; admins reset members and mailbox users). Login limits now count failures only: 10 per address and IP, 100 per address per 15 minutes, so a correct password is never locked out by someone else's wrong guesses. Web: PIN step on the sign-in page, Login PIN and remembered devices in Settings → Security, Reset PIN on the mailbox page and in Team settings. Owner recovery is documented in docs/self-hosting-ses.md.
- **WebAuthn crash fixed; passkeys off by default.** Concurrent `POST /security/webauthn/register/begin` (or `authenticate/begin`) requests from any signed-in user, mailbox users included, could crash the API with `concurrent map writes`, and pending challenges were never freed. The challenge store is now locked, challenges expire after 5 minutes, a user holds at most 5 and the server at most 10,000 (oldest evicted), and begin is limited to 10 calls per user per 15 minutes. The ceremonies still do not verify attestation, origin or signatures, so they are now behind `WEBAUTHN_ENABLED` (default `false`; experimental and insecure). Off, the four begin/finish endpoints answer 404 "Passkeys are not available on this server yet"; listing and deleting stored keys still work. Settings no longer shows the non-working "Add Key" button. No API contract or migration changes.
- **Inbound mail abuse fixes** (API only; no migration, no API change):
  - From, Reply-To and the auto-reply sender are parsed with `net/mail`. A display name such as `"<victim@x>" <attacker@y>` no longer becomes the stored sender, so it can't steer auto-replies, sender filters or trusted-sender images. Auto-replies also require exactly one From mailbox that matches the From SES checked DMARC against.
  - Forwarding skips mail whose DMARC verdict is FAIL, or whose SPF and DKIM both FAIL. The local copy is kept and the job is recorded as `skipped:unauthenticated`.
  - Mail to unknown addresses (no identity and no catch-all) no longer stays in S3 forever. Its raw object is queued for deletion after an hour, and the cleanup worker still never deletes an object that a mailbox row references.
  - Mail with an SES virus verdict of FAIL is not delivered to any mailbox (before, it went to Spam). Its raw object is queued for deletion and the rejection is logged. Spam verdicts are unchanged.
  - The receiving bucket still has no S3 lifecycle rule. It also holds the attachment objects that downloads and forwards read, so an expiry rule would delete mail people still have.

## Domains: list, domain pages and "who reads mail"

Not yet deployed; web only (no API, DTO or migration changes).

### Added

- **Who reads mail on this domain?** Owners and admins choose one of four options in Add Domain, and again on the domain page once the domain is verified:
  - **Me, in my one inbox**: turns on receiving if it is off, creates your address and optionally makes it the catch-all.
  - **Private logins**: opens New mailbox.
  - **Both**.
  - **Nobody, sending only**.

  The step is offered only on domains where nobody gets mail yet (no private logins, no other address, no catch-all), so it never moves an existing catch-all. It shows exactly what it will do, keeps a summary (or what was done before an error), and **Not now** is remembered per browser. Addresses has a link to bring it back. It never publishes DNS.

### Changed

- **Domains** is a status list: one row per domain with its state (Needs DNS, Bounce tracking off, Active), your addresses (catch-all marked) and receiving. A row opens the domain's own page, `/domains/<uuid>`.
- The domain page has a breadcrumb and tabs: **Overview** (verification, readiness, who reads mail, receiving), **Addresses** (your addresses on the domain, catch-all, add address), **DNS & sending** (records, zone download, sending resources, DMARC) and **Private logins**. Adding a domain goes straight to its DNS records.
- Private logins pages link back to their domain. Links to a domain's receiving section use `/domains/<uuid>?receiving=1`; old `/domains?receiving=<uuid>` links still work.

## Inboxes by domain and address

Not yet deployed; migration `021_inbox_scope_indexes`. OpenAPI `contractVersion` is `2026-11-16`.

### Added

- **Inboxes** in the sidebar: **All inboxes**, then each domain, then each of your addresses (and shared mailboxes you read), each with its unread count. A domain opens only that domain's mail; an address opens only that address. Shown once you read more than one address. Expanded domains are remembered per browser.
- `GET /api/v1/inbox/received/scopes` (`email:read`, mailbox users too): the caller's Inbox split by domain and address with `total` and `unread` per scope. Only domains with an address the caller can read are listed, and other users' mailboxes are never included.
- `GET /api/v1/inbox/received/counts` accepts `domainId`.
- Each message row shows the address the sender actually wrote to (for catch-all and `+tag` mail, e.g. `support@` arriving at the `info@` catch-all), and the reader shows **Delivered to** when that address is not in To or Cc.

### Changed

- Sent, Archive, Trash and the other folder links keep the selected domain or address, and the toolbar says which one ("Inbox · example.com").
- Unread badges follow the selected domain or address.
- The Inbox **Domain** filter lists only domains you can read mail on (picking another one used to show an error).

## Bounce notices out of the Inbox

Deployed to mail.dublyo.com on 2026-11-16 (c44e3eb, then the cache fix); migration `020_file_existing_reports`. No API, DTO or IAM changes (OpenAPI `contractVersion` stays `2026-10-10`).

### Added

- **Bounce tracking off** badge (owners and admins) on every verified SES domain whose bounce and complaint feedback is not set up. Without it Mailat never records bounces or suppresses dead addresses, and SES only emails the bounce to the sender. The badge opens the card at **Set up sending resources**.

### Changed

- Bounce notices (RFC 3464 `multipart/report; report-type=delivery-status`, or MAILER-DAEMON/postmaster mail with a bounce subject such as "Delivery Status Notification (Failure)") are filed in **Reports** instead of the Inbox, like verified DMARC reports. Replies and forwards (`Re:`/`Fwd:`) stay in the Inbox, spam still wins, and forwarding behaves as before. Turning off Settings → Filters & Rules → **Automatically file reports** keeps them in the Inbox. SES email feedback forwarding is left on deliberately: another sender on the same SES identity may rely on it.
- The **DMARC Reports** folder is now **Reports** and is shown to mailbox users too, so bounces for mail they send are not hidden.
- Migration 020 moves bounce notices already in the Inbox to Reports for users who keep automatic filing on, including any a user had moved back to the Inbox. Nothing is deleted.
- The sending-resources readiness item says what is lost without it.

### Fixed

- The web app's HTML is sent with `Cache-Control: no-cache`, so browsers load a new release right after a deploy instead of running the old bundle from a heuristic cache. Hashed assets stay cached for a year.
- Health → sending by domain counted display-name senders (`"Name <a@example.com>"`, accepted since 89a348b) as a separate `example.com>` domain.

## Domain readiness and automatic setup on verification

Not yet deployed; migrations `018_domain_ready_automation` and `019_domain_ready_backfill`. OpenAPI `contractVersion` is `2026-10-10`.

### Added

- **API sending readiness** on every SES domain card: domain verified, DMARC (published, inherited or missing), sending resources, your sending identity and optional receiving, each with a fix (Verify now, Show DMARC setup with the copyable record, Set up sending resources, Create `noreply@<domain>` or Use another address, Show receiving). Members see the list; owners and admins fix it. While the automatic setup runs it shows **Setting up automatically…** and re-reads itself.
- `GET /api/v1/domains/:uuid/readiness` (members, `domains:read`): `ready`, ordered `items` (`key`, `status` `ok`/`missing`/`pending`/`attention`/`unknown`/`off`, `state`, `optional`, `detail`, `fix`, `value`), `suggestedIdentity` and `automaticSetup`; `refresh=true` re-checks the briefly cached DMARC and MX lookups.
- When a domain first becomes active and SES verified (Verify, or an owner or admin's SES status check), Mailat creates `noreply@<domain>` for the person who added it (or the owner) if they have no identity on the domain, the address is free (not an identity, alias, invite or another person's login) and the identity limit allows it, then runs the existing sending setup once in the background. Failures show on the sending status; nothing is retried automatically. No DNS is published and receiving stays off.

### Changed

- A send from a domain where the caller has no identity now fails with `you have no sending identity on <domain>; add one (e.g. noreply@<domain>) under Domains → <domain> → Add identity`; members and their keys are told to ask an organization owner or admin.
- Migration 018 records who added a domain (`domains.created_by`) and when the one-time setup ran (`ready_automation_at`). Migration 019 also marks every domain that was ever active or verified (including active domains whose last SES check failed) as done, so the upgrade creates no identities and runs no setup for them.

## Receiving MX visibility

Not yet deployed; no new migrations. OpenAPI `contractVersion` is `2026-10-08`.

### Added

- Every domain card shows a **Receiving (MX)** row: the exact root MX (`MX @ inbound-smtp.<region>.amazonaws.com`, priority 10) with copy buttons and its live status (Off, On – MX missing, Published, Points elsewhere, Unknown), plus Re-check, Enable receiving (behind the existing confirm text) and Add MX to Cloudflare. Receiving stays opt-in per domain.
- `GET /api/v1/domains/:uuid/receiving` (members, `domains:read`): `enabled`, `mxRecord`, `mxStatus` (`not_enabled`, `missing`, `published`, `conflict`, `unknown`) and `existingMx`, from a live lookup with a 3-second timeout and a one-minute cache (`refresh=true` bypasses it).
- Mailbox pages say why mail will not arrive (receiving off, no MX, MX elsewhere) with a **Fix receiving** link; the New mailbox form warns inline; mailbox Overview shows **Receives mail**.

### Changed

- Cloudflare DNS setup adds the root receiving MX only while receiving is enabled and the zone has no other root MX (otherwise a conflict, nothing changed). `"scope": "receiving-mx"` adds just that record.

## M6: docs, hygiene and release

Pushed through `c017fd8`; no new migrations. Not yet on a release-verified deployment.

### Breaking

- **SDKs 0.2.0 require a base URL.** The JS (`baseUrl`), Python (`base_url`) and Go SDKs no longer fall back to a hosted URL and reject a missing or invalid value. Go: `NewClient(baseURL, apiKey string, opts ...ClientOption) (*Client, error)`; `DefaultBaseURL` and `WithBaseURL` are removed.
- **The legacy `sdks/` tree is removed.** The SDKs live in `packages/sdk-go`, `packages/sdk-js` and `packages/sdk-python`.

### Changed

- OpenAPI `info.version` is now a date-based contract version (`contractVersion`, currently `2026-10-07`). The generator records it with a digest of the paths and components in `apps/api/internal/apidocs/openapi.version.json`; `go run ./cmd/openapi --check` fails when the contract changes without a version bump. The contract itself is unchanged from M5.1.
- Go toolchain 1.27.1. Base images are supported versions pinned by digest: `golang:1.27.1-alpine3.24` and `alpine:3.24.2` for the API, `node:24` and `nginx:1.30.5-alpine` for the web image. The API image sets `PORT=8000`.
- CI: actions pinned to commit SHAs, read-only default permissions, checkouts without persisted credentials, `go mod tidy -diff`, `go vet` and the OpenAPI check before tests, and image smoke tests (the API image must not run as uid 0; the web image must serve `/`, `/nginx-health` and `/subscribe/*` headers) before push. Dependabot updates actions and base images weekly.
- **Redis is required.** Startup has always exited without Redis and `/health` reports it; the code comments and docs that called it optional now say so. `WORKER_ENABLED` only controls the Asynq worker and scheduler; the database-backed recovery loops always run.
- Signup forms get a Delete button; the per-form signup rate limit is keyed on the form UUID.
- The Domains DMARC panel explains that the default record has no `rua`, so no aggregate reports arrive until you add one.
- A least-privilege IAM policy, an accurate startup banner, and rewritten README and operator guides.

### Added

- A **Mailboxes** item in the sidebar for owners and admins opens `/mailboxes`: the mailbox list, create and CSV import screen with a domain switcher (active, SES-verified domains) kept in `?domain=`. The per-domain route still works, and mailbox detail pages return to this list (`c017fd8`).

### Fixed

- Request validation accepts plus addresses such as `name+tag@example.com` and other valid addresses that GoFrame's `email` rule rejected (`28a7f1e`, found in live testing). This affects every endpoint that validates an email field, including compose.

### Not changed

- The web image's nginx master process still runs as root; the workers run as the `nginx` user. A fully non-root web image is deferred.
- The web image keeps the `appuser` account and its writable nginx paths but has no `USER` line. A first M6 commit removed them and `ad01286` restored them, so a compose file that sets `user: appuser` on the web service still starts. If your compose sets `user:`, `cap_drop` or `read_only` on the web service, check that it still starts before you upgrade; the default runtime (no `user:`) is unaffected.

## M5.1: mailbox users

Migration `017_mailbox_users`.

- Mailbox users (role `mailbox`): one login, one inbox, a mail-only UI, invite links or admin-set passwords, admin password and 2FA reset, send-as aliases, optional wildcard sender and CSV import. They take no seat and cannot use API keys.
- One send-as rule for compose and `POST /emails`: own address and its `+tag`s plus granted aliases; owners and admins may use any unused address. Reply routing: exact address, then `+tag` base, then alias owner, then catch-all.
- All `/api/v1/health/*` routes are owner and admin only.

## M5: arrival jobs, forwarding, teams and live updates

Migrations `015_mail_arrival` and `016_multi_user_live` (016 builds two `received_emails` indexes inside its transaction; upgrade large installations in a short maintenance window).

### Breaking

- **Sieve routes removed.** The six `/sieve-scripts` routes are gone; existing rows are kept but marked unsupported and never run. Inbox filters are the only rule engine.
- **Live updates need a stream ticket.** Clients fetch a single-use ticket from `POST /api/v1/auth/stream-token` before opening `GET /api/v1/sse/connect`; session JWTs are not accepted in the URL.

### Added

- Auto-replies and verified forwards run from a leased mail-arrival job queue. Forwards are sent through SES as "Name via Mailat" with Reply-To set to the original sender.
- Invites and owner/admin/member roles with server-side checks; shared mailboxes deliver a copy to each member.
- Live updates from a `mailbox_changes` feed announced with PostgreSQL `NOTIFY`, resumable by cursor; web push for new mail with VAPID keys.

## M4: automation executor

Migration `014_automation_executor`.

- Versioned automations with publish, pause, resume and archive, run by a DB-leased executor in the API (no Redis needed). Email steps queue through the campaign pipeline; webhook steps go through the event outbox.
- Manual enrollment, enrollment list and detail, cancel and retry. Automation data is included in GDPR export and erasure.

## M3: campaigns through SES

Migration `013_campaign_sending`.

- Campaigns send through a leased SES runner with an audience snapshot, an eligibility recheck before every message, pacing and monthly send reservations.
- Unsubscribe footer and `List-Unsubscribe`/`List-Unsubscribe-Post` on every campaign email; signed open and click tracking, on by default and switchable per campaign. SES feedback updates campaign recipients.
- The Asynq campaign handler is removed; the campaign UI uses the flat API.

## M2: hardening

Migration `012_hardening` (moves boot-time DDL into a migration and lowercases contact emails where that cannot collide).

### Breaking

- **`/settings/aws/*` removed**, with the AWS setup wizard and the unused provisioner.
- **Legacy `GET /confirm/:token` removed.** Double opt-in confirmation is an explicit POST from the signup confirmation page.
- **`EMAIL_PROVIDER` defaults to `ses`** (was `smtp`); unknown values stop startup.
- **Web liveness moved to `/nginx-health`.** `/health` is now the SPA health page.
- **Startup fails on weak secrets.** `JWT_SECRET` and `ENCRYPTION_KEY` must be set, at least 32 bytes and different.
- Prisma tooling is removed; the SQL migrations in `apps/api/internal/database/migrations` are the only schema.

### Changed

- CORS allowlist (`WEB_URL` and `CORS_ORIGINS`), an SPA Content-Security-Policy from an nginx template, and per-route framing for `/subscribe/*`.
- One trusted-proxy client-IP resolver (`TRUSTED_PROXY_CIDRS`) for rate limits, consent records and audit logs.
- Auth rate limits, reworked OAuth linking and complete 2FA enrollment.
- Remote images blocked by default; mail rules move to the server.
- Unsubscribed and erased contacts are kept out of marketing mail; SES throttles are retried safely.
