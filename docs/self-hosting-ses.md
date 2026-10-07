# Self-hosting Mailat with AWS SES

This guide describes the SES mailbox implementation in this release. It supersedes older README examples for draft routes, Stalwart requirements, and receiving-region lists. See [validation evidence](mailat-validation.md) for the checks actually completed; successful local tests do not establish production AWS connectivity or deliverability.

## Runtime layout

The Vue application calls the Go API through the same origin under `/api/v1`. PostgreSQL stores users, identities, mailbox copies, draft versions, send receipts, and delivery events. Redis is required at API startup and supports the Asynq workers. Private S3 objects hold received MIME and attachment bytes. SES handles submission and receiving; signed SNS notifications bring receiving and delivery events back to Mailat.

SES mode does not require Stalwart, an IMAP password, or a Stalwart database. The optional `stalwart` Compose profile retains the legacy SMTP/JMAP service. SES mode provides the web inbox; it does not expose an IMAP server.

## Configure the deployment

1. Copy [`.env.production.example`](../.env.production.example) to a protected `.env` beside [the production Compose file](../docker-compose.prod.yml), or enter equivalent variables in the deployment environment. Every credential in the example is a placeholder.
2. Supply reachable PostgreSQL and Redis endpoints. Use TLS as required by your providers; these services are not created by this Compose file.
3. Set `DOMAIN`, `APP_DOMAIN`, `API_URL`, and `WEB_URL` to the public host/origin. Recommended example: `DOMAIN=mail.example.com`, `APP_DOMAIN=mail.example.com`, and both URLs `https://mail.example.com`. `API_URL` is an origin without `/api/v1`; it must be publicly reachable by SNS. `APP_DOMAIN` is a hostname without a scheme.
4. Set separate random `JWT_SECRET` and `ENCRYPTION_KEY` values, for example generated individually with `openssl rand -hex 32`. The API refuses to start when either is missing, shorter than 32 bytes, still a placeholder, or equal to the other; the error names the variable, never its value. Keep the encryption key stable and back it up securely with the database: changing it makes existing encrypted data unreadable. Changing `JWT_SECRET` signs everyone out and invalidates unsubscribe and preference links already sent.
   `JWT_EXPIRES_IN` sets the session lifetime: a day count such as `7d` or `30d`, or a Go duration such as `168h`, between `1h` and `90d`. Anything else stops startup.
5. Set `EMAIL_PROVIDER=ses` (the default when unset; the only other accepted value is `smtp`), `AWS_REGION`, `AWS_ACCESS_KEY_ID`, and `AWS_SECRET_ACCESS_KEY`. This implementation uses these explicit credentials; it does not automatically use an instance role or session token. SES initialization errors must not silently switch delivery to SMTP.
6. Set `TRUSTED_PROXY_CIDRS` to the networks of every reverse proxy in front of the API. `X-Forwarded-For` is honoured only from these addresses, and the result feeds rate limits, consent records, and audit logs. The API's own default is loopback only; `docker-compose.prod.yml` already defaults to loopback plus the private ranges `172.16.0.0/12`, `10.0.0.0/8`, and `192.168.0.0/16`. If your proxy is missing from the list, every request appears to come from the proxy and shares one per-IP rate limit.
7. Browser CORS is an allowlist: `WEB_URL` plus any origins in `CORS_ORIGINS` (comma-separated `https://host[:port]`). Same-origin deployments, where Caddy serves the app and `/api` on one host, need nothing extra. Add an origin only for a separately hosted front end that calls authenticated routes. Public routes such as signup-form submission stay open to any origin, and server-side SDK calls are unaffected.
8. Keep `WORKER_ENABLED=true` unless another compatible worker consumes the same Redis queues. Disabling it in a single-container installation leaves transactional/background jobs unprocessed. Compose sends are submitted directly after their database claim.
9. Keep `AUTO_MIGRATE=true` for the included ordered, checksum-verified SQL migrations. The migration account needs DDL privileges. When false, arrange a compatible schema migration before starting this API.
10. Pin `VERSION` to a tested image tag or digest through your deployment tooling. The service image names remain `ghcr.io/dublyo/mailat-api` and `ghcr.io/dublyo/mailat-web`.

For a Docker Compose installation, validate configuration without printing secrets, then start the reviewed configuration:

```sh
docker compose --env-file .env -f docker-compose.prod.yml config --quiet
docker compose --env-file .env -f docker-compose.prod.yml up -d
```

Default services are Caddy, web, and API. Caddy receives `DOMAIN` and routes `/api/*` to the API. `API_DOMAIN` and `MAIL_DOMAIN` are optional extra Caddy virtual hosts; leave their localhost defaults unless intentionally exposing them. The Stalwart admin virtual host has no backend when its profile is disabled. Enable the legacy service only when needed:

```sh
docker compose --env-file .env -f docker-compose.prod.yml --profile stalwart up -d
```

The web container's own liveness check is `/nginx-health`; `/health` is the in-app Health dashboard, so update any container healthcheck that still probes `/health`. The web container sends a Content-Security-Policy on app pages (not on `/api/` or `/docs/`). If the app calls an API on a different origin (`VITE_API_URL` set at build time), list that origin in the web container's `MAILAT_CSP_CONNECT_SRC`, or the browser blocks those requests.

Portainer deployments must supply the environment variables and make repository bind-mounted files, including the Caddyfile, available to their Docker environment. Existing installations with their own ingress can retain that ingress and equivalent same-origin routing. Do not start a second proxy on ports already in use. This guide does not confirm that any particular Portainer stack has been updated.

For local API/Vue development, [`.env.example`](../.env.example) uses API port 3001 and web port 3000. Run the API from `apps/api` so its `../../.env` lookup resolves to the repository root. Receiving still requires a publicly reachable HTTPS endpoint; localhost alone cannot receive SNS notifications.

## AWS setup and receiving

Use a region that currently supports SES receiving; consult [AWS's region documentation](https://docs.aws.amazon.com/ses/latest/dg/regions.html) rather than an old hard-coded region list. Domain verification, sending quotas, and sandbox status are regional. Request [SES production access](https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html) for sending to unverified recipients. Sender verification remains necessary after sandbox removal.

Add an owned domain in Mailat, publish the displayed verification/DKIM DNS records, and verify it. Create identities on that domain. SES identities can omit an IMAP password. Mailat requires an active, SES-verified domain and a sending-enabled identity owned by the authenticated user. A `fromEmail` alias must stay on that identity's domain and cannot use another user's explicit identity address.

SES DNS onboarding configures sending by default and preserves the domain's existing inbox provider. It does not automatically add or change the root-domain receiving MX. The MX shown on `bounce.<domain>` is SES's custom MAIL FROM record; it does not move ordinary `person@<domain>` inbox delivery. Automatic Cloudflare setup also skips legacy stored root MX and root SPF records. Required DKIM/ownership/MAIL FROM records are inspected before creation: identical records are preserved, conflicts are reported without updates or deletion, and a failed preflight prevents creation.

DMARC setup checks for an existing or inherited policy before proposing a new record. One valid existing policy is preserved, including its reporting addresses and alignment/subdomain settings, whether its policy is `none`, `quarantine`, or `reject`. When no policy applies, automatic Cloudflare setup can add `v=DMARC1; p=quarantine;` after a fresh server-side check. It never invents a reporting mailbox. Multiple or invalid policies, unresolved delegation, and DNS/provider read failures require review and prevent automatic creation. Quarantine applies to every sender using that From domain, so other mail providers must also authenticate with aligned SPF or DKIM.

Manual DNS users can check DMARC and copy a missing-policy suggestion separately. The bulk DNS download continues to exclude DMARC, root MX, and root SPF: a BIND import cannot conditionally create a record only while it is absent and could otherwise introduce a duplicate policy. Review the current DMARC status before publishing any manually copied record. See [the DMARC setup specification](mailat-dmarc-spec.md) for behavior and edge cases.

Enable receiving for the domain through the Domains screen. The setup creates/reuses an organization bucket and SNS topic, creates an SES receipt rule, and activates its rule set. Activation can replace the account's currently active receipt rule set in that region: review existing receiving workloads before enabling it. Publish the generated MX record using your region's receiving endpoint. Do not replace an existing provider's MX records unless intentionally moving inbound delivery.

If the domain already receives mail through Google Workspace, Microsoft 365, or another provider, keep that provider's root MX. Configure a separate receiving subdomain in Mailat, or arrange forwarding from the existing provider to a configured Mailat receiving address. An SES receipt rule alone cannot receive messages still routed exclusively to another provider. Set the receiving MX manually only for the domain/subdomain whose inbound mail you intend to route to SES.

The setup credentials need the AWS operations used by the application: SES identity verification/DKIM and receipt-rule management, SES sending/account inspection, S3 bucket creation/policy/public-access-block management and private object read/write/delete, SNS topic/policy/subscription management, and STS caller-identity lookup. Scope IAM access to the installation's account, regions, identities, topics, and buckets where the operations support it. The old README policy is not a complete deployment policy for this release, particularly for `s3:PutBucketPublicAccessBlock` and `sns:SetTopicAttributes`.

The receiving rule uses an S3 action with prefix `incoming/<domain>/` and the stored SNS topic. Mailat authenticates `/api/v1/webhooks/ses/incoming?secret=<generated-secret>` using the stored topic, organization secret, AWS signature, and region. Subscription confirmation is restricted to the signed AWS SNS URL. Secrets are generated and stored by setup, not a separate environment variable. Redact webhook query strings from access logs and monitoring exports.

SNS success is acknowledged after durable processing. Temporary storage/database failures return a retryable error. Retries deduplicate per mailbox identity; each eligible recipient receives an independent copy. Exact receiving-enabled identities take precedence, otherwise the domain's configured receiving-enabled catch-all receives unmatched addresses. Routing uses SMTP envelope recipients, not an untrusted `To` header. An organization topic cannot route mail into another organization's mailbox.

### Delivery, bounces, and complaints

`SES_CONFIGURATION_SET` names an existing SES configuration set; setting the variable does not create a configuration set or its destinations. Configure SES delivery, bounce, and complaint publishing to the same SNS topic stored in that organization's receiving configuration. The webhook authorizes that exact topic and accepts both SES `notificationType` and configuration-set `eventType` payloads. For a deployment spanning multiple organizations, design topic/event routing explicitly: one globally configured destination is not automatically authorized for every organization.

Delivery events update recorded attempts and suppression state with organization isolation and duplicate handling. SES acceptance (`sent`) is not proof of delivery. Without a working event destination, accepted messages can remain `sent` indefinitely. Test the complete event path before relying on bounce/complaint suppression.

## Authentication and mailbox ownership

`GET /api/v1/auth/register-status` reports whether initial registration is available. Registration creates the first owner and closes once a user exists; everyone else joins through an invite. Do not expose an unclaimed installation publicly for an extended period. Login uses `POST /api/v1/auth/login`; browser requests use `Authorization: Bearer <JWT>`.

API keys have the `ue_` prefix and are accepted through the same Authorization header. Keys are associated with a user; legacy keys fall back to the organization's first user. Sender authorization applies to both authenticated users and these keys. Keep keys private and rotate/revoke them through the API-key controls when appropriate.

Mailbox queries, message mutations, draft operations, labels, and attachment downloads are scoped to the mailbox copies the user owns: mail to their personal identities and their own copy of mail to shared mailboxes they read. A unified inbox combines those; it is not an organization-wide mailbox browser, and owners and admins cannot read another member's mail. Live updates use the authenticated `/api/v1/sse/connect` route (see [Live updates and web push](#live-updates-and-web-push)).

### Members, roles, and shared mailboxes

Users are `owner`, `admin` or `member`. Owners and admins invite people from Settings (`POST /api/v1/org/invites`); admins can invite and remove members only, and only the owner changes roles. The invite email is sent through SES from the inviter's own identity and links to `${WEB_URL}/invite#token=...`; the link is valid for `INVITE_TTL_HOURS` (default 168, at most 720), single use, and replaced on resend (60-second cooldown, five sends per invite). The invitee sets a name and password, then can link OAuth later. With `DISABLE_APP_LIMITS=false`, active users plus open invites count against the organization's `max_users`. While the SES account is in the sandbox, invites reach only verified addresses.

Domain, identity, receiving, branding, shared-mailbox creation and member/invite management need the owner or admin role, checked on every request so a demotion applies at once. API keys can reach those routes only when their owner is an owner or admin, and API keys themselves are managed only from an owner or admin session. Contacts, lists, campaigns, automations and templates remain open to every member. Removing a member revokes their sessions, API keys and push subscriptions, deletes their shared-mailbox copies, and moves their personal identities to another member or disables them; their received mail is kept but no one can read it.

A shared mailbox is a shared identity, such as `support@your-domain`. Each member with read access gets an independent copy of every message that arrives after they join (read state, labels and deletion are per member), and members with send access can compose as the shared address. A shared mailbox has at most 50 members, and its last reader cannot be removed. Removing a member, or taking away their read access, deletes their copies of the mailbox's mail; mail they sent as the shared address stays in their Sent folder when only read access is taken away. Every copy stores its own text and HTML bodies in PostgreSQL (attachments and raw MIME in S3 are shared), so large teams multiply database storage for that mailbox.

## Draft, compose, and attachment contracts

The API prefix below is `/api/v1`. Successful JSON responses wrap results in `data`.

| Method and path | Contract |
| --- | --- |
| `POST /compose/drafts` | Create a draft; returns `id`, `identityId`, `version`, timestamps. |
| `PUT /compose/drafts/:id` | Save with the last returned `version`; stale versions return HTTP 409. |
| `DELETE /compose/drafts/:id` | Delete an owned draft. |
| `POST /compose/send` | Submit a frozen payload with a stable `Idempotency-Key`. |
| `GET /inbox/received` | Read mailbox copies, including SES drafts, Sent, and Outbox. |
| `GET /inbox/received/:uuid` | Read an owned message and attachment metadata. |
| `GET /inbox/received/:uuid/attachments/:attachmentUuid` | Authenticated attachment bytes with download headers; no public bucket required. |
| `POST /compose/attachments` | Optional multipart upload: `identityId` plus `file`; persists a private object and returns an owned `blobId`. |

Drafts and sends use numeric `identityId`, optional plain-address `fromEmail`, recipient arrays containing `{ "email": "person@example.net", "name": "Person" }`, `subject`, `textBody`, `htmlBody`, optional `replyTo`, `inReplyTo`, `references`, and `attachments`. Compose supports one Reply-To address. Save an empty draft with empty recipient arrays; recipient entries provided to the HTTP DTO must be valid addresses. To send an existing draft, supply its UUID as `draftId` and current version as `draftVersion`. Its durable outgoing copy is created in the same transaction that removes the draft.

A newly attached file is represented as:

```json
{
  "name": "notes.txt",
  "type": "text/plain",
  "content": "SGVsbG8="
}
```

`content` is base64 of the file bytes. A previously owned attachment uses `{ "blobId": "<attachment-uuid>" }` instead; never combine `blobId` and `content`. Existing attachments can be forwarded only by a user who owns the source mailbox copy. Saved draft attachment UUIDs remain stable across subsequent saves.

Application safety limits are 50 recipients per message, 50 attachments, 10 MiB combined decoded attachment bytes, and 2 MiB combined compose HTML/text. The compose JSON body is bounded at 18 MiB. Received raw MIME is bounded at 40 MiB. These limits still apply when plan caps are disabled, and AWS's [service quotas](https://docs.aws.amazon.com/ses/latest/dg/quotas.html) apply independently. A received file larger than the compose attachment limit cannot be forwarded through compose unchanged.

Outbound attachments use private objects in the domain/organization receiving bucket. Configure receiving/storage before sending attachments; a send-only domain without a configured bucket cannot store new attachment bytes. If storage fails, the request fails instead of returning a fake blob or silently omitting a file.

### Send status and safe retries

Compose requires an `Idempotency-Key` of 8–128 characters. Generate it once for a frozen submission payload. Reuse that same key and identical payload when retrying a lost HTTP response. Reusing a key with changed content is rejected. A durable receipt retains the payload hash, provider ID, and result even after permanent deletion of the mailbox copy. Authenticated late delivery events can still resolve that receipt. User/account deletion can remove those receipts.

A successful HTTP response records an attempt, not necessarily successful delivery. Inspect `data.status` and `data.sendError`:

| Status | Meaning and action |
| --- | --- |
| `sending` | A submission is claimed or in progress; do not create a second attempt automatically. |
| `sent` | Provider accepted the message; a Sent copy exists. |
| `delivered` | A later authenticated event reports delivery. |
| `failed` | Validation/quota/provider rejection recorded for that attempt; inspect the error before deliberately creating a new attempt. |
| `unknown` | Timeout or other uncertain provider result; verify delivery before considering a new attempt. |
| `bounced`, `complained` | A later authenticated provider event reports a delivery/reputation problem. |

AWS SES submission has no application idempotency token here. The SES SDK and Mailat worker do not repeat uncertain submissions automatically. This prevents blind duplicate sends but does not promise exactly-once delivery. A process crash after claiming a submission can leave `sending` indefinitely; there is no automatic reconciliation/resend job. Operators must inspect delivery events/provider evidence before resolving it. Mailbox mutations are rejected while a message is actively `sending`.

After a definitive failed draft send, an explicit retry reloads the owned failed Outbox copy and its current attachment UUIDs, clears the consumed draft identifier/version, and creates a new submission key. The original draft was consumed when the outgoing copy was persisted. Infrastructure failures return HTTP 503; keep the same frozen submission key and payload. After an uncertain response, a later request error is not proof that the first send was rejected.

The transactional endpoint `POST /emails` uses its existing string-array address contract (`from`, `to`, `cc`, `bcc`, `replyTo`, `subject`, `text`/`html`/`templateId`), separate from the compose DTO. Its optional `Idempotency-Key` is scoped to organization and request content; omitting it creates a new attempt each call. For `/emails/batch`, specify `idempotencyKey` on each item when retries need protection. Batch size is at most 100. Transactional records are tracked through `/emails/:id`, not automatically copied into the personal Sent folder. Scheduled sends require a future RFC3339 `scheduledFor` and a functioning queue; scheduling failure never changes them to immediate sends.

### Forwarding and auto-replies

Forwards and auto-replies run when mail arrives through SES and send through SES from the receiving identity, so its domain must be active and SES-verified. A forward starts `pending`: the destination gets a verification link (`${WEB_URL}/forwards/verify#...`, valid 48 hours) and nothing is forwarded until it is opened. Forwarded copies come from "Original Name via Mailat <identity@your-domain>" with Reply-To set to the original sender; subject, body and attachments are unchanged. A complaint, a permanent bounce or a suppressed destination suspends the forward until it is verified again. `FORWARD_MAX_BYTES` (default 10485760, at most 10 MiB) skips larger messages, which stay in the mailbox, and `FORWARD_DAILY_LIMIT` (default 200) caps messages per forward per UTC day; `AUTO_REPLY_DAILY_LIMIT` (default 200) caps replies per rule. These sends count toward the monthly send limit. While the SES account is in the sandbox, verification mail, forwards and auto-replies reach only verified recipients.

Sieve scripts are not supported. Existing rows are kept but never run; use inbox filters.

### Live updates and web push

Open mailboxes update live over Server-Sent Events. Every committed mailbox change is recorded in `mailbox_changes` and announced with PostgreSQL `NOTIFY`, so all API replicas see it; `LISTEN` needs a direct (session-mode) PostgreSQL connection, not a transaction-pooling proxy. If the listener drops, streams fall back to polling every 5 seconds. Browsers fetch a single-use stream ticket for each connection, and a stream stays open as long as its session. After a reconnect the client resumes from its last cursor, so nothing committed in between is missed; a cursor that is too old asks the client to reload. The API sends `X-Accel-Buffering: no`; any reverse proxy in front of it must pass `text/event-stream` responses through unbuffered. `SSE_MAX_CONNECTIONS` (default 5000) caps streams per replica; each user keeps at most 10.

Desktop notifications for new inbox mail use web push with VAPID keys. Generate a pair once with `cd apps/api && go run ./cmd/vapid-keys` and set `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY` and `VAPID_SUBJECT` (a `mailto:` or `https:` contact). Set all three or none; a partial or mismatched set stops startup, and with none push is disabled. Keep the private key secret. Rotating the keys retires existing subscriptions, so users enable notifications again in Settings. Subscriptions are accepted only for push-service hosts in `PUSH_ENDPOINT_HOST_SUFFIXES` (Chrome/Edge, Firefox, Safari and Windows by default), and deliveries go only to public addresses without redirects.

## Limits, retention, upgrades, and validation

`DISABLE_APP_LIMITS=true` is the self-host default and bypasses application plan caps, including old positive organization values. All four `DEFAULT_*` limits default to `0`, meaning no application cap. To impose operator quotas, set `DISABLE_APP_LIMITS=false` and positive organization limits; default values apply to newly created organizations and do not rewrite existing rows. A positive monthly send limit counts reserved attempts atomically by UTC calendar month. Retries using the same recorded key do not reserve a second attempt. This setting does not remove AWS account quotas, sandbox restrictions, message bounds, or resource costs.

Back up PostgreSQL, private S3 data, deployment configuration, and encryption keys before upgrading. Schema upgrades use `mailat_schema_migrations`, checksums, and an advisory lock; they preserve existing mailbox data and replace global inbound Message-ID uniqueness with per-identity deduplication. Do not edit migration files after they have been applied.

Migration `012_hardening` lowercases contact emails where that cannot collide. Contacts that differ only by case within one organization are left as separate rows for manual merging; matching, suppression, and erasure treat them as one address. List them with:

```sql
SELECT org_id, lower(email) AS email, count(*) AS rows, array_agg(uuid) AS contact_uuids
FROM contacts
GROUP BY org_id, lower(email)
HAVING count(*) > 1;
```

Migration `016_multi_user_live` builds two indexes on `received_emails` inside its transaction, which blocks new mail and mailbox changes until it finishes. Small installations will not notice; on a large mailbox table, upgrade in a short maintenance window. SNS retries receiving notifications that time out meanwhile. Migration `015_mail_arrival` turns off existing Sieve scripts and marks them unsupported.

**Do not blindly downgrade the API or rerun an older schema initializer against this database.** Older initialization can recreate a global Message-ID unique index, which conflicts with multiple legitimate mailbox copies. A container image rollback is not a database rollback. Verify schema compatibility or restore a coordinated pre-upgrade backup; restoring it also loses changes made after that backup. Disabling `AUTO_MIGRATE` does not make an incompatible old binary safe.

Permanent mailbox deletion preserves objects still referenced by another mailbox, attachment, or staged upload. Draft replacement/deletion and failed upload/save paths can leave unreferenced S3 objects. Complete orphan cleanup and staged-upload expiry are not automated; use a reviewed reference-aware cleanup process. Do not apply blanket age-based deletion to objects still used by messages. Existing buckets may retain older lifecycle rules; audit those rules before relying on long-lived attachment links. Deleting a domain or disabling receiving does not automatically delete its bucket.

### GDPR contact erasure

`DELETE /api/v1/contacts/:uuid/gdpr` removes the address from the organization's marketing data in one transaction: every case variant of the contact, list memberships, consent history, automation enrollments and logs, campaign email content, campaign recipient addresses and tracking IPs/user agents, webhook event payloads and undelivered webhook deliveries, signup requests, and plaintext suppressions. It leaves a hashed `erased:` suppression so the address cannot be re-imported, re-subscribed, or mailed by campaigns.

It does not cover: the transactional `suppression_list` (SES bounce and complaint deliverability data, kept in plaintext), the organization users' own mailboxes (`received_emails`, `transactional_emails`), raw MIME and attachment objects in S3, or backups. A webhook delivery already being sent when the erasure runs may still go out once. Handle those stores separately when a request requires it.

Use `/api/v1/health` and `/api/v1/ready` as basic process/dependency checks; neither proves delivery or correct SNS/DNS configuration. Review [the validation record](mailat-validation.md) for local evidence and remaining runtime checks. Before treating a production installation as verified, complete controlled tests for real domain verification/MX, receiving and catch-all routing, owned attachment upload/download, provider acceptance, delivery/bounce events, and worker processing. Do not infer those results from fake-provider unit/integration tests.
