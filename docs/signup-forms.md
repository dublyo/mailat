# Signup forms

Give people a place to join your mailing list: a hosted page, an embedded form, or both. Find **Signup forms** in Mailat’s sidebar.

## Create and share

1. In **Contacts**, create a static list. New lists use **single opt-in**: a valid form submission immediately joins the list. Choose **double opt-in** to require an email confirmation first.
2. Open **Signup forms → Create form**, choose the list, and write the headline, introduction, consent wording and button label. Include your business name, the type of updates and frequency in the consent wording.
3. Optionally collect a first name and add an HTTPS privacy policy link. For double opt-in, choose your own verified sending identity.
4. Check **Published**, save, then copy the hosted link or iframe snippet into your website’s HTML block. No API key belongs in the embed. You can create multiple forms for one list.
5. Use **View signups** to review requests, or **Refresh** to update counts. Unpublish a form to stop new requests and pause outstanding confirmations. Changing the form’s list requires creating another form. **Delete** removes a form after you confirm its name.

Forms are visible only to the user who created them, and the `contact.subscribed` webhook event goes to that user's webhooks.

The preview uses the same component as the live page. Configuration is plain text; HTML/scripts are not accepted. The iframe is responsive, with a default height of 760 pixels. Increase its height if your introduction or consent wording is longer.

Single opt-in records a request and disclosure acceptance; it does not verify control of the email address. Double opt-in records confirmation before adding membership. Existing imported contacts are not reclassified by a list policy change.

## Confirmation and unsubscribe

Double-opt-in requests stay separate from contacts and campaign recipients. Confirmation links expire after 24 hours and can be used once. Opening the link does not subscribe anyone: the visitor must press **Confirm my subscription**. Requests already pending under double opt-in remain pending when the list’s setting changes.

A repeated signup does not overwrite a contact’s name or create another membership. Unsubscribed, bounced, complained, erased or suppressed addresses cannot be reactivated through a public form. The public response deliberately does not disclose those statuses.

Signup history describes the request, not current permission to send. Current contact status and suppressions always take priority. A campaign snapshots its audience when sending starts, so someone who joins a list afterwards is not added to a campaign already in progress. Just before each message, the sender rechecks that the contact is still active, still on the list, has the same address and is not suppressed; otherwise that recipient is skipped. Pending double-opt-in requests are never campaign recipients. Every campaign email carries a signed unsubscribe link, the organization's postal address and `List-Unsubscribe` / `List-Unsubscribe-Post` one-click headers. Unsubscribing is organization-wide and also suppresses the address, so a later signup through a public form cannot reactivate it. See [Campaigns](campaigns.md).

Successful new membership emits `contact.subscribed` through the existing durable webhook outbox, scoped to the form creator. Data includes `contact_id`, `email`, `list_id`, `form_id`, and `confirmation_mode`. IDs are UUIDs. Duplicate membership does not emit another event.

## APIs

The complete schemas appear in `/api-docs` and `/api/v1/openapi.json`.

| Method | Path | Access |
| --- | --- | --- |
| GET, POST | `/api/v1/signup-forms` | Session or API key with `contacts:manage` |
| GET, PUT, DELETE | `/api/v1/signup-forms/{uuid}` | Same; organization and creator scoped |
| GET | `/api/v1/signup-forms/{uuid}/signups?page=1` | Same; 25 records per page |
| GET | `/api/v1/public/forms/{uuid}` | Public; published forms only |
| POST | `/api/v1/public/forms/{uuid}/submit` | Public |
| POST | `/api/v1/public/forms/confirm` | Public; one-time token |

Create/update uses `listId`, `identityId` (optional for single opt-in), `name`, `title`, `description`, `consentText`, `buttonText`, `privacyUrl`, `collectName`, and `published`. List create/update accepts `confirmationMode: "single" | "double"`.

For a public submission, first fetch the public form metadata and its `challenge`, then POST JSON with `email`, optional `firstName`, `consent: true`, the challenge, and an empty `website` honeypot. Confirmation accepts `{ "token": "…" }`. Management uses full-form PUT; the original list is immutable. Deleting a form deletes its signup history and pending tokens, while existing contacts and consent audit remain.

Public JSON bodies are limited to 16 KiB. Expect 400 for validation, 404 for unavailable forms, 409 for changed/expired form challenges, 410 for used/expired confirmation tokens, 429 for rate limits, and 503 when confirmation dispatch is unavailable. Reload the form after a 409; retries remain subject to rate limits.

## Hosting and safeguards

- Migration `011_signup_forms.sql` is additive and is applied by the existing Go migration runner. Back up the database before upgrading. Set `WEB_URL` to the externally reachable HTTPS app URL, and keep `API_URL` correct for email unsubscribe headers.
- Use the updated web Nginx configuration: only `/subscribe/{uuid}` permits cross-site framing; `/subscribe/confirm` denies framing. Private SPA routes retain frame protection, and the client prevents iframe navigation into authenticated views. Custom reverse proxies must preserve those headers. See [MDN frame-ancestors](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/frame-ancestors) and [iframe accessibility](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe).
- Set `TRUSTED_PROXY_CIDRS` to your actual trusted proxy networks. The application defaults to loopback. The Docker deployment additionally trusts private container networks. Do not expose the API directly to untrusted clients on a network included in this list. Forwarded addresses are considered only from a trusted immediate peer.
- Database-backed limits apply per hour: 30 submissions per client IP, 3 per organization/email, 1,000 per form, and 60 confirmation attempts per IP. Rate-limit keys are HMAC hashes. Old buckets are removed after two days; expired pending requests are removed after 30 days. These limits and the honeypot bound basic abuse; they are not a CAPTCHA or proof of consent by the address owner.
- Confirmation tokens are random and stored only as SHA-256 digests. Public URLs use fragments to keep tokens out of HTTP request URLs/referrers. Consent wording and policy are retained in request history and the contact audit, together with the submitting client's IP address (resolved through `TRUSTED_PROXY_CIDRS`) and user agent. Contact data export includes signup history. GDPR erasure deletes signup history, pending links and consent history and keeps a hashed `erased:` suppression so the address cannot be re-subscribed; see [GDPR contact erasure](self-hosting-ses.md#gdpr-contact-erasure) for the stores it does not cover.
- Confirmation emails use Mailat’s configured transactional provider and queue. An accepted/queued send is not proof of delivery; delivery failures leave the subscriber pending. No welcome campaign is sent automatically.

## Local acceptance

Integration tests use randomly named, disposable PostgreSQL schemas when `MAILAT_TEST_DATABASE_URL` is explicitly set. They cover single/double opt-in, actual HTTP routing and API scopes, a loopback SES MIME capture, duplicate/concurrent requests, invalid/used/expired tokens, unpublish, suppression, erasure, sender ownership, failed dispatch and rate limits. No real outbound mail or production data is required.

```sh
MAILAT_TEST_DATABASE_URL='postgresql://USER@127.0.0.1:5432/postgres?sslmode=disable' go test ./...
```

Run from `apps/api`. In `apps/web`, run `npm test` and `npm run build`. The HTTP fixture can remain available for browser checks with `MAILAT_FORMS_UI_ADDRESS=127.0.0.1:3101` and a fresh `MAILAT_FORMS_UI_STOP_FILE` path; creating the stop file closes the fixture and drops its schema. This is test-only code and is not available in production builds.

Verified locally on 2026-10-05: all Go packages with isolated PostgreSQL integration tests; 64 frontend tests; Vue typecheck/Vite production build; Prisma schema validation; fresh and legacy migrations; Chrome editor/publish/share/history, mobile hosted signup, double confirmation and unsubscribe; and cross-origin iframe submission against the production build served by Nginx. The local SES capture exercised real MIME generation and provider transport without sending external mail. Production deployment and real SES delivery remain separate acceptance steps.
