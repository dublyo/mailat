# Automatic DMARC setup — approved October 4, 2026

## Goal

Include DMARC in new-domain setup without overwriting policies used by existing mail providers. The user approved adding `v=DMARC1; p=quarantine;` only when no applicable policy exists, preserving valid existing policies, and flagging conflicts.

## Scope

- Mailat SES domain onboarding, DNS verification, Cloudflare setup, and domain UI.
- Apply the approved missing-policy default to the newly added `vaybcode.com` after live inspection.
- Preserve root MX/root SPF, existing receiving configuration, other domains' policies, and all other Portainer stacks.
- No mass rewrite of existing domains or automatic reporting-mailbox creation. The separate delivery-feedback onboarding gap is recorded in the live validation report and is outside this DMARC change.

## User flow

1. Add a domain or open its sending setup.
2. Mailat checks the current DMARC records and applicable inherited policy.
3. Show an existing policy as preserved, including its source and effective policy. Existing `none`, `quarantine`, and `reject` are all valid configured states.
4. When no policy applies, show the approved quarantine record. Cloudflare setup creates it only after checking again on the server.
5. Invalid, multiple, delegated/unresolved, or unreadable records show an actionable explanation and prevent automatic creation.
6. Manual DNS users get a separate current-status check and copyable record when appropriate. Bulk BIND exports remain DMARC-free because imports cannot enforce conditional creation.

## Requirements

- Keep one DMARC policy at a given owner name; do not concatenate policies or append a second record.
- Preserve existing values, reporting recipients, alignment modes, subdomain settings, and extension tags verbatim.
- Quarantine is the user-approved default for missing policy. Explain that it affects all senders using the domain; preserving MX/SPF cannot establish that every other sender authenticates correctly.
- Do not invent `rua` or `ruf` recipients.
- Do not treat lookup errors as evidence of absence or rely on stale browser status for a write.
- Support bounded discovery of inherited policies, including subdomain and public-suffix policy boundaries under RFC 9989.
- Never overwrite CNAME delegation with TXT records. Resolve valid delegated policy for status where possible, and preserve the delegation.

## Frontend and UI/UX

Keep the approved Mailat Tailwind style. Add a compact DMARC status component used in the expanded domain card and sending wizard. Show checking, missing, preserved, inherited, needs-review, and failed-check states with a refresh action, policy hostname/value, and explanation. Offer copying the new default only after a successful missing-policy check. Keep DMARC status separate from the existing three SES sending checks and update all obsolete manual-only copy. Use accessible buttons and announced loading/error/status text; preserve keyboard operation.

## Backend and APIs

- Add an authenticated organization-scoped `GET /api/v1/domains/:uuid/dmarc` inspection endpoint using the existing response wrapper.
- Inspection response: `status` (`absent`, `existing`, `inherited`, `conflict`, `unknown`), `hostname`, `policyHostname`, `value`, `policy`, `reason`, `checkedAt`, `canCreate`, `verified`, `suggestedValue`.
- `suggestedValue` is `v=DMARC1; p=quarantine;`. Optional text fields may be empty when unknown.
- Keep the existing Cloudflare write endpoint. Reinspect DNS and all provider pages immediately before deciding; preserve valid provider records and refuse ambiguous/read-failure cases. Serialize competing Mailat writes for a domain as needed; report external races without deleting records.
- Replace DMARC prefix-only verification with record-set validation. Valid existing/inherited policies count as configured even when different from the suggested value.
- Update generated DMARC suggestions consistently across SES setup; do not depend on old stored `p=none` suggestions for the create-if-absent default.

## Database

Use the existing domain DNS verification fields, actual value, and check time. No migration is required. Do not overwrite the published policy merely to match a stored expected value.

## Edge cases and security

Cover unrelated TXT records, split TXT values, quoted provider contents, trailing semicolons, multiple policies, invalid or duplicate recognized tags, unknown extension tags, delegated records, inherited `sp`/`p`, DNS timeouts/SERVFAIL, pagination, provider permission errors, concurrent setup, and repeated setup. Bound DNS work and network requests. Verify organization ownership before inspecting a domain. Keep tokens out of logs. No external report destinations are added.

## Testing

- Backend parser/discovery, provider and service regressions for absent/create, repeat/preserve, alternate valid policy/preserve, invalid/conflict, inheritance, delegation, read failure/no write, pagination, concurrent setup and root-record preservation.
- Frontend state rendering, status/refresh handling, conditional copy, and unchanged safe export behavior.
- Run Go race tests with the established disposable PostgreSQL fixture, frontend tests, typecheck and production build.
- Use browser fixtures to inspect the new states locally. After CI and Mailat-only deployment, verify live existing-policy preservation and add/check the missing `vaybcode.com` policy. Confirm root MX and existing `vayb.dev` setup are unchanged.

## Implementation steps

1. Implement the DMARC inspector, validation and API response.
2. Integrate safe Cloudflare setup and DNS verification.
3. Add the reusable status UI and update setup instructions/docs.
4. Run local tests and independent review; fix regressions.
5. Push to GitHub, verify CI, deploy only Mailat, and record live evidence.

## References

- https://www.rfc-editor.org/rfc/rfc9989.html
- https://docs.aws.amazon.com/ses/latest/dg/send-email-authentication-dmarc.html
- https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Attributes/aria-live
