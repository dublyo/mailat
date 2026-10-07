# External automation implementation and validation — 2026-10-04

The approved local implementation is complete. This report describes local
verification, not a production release. No GitHub push, Portainer redeploy,
Cloudflare change, real email or AWS provisioning was performed in this phase.

The subsequent approved [DMARC Reports addition](dmarc-reports.md) reran the
combined API suite (95 tests plus 163 subcases) and frontend suite (64 tests).
The results below retain the original automation acceptance snapshot.

## Delivered

- Explicit API-key route scopes, ownership, expiry, shared request limits and
  human-only credential/security administration. Persisted sessions, logout and
  password revocation, enforced TOTP login, one-use challenges and single-use
  SSE stream tickets (a fresh 60-second ticket for every connection).
- Durable versioned HMAC webhook events, transactional outbox, connection-time
  SSRF checks, retry/lease recovery, dead letters, replay, secret rotation and
  truthful receiver outcomes in Settings → Integrations.
- Stable single/batch idempotency, attachments through queued sends, public UUID
  reply/forward context, unified Sent and explicit ambiguous outcomes.
- Sending-only private storage and feedback setup/readiness, independent of
  receiving and root MX. Existing provider feedback topics remain protected.
- Non-mutating message reads, label CRUD/assignment, SES filter CRUD/test, legacy
  rule migration/compatibility, commit-ordered change cursors and tombstones.
- Embedded generated OpenAPI for 180 paths / 237 operations, searchable Mailat
  documentation, corrected examples, route/reference drift tests, and repaired
  JavaScript, Python and Go SDKs. CI includes SDK regressions.
- Importable and executed n8n incoming-mail and send/batch workflows with durable
  receipt state. Removed misleading integration buttons and placeholder SMTP
  connection instructions.
- Additional defects caught by acceptance: nullable legacy domain/identity
  metadata, SMTP create/reload inconsistency, malformed legacy filter data,
  regex handling and concurrent label rename/assignment.

## Final local checks

| Check | Result |
| --- | --- |
| Go API `go test -race -count=1 ./...` with isolated PostgreSQL | 83 top-level tests + 109 subcases passed; zero failures |
| Opt-in n8n fixture in default Go suite | Intentionally skipped there; executed separately below |
| Frontend `npm test` | 53 passed, zero skipped/failed |
| Frontend `npm run build` | Vue typecheck and Vite production build passed |
| API Linux build | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/server` passed |
| JavaScript SDK | 2 tests passed; clean npm install, typecheck, CJS/ESM/declaration builds and CJS import smoke passed |
| Python SDK | 2 tests passed after an actual editable package installation |
| Go SDK | 2 tests passed with `-race` |
| OpenAPI | Regeneration/staleness check, references, unique operation IDs, route parameters, all curated UI links and 10 strict DTO example cases passed |
| Whitespace | `git diff --check` passed |

HTTP acceptance uses the actual application router, middleware and controllers
against migrated disposable PostgreSQL. It creates labels and filters through
HTTP, ingests a message through SES receiving with a mock S3 endpoint, verifies
the filter's folder/label action, checks unread preservation and cross-user
denial, exercises cursor and malformed-input responses, and checks generic
database-failure responses. Fresh/legacy migration, concurrent idempotency,
ownership, expired/revoked credentials, rate limiting, attachment fidelity,
out-of-order/late feedback, outbox crash/retry/replay, and cursor commit ordering
are covered by the service/controller/provider regression suites.

Native browser checks covered real login, sign-out, TOTP challenge and successful
verification, API key scope display, corrected send examples, API reference
search/pagination/schema controls, domain rendering and sending-resource
readiness, and the integration/delivery-history screen. A development-server
restart initially left mixed cached modules; a clean root-page navigation
resolved it. This was not treated as a successful UI reload until the current
controls were observed. No new production credentials were used for these tests.

## Actual n8n execution

n8n 2.41.6 was installed in a private temporary directory. The imported production
Webhook workflow ran against a real Mailat service/database fixture with mock
SES and storage; it was not merely checked as JSON.

- Correct raw-body signatures processed one received event.
- Tampered bodies and expired signatures returned 401 before any mail action.
- The workflow downloaded the private attachment, used the owned alias,
  Reply-To recipient, thread headers and inline CID context, replied, applied a
  label and archived the message while preserving unread state.
- Duplicate delivery returned success without another reply, including after an
  n8n process restart.
- The single-send and partial-batch workflow ran twice. UUIDs stayed stable;
  two valid batch items succeeded, the invalid item stayed an explicit failure,
  and total fake provider submissions remained exactly four.
- The Go fixture completed successfully under `-race` in 132.717 seconds.

The n8n fixture uses test HTTP adapters over real Mailat services. Production
controllers/authentication, SNS verification and public webhook dispatch are
covered separately by Go tests. No email left the test environment.

The test n8n processes, databases and private installation were removed. The
disposable PostgreSQL cluster and browser fixture are stopped after final checks;
the machine's existing PostgreSQL and all other application stacks are untouched.

## Release boundary and remaining live verification

The normal test suite does not execute all 237 routes as production workflows.
That number is the generated route inventory. Acceptance targets the approved
external sending, receiving and mailbox-management scope; unrelated marketing,
Stalwart, shared mailbox and WebAuthn product flows are not certified by it.

External OAuth-provider callbacks, AWS/SES/IAM behavior in the live account,
public DNS propagation, real recipient placement and feedback, published SDK
packages, multiple deployed API replicas, and a live n8n receiver still require
the controlled release smoke test. Local provider simulations do not replace
those checks.

Release changes to account for:

1. Back up the Mailat database and record the current API/web image digests.
2. Old JWTs without persisted active sessions require re-login. Unbound legacy
   API keys fail closed and need scoped replacements.
3. Existing webhook consumers must adopt the new event/signature protocol; review
   legacy webhook ownership and migrated filter warnings.
4. Deploy only the Mailat stack, verify migrations 005–008, prepare sending-only
   resources explicitly, and check SNS confirmation/readiness.
5. Use the agreed domain and recipient for live sending/receiving/retry/feedback
   smoke tests. Preserve existing root MX/SPF/DMARC and `vayb.dev` receiving.

The approved specification reserves deployment for a separately approved release
stage. The current working tree is ready for review, commit, GitHub push and that
Mailat-only release; those actions have not been performed here.

See [the integration contract and upgrade notes](api-automation.md) and
the local n8n validation instructions (not included in this repository).
