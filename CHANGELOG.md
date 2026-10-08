# Changelog

Changes by milestone, newest first. Images are published as `ghcr.io/dublyo/mailat-api` and `ghcr.io/dublyo/mailat-web` tagged `sha-<full commit SHA>`; there are no version tags. Upgrade notes, backups and rollback are in [docs/self-hosting-ses.md](docs/self-hosting-ses.md).

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
