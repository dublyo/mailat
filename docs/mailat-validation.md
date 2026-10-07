# Mailat validation evidence

## Automatic DMARC follow-up — October 4, 2026

- Implements the user-approved [DMARC setup specification](mailat-dmarc-spec.md): conditional quarantine creation, preservation of existing/inherited policies, and explicit review of ambiguous or unreadable DNS.
- **45 frontend tests passed**, and `npm run build` passed Vue typechecking and the Vite production build.
- A production-build browser fixture confirmed the missing-policy loading state and copy feedback, then simulated an externally added `p=reject` policy with reporting/subdomain tags. Refresh removed the stale copy controls while checking and displayed the preserved policy without offering another record.
- The same browser run confirmed inherited source/effective policy display, duplicate-policy review, and failed-lookup states. Conflict/failed lookups exposed no create suggestion. Browser console reported no warnings or errors.
- Bulk export regression coverage confirms that DMARC owner names, including malformed/delegated records, cannot be included in unconditional DNS imports.
- A regression also confirms that a successful Cloudflare result suppresses duplicate manual-copy suggestions while public DNS still returns cached absence, including when the wizard closes during the write.
- Full backend `go test -race -count=1 ./...` passed with disposable PostgreSQL. Tests include concurrent setup of the same hostname across two organization rows producing exactly one DMARC POST, and other-organization inspection producing zero DNS queries. The disposable cluster was stopped and removed.
- A read-only probe using the real DMARC inspector and system resolver found `vaybcode.com` absent/eligible for creation, while `vayb.dev` preserved its verified quarantine policy and original reporting address. This confirms real resolver behavior before the live DMARC write.
- These UI checks used fake loopback-only records and did not modify live DNS or send email. CI/deployment evidence is recorded separately after release.

Date: 2026-10-03. This report records checks run by the implementation agents and release owner. It does not certify deployment or live email delivery. Final integration checks below used the canonical `mailat` checkout.

## Final local integration and browser acceptance

- Canonical `apps/api`: `go test -race -count=1 ./...` passed across all packages (32 top-level tests, excluding nested subtest counts) with disposable PostgreSQL, including the final independent compose receipt table. Fresh schema and legacy schema migration/repeat tests passed.
- Restored the private Mailat production backup into a disposable local database, ran all migrations twice, and read every existing Inbox detail, list, counts, and health summary through the new services. All 16 existing messages were preserved and received 16 per-identity replay markers; no production database write occurred during this rehearsal.
- Prisma schema validation passed. Versioned Go SQL migrations remain authoritative, including partial indexes and receipt triggers.
- Tested the production frontend build in Chromium using isolated API fixtures: search and clear; combined domain/unread filters (10 correct matches); selection/archive/restore; lazy detail; binary attachment download; Reply-To, reply-all Cc and actual catch-all alias; draft save/close/reopen preserving content; uncertain send checks reusing one key and identical payload; terminal delivery result closes compose.
- Desktop inbox at 1440px uses 62px rows. Mobile at 390×844 has no horizontal page overflow, navigation closes after selection, and composer fits within the viewport. Screenshots were visually reviewed.
- Additional browser checks confirmed a reopened reply draft retains In-Reply-To/References; an uncertain attempt followed by a simulated HTTP 400 still retains its original key, frozen payload, and disabled editor. A rejected attachment draft was then explicitly retried from its Outbox copy: a new key, no consumed draft identifier, current attachment references, and no forward threading.
- Browser fixtures do not prove real SES/S3 permissions or external delivery. Mock send checks sent no email.
- Production Compose parsing passed with `.env.production.example`; default services are API/web/Caddy and the optional `stalwart` profile adds Stalwart. No containers were started by this validation.
- Captured the existing Mailat stack configuration, old image digests, and a verified custom-format PostgreSQL backup privately outside the repository before deployment.

## Frontend

### Realtime follow-up after live onboarding

- The live SSE endpoint delivered `connected` in 0.18 seconds and its heartbeat at 30.18 seconds. The reported stale inbox was traced to missing reconciliation after a connection gap, not demonstrated proxy buffering.
- Inbox now catches up after initial connection/reconnection, tab visibility/focus return, and network recovery. Visible tabs reconcile every 60 seconds even with a healthy stream; disconnected fallback runs every 15 seconds. Failed EventSource instances close before the client schedules its own retry.
- **31 frontend tests and the production build passed.** Added regressions cover missed events, reconnect bursts, hidden-tab return, periodic fallback, events arriving during refresh, stale unread-count requests, and retired connections. Existing auth/account-isolation and compose regressions remain green.
- A production-build Chromium test used a loopback-only HTTP/SSE fixture: severed the stream after adding a message without emitting its notification. Reconnection fetched and displayed that message automatically, retained all three URL filters, and preserved the selected existing row. A subsequent completed browser check kept the stream healthy and added a message without any SSE event. The inbox fetched it automatically at 59.998 seconds from initial loading, preserved all three filters and the selected baseline row, and retained one continuous SSE connection.

- `cd apps/web && npm test`: **24 passed, 0 failed** in canonical `mailat` after the security, reply-recipient, login-error, metadata-account-isolation, and final compose retry/threading changes.
- `cd apps/web && npm run build`: **passed** (`vue-tsc --noEmit` and Vite production build), in canonical `mailat`, including compact desktop rows, incoming-status cleanup, auth/metadata isolation, and the final compose retry/threading follow-up.
- Regression tests exercise unified-identity search/clear, obsolete-response protection, in-flight request deduplication, cache return, post-mutation forced refresh, detail races, unified counts, failed-mutation selection preservation, account isolation, Reply-To/Reply All, envelope-alias sender selection, and subject prefixes. Five additional regressions verify inline login 401 errors with redirect preservation, protected API 401 expiry handling, late list responses after logout, old-account success/failure isolation, and pending creates after logout even when a token is reused.
- Five final compose regressions cover reply-draft threading retention, forward threading exclusion, retry hydration from a confirmed failed Outbox copy with current attachment UUIDs and no consumed draft, rejection of incomplete/uncertain retry copies, and retention of uncertain submission keys after later client errors.
- Reply All excludes exact owned identities and the delivered copy's envelope aliases; teammates on the same catch-all domain remain recipients. The receiving agent confirmed envelope aliases are scoped to that row's routed identity.
- These Node tests use controlled API fixtures. They do not prove browser layout, accessibility, real SSE transport, composer autosave interaction, attachment rendering/downloads, or real send delivery. Browser acceptance and final canonical/CI validation are recorded separately by the release owner.

## Backend agent checks

Receiving agent:

- `go test ./internal/service ./internal/controller ./internal/provider -count=1`: **all three packages passed** with `MAILAT_TEST_DATABASE_URL` pointing to the release owner's disposable local PostgreSQL.
- After the in-flight-send deletion guard, `go test ./internal/service -run 'TestReceived|TestSESEvent' -count=1`: **passed**.
- Integration coverage includes multiple recipients/two domains/catch-all aliases; user and organization isolation; list filters and bulk authorization; restore; shared attachment cleanup; receive-deletion tombstone replay; empty messages/missing RFC Message-ID; event correlation/order; repeated bounces and suppression isolation; rejection of deletion while sending.
- SNS tests cover signature versions 1/2, tampering, certificate/URL restrictions, transient certificate-fetch classification, and configuration-set `eventType` normalization. Receiving setup preservation uses a fake AWS SDK.

Compose agent:

- `go test -race -count=1 ./internal/provider ./internal/service ./internal/controller ./internal/worker`: **29 tests passed across four packages, with no races reported** using disposable local PostgreSQL, fake SES, and in-memory S3.
- Coverage includes draft save/update/version conflict, attachment ownership, stable UUIDs, durable send/Sent state, retry and uncertain outcomes, concurrent duplicate submissions, transactional organization/payload idempotency, sender ownership, authorized aliases, and worker no-repeat behavior for accepted/rejected/unknown outcomes.
- The final race-enabled run includes same-key replay after permanent deletion of delivered Sent and unknown Outbox records, with no extra fake-provider calls. A crash during an in-flight send can still leave `sending`; automatic resend is intentionally avoided and operator review is required.

The hosted database connection attempt timed out before test schemas were created. Local integration schemas were temporary and removed by the test harness. No live SES or S3 operation, real SNS round trip, production migration, or end-to-end delivery is claimed by these checks.

## Health follow-up from live smoke checks

The first live rollout exposed outdated frontend metric field names. The follow-up binds the actual API totals and warning actions, replaces the unsupported received-today card with the recorded read count, and computes virus verdict totals in the backend. A PostgreSQL regression checks date/direction scoping and zero-valued JSON fields; three rendered-view tests cover zero, nonzero, and missing metrics. The targeted backend race test, all 24 frontend tests, and production build passed.

## Dependency audit

Compatible security updates were applied without `npm audit fix --force` or major framework upgrades: DOMPurify **3.4.16**, TipTap **3.31.4**, Axios **1.20.0**, and compatible transitive fixes. The audit is a point-in-time advisory check, not a proof of complete security.

- `npm audit --omit=dev --json`: **0 reported vulnerabilities**.
- Full `npm audit --json`: **7 findings: 6 high, 1 moderate**, down from 28. Remaining findings are in the development/build toolchain: Vite/esbuild and Tailwind/chokidar/fast-glob/micromatch/braces.
- The remaining esbuild development-server advisory is [GHSA-67mh-4wv8-2f99](https://github.com/advisories/GHSA-67mh-4wv8-2f99); the braces stack-exhaustion advisory is [GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm). Vite also has filesystem/path-handling advisories reported by the audit. npm proposes major Vite/Tailwind upgrades, which were outside this compatible-update pass.
- These packages remain relevant to developers and build environments even though they are absent from the production dependency audit. Keep development servers private; do not represent the full audit as clean.

## Canonical route/auth integration review

Read-only source review of the canonical checkout found no introduced route/auth integration regression:

- `src/main.ts` installs Pinia before the router; the route guard awaits initial auth restoration before protected navigation.
- `/`, successful login, and successful registration resolve to the SES inbox; legacy JMAP retains its separate route.
- Upstream Login/Register still use `GET /api/v1/auth/register-status`; the API helper and first-admin-only backend registration behavior remain present. Backend registration remains the authority when the status request fails.
- Logout clears received-mail caches/requests, closes compose, and clears domain/identity lists. The global composer is lazy and initially closed.

The review found and resolved two pre-existing issues:

- Login HTTP 401 responses now reach the form as inline errors without navigating away or losing the redirect query. Protected API 401 responses still clear the expired token and return to login.
- Domain/identity operations now guard state writes and follow-up loads by account token and reset generation. Logout invalidates pending operations, including mutations, so old metadata/errors cannot overwrite another account's state.

These fixes have controlled regression coverage; live auth behavior and concurrent first-admin registration were not exercised by this agent.

## Release evidence still required

Record final canonical tests after all agents' edits, desktop/mobile browser acceptance, immutable image/commit identity, backup/migration outcome, Portainer rollout health, and post-rollout smoke checks separately. A real external delivery check also requires an explicitly authorized test recipient. No guessed production results are included here.

## Existing-provider DNS onboarding follow-up

- SES create/re-registration no longer generates root MX/SPF. Legacy SES root records are excluded from sending verification/instructions without changing DNS. SMTP manual instructions are retained.
- Automatic Cloudflare setup skips root MX/SPF and DMARC policies, checks required records before creation, preserves identical records, and reports conflicts without update/delete operations. New manual DMARC suggestions use `p=none`; existing policies are never rewritten.
- Sending previews/import files exclude SES root MX/SPF and DMARC. Receiving is a separate explicit flow explaining forwarding or a dedicated receiving subdomain.
- **36 frontend tests and the TypeScript/Vite production build passed**, including legacy DNS filtering, SMTP compatibility, and five distinct Cloudflare result states. A local production-build browser smoke confirmed that legacy root MX/SPF and enforcing DMARC records were absent from the SES preview, bounce MX/SPF remained visible, and receiving options opened without creating infrastructure. No browser warnings/errors were reported.
- The user explicitly chose to preserve the tested `vayb.dev` apex MX. No live DNS mutation is part of this follow-up.
- Final backend verification: `go test -race -count=1 ./...` passed with disposable PostgreSQL 16. Focused provider/service suites also passed. Fake Cloudflare cases covered existing MX/SPF/CNAME conflicts, duplicate SPF, proxied DKIM, failed reads, later-page conflicts, and the service boundary ensuring either MAIL FROM conflict blocks both pair members. Both exact disposable clusters were stopped/removed; the existing local PostgreSQL service was left untouched.

## Release status record: M2–M5.1 and M6 docs, October 7, 2026

Recorded by the M6 handoff agent. Only facts checked on 2026-10-07 are listed; nothing here deployed, pushed or sent mail.

**Pushed and built.** `origin/main` is `f696e552028e6e53117a35fca46a02afde5dbab3` (M5.1). GitHub Actions run 37617787008 for that commit completed successfully. Anonymous GHCR manifest requests for `ghcr.io/dublyo/mailat-api:sha-f696e552028e6e53117a35fca46a02afde5dbab3` and the matching `mailat-web` tag both returned HTTP 200. Earlier milestone runs: M5 37559582969 and M4 37529503767 succeeded; M3 run 37510982737 failed and the next push (run 37511808158) succeeded.

**Live instance (read-only, 2026-10-07T14:13Z).**

- `GET /api/v1/health` on the production instance: `healthy`, PostgreSQL `ok`, Redis `ok`, version `f696e552028e6e53117a35fca46a02afde5dbab3`.
- `GET /api/v1/openapi.json`: 279 operations, including the M5.1 `/api/v1/org/mailboxes/...` routes; `info.version` is still `2026-10-04` because the date-based contract version is an unpushed M6 change.
- The web root's `last-modified` (12:10:15 GMT) matches the `mailat-web` build time for f696e55. This is an inference, not a digest comparison.
- Not verified by this record: who performed the rollout from `18b8dcc` to `f696e55`, the pre-upgrade backup, the `mailat_schema_migrations` ledger (015–017 expected once), browser workflows, signup/campaign/forward mail behaviour, and response headers on `/subscribe/*`.

**Local M6 tree.** Local `main` at `da71cd4` (14 commits ahead of `origin/main`: 13 M6 commits plus `28a7f1e`, which accepts plus addresses such as `name+tag@example.com` in request email validation and was found in live testing; clean tree, `scripts/` and `examples/` untracked):

- `go build ./...`, `go vet ./...` and `go run ./cmd/openapi --check` ("OpenAPI matches routes and DTOs") passed.
- `go test -race -count=1 ./...` with a disposable local PostgreSQL (`MAILAT_TEST_DATABASE_URL`) passed in every package that has tests.
- `apps/web`: `npm test` 190 passed, 0 failed; `npm run build` (vue-tsc and Vite) passed.
- Docker image builds, the image smoke steps (API non-root uid check; web `/`, `/nginx-health` and `/subscribe` headers) and Caddy validation were not run locally (the Docker daemon was off); CI is the build proof once pushed. The web nginx master still runs as root (workers run as `nginx`); the spec's non-root web image was deferred, and CI checks no web uid.

**Update: M6 pushed (2026-10-07).** After the record above, review fixes `ad01286` (keep the `appuser` account in the web image, no `USER` line) and `dd71fdc` (CHANGELOG.md, docs reconciliation) and `c017fd8` (Mailboxes sidebar item and `/mailboxes` page with a domain switcher; not M6 scope) were committed. `origin/main` is now `c017fd8a0e7e5f970b55f28f05aedca3e3d4c0f6`; GitHub Actions runs 37647725133, 37647740577 and 37647799060 for that commit succeeded. Anonymous GHCR manifest requests for `sha-c017fd8a0e7e5f970b55f28f05aedca3e3d4c0f6` returned `mailat-api` `sha256:db59303e0c9aecd9a778b401b3ad8bba1ab8960b14d7b3801e744c73074f0927` and `mailat-web` `sha256:af4cc00635e739379b18aa06f5a345a9ae872581528cd0f463c8055bcac5fb7f`. The production instance still runs `f696e55`; nothing was deployed.

**Still required before an R1 record can be written.** Owner approval, push and green CI for the M6 head, `sha-<full>` images, the backup and isolated rehearsal, the stack 5 tag change, ledger and row-count verification, the post-deploy checklist and a 30-minute watch (runbook in `MAILAT-AI-HANDOFF-2026-10-07.md`, section 10.1, outside this repository).
