# Changelog

Changes by milestone, newest first. Images are published as `ghcr.io/dublyo/mailat-api` and `ghcr.io/dublyo/mailat-web` tagged `sha-<full commit SHA>`; there are no version tags. Upgrade notes, backups and rollback are in [docs/self-hosting-ses.md](docs/self-hosting-ses.md).

## M6: docs, hygiene and release (unreleased)

No new migrations.

### Breaking

- **SDKs 0.2.0 require a base URL.** The JS (`baseUrl`), Python (`base_url`) and Go SDKs no longer fall back to a hosted URL and reject a missing or invalid value. Go: `NewClient(baseURL, apiKey string, opts ...ClientOption) (*Client, error)`; `DefaultBaseURL` and `WithBaseURL` are removed.
- **The legacy `sdks/` tree is removed.** The SDKs live in `packages/sdk-go`, `packages/sdk-js` and `packages/sdk-python`.

### Changed

- OpenAPI `info.version` is now a date-based contract version (`contractVersion`, currently `2026-10-07`). The generator records it with a digest of the paths and components in `apps/api/internal/apidocs/openapi.version.json`; `go run ./cmd/openapi --check` fails when the contract changes without a version bump. The contract itself is unchanged from M5.1.
- Go toolchain 1.27.1. Base images are supported versions pinned by digest: `golang:1.27.1-alpine3.24` and `alpine:3.24.2` for the API, `node:24` and `nginx:1.30.5-alpine` for the web image. The API image sets `PORT=8000`.
- CI: actions pinned to commit SHAs, read-only default permissions, checkouts without persisted credentials, `go mod tidy -diff`, `go vet` and the OpenAPI check before tests, and image smoke tests (the API image must not run as uid 0; the web image must serve `/`, `/nginx-health` and `/subscribe/*` headers) before push. Dependabot updates actions and base images weekly.
- Signup forms get a Delete button; the per-form signup rate limit is keyed on the form UUID.
- The Domains DMARC panel explains that the default record has no `rua`, so no aggregate reports arrive until you add one.
- A least-privilege IAM policy, an accurate startup banner, and rewritten README and operator guides.

### Fixed

- Request validation accepts plus addresses such as `name+tag@example.com` and other valid addresses that GoFrame's `email` rule rejected (`28a7f1e`, found in live testing). This affects every endpoint that validates an email field, including compose.

### Not changed

- The web image's nginx master process still runs as root; the workers run as the `nginx` user. A fully non-root web image is deferred.

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
