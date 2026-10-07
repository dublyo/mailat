<p align="center">
  <img src="logo.jpg" alt="Mailat" width="120" />
</p>

# Mailat

Mailat is an MIT-licensed, self-hosted email platform built on Amazon SES. One install gives you team mailboxes, a transactional sending API, signup forms, campaigns and automations, all running on your own AWS account and your own domains.

<p align="center">
  <img src="https://dublyo.com/images/ossaas/mailat-gmail-inbox-like.jpg" alt="Mailat — Gmail-like unified inbox" width="100%" />
</p>

## Features

- **Send and receive through SES.** Domains are verified with Easy DKIM and a custom MAIL FROM. Receiving uses SES receipt rules, a private S3 bucket and signed SNS notifications. Bounce, complaint and delivery feedback updates send status and suppressions.
- **Unified inbox per user.** A Gmail-like web inbox with folders, labels, search, drafts, attachments, live updates and desktop push. Each user sees only their own mail; owners and admins cannot read other people's mailboxes.
- **Server-side inbox filters.** Filters run when mail arrives (labels, folder, star, read, archive, trash). A DMARC Reports folder files aggregate reports automatically. Sieve is not supported.
- **Teams.** Owner, admin and member roles, email invites, and shared mailboxes such as `support@` where every member gets their own copy.
- **Mailbox users.** Turn any address on a verified domain into its own mail-only login, with invite links or admin-set passwords, send-as aliases and CSV import (see below).
- **Forwarding and auto-replies.** Verified forwards are sent through SES as "Name via Mailat" with Reply-To set to the original sender. Vacation replies have daily caps.
- **Transactional API.** `POST /emails` and `/emails/batch` with templates, attachments, scheduling, required idempotency keys and per-message status.
- **Contacts, lists and signup forms.** Static and dynamic lists, CSV import, hosted and embeddable signup forms with single or double opt-in, preference center and GDPR erasure.
- **Campaigns via SES.** Durable, paced sending with an audience snapshot and a recheck before every message, an unsubscribe footer and `List-Unsubscribe`/`List-Unsubscribe-Post` headers on every email, and open and click tracking that is on by default and can be switched off per campaign.
- **Automations.** Trigger-based workflows (contact subscribed, contact created, manual) with send-email, wait, if/else, filter, list, field-update and webhook steps, run by a durable executor in the API.
- **Webhooks and API keys.** Signed, versioned webhook events with retries, dead letters and replay. API keys carry explicit scopes; routes no scope covers are closed to keys.

### Known limits

- No conversation threading for SES mail: messages are listed one by one.
- Run a single API container; multiple API replicas are unsupported.
- No IMAP, POP3 or SMTP submission. Mail is read and sent in the web app or through the API.

## Mailbox users

Owners and admins can turn any address on an SES-verified domain into its own login, the way Migadu mailboxes work. Open **Domains → Mailboxes** on a domain, then **New mailbox**:

- **Invite user to set own password**: Mailat emails a 72-hour, single-use setup link to the person's outside address. Mail to the new address is kept for them in the meantime.
- **Set initial password**: the login works at once; hand over the address and password through a secure channel.

A mailbox user (role `mailbox`) sees only their own mail: inbox, compose, their signature, filters, vacation replies, forwarding, security and shared mailboxes they were added to. Every other page and API route is closed to them (403), and they cannot use API keys. Mailbox users don't take a seat.

- **Sending:** a mailbox user (or member) can send as their address, any `+tag` of it, send-as aliases an admin granted, and, if the admin turned on Wildcard sender for that mailbox, any unused address on the domain. Owners and admins can use any unused address. No one can send as another person's address or alias.
- **Receiving:** mail goes to the exact address first, then `local+tag` to `local`, then the owner of a send-as alias, and only then to the domain's catch-all.
- **Admin actions:** change the May send / May receive switches, add or remove aliases, set a new password or send a reset link, reset 2FA, suspend, reactivate, or remove (new mail then falls back to the catch-all).
- **CSV import:** columns `local_part` (or `address`), `name`, then exactly one of `invite_email` or `password`, plus optional `may_send` and `may_receive` (`true`/`false`). The limit is 200 rows or 1 MiB, and a dry run checks every row before anything is created.

There is no IMAP/SMTP access and no self-service password reset. Details are in [docs/self-hosting-ses.md](docs/self-hosting-ses.md#mailbox-users).

## Quick Start

### Prerequisites

- Go 1.27.1 (pinned by the `toolchain` line in `apps/api/go.mod`; an older Go 1.21+ downloads it automatically unless `GOTOOLCHAIN=local`)
- Node.js 24 and pnpm 9
- PostgreSQL 16
- Redis 7
- An AWS account with SES in a region that supports receiving

### Local development

Start PostgreSQL and Redis any way you like. With Docker, for example:

```bash
docker run -d --name mailat-postgres -p 5432:5432 \
  -e POSTGRES_USER=mailat -e POSTGRES_PASSWORD=change-me -e POSTGRES_DB=mailat postgres:16-alpine
docker run -d --name mailat-redis -p 6379:6379 redis:7-alpine
```

Then:

```bash
cp .env.example .env        # fill in DATABASE_URL, REDIS_URL, secrets and AWS values
pnpm install

# API on port 3001 (run from apps/api so ../../.env resolves)
cd apps/api
go run ./cmd/server

# Web app on port 3000 (in another terminal)
cd apps/web
npm run dev
```

The API applies the versioned SQL migrations in `apps/api/internal/database/migrations` on startup (`AUTO_MIGRATE=true` by default). They are the authoritative schema. The root `docker-compose.yml` only starts the legacy Stalwart server and is not needed for SES mode. Receiving mail needs a public HTTPS URL that SNS can reach, so local development covers sending and the UI but not inbound mail.

### Environment variables

`.env.example` lists every variable with comments. The important ones:

```bash
# Database
DATABASE_URL="postgresql://user:password@host:5432/database?sslmode=require"

# Redis (required: the API exits at startup without it, and /api/v1/health reports it)
REDIS_URL="redis://:password@host:6379"

# Authentication (startup fails unless both are set, at least 32 bytes and different;
# generate each with: openssl rand -hex 32)
JWT_SECRET="replace-with-a-long-random-jwt-secret"
ENCRYPTION_KEY="replace-with-a-separate-long-random-encryption-key"
JWT_EXPIRES_IN="7d"

# Email provider: "ses" (the default when unset) or legacy "smtp" (Stalwart/JMAP)
EMAIL_PROVIDER="ses"
AWS_REGION="us-east-1"
AWS_ACCESS_KEY_ID="your-access-key"
AWS_SECRET_ACCESS_KEY="your-secret-key"

# Reverse proxies whose X-Forwarded-For is trusted (rate limits, consent records, audit logs).
# The API default is loopback only; never include networks untrusted clients can reach.
TRUSTED_PROXY_CIDRS="127.0.0.1/32,::1/128"
```

`docker-compose.prod.yml` defaults `TRUSTED_PROXY_CIDRS` to loopback plus the private ranges `172.16.0.0/12`, `10.0.0.0/8` and `192.168.0.0/16`, which suits Caddy on the Compose network; narrow it if untrusted clients can reach the API from those ranges.

`WORKER_ENABLED` only controls the Redis (Asynq) worker and scheduler; Redis is required either way. The legacy `smtp` provider keeps the old Stalwart/JMAP path and its `STALWART_*` and `SMTP_*` variables; SES mode needs none of them.

## AWS

Mailat uses SES for sending, receiving and feedback, S3 for raw mail and attachments, and SNS to notify the API. The API creates its own buckets and topics (all named `mailat-…`) when you set up sending or receiving for a domain.

- **IAM permissions:** use the least-privilege policy in [docs/self-hosting-ses.md#iam-policy](docs/self-hosting-ses.md#iam-policy).
- **Regions, DNS, receiving and feedback setup:** see [AWS setup and receiving](docs/self-hosting-ses.md#aws-setup-and-receiving).

## API

The running API documents itself:

- `/docs/`: interactive reference (Swagger UI)
- `/api/v1/openapi.json`: the generated OpenAPI contract; `info.version` is the contract date
- `/api-docs`: the in-app guide with API key management, examples and route search

[docs/api-automation.md](docs/api-automation.md) explains scopes, idempotency, the change feed, webhooks and automations.

### Authentication

Browsers sign in with `POST /api/v1/auth/login` and send `Authorization: Bearer <JWT>`. Integrations use API keys (`ue_…`) in the same header, limited by their scopes:

```bash
curl -X POST https://mail.example.com/api/v1/emails \
  -H "Authorization: Bearer ue_<api_key>" \
  -H "Idempotency-Key: order-123-confirmation" \
  -H "Content-Type: application/json" \
  -d '{
    "from": "sender@yourdomain.com",
    "to": ["recipient@example.com"],
    "subject": "Hello {{firstName}}",
    "html": "<p>Welcome, {{firstName}}!</p>",
    "variables": {"firstName": "John"}
  }'
```

The idempotency key (8–128 characters) is required. For live updates, the browser fetches a 60-second, single-use stream ticket from `POST /api/v1/auth/stream-token` and opens `GET /api/v1/sse/connect` with it, fetching a new ticket on every reconnect. Session JWTs and API keys are never accepted in a URL; headless clients send an `email:read` key in the `Authorization` header instead.

### SDKs

Client libraries live in `packages/`: [JavaScript/TypeScript](packages/sdk-js/README.md) (`@mailat/sdk`), [Python](packages/sdk-python/README.md) (`mailat`) and [Go](packages/sdk-go/README.md) (`github.com/dublyo/mailat-go`). Version 0.2.0 requires your instance's base URL; there is no default host.

## Deployment

Images are published to GHCR as `ghcr.io/dublyo/mailat-api` and `ghcr.io/dublyo/mailat-web`, tagged `sha-<full commit SHA>`. The API image listens on port 8000 (local development uses 3001); the web image serves the app with nginx on port 80 and answers liveness checks at `/nginx-health`.

`docker-compose.prod.yml` runs Caddy, web and API, with the images set to `:${VERSION:-latest}`. Always set `VERSION=sha-<full commit SHA>` so an upgrade is deliberate. The `stalwart` Compose profile and the Caddy `MAIL_DOMAIN` block are legacy and stay off unless you need the old Stalwart server.

Start with `.env.production.example` and follow the [SES self-hosting guide](docs/self-hosting-ses.md): it covers configuration, the IAM policy, receiving, delivery feedback, mailbox ownership, send and retry behaviour, limits, upgrades and rollback.

Further guides:

- [Campaigns](docs/campaigns.md): sending requirements, statuses, pacing and tracking
- [Signup forms](docs/signup-forms.md): hosted and embedded forms, opt-in and safeguards
- [DMARC reports](docs/dmarc-reports.md): the DMARC Reports folder and how to receive reports
- [API automation](docs/api-automation.md): API keys, webhooks, change feed and automations

## Project Structure

```
mailat/
├── apps/
│   ├── api/                          # Go API (GoFrame)
│   │   ├── cmd/server/               # API entry point
│   │   ├── cmd/openapi/              # OpenAPI generator (--check detects drift)
│   │   ├── cmd/vapid-keys/           # Web push key generator
│   │   └── internal/
│   │       ├── controller/           # HTTP handlers
│   │       ├── database/migrations/  # Versioned SQL migrations (authoritative schema)
│   │       ├── middleware/           # Auth, API key scopes, roles
│   │       ├── provider/             # SES, S3, SNS, Cloudflare and legacy SMTP providers
│   │       ├── router/               # Route definitions
│   │       ├── service/              # Business logic
│   │       └── worker/               # Asynq jobs and send recovery
│   └── web/                          # Vue 3 front end (nginx image)
├── packages/
│   ├── sdk-js/                       # @mailat/sdk
│   ├── sdk-python/                   # mailat
│   └── sdk-go/                       # github.com/dublyo/mailat-go
├── docker/caddy/Caddyfile            # Reverse proxy for docker-compose.prod.yml
├── docker-compose.prod.yml           # Production stack (stalwart profile is legacy)
├── docker-compose.yml                # Legacy Stalwart server only
└── docs/                             # Operator and API guides
```

## Tech Stack

| Component | Technology |
|-----------|------------|
| **Backend** | Go (toolchain 1.27.1), GoFrame v2 |
| **Frontend** | Vue 3, TypeScript, Pinia, Tailwind |
| **Database** | PostgreSQL 16 (tested in CI) |
| **Queue** | Redis 7, Asynq |
| **Email** | Amazon SES |
| **Storage** | Amazon S3 |
| **Notifications** | Amazon SNS |
| **Real-time** | Server-Sent Events, Web Push |
| **Reverse proxy** | Caddy |

## n8n Integration

Automate email workflows with the [n8n community node](https://www.npmjs.com/package/n8n-nodes-mailat). Send emails, manage your inbox, and react to email events from n8n.

```
n8n-nodes-mailat
```

Install via **Settings > Community Nodes > Install** in your n8n instance, or manually:

```bash
cd ~/.n8n && npm install n8n-nodes-mailat
```

**Supported operations:** send email, batch send, inbox management, domain and identity listing, and webhook triggers for mail and contact events. Example n8n workflow files are not part of this repository.

See the [n8n-nodes-mailat README](https://github.com/dublyo/n8n-nodes-mailat) for full documentation.

## License

MIT
