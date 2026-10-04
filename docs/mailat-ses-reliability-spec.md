# Mailat SES mailbox implementation specification

Date: 2026-10-03

## Authority and goal

The user confirmed that the unified inbox receives addresses on their own domains through AWS SES, approved the audit recommendations, and then explicitly instructed implementation, local testing, GitHub publishing, and Portainer redeployment. This document records that approved scope and the implementation contracts before source changes.

Mailat must provide a dependable self-hosted web mailbox for many verified domains and addresses. A signed-in user sees all their authorized identities in one inbox, can narrow the view, manage messages, and send from an authorized address on a verified domain. “Unlimited” means no artificial application subscription cap in the self-hosted default; AWS quotas, message-size restrictions, and infrastructure capacity still apply.

## Scope

- Work in the current `dublyo/mailat` repository (`mailat/`). The initial local `until-email/` audit copy is older; preserve newer Mailat branding, registration restrictions, n8n integrations, and installation behavior while integrating fixes. Keep Go, Vue, Tailwind, PostgreSQL, Redis, SES, and S3. Do not rewrite the stack or modify the separate Mailat hosting/billing control plane.
- Correct SNS authentication, receive retries, multiple recipients, MIME decoding, attachment storage, and second-domain setup.
- Complete the SES mailbox lifecycle: drafts, compose attachments, authorized sender aliases, durable send attempts/Sent copies, delivery events, and reliable management actions.
- Improve inbox loading, search, filters, bulk actions, navigation, and mobile layout; preserve the existing visual language unless the user supplies an alternative design.
- Make fresh installation and upgrading reproducible, with schema migration, explicit configuration, an MIT license consistent with the README, and tests.
- Preserve existing mail, credentials, settings, and unrelated working-tree changes. Keep legacy JMAP support available; SES mailbox functionality must not require Stalwart.

## User flow

1. Administrator installs the application using PostgreSQL and Redis and configures SES/S3 for a supported receiving region.
2. User registers or signs in, adds a domain they control, and verifies its DNS and SES identity.
3. User adds explicit addresses and optionally one catch-all per domain. Additional verified domains reuse receiving infrastructure with the correct region.
4. Inbox initially lists compact summaries across that user's identities, newest first. Body and attachment details load when a message opens.
5. User searches or filters by identity, unread/read, starred, attachment presence, sender, and dates. Active filters are visible and can be cleared. Selection applies to the displayed page, with an explicit count.
6. User reads, stars, archives, moves to spam/trash, restores, or permanently deletes selected messages. Failures leave recoverable UI state and an actionable error.
7. Compose accepts an identity or an authorized alias on its domain. Replies use Reply-To where present and prefer the actual receiving alias. Reply All preserves To/Cc and excludes the user's addresses. Forward preserves content and attachments without reply-thread headers.
8. Drafts save and reopen from the same mailbox. Sending records a durable attempt and Sent copy; retries with the same submission key do not blindly resubmit an already accepted or uncertain send.
9. Delivery, bounce, and complaint events update status and suppression data. UI distinguishes provider acceptance from confirmed delivery and does not invent healthy metrics for absent data.

## Requirements

- Per-user mailbox authorization applies to list, counts, detail, attachment downloads, bulk operations, drafts, and sender selection.
- An inbound delivery is stored for every matching recipient identity. Multiple aliases mapped to one catch-all must not duplicate the same message in that identity. Explicit identities take precedence over catch-all.
- SES receipt identity and recipient associations determine deduplication; an attacker-controlled RFC Message-ID alone must not suppress unrelated mail.
- SNS acknowledgment occurs only after durable processing. Failed transient processing returns a retryable error. Partial retries must complete missing recipients without duplicating completed ones.
- MIME processing supports nested multipart, quoted-printable/base64, common charsets, HTML/text alternatives, and inline/normal attachments, with bounds on size and nesting.
- External-send uncertainty must be represented explicitly; do not claim exactly-once delivery across an SES network timeout.
- Self-hosted application limits default to zero/unlimited with documented semantics. Explicit positive operator limits remain enforceable and account-scoped. Remove the invented monthly-limit-divided-by-30 restriction.

## Frontend and UI/UX

- Retain current Vue/Pinia/Tailwind components and branding. Use semantic buttons/inputs, labels, visible keyboard focus, useful disabled states, and accessible busy/error messages following MDN guidance.
- Cancel or ignore obsolete requests. Debounce search; clearing search must immediately reset the query and page. Apply search to All Identities and individual identities consistently.
- Keep prior rows visible during background refresh; show a skeleton only for an uncached initial view. Avoid duplicate initialization and SSE fetch storms. Invalidate caches after mutations and logout; never reuse one user's results for another.
- Render mobile rows without a desktop minimum table width. Mobile sidebar overlays and closes on navigation; message reading and compose remain usable at 390px width.
- Implement a real filter panel, active filter count, clear-all action, accurate zero-result wording/range, page navigation, and bulk controls for mail management.
- Draft state and attachment upload progress/errors must be visible. Prevent accidental duplicate sends and loss of a failed draft save. Use the existing rich-text dependencies where useful rather than introduce a new editor stack.
- Restore shared layout on Automations. Correct misleading counts/health where backed by available data.

## Backend

- SNS: constant-time secret check against the configured organization/topic, strict signature verification, HTTPS AWS certificate validation, bounded fetches, no untrusted redirects, and safe subscription confirmation. Validate topic and configured S3 storage before processing.
- Receiving: parse once, persist per-identity copies/associations transactionally, store attachment bytes privately in S3, apply only the receiving user's filters after body parsing, and emit SSE after commit.
- Inbox: bounded page sizes and stable ordering; list query excludes full bodies; server-side filters and indexed ordering; all counts and mutations share the same ownership checks. Fix trash placeholders and mutually consistent folder flags on restore/move.
- Sending: authorize domain and can-send permission, validate headers and recipient limits, resolve only user-owned attachments, persist submission state before contacting SES, save the successful content to Sent, and handle ambiguous results without automatic duplicate sending.
- Drafts: PostgreSQL-backed for SES, with full recipients/body/attachment references. Preserve legacy JMAP paths for SMTP use where applicable.
- Events: idempotent status updates and bounce/complaint suppression; transactional idempotency keys scoped to organization and workers must not resend terminal messages.

## Database

- Use additive, versioned SQL migrations and an initial schema suitable for an empty database. Track app migrations, take an advisory lock, and stop startup on migration failure.
- Existing `received_emails` remains the mailbox copy table to avoid a destructive rewrite. Add envelope-recipient metadata and a provider-delivery-plus-identity uniqueness rule for incoming deduplication; retire global RFC Message-ID uniqueness.
- Add fields/tables required for durable compose submissions and attachment uploads. Draft and Sent records use the existing mailbox views and ownership joins.
- Add indexes for identity/folder/time/id and filters that materially improve the listing queries. Do not assume an index helps without inspecting the resulting query plan in an isolated test schema.
- Align Prisma documentation with actual SQL columns, including organization identity limits. Existing installations must migrate without deleting or reassigning mail.

## APIs

- Preserve `/api/v1/inbox/received`, detail, counts, mark, star, move, and trash paths, extending list filters through validated query parameters.
- Preserve plural `/api/v1/compose/drafts` and UUID/id update/delete routes. Frontend and backend must agree on recipient arrays, HTML/text, attachments, draft identifier, sender alias, and submission key.
- Attachment upload returns an opaque owned reference; received/download routes authorize the requesting user and use short-lived access or controlled streaming.
- Add the missing `PUT /api/v1/identities/:uuid` update route and persist catch-all changes. SES identity creation must not require a Stalwart password.
- Document actual contracts after integration; remove misleading success from unimplemented actions.

## Edge cases and security

- Cover empty inbox, zero matches, last-page deletion, rapid folder/query changes, failed network refresh, expired login, concurrent mutations, and SSE reconnect.
- Cover two domains, multiple identity recipients, several aliases on one catch-all, duplicate/reordered SNS notifications, malformed MIME, empty/HTML-only bodies, large attachments, invalid From aliases, and cross-user access attempts.
- Prevent attachment reference theft, CRLF/header injection, unsafe HTML rendering, arbitrary SNS URL fetching, and cross-organization idempotency collisions.
- Keep secrets out of output, logs, tracked files, commits, and screenshots. Existing local notes may contain credentials and are excluded from the implementation commit.
- Permanent deletion must respect raw/attachment objects referenced by another mailbox copy. Storage cleanup must be retryable or documented if deferred; deleting one recipient's copy must not break another's attachments.

## Testing and acceptance

- Focused Go tests for signature/URL/header validation, recipient matching, MIME decoding, deduplication, filters, authorization, drafts/send state, quotas, and folder transitions.
- Run Go test/build and Vue TypeScript/build. Use isolated schemas for migration and SQL integration tests; never seed or truncate production tables. The configured hosted PostgreSQL was unreachable, so the approved local-testing stage used disposable loopback PostgreSQL, including a private snapshot upgrade rehearsal.
- Test frontend behavior with controlled API fixtures: slow/out-of-order requests, clear search, filter combinations, bulk actions, draft save/reopen, send errors, attachments, and desktop/mobile layouts. Fixtures are not live delivery evidence.
- Verify a fresh schema and an upgrade schema, then rerun migrations to prove idempotency. Test recipient isolation and preserved existing mail.
- Inspect Portainer deployment configuration read-only, retain its network/env/volume settings, back up the database before applying live migrations, and publish versioned images tied to the tested commit.
- GitHub must build and test before publishing. Automatic SSH deployment on an ordinary push must not bypass the requested Portainer stage.
- After Portainer rollout, check container health, migration version, UI/API availability, login/inbox/folders/filter behavior, and version identity. Sending a real test email requires a user-authorized test recipient; do not send messages to arbitrary contacts as part of a smoke test.

## Implementation steps

1. Record baseline and this spec, settle UI reference, and coordinate schema/API contracts.
2. Implement receiving/security and inbox backend, compose/mail lifecycle, and frontend improvements in separate file ownership areas.
3. Integrate identity/domain rules, configuration limits, schema migrations, installation guidance, and deployment workflow.
4. Run local unit/build/integration/browser checks; resolve failures and review final diffs for secrets and unrelated edits.
5. Commit and push the tested scope, wait for CI/images, then update the existing Portainer stack using versioned images after backup and config review.
6. Run live smoke checks and report evidence, any unverified external-delivery conditions, and rollback instructions.

## References

- https://developer.mozilla.org/en-US/docs/Web/API/AbortController
- https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Attributes/aria-busy
- https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html
- https://docs.aws.amazon.com/ses/latest/dg/creating-identities.html
- https://docs.aws.amazon.com/ses/latest/dg/manage-sending-quotas.html

## Approved onboarding amendment: preserve existing mail providers

The user explicitly requested that Mailat not add a root-domain MX because a domain may already use another mail service. This refines SES onboarding without changing the existing inbox architecture.

- **Goal and flow:** default SES setup configures sending authentication. Receiving is a separate deliberate choice. Keep current Tailwind components and explain the distinction in the DNS wizard and receiving controls.
- **Backend and APIs:** SES registration/re-registration must not generate the domain-apex receiving MX. Cloudflare automatic synchronization must skip an apex MX even if an older stored DNS row contains it. The skip result must be visible to the caller. Preserve the required custom MAIL FROM MX on `bounce.<domain>`.
- **Database and existing installations:** no mail migration or automatic live-DNS cleanup. Previously configured receiving domains continue to operate. The user explicitly chose to keep `vayb.dev`'s tested receiving setup, including its existing apex MX. No live DNS changes are part of this amendment.
- **Receiving choices:** use the existing provider's forwarding to a separately configured receiving address/subdomain, or deliberately route the selected receiving domain to SES. Mailat cannot receive every root-domain message through SES while its root MX independently routes all mail to another provider without that provider's forwarding.
- **Edge cases and security:** protect legacy stored apex MX records during automatic synchronization, distinguish MAIL FROM MX from inbound MX, and report conflicts with existing authentication records instead of silently disrupting another sender/provider. Preserve root SPF and existing DMARC; automatic setup skips these shared policies. New SES guidance uses a monitoring-only DMARC suggestion for manual review, never an automatic policy change.
- **Testing:** prove fresh/repeated SES setup excludes root MX, preserves ownership TXT and bounce MX, and automatic Cloudflare sync performs no apex-MX creation or update. Verify the frontend explains skipped/manual records accurately; use fake provider APIs for mutation tests.
- **Implementation:** update SES record generation, enforce the DNS write guard, update the wizard/receiving explanation, run focused regressions and frontend build, then publish and deploy only Mailat after CI succeeds.
