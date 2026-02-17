# mailat.co

All-in-one email platform combining transactional email, marketing campaigns, email receiving, and unified inbox management.

<p align="center">
  <img src="https://dublyo.com/images/ossaas/mailat-gmail-inbox-like.jpg" alt="Mailat — Gmail-like unified inbox" width="100%" />
</p>

## Features

- **Send Emails**: Transactional email API with templates, webhooks, and tracking
- **Receive Emails**: AWS SES integration for receiving emails with real-time notifications
- **Unified Inbox**: Gmail-like interface with folders, labels, and search across all identities
- **Multi-Domain**: Support for multiple domains with DNS management
- **Real-time**: SSE (Server-Sent Events) for instant email notifications
- **Identity Management**: Multiple identities per domain with color coding
- **Catch-All Support**: Route unmatched emails to a designated identity per domain
- **Smart Reply**: Auto-selects correct sender identity when replying (including catch-all)

## Quick Start

### Prerequisites

- Go 1.24+
- Node.js 20+
- Docker
- PostgreSQL database
- AWS account (for SES email sending/receiving)

### Local Development

```bash
# Install dependencies
pnpm install

# Start Stalwart mail server
docker-compose up -d

# Build and run Go API
cd apps/api
go build -o bin/server ./cmd/server
./bin/server

# Run Vue frontend (in another terminal)
cd apps/web
npm run dev
```

### Environment Variables

Copy `.env.example` to `.env` and configure:

```bash
# Database
DATABASE_URL="postgresql://user:password@host:5432/database?sslmode=require"

# Redis (optional)
REDIS_URL="redis://:password@host:6379"

# Authentication
JWT_SECRET="your-jwt-secret-min-32-chars"
JWT_EXPIRES_IN="7d"

# AWS SES (Required for email sending/receiving)
AWS_REGION="us-east-1"
AWS_ACCESS_KEY_ID="your-access-key"
AWS_SECRET_ACCESS_KEY="your-secret-key"

# Stalwart Mail Server
STALWART_URL="http://localhost:8080"
STALWART_ADMIN_TOKEN="your-admin-token"
```

---

## AWS SES Requirements

### Overview

Mailat uses AWS SES for:
- **Sending emails** via SES SMTP or API
- **Receiving emails** via SES Receipt Rules → S3 → SNS → Webhook

### Required AWS Services

| Service | Purpose |
|---------|---------|
| **SES** | Send and receive emails |
| **S3** | Store raw received emails |
| **SNS** | Notify webhook of new emails |
| **IAM** | API credentials with required permissions |

### IAM Policy

Create an IAM user with the following policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ses:SendEmail",
        "ses:SendRawEmail",
        "ses:VerifyDomainIdentity",
        "ses:VerifyDomainDkim",
        "ses:GetIdentityVerificationAttributes",
        "ses:CreateReceiptRule",
        "ses:CreateReceiptRuleSet",
        "ses:SetActiveReceiptRuleSet",
        "ses:DescribeReceiptRuleSet",
        "ses:DeleteReceiptRule"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:CreateBucket",
        "s3:PutBucketPolicy",
        "s3:GetBucketPolicy",
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::mailat-*",
        "arn:aws:s3:::mailat-*/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "sns:CreateTopic",
        "sns:Subscribe",
        "sns:ConfirmSubscription",
        "sns:Publish",
        "sns:DeleteTopic"
      ],
      "Resource": "arn:aws:sns:*:*:mailat-*"
    }
  ]
}
```

### SES Region Requirements

Email receiving is only available in these AWS regions:
- `us-east-1` (N. Virginia)
- `us-west-2` (Oregon)
- `eu-west-1` (Ireland)

### Domain Setup for Receiving

1. **Verify Domain in SES Console**
   - Go to AWS SES → Verified Identities → Create Identity
   - Add your domain and complete DNS verification

2. **Configure MX Record**
   ```
   MX  @  10  inbound-smtp.us-east-1.amazonaws.com
   ```
   (Replace region with your SES region)

3. **Enable Receiving in Mailat**
   - Go to Domains page in the app
   - Click "Enable Receiving" on your verified domain
   - The system automatically creates:
     - S3 bucket for email storage
     - SNS topic for notifications
     - SES receipt rule for your domain

### Email Receiving Flow

```
1. Email sent to user@yourdomain.com
2. AWS SES receives email (via MX record)
3. SES stores raw email in S3
4. SES sends notification to SNS
5. SNS POSTs to your webhook endpoint
6. API processes and stores email metadata
7. SSE notifies connected clients in real-time
```

---

## API Endpoints

### Authentication

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/auth/register` | Register new user with organization |
| POST | `/api/v1/auth/login` | Login and get JWT token |
| GET | `/api/v1/auth/me` | Get current user profile |

### Domains

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/domains` | Add domain with DKIM generation |
| GET | `/api/v1/domains` | List all domains |
| GET | `/api/v1/domains/:uuid` | Get domain with DNS records |
| POST | `/api/v1/domains/:uuid/verify` | Verify DNS records |
| POST | `/api/v1/domains/:uuid/setup-receiving` | Setup email receiving |
| DELETE | `/api/v1/domains/:uuid` | Delete domain |

### Identities (Mailboxes)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/identities` | Create identity with Stalwart sync |
| GET | `/api/v1/identities` | List all identities |
| GET | `/api/v1/identities/:uuid` | Get identity details |
| PUT | `/api/v1/identities/:uuid/password` | Update identity password |
| POST | `/api/v1/identities/:uuid/catch-all` | Set as catch-all for domain |
| DELETE | `/api/v1/identities/:uuid` | Delete identity |

### Received Inbox (AWS SES)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/inbox/received` | List received emails with filters |
| GET | `/api/v1/inbox/received/:uuid` | Get single email with content |
| GET | `/api/v1/inbox/received/counts` | Get folder counts |
| POST | `/api/v1/inbox/received/mark` | Mark emails as read/unread |
| POST | `/api/v1/inbox/received/star` | Star/unstar emails |
| POST | `/api/v1/inbox/received/move` | Move emails to folder |
| POST | `/api/v1/inbox/received/trash` | Trash or permanently delete |

### Compose (Email Sending)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/compose/send` | Send email via AWS SES |
| POST | `/api/v1/compose/draft` | Save email as draft |
| PUT | `/api/v1/compose/draft/:uuid` | Update existing draft |
| DELETE | `/api/v1/compose/draft/:uuid` | Delete draft |
| GET | `/api/v1/compose/reply/:uuid` | Get reply context for email |
| GET | `/api/v1/compose/forward/:uuid` | Get forward context for email |

**Query Parameters for listing:**
- `identityId` - Filter by identity ID (0 or omitted = unified inbox, all identities)
- `folder` - inbox, sent, drafts, spam, trash, archive, all
- `search` - Search in subject, from, body
- `labels` - Filter by label UUIDs
- `page` - Page number (default: 1)
- `pageSize` - Items per page (default: 50)

**Unified Inbox:**
- When `identityId=0` or omitted, emails from all user's identities are returned
- Response includes `identityEmail`, `identityDisplayName`, `identityColor` for UI display
- Counts endpoint also supports unified view with `identityId=0`

### Real-time Updates (SSE)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/sse/connect?token=JWT` | SSE connection for real-time updates |

**Event Types:**
- `connected` - Connection established
- `heartbeat` - Keep-alive (every 30s)
- `new_email` - New email received
- `email_update` - Email status changed
- `email_deleted` - Email deleted
- `counts_update` - Folder counts changed

### Labels

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/labels` | List user labels |
| POST | `/api/v1/labels` | Create label |
| DELETE | `/api/v1/labels/:uuid` | Delete label |

### Transactional Email API

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/emails` | Send single transactional email |
| POST | `/api/v1/emails/batch` | Batch send (up to 100) |
| GET | `/api/v1/emails/:id` | Get email status and events |
| DELETE | `/api/v1/emails/:id` | Cancel scheduled email |

### Email Templates

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/templates` | Create new template |
| GET | `/api/v1/templates` | List all templates |
| GET | `/api/v1/templates/:uuid` | Get template details |
| PUT | `/api/v1/templates/:uuid` | Update template |
| DELETE | `/api/v1/templates/:uuid` | Delete template |
| POST | `/api/v1/templates/:uuid/preview` | Preview with variables |

### Webhooks (Outgoing)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/webhooks` | Create webhook endpoint |
| GET | `/api/v1/webhooks` | List all webhooks |
| PUT | `/api/v1/webhooks/:uuid` | Update webhook |
| DELETE | `/api/v1/webhooks/:uuid` | Delete webhook |
| POST | `/api/v1/webhooks/:uuid/rotate-secret` | Rotate webhook secret |
| POST | `/api/v1/webhooks/:uuid/test` | Send test webhook |

### Webhooks (Incoming - Public)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/webhooks/ses/incoming` | AWS SNS notifications for received emails |

### API Keys

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/api-keys` | Create new API key |
| GET | `/api/v1/api-keys` | List all API keys |
| DELETE | `/api/v1/api-keys/:uuid` | Revoke API key |

### Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/health` | Health check (PostgreSQL + Redis) |
| GET | `/api/v1/ready` | Readiness check |

---

## Authentication

### JWT Token (for users)
```bash
curl -X POST http://localhost:3001/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","password":"password"}'

# Use the token
curl http://localhost:3001/api/v1/auth/me \
  -H "Authorization: Bearer <jwt_token>"
```

### API Key (for programmatic access)
```bash
curl -X POST http://localhost:3001/api/v1/emails \
  -H "Authorization: Bearer ue_<api_key>" \
  -H "Idempotency-Key: unique-request-id" \
  -H "Content-Type: application/json" \
  -d '{
    "from": "sender@yourdomain.com",
    "to": ["recipient@example.com"],
    "subject": "Hello {{firstName}}",
    "html": "<p>Welcome, {{firstName}}!</p>",
    "variables": {"firstName": "John"}
  }'
```

---

## Deployment

### One-Click Deploy (Recommended)

Deploy Mailat instantly on [Dublyo PaaS](https://dublyo.com/templates/mailat) — handles SSL, DNS, containers, and updates automatically.

### Self-Hosting

For Docker-based self-hosting, see `docker-compose.prod.yml` and `.env.production.example` in this repository.

---

## Project Structure

```
mailat/
├── apps/
│   ├── api/                        # Go + GoFrame Backend API
│   │   ├── cmd/server/             # Entry point
│   │   └── internal/
│   │       ├── config/             # Configuration
│   │       ├── controller/         # HTTP handlers
│   │       │   ├── received_inbox.go  # Received email handlers
│   │       │   ├── compose.go         # Compose/Send handlers
│   │       │   └── sse.go             # SSE real-time
│   │       ├── database/           # DB connections
│   │       ├── handler/            # Webhook handlers
│   │       │   └── sns_webhook.go     # AWS SNS handler
│   │       ├── middleware/         # Auth middleware
│   │       ├── model/              # Data models
│   │       ├── provider/           # External providers
│   │       │   └── receiving_provider.go  # AWS setup
│   │       ├── router/             # Route definitions
│   │       └── service/            # Business logic
│   │           ├── inbox.go           # Inbox service
│   │           ├── compose.go         # Email sending (SES/JMAP)
│   │           ├── identity.go        # Identity & Stalwart sync
│   │           ├── receiving.go       # Email receiving
│   │           └── transactional.go   # Sending API
│   └── web/                        # Vue 3 Frontend
│       └── src/
│           ├── views/
│           │   └── ReceivedInbox.vue  # Gmail-like inbox
│           ├── stores/
│           │   └── receivedInbox.ts   # Pinia state
│           └── lib/
│               └── api.ts             # API client
├── prisma/
│   ├── schema.prisma               # Database schema
│   └── migrations/                 # SQL migrations
├── docker/
│   └── caddy/Caddyfile             # Reverse proxy config
└── docker-compose.yml              # Local development
```

---

## Tech Stack

| Component | Technology |
|-----------|------------|
| **Backend** | Go 1.24+, GoFrame v2 |
| **Frontend** | Vue 3.5+, TypeScript, Pinia, Tailwind |
| **Database** | PostgreSQL 17 |
| **Cache/Queue** | Redis 7.4+, Asynq |
| **Mail Server** | Stalwart (IMAP/SMTP/JMAP) |
| **Email Provider** | AWS SES |
| **Storage** | AWS S3 |
| **Notifications** | AWS SNS |
| **Real-time** | Server-Sent Events (SSE) |
| **Reverse Proxy** | Caddy (auto SSL) |

---

## Development Progress

### Phase 0: Stalwart Integration ✅
- Stalwart mail server with RocksDB backend
- Management API integration
- Account provisioning

### Phase 1: Core Foundation ✅
- User authentication with JWT
- Organization and role management
- Domain management with DNS records
- Identity/mailbox management
- JMAP client for Stalwart

### Phase 2: Transactional Email API ✅
- Send single and batch emails
- Template system with variables
- Job queue with Redis/Asynq
- Webhook notifications
- Idempotency support
- Rate limiting

### Phase 2.5: Email Receiving ✅
- AWS SES receipt rules
- S3 storage for raw emails
- SNS → Webhook processing
- Real-time SSE notifications
- Gmail-like inbox UI
- Folder management
- Labels and filters
- Search functionality

### Phase 2.6: Email Sending ✅
- AWS SES as primary email provider
- Compose/Reply/Forward UI
- Draft saving and editing
- Email threading support
- Stalwart JMAP as fallback
- Automatic From address handling

### Phase 2.7: Unified Inbox & Identity Management ✅ NEW
- **Unified Inbox**: View emails from all identities in one place (`identityId=0`)
- **Identity Filter**: Dropdown to filter by specific identity
- **Identity Color Coding**: Visual distinction with colored dots
- **Catch-All Support**: One catch-all address per domain for unmatched emails
- **Smart Reply**: Auto-selects correct sender identity (works with catch-all)
- **Identity Actions Menu**: Set default, toggle catch-all, delete identities

### Phase 3: Marketing Campaigns ⏳
- Contact management
- List segmentation
- Campaign builder
- A/B testing

### Phase 4: Health & Operations ⏳
- Monitoring dashboard
- Analytics and reporting
- Alerting system

---

## n8n Integration

Automate your email workflows with the official [n8n community node](https://www.npmjs.com/package/n8n-nodes-mailat). Send emails, manage your inbox, and react to email events directly from n8n.

```
n8n-nodes-mailat
```

Install via **Settings > Community Nodes > Install** in your n8n instance, or manually:

```bash
cd ~/.n8n && npm install n8n-nodes-mailat
```

**Supported operations:** Send email, batch send, inbox management, domain & identity listing, and 8 webhook trigger events (email received, email sent, contact CRUD, bounces, complaints).

See the [n8n-nodes-mailat README](https://github.com/dublyo/n8n-nodes-mailat) for full documentation.

---

## License

MIT
