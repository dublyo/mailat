-- CreateTable
CREATE TABLE "organizations" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "name" VARCHAR(255) NOT NULL,
    "slug" VARCHAR(100) NOT NULL,
    "settings" JSONB NOT NULL DEFAULT '{}',
    "max_domains" INTEGER NOT NULL DEFAULT 5,
    "max_users" INTEGER NOT NULL DEFAULT 10,
    "max_contacts" INTEGER NOT NULL DEFAULT 1000,
    "monthly_email_limit" INTEGER NOT NULL DEFAULT 10000,
    "plan" VARCHAR(50) NOT NULL DEFAULT 'free',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "organizations_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "users" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "email" VARCHAR(255) NOT NULL,
    "password_hash" VARCHAR(255) NOT NULL,
    "name" VARCHAR(255),
    "role" VARCHAR(50) NOT NULL DEFAULT 'member',
    "email_verified" BOOLEAN NOT NULL DEFAULT false,
    "email_verified_at" TIMESTAMPTZ(6),
    "status" VARCHAR(50) NOT NULL DEFAULT 'active',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,
    "last_login_at" TIMESTAMPTZ(6),
    "backup_codes" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "totp_enabled" BOOLEAN NOT NULL DEFAULT false,
    "totp_secret" VARCHAR(64),
    "totp_verified_at" TIMESTAMPTZ(6),

    CONSTRAINT "users_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "oauth_connections" (
    "id" SERIAL NOT NULL,
    "user_id" INTEGER NOT NULL,
    "provider" VARCHAR(50) NOT NULL,
    "provider_user_id" VARCHAR(255) NOT NULL,
    "access_token" TEXT,
    "refresh_token" TEXT,
    "token_expiry" TIMESTAMPTZ(6),
    "email" VARCHAR(255),
    "name" VARCHAR(255),
    "avatar_url" TEXT,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "oauth_connections_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "api_keys" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER,
    "name" VARCHAR(255) NOT NULL,
    "key_prefix" VARCHAR(10) NOT NULL,
    "key_hash" VARCHAR(255) NOT NULL,
    "permissions" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "rate_limit" INTEGER NOT NULL DEFAULT 100,
    "last_used_at" TIMESTAMPTZ(6),
    "expires_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "api_keys_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "domains" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "verification_token" VARCHAR(100) NOT NULL,
    "verified_at" TIMESTAMPTZ(6),
    "mx_verified" BOOLEAN NOT NULL DEFAULT false,
    "spf_verified" BOOLEAN NOT NULL DEFAULT false,
    "dkim_verified" BOOLEAN NOT NULL DEFAULT false,
    "dmarc_verified" BOOLEAN NOT NULL DEFAULT false,
    "dkim_selector" VARCHAR(50) NOT NULL DEFAULT 'mail',
    "dkim_private_key" TEXT,
    "dkim_public_key" TEXT,
    "default_mailbox_quota" BIGINT NOT NULL DEFAULT 1073741824,
    "max_message_size" INTEGER NOT NULL DEFAULT 26214400,
    "open_tracking" BOOLEAN NOT NULL DEFAULT true,
    "click_tracking" BOOLEAN NOT NULL DEFAULT true,
    "status" VARCHAR(50) NOT NULL DEFAULT 'pending',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,
    "last_dns_check_at" TIMESTAMPTZ(6),
    "ses_verified" BOOLEAN DEFAULT false,
    "ses_dkim_tokens" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "ses_identity_arn" VARCHAR(255),
    "email_provider" VARCHAR(20) DEFAULT 'ses',
    "receiving_enabled" BOOLEAN NOT NULL DEFAULT false,
    "receiving_s3_bucket" VARCHAR(255),
    "receiving_sns_topic_arn" VARCHAR(255),
    "receiving_rule_set_name" VARCHAR(255),
    "receiving_rule_name" VARCHAR(255),
    "receiving_setup_at" TIMESTAMPTZ(6),

    CONSTRAINT "domains_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "domain_dns_records" (
    "id" SERIAL NOT NULL,
    "domain_id" INTEGER NOT NULL,
    "record_type" VARCHAR(10) NOT NULL,
    "hostname" VARCHAR(255) NOT NULL,
    "expected_value" TEXT NOT NULL,
    "actual_value" TEXT,
    "verified" BOOLEAN NOT NULL DEFAULT false,
    "last_checked_at" TIMESTAMPTZ(6),

    CONSTRAINT "domain_dns_records_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "identities" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "domain_id" INTEGER NOT NULL,
    "email" VARCHAR(255) NOT NULL,
    "display_name" VARCHAR(255),
    "signature_html" TEXT,
    "signature_text" TEXT,
    "is_default" BOOLEAN NOT NULL DEFAULT false,
    "can_send" BOOLEAN NOT NULL DEFAULT true,
    "can_receive" BOOLEAN NOT NULL DEFAULT true,
    "is_catch_all" BOOLEAN NOT NULL DEFAULT false,
    "color" VARCHAR(7),
    "password_hash" VARCHAR(255),
    "encrypted_password" TEXT,
    "quota_bytes" BIGINT NOT NULL DEFAULT 1073741824,
    "used_bytes" BIGINT NOT NULL DEFAULT 0,
    "stalwart_account_id" VARCHAR(255),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "identities_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "message_metadata" (
    "id" BIGSERIAL NOT NULL,
    "stalwart_message_id" VARCHAR(255) NOT NULL,
    "stalwart_account_id" VARCHAR(255) NOT NULL,
    "identity_id" INTEGER,
    "labels" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "tags" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "source" VARCHAR(50),
    "campaign_id" INTEGER,
    "contact_id" BIGINT,
    "thread_group_id" BIGINT,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "message_metadata_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "thread_groups" (
    "id" BIGSERIAL NOT NULL,
    "user_id" INTEGER NOT NULL,
    "subject_hash" VARCHAR(64),
    "participants_hash" VARCHAR(64),
    "message_count" INTEGER NOT NULL DEFAULT 1,
    "last_message_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "thread_groups_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "contacts" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "email" VARCHAR(255) NOT NULL,
    "first_name" VARCHAR(100),
    "last_name" VARCHAR(100),
    "attributes" JSONB NOT NULL DEFAULT '{}',
    "status" VARCHAR(50) NOT NULL DEFAULT 'active',
    "consent_source" VARCHAR(100),
    "consent_timestamp" TIMESTAMPTZ(6),
    "consent_ip" VARCHAR(45),
    "consent_user_agent" TEXT,
    "last_engaged_at" TIMESTAMPTZ(6),
    "engagement_score" DOUBLE PRECISION NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "contacts_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "lists" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "type" VARCHAR(50) NOT NULL DEFAULT 'static',
    "segment_rules" JSONB,
    "contact_count" INTEGER NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "lists_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "list_contacts" (
    "id" BIGSERIAL NOT NULL,
    "list_id" INTEGER NOT NULL,
    "contact_id" BIGINT NOT NULL,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "list_contacts_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "campaigns" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "subject" VARCHAR(500) NOT NULL,
    "html_content" TEXT,
    "text_content" TEXT,
    "template_id" INTEGER,
    "from_name" VARCHAR(255) NOT NULL,
    "from_email" VARCHAR(255) NOT NULL,
    "reply_to" VARCHAR(255),
    "list_id" INTEGER NOT NULL,
    "status" VARCHAR(50) NOT NULL DEFAULT 'draft',
    "scheduled_at" TIMESTAMPTZ(6),
    "started_at" TIMESTAMPTZ(6),
    "completed_at" TIMESTAMPTZ(6),
    "total_recipients" INTEGER NOT NULL DEFAULT 0,
    "sent_count" INTEGER NOT NULL DEFAULT 0,
    "delivered_count" INTEGER NOT NULL DEFAULT 0,
    "open_count" INTEGER NOT NULL DEFAULT 0,
    "click_count" INTEGER NOT NULL DEFAULT 0,
    "bounce_count" INTEGER NOT NULL DEFAULT 0,
    "unsubscribe_count" INTEGER NOT NULL DEFAULT 0,
    "complaint_count" INTEGER NOT NULL DEFAULT 0,
    "is_ab_test" BOOLEAN NOT NULL DEFAULT false,
    "ab_test_settings" JSONB,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "campaigns_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "templates" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "html_content" TEXT NOT NULL,
    "text_content" TEXT,
    "category" VARCHAR(50) NOT NULL DEFAULT 'general',
    "variables_schema" JSONB,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "templates_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "transactional_emails" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "identity_id" INTEGER NOT NULL DEFAULT 0,
    "message_id" VARCHAR(500) NOT NULL,
    "from_address" VARCHAR(255) NOT NULL,
    "to_addresses" TEXT NOT NULL,
    "cc_addresses" TEXT,
    "bcc_addresses" TEXT,
    "reply_to" VARCHAR(255),
    "subject" VARCHAR(500) NOT NULL,
    "html_body" TEXT,
    "text_body" TEXT,
    "template_id" INTEGER,
    "tags" TEXT,
    "metadata" TEXT,
    "status" VARCHAR(50) NOT NULL DEFAULT 'queued',
    "scheduled_for" TIMESTAMPTZ(6),
    "sent_at" TIMESTAMPTZ(6),
    "delivered_at" TIMESTAMPTZ(6),
    "opened_at" TIMESTAMPTZ(6),
    "clicked_at" TIMESTAMPTZ(6),
    "bounced_at" TIMESTAMPTZ(6),
    "bounce_type" VARCHAR(20),
    "bounce_reason" TEXT,
    "idempotency_key" VARCHAR(255),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,
    "provider_message_id" VARCHAR(255),
    "email_provider" VARCHAR(20) DEFAULT 'ses',

    CONSTRAINT "transactional_emails_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "email_templates" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "subject" VARCHAR(500) NOT NULL,
    "html_body" TEXT NOT NULL,
    "text_body" TEXT,
    "variables" TEXT,
    "is_active" BOOLEAN NOT NULL DEFAULT true,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "email_templates_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "transactional_delivery_events" (
    "id" BIGSERIAL NOT NULL,
    "email_id" BIGINT NOT NULL,
    "event_type" VARCHAR(50) NOT NULL,
    "details" TEXT,
    "ip_address" VARCHAR(45),
    "user_agent" TEXT,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "transactional_delivery_events_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "suppression_list" (
    "id" BIGSERIAL NOT NULL,
    "org_id" INTEGER NOT NULL,
    "email" VARCHAR(255) NOT NULL,
    "reason" TEXT,
    "source" VARCHAR(50) NOT NULL DEFAULT 'manual',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "suppression_list_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "emails" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "message_id" VARCHAR(255) NOT NULL,
    "identity_id" INTEGER NOT NULL,
    "from_email" VARCHAR(255) NOT NULL,
    "from_name" VARCHAR(255),
    "to_emails" TEXT[],
    "cc_emails" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "bcc_emails" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "reply_to" VARCHAR(255),
    "subject" VARCHAR(500) NOT NULL,
    "html_content" TEXT,
    "text_content" TEXT,
    "source" VARCHAR(50) NOT NULL DEFAULT 'api',
    "domain_id" INTEGER NOT NULL,
    "campaign_id" INTEGER,
    "template_id" INTEGER,
    "contact_id" BIGINT,
    "tags" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "metadata" JSONB NOT NULL DEFAULT '{}',
    "headers" JSONB NOT NULL DEFAULT '{}',
    "status" VARCHAR(50) NOT NULL DEFAULT 'queued',
    "scheduled_at" TIMESTAMPTZ(6),
    "sent_at" TIMESTAMPTZ(6),
    "delivered_at" TIMESTAMPTZ(6),
    "open_count" INTEGER NOT NULL DEFAULT 0,
    "click_count" INTEGER NOT NULL DEFAULT 0,
    "idempotency_key" VARCHAR(255),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,
    "provider_message_id" VARCHAR(255),
    "email_provider" VARCHAR(20) DEFAULT 'ses',

    CONSTRAINT "emails_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "delivery_events" (
    "id" BIGSERIAL NOT NULL,
    "email_id" BIGINT NOT NULL,
    "event_type" VARCHAR(50) NOT NULL,
    "stalwart_message_id" VARCHAR(255),
    "data" JSONB NOT NULL DEFAULT '{}',
    "occurred_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "delivery_events_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "webhooks" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "url" VARCHAR(2048) NOT NULL,
    "secret" VARCHAR(255) NOT NULL,
    "events" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "active" BOOLEAN NOT NULL DEFAULT true,
    "success_count" INTEGER NOT NULL DEFAULT 0,
    "failure_count" INTEGER NOT NULL DEFAULT 0,
    "last_triggered_at" TIMESTAMPTZ(6),
    "last_success_at" TIMESTAMPTZ(6),
    "last_failure_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "webhooks_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "webhook_calls" (
    "id" BIGSERIAL NOT NULL,
    "webhook_id" INTEGER NOT NULL,
    "event_type" VARCHAR(50) NOT NULL,
    "payload" JSONB NOT NULL,
    "response_status" INTEGER,
    "response_body" TEXT,
    "response_time_ms" INTEGER,
    "status" VARCHAR(50) NOT NULL DEFAULT 'pending',
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "error" TEXT,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "completed_at" TIMESTAMPTZ(6),

    CONSTRAINT "webhook_calls_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "suppressions" (
    "id" BIGSERIAL NOT NULL,
    "org_id" INTEGER NOT NULL,
    "email" VARCHAR(255) NOT NULL,
    "reason" VARCHAR(50) NOT NULL,
    "source_type" VARCHAR(50) NOT NULL,
    "source_id" VARCHAR(255),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "suppressions_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "warmup_progress" (
    "id" SERIAL NOT NULL,
    "org_id" INTEGER NOT NULL,
    "ip_address" VARCHAR(45) NOT NULL,
    "schedule_name" VARCHAR(50) NOT NULL DEFAULT 'conservative',
    "current_day" INTEGER NOT NULL DEFAULT 1,
    "status" VARCHAR(20) NOT NULL DEFAULT 'active',
    "pause_reason" TEXT,
    "started_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "completed_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "warmup_progress_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "alerts" (
    "id" BIGSERIAL NOT NULL,
    "org_id" INTEGER NOT NULL,
    "type" VARCHAR(50) NOT NULL,
    "severity" VARCHAR(20) NOT NULL,
    "title" VARCHAR(255) NOT NULL,
    "message" TEXT NOT NULL,
    "data" JSONB,
    "acknowledged" BOOLEAN NOT NULL DEFAULT false,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "alerts_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "blacklist_checks" (
    "id" BIGSERIAL NOT NULL,
    "ip_address" VARCHAR(45) NOT NULL,
    "listed_count" INTEGER NOT NULL DEFAULT 0,
    "results" JSONB NOT NULL,
    "checked_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "blacklist_checks_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "email_rules" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "priority" INTEGER NOT NULL DEFAULT 0,
    "conditions" JSONB NOT NULL,
    "condition_logic" VARCHAR(10) NOT NULL DEFAULT 'all',
    "actions" JSONB NOT NULL,
    "identity_ids" INTEGER[] DEFAULT ARRAY[]::INTEGER[],
    "active" BOOLEAN NOT NULL DEFAULT true,
    "match_count" INTEGER NOT NULL DEFAULT 0,
    "last_matched_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "email_rules_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "auto_replies" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "start_date" TIMESTAMPTZ(6) NOT NULL,
    "end_date" TIMESTAMPTZ(6),
    "subject" VARCHAR(500) NOT NULL,
    "html_content" TEXT NOT NULL,
    "text_content" TEXT,
    "reply_once" BOOLEAN NOT NULL DEFAULT true,
    "reply_to_all" BOOLEAN NOT NULL DEFAULT true,
    "exclude_patterns" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "identity_ids" INTEGER[] DEFAULT ARRAY[]::INTEGER[],
    "active" BOOLEAN NOT NULL DEFAULT true,
    "reply_count" INTEGER NOT NULL DEFAULT 0,
    "last_replied_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "auto_replies_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "auto_reply_senders" (
    "id" BIGSERIAL NOT NULL,
    "auto_reply_id" INTEGER NOT NULL,
    "sender_email" VARCHAR(255) NOT NULL,
    "replied_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "auto_reply_senders_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "email_forwards" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "org_id" INTEGER NOT NULL,
    "identity_id" INTEGER NOT NULL,
    "forward_to" VARCHAR(255) NOT NULL,
    "keep_copy" BOOLEAN NOT NULL DEFAULT true,
    "active" BOOLEAN NOT NULL DEFAULT true,
    "verified" BOOLEAN NOT NULL DEFAULT false,
    "verify_token" VARCHAR(100),
    "verified_at" TIMESTAMPTZ(6),
    "forward_count" INTEGER NOT NULL DEFAULT 0,
    "last_forwarded_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "email_forwards_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "audit_logs" (
    "id" BIGSERIAL NOT NULL,
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER,
    "action" VARCHAR(100) NOT NULL,
    "resource" VARCHAR(100) NOT NULL,
    "resource_id" VARCHAR(255),
    "description" TEXT,
    "ip_address" VARCHAR(45),
    "user_agent" TEXT,
    "request_id" VARCHAR(100),
    "old_values" JSONB,
    "new_values" JSONB,
    "status" VARCHAR(20) NOT NULL DEFAULT 'success',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "audit_logs_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "user_sessions" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "org_id" INTEGER NOT NULL,
    "token_hash" VARCHAR(64) NOT NULL,
    "device_name" VARCHAR(255),
    "device_type" VARCHAR(50),
    "browser" VARCHAR(100),
    "os" VARCHAR(100),
    "ip_address" VARCHAR(45),
    "location" VARCHAR(255),
    "active" BOOLEAN NOT NULL DEFAULT true,
    "last_seen_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expires_at" TIMESTAMPTZ(6) NOT NULL,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "revoked_at" TIMESTAMPTZ(6),

    CONSTRAINT "user_sessions_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "webauthn_credentials" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "credential_id" BYTEA NOT NULL,
    "public_key" BYTEA NOT NULL,
    "sign_count" INTEGER NOT NULL DEFAULT 0,
    "transports" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "aaguid" VARCHAR(36),
    "attestation_type" VARCHAR(50),
    "name" VARCHAR(255) NOT NULL,
    "device_type" VARCHAR(50),
    "last_used_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "webauthn_credentials_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "shared_mailboxes" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "email" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "auto_reply_enabled" BOOLEAN NOT NULL DEFAULT false,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "shared_mailboxes_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "shared_mailbox_members" (
    "id" SERIAL NOT NULL,
    "shared_mailbox_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    "can_read" BOOLEAN NOT NULL DEFAULT true,
    "can_send" BOOLEAN NOT NULL DEFAULT false,
    "can_manage" BOOLEAN NOT NULL DEFAULT false,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "shared_mailbox_members_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "sieve_scripts" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "script" TEXT NOT NULL,
    "active" BOOLEAN NOT NULL DEFAULT false,
    "is_default" BOOLEAN NOT NULL DEFAULT false,
    "is_valid" BOOLEAN NOT NULL DEFAULT true,
    "last_error" TEXT,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "sieve_scripts_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "webhook_triggers" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "trigger_type" VARCHAR(50) NOT NULL,
    "filters" JSONB,
    "webhook_url" TEXT NOT NULL,
    "secret" VARCHAR(255),
    "active" BOOLEAN NOT NULL DEFAULT true,
    "last_triggered_at" TIMESTAMPTZ(6),
    "trigger_count" INTEGER NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "webhook_triggers_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "push_subscriptions" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "endpoint" TEXT NOT NULL,
    "p256dh_key" TEXT NOT NULL,
    "auth_key" VARCHAR(255) NOT NULL,
    "user_agent" TEXT,
    "device_name" VARCHAR(255),
    "notify_new_email" BOOLEAN NOT NULL DEFAULT true,
    "notify_campaign" BOOLEAN NOT NULL DEFAULT false,
    "notify_mentions" BOOLEAN NOT NULL DEFAULT true,
    "active" BOOLEAN NOT NULL DEFAULT true,
    "last_used_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "push_subscriptions_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "tenant_brandings" (
    "id" SERIAL NOT NULL,
    "org_id" INTEGER NOT NULL,
    "logo_url" TEXT,
    "logo_light_url" TEXT,
    "favicon_url" TEXT,
    "primary_color" VARCHAR(7),
    "accent_color" VARCHAR(7),
    "custom_domain" VARCHAR(255),
    "custom_domain_verified" BOOLEAN NOT NULL DEFAULT false,
    "email_footer_html" TEXT,
    "email_header_html" TEXT,
    "hide_powered_by" BOOLEAN NOT NULL DEFAULT false,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "tenant_brandings_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "user_settings" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "user_id" INTEGER NOT NULL,
    "org_id" INTEGER NOT NULL,
    "display_name" VARCHAR(255),
    "show_snippets" BOOLEAN NOT NULL DEFAULT true,
    "conversation_view" BOOLEAN NOT NULL DEFAULT true,
    "auto_advance" BOOLEAN NOT NULL DEFAULT false,
    "new_email_notifications" BOOLEAN NOT NULL DEFAULT true,
    "campaign_reports" BOOLEAN NOT NULL DEFAULT true,
    "weekly_digest" BOOLEAN NOT NULL DEFAULT false,
    "blacklist_alerts" BOOLEAN NOT NULL DEFAULT true,
    "bounce_rate_warnings" BOOLEAN NOT NULL DEFAULT true,
    "quota_warnings" BOOLEAN NOT NULL DEFAULT true,
    "browser_notifications" BOOLEAN NOT NULL DEFAULT false,
    "theme" VARCHAR(20) NOT NULL DEFAULT 'light',
    "density" VARCHAR(20) NOT NULL DEFAULT 'comfortable',
    "inbox_layout" VARCHAR(20) NOT NULL DEFAULT 'default',
    "two_factor_enabled" BOOLEAN NOT NULL DEFAULT false,
    "two_factor_method" VARCHAR(20),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "user_settings_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "automations" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "name" VARCHAR(255) NOT NULL,
    "description" TEXT,
    "trigger_type" VARCHAR(50) NOT NULL,
    "trigger_config" JSONB NOT NULL DEFAULT '{}',
    "workflow" JSONB NOT NULL DEFAULT '{"edges": [], "nodes": []}',
    "status" VARCHAR(50) NOT NULL DEFAULT 'draft',
    "enrolled_count" INTEGER NOT NULL DEFAULT 0,
    "completed_count" INTEGER NOT NULL DEFAULT 0,
    "in_progress_count" INTEGER NOT NULL DEFAULT 0,
    "error_count" INTEGER NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "automations_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "automation_enrollments" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "automation_id" INTEGER NOT NULL,
    "contact_id" BIGINT NOT NULL,
    "org_id" INTEGER NOT NULL,
    "status" VARCHAR(50) NOT NULL DEFAULT 'active',
    "step_index" INTEGER NOT NULL DEFAULT 0,
    "step_data" JSONB NOT NULL DEFAULT '{}',
    "next_run_at" TIMESTAMPTZ(6),
    "completed_at" TIMESTAMPTZ(6),
    "error_message" TEXT,
    "retry_count" INTEGER NOT NULL DEFAULT 0,
    "enrolled_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "automation_enrollments_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "automation_logs" (
    "id" BIGSERIAL NOT NULL,
    "enrollment_id" BIGINT NOT NULL,
    "automation_id" INTEGER NOT NULL,
    "step_index" INTEGER NOT NULL,
    "step_type" VARCHAR(50) NOT NULL,
    "status" VARCHAR(50) NOT NULL DEFAULT 'success',
    "message" TEXT,
    "data" JSONB,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "automation_logs_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "consent_audit" (
    "id" BIGSERIAL NOT NULL,
    "contact_id" BIGINT NOT NULL,
    "org_id" INTEGER NOT NULL,
    "action" VARCHAR(50) NOT NULL,
    "source" VARCHAR(50) NOT NULL,
    "list_id" INTEGER,
    "ip_address" VARCHAR(45),
    "user_agent" TEXT,
    "details" TEXT,
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "consent_audit_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "received_emails" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "domain_id" INTEGER NOT NULL,
    "identity_id" INTEGER NOT NULL,
    "message_id" VARCHAR(500) NOT NULL,
    "in_reply_to" VARCHAR(500),
    "references" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "thread_id" VARCHAR(100),
    "from_email" VARCHAR(255) NOT NULL,
    "from_name" VARCHAR(255),
    "to_emails" TEXT[],
    "cc_emails" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "bcc_emails" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "reply_to" VARCHAR(255),
    "subject" VARCHAR(1000) NOT NULL,
    "text_body" TEXT,
    "html_body" TEXT,
    "snippet" VARCHAR(500),
    "raw_s3_key" VARCHAR(500),
    "raw_s3_bucket" VARCHAR(255),
    "size_bytes" INTEGER NOT NULL DEFAULT 0,
    "has_attachments" BOOLEAN NOT NULL DEFAULT false,
    "folder" VARCHAR(50) NOT NULL DEFAULT 'inbox',
    "is_read" BOOLEAN NOT NULL DEFAULT false,
    "is_starred" BOOLEAN NOT NULL DEFAULT false,
    "is_archived" BOOLEAN NOT NULL DEFAULT false,
    "is_trashed" BOOLEAN NOT NULL DEFAULT false,
    "is_spam" BOOLEAN NOT NULL DEFAULT false,
    "labels" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "spam_score" DOUBLE PRECISION,
    "spam_verdict" VARCHAR(20),
    "virus_verdict" VARCHAR(20),
    "spf_verdict" VARCHAR(20),
    "dkim_verdict" VARCHAR(20),
    "dmarc_verdict" VARCHAR(20),
    "ses_message_id" VARCHAR(255),
    "sns_notification_id" VARCHAR(255),
    "received_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "read_at" TIMESTAMPTZ(6),
    "trashed_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "received_emails_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "email_attachments" (
    "id" BIGSERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "received_email_id" BIGINT NOT NULL,
    "filename" VARCHAR(255) NOT NULL,
    "content_type" VARCHAR(255) NOT NULL,
    "size_bytes" INTEGER NOT NULL,
    "s3_key" VARCHAR(500) NOT NULL,
    "s3_bucket" VARCHAR(255) NOT NULL,
    "content_id" VARCHAR(255),
    "is_inline" BOOLEAN NOT NULL DEFAULT false,
    "checksum" VARCHAR(64),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "email_attachments_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "email_labels" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    "name" VARCHAR(100) NOT NULL,
    "color" VARCHAR(7) NOT NULL DEFAULT '#6366f1',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "email_labels_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "inbox_filters" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    "identity_id" INTEGER,
    "name" VARCHAR(255) NOT NULL,
    "priority" INTEGER NOT NULL DEFAULT 0,
    "active" BOOLEAN NOT NULL DEFAULT true,
    "conditions" JSONB NOT NULL DEFAULT '[]',
    "condition_logic" VARCHAR(10) NOT NULL DEFAULT 'all',
    "action_labels" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "action_folder" VARCHAR(50),
    "action_star" BOOLEAN NOT NULL DEFAULT false,
    "action_mark_read" BOOLEAN NOT NULL DEFAULT false,
    "action_archive" BOOLEAN NOT NULL DEFAULT false,
    "action_trash" BOOLEAN NOT NULL DEFAULT false,
    "action_forward" VARCHAR(255),
    "match_count" INTEGER NOT NULL DEFAULT 0,
    "last_matched_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "inbox_filters_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "receiving_configs" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "s3_bucket" VARCHAR(255) NOT NULL,
    "s3_region" VARCHAR(50) NOT NULL,
    "sns_topic_arn" VARCHAR(255) NOT NULL,
    "ses_rule_set_name" VARCHAR(255) NOT NULL,
    "webhook_secret" VARCHAR(255) NOT NULL,
    "status" VARCHAR(50) NOT NULL DEFAULT 'pending',
    "last_health_check" TIMESTAMPTZ(6),
    "setup_completed_at" TIMESTAMPTZ(6),
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "receiving_configs_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "organizations_uuid_key" ON "organizations"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "organizations_slug_key" ON "organizations"("slug");

-- CreateIndex
CREATE UNIQUE INDEX "users_uuid_key" ON "users"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "users_email_key" ON "users"("email");

-- CreateIndex
CREATE INDEX "oauth_connections_user_id_idx" ON "oauth_connections"("user_id");

-- CreateIndex
CREATE UNIQUE INDEX "oauth_connections_provider_provider_user_id_key" ON "oauth_connections"("provider", "provider_user_id");

-- CreateIndex
CREATE UNIQUE INDEX "oauth_connections_user_id_provider_key" ON "oauth_connections"("user_id", "provider");

-- CreateIndex
CREATE UNIQUE INDEX "api_keys_uuid_key" ON "api_keys"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "domains_uuid_key" ON "domains"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "domains_org_id_name_key" ON "domains"("org_id", "name");

-- CreateIndex
CREATE UNIQUE INDEX "domain_dns_records_domain_id_record_type_hostname_key" ON "domain_dns_records"("domain_id", "record_type", "hostname");

-- CreateIndex
CREATE UNIQUE INDEX "identities_uuid_key" ON "identities"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "identities_email_key" ON "identities"("email");

-- CreateIndex
CREATE INDEX "identities_stalwart_account_id_idx" ON "identities"("stalwart_account_id");

-- CreateIndex
CREATE INDEX "identities_domain_id_is_catch_all_idx" ON "identities"("domain_id", "is_catch_all");

-- CreateIndex
CREATE UNIQUE INDEX "message_metadata_stalwart_message_id_key" ON "message_metadata"("stalwart_message_id");

-- CreateIndex
CREATE INDEX "message_metadata_identity_id_idx" ON "message_metadata"("identity_id");

-- CreateIndex
CREATE INDEX "message_metadata_campaign_id_idx" ON "message_metadata"("campaign_id");

-- CreateIndex
CREATE INDEX "message_metadata_thread_group_id_idx" ON "message_metadata"("thread_group_id");

-- CreateIndex
CREATE INDEX "thread_groups_user_id_last_message_at_idx" ON "thread_groups"("user_id", "last_message_at" DESC);

-- CreateIndex
CREATE UNIQUE INDEX "contacts_uuid_key" ON "contacts"("uuid");

-- CreateIndex
CREATE INDEX "contacts_org_id_status_idx" ON "contacts"("org_id", "status");

-- CreateIndex
CREATE UNIQUE INDEX "contacts_org_id_email_key" ON "contacts"("org_id", "email");

-- CreateIndex
CREATE UNIQUE INDEX "lists_uuid_key" ON "lists"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "list_contacts_list_id_contact_id_key" ON "list_contacts"("list_id", "contact_id");

-- CreateIndex
CREATE UNIQUE INDEX "campaigns_uuid_key" ON "campaigns"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "templates_uuid_key" ON "templates"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "transactional_emails_uuid_key" ON "transactional_emails"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "transactional_emails_message_id_key" ON "transactional_emails"("message_id");

-- CreateIndex
CREATE INDEX "transactional_emails_org_id_created_at_idx" ON "transactional_emails"("org_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "transactional_emails_status_idx" ON "transactional_emails"("status");

-- CreateIndex
CREATE INDEX "transactional_emails_idempotency_key_idx" ON "transactional_emails"("idempotency_key");

-- CreateIndex
CREATE INDEX "transactional_emails_provider_message_id_idx" ON "transactional_emails"("provider_message_id");

-- CreateIndex
CREATE UNIQUE INDEX "email_templates_uuid_key" ON "email_templates"("uuid");

-- CreateIndex
CREATE INDEX "email_templates_org_id_idx" ON "email_templates"("org_id");

-- CreateIndex
CREATE INDEX "transactional_delivery_events_email_id_created_at_idx" ON "transactional_delivery_events"("email_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "suppression_list_org_id_email_idx" ON "suppression_list"("org_id", "email");

-- CreateIndex
CREATE UNIQUE INDEX "suppression_list_org_id_email_key" ON "suppression_list"("org_id", "email");

-- CreateIndex
CREATE UNIQUE INDEX "emails_uuid_key" ON "emails"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "emails_message_id_key" ON "emails"("message_id");

-- CreateIndex
CREATE UNIQUE INDEX "emails_idempotency_key_key" ON "emails"("idempotency_key");

-- CreateIndex
CREATE INDEX "emails_org_id_created_at_idx" ON "emails"("org_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "emails_status_idx" ON "emails"("status");

-- CreateIndex
CREATE INDEX "emails_message_id_idx" ON "emails"("message_id");

-- CreateIndex
CREATE INDEX "emails_provider_message_id_idx" ON "emails"("provider_message_id");

-- CreateIndex
CREATE INDEX "delivery_events_email_id_occurred_at_idx" ON "delivery_events"("email_id", "occurred_at" DESC);

-- CreateIndex
CREATE UNIQUE INDEX "webhooks_uuid_key" ON "webhooks"("uuid");

-- CreateIndex
CREATE INDEX "webhook_calls_webhook_id_created_at_idx" ON "webhook_calls"("webhook_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "suppressions_org_id_email_idx" ON "suppressions"("org_id", "email");

-- CreateIndex
CREATE UNIQUE INDEX "suppressions_org_id_email_key" ON "suppressions"("org_id", "email");

-- CreateIndex
CREATE UNIQUE INDEX "warmup_progress_org_id_ip_address_key" ON "warmup_progress"("org_id", "ip_address");

-- CreateIndex
CREATE INDEX "alerts_org_id_acknowledged_created_at_idx" ON "alerts"("org_id", "acknowledged", "created_at" DESC);

-- CreateIndex
CREATE INDEX "blacklist_checks_ip_address_checked_at_idx" ON "blacklist_checks"("ip_address", "checked_at" DESC);

-- CreateIndex
CREATE UNIQUE INDEX "email_rules_uuid_key" ON "email_rules"("uuid");

-- CreateIndex
CREATE INDEX "email_rules_user_id_active_priority_idx" ON "email_rules"("user_id", "active", "priority");

-- CreateIndex
CREATE INDEX "email_rules_org_id_idx" ON "email_rules"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "auto_replies_uuid_key" ON "auto_replies"("uuid");

-- CreateIndex
CREATE INDEX "auto_replies_user_id_active_idx" ON "auto_replies"("user_id", "active");

-- CreateIndex
CREATE INDEX "auto_replies_org_id_idx" ON "auto_replies"("org_id");

-- CreateIndex
CREATE INDEX "auto_reply_senders_auto_reply_id_sender_email_idx" ON "auto_reply_senders"("auto_reply_id", "sender_email");

-- CreateIndex
CREATE UNIQUE INDEX "auto_reply_senders_auto_reply_id_sender_email_key" ON "auto_reply_senders"("auto_reply_id", "sender_email");

-- CreateIndex
CREATE UNIQUE INDEX "email_forwards_uuid_key" ON "email_forwards"("uuid");

-- CreateIndex
CREATE INDEX "email_forwards_user_id_active_idx" ON "email_forwards"("user_id", "active");

-- CreateIndex
CREATE UNIQUE INDEX "email_forwards_identity_id_forward_to_key" ON "email_forwards"("identity_id", "forward_to");

-- CreateIndex
CREATE INDEX "audit_logs_org_id_created_at_idx" ON "audit_logs"("org_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "audit_logs_user_id_created_at_idx" ON "audit_logs"("user_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "audit_logs_action_created_at_idx" ON "audit_logs"("action", "created_at" DESC);

-- CreateIndex
CREATE UNIQUE INDEX "user_sessions_uuid_key" ON "user_sessions"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "user_sessions_token_hash_key" ON "user_sessions"("token_hash");

-- CreateIndex
CREATE INDEX "user_sessions_user_id_active_idx" ON "user_sessions"("user_id", "active");

-- CreateIndex
CREATE INDEX "user_sessions_org_id_idx" ON "user_sessions"("org_id");

-- CreateIndex
CREATE INDEX "user_sessions_token_hash_idx" ON "user_sessions"("token_hash");

-- CreateIndex
CREATE INDEX "user_sessions_expires_at_idx" ON "user_sessions"("expires_at");

-- CreateIndex
CREATE UNIQUE INDEX "webauthn_credentials_uuid_key" ON "webauthn_credentials"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "webauthn_credentials_credential_id_key" ON "webauthn_credentials"("credential_id");

-- CreateIndex
CREATE INDEX "webauthn_credentials_user_id_idx" ON "webauthn_credentials"("user_id");

-- CreateIndex
CREATE UNIQUE INDEX "shared_mailboxes_uuid_key" ON "shared_mailboxes"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "shared_mailboxes_email_key" ON "shared_mailboxes"("email");

-- CreateIndex
CREATE INDEX "shared_mailboxes_org_id_idx" ON "shared_mailboxes"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "shared_mailbox_members_shared_mailbox_id_user_id_key" ON "shared_mailbox_members"("shared_mailbox_id", "user_id");

-- CreateIndex
CREATE UNIQUE INDEX "sieve_scripts_uuid_key" ON "sieve_scripts"("uuid");

-- CreateIndex
CREATE INDEX "sieve_scripts_user_id_active_idx" ON "sieve_scripts"("user_id", "active");

-- CreateIndex
CREATE UNIQUE INDEX "webhook_triggers_uuid_key" ON "webhook_triggers"("uuid");

-- CreateIndex
CREATE INDEX "webhook_triggers_org_id_trigger_type_active_idx" ON "webhook_triggers"("org_id", "trigger_type", "active");

-- CreateIndex
CREATE UNIQUE INDEX "push_subscriptions_uuid_key" ON "push_subscriptions"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "push_subscriptions_endpoint_key" ON "push_subscriptions"("endpoint");

-- CreateIndex
CREATE INDEX "push_subscriptions_user_id_active_idx" ON "push_subscriptions"("user_id", "active");

-- CreateIndex
CREATE UNIQUE INDEX "tenant_brandings_org_id_key" ON "tenant_brandings"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "tenant_brandings_custom_domain_key" ON "tenant_brandings"("custom_domain");

-- CreateIndex
CREATE UNIQUE INDEX "user_settings_uuid_key" ON "user_settings"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "user_settings_user_id_key" ON "user_settings"("user_id");

-- CreateIndex
CREATE INDEX "user_settings_user_id_idx" ON "user_settings"("user_id");

-- CreateIndex
CREATE INDEX "user_settings_org_id_idx" ON "user_settings"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "automations_uuid_key" ON "automations"("uuid");

-- CreateIndex
CREATE INDEX "automations_org_id_status_idx" ON "automations"("org_id", "status");

-- CreateIndex
CREATE UNIQUE INDEX "automation_enrollments_uuid_key" ON "automation_enrollments"("uuid");

-- CreateIndex
CREATE INDEX "automation_enrollments_automation_id_status_idx" ON "automation_enrollments"("automation_id", "status");

-- CreateIndex
CREATE INDEX "automation_enrollments_org_id_idx" ON "automation_enrollments"("org_id");

-- CreateIndex
CREATE INDEX "automation_enrollments_next_run_at_idx" ON "automation_enrollments"("next_run_at");

-- CreateIndex
CREATE UNIQUE INDEX "automation_enrollments_automation_id_contact_id_key" ON "automation_enrollments"("automation_id", "contact_id");

-- CreateIndex
CREATE INDEX "automation_logs_enrollment_id_created_at_idx" ON "automation_logs"("enrollment_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "automation_logs_automation_id_created_at_idx" ON "automation_logs"("automation_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "consent_audit_contact_id_created_at_idx" ON "consent_audit"("contact_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "consent_audit_org_id_created_at_idx" ON "consent_audit"("org_id", "created_at" DESC);

-- CreateIndex
CREATE UNIQUE INDEX "received_emails_uuid_key" ON "received_emails"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "received_emails_message_id_key" ON "received_emails"("message_id");

-- CreateIndex
CREATE INDEX "received_emails_org_id_identity_id_folder_received_at_idx" ON "received_emails"("org_id", "identity_id", "folder", "received_at" DESC);

-- CreateIndex
CREATE INDEX "received_emails_org_id_identity_id_is_read_idx" ON "received_emails"("org_id", "identity_id", "is_read");

-- CreateIndex
CREATE INDEX "received_emails_identity_id_folder_is_trashed_received_at_idx" ON "received_emails"("identity_id", "folder", "is_trashed", "received_at" DESC);

-- CreateIndex
CREATE INDEX "received_emails_identity_id_is_starred_idx" ON "received_emails"("identity_id", "is_starred");

-- CreateIndex
CREATE INDEX "received_emails_thread_id_idx" ON "received_emails"("thread_id");

-- CreateIndex
CREATE INDEX "received_emails_ses_message_id_idx" ON "received_emails"("ses_message_id");

-- CreateIndex
CREATE UNIQUE INDEX "email_attachments_uuid_key" ON "email_attachments"("uuid");

-- CreateIndex
CREATE INDEX "email_attachments_received_email_id_idx" ON "email_attachments"("received_email_id");

-- CreateIndex
CREATE UNIQUE INDEX "email_labels_uuid_key" ON "email_labels"("uuid");

-- CreateIndex
CREATE INDEX "email_labels_org_id_user_id_idx" ON "email_labels"("org_id", "user_id");

-- CreateIndex
CREATE UNIQUE INDEX "email_labels_user_id_name_key" ON "email_labels"("user_id", "name");

-- CreateIndex
CREATE UNIQUE INDEX "inbox_filters_uuid_key" ON "inbox_filters"("uuid");

-- CreateIndex
CREATE INDEX "inbox_filters_user_id_active_priority_idx" ON "inbox_filters"("user_id", "active", "priority");

-- CreateIndex
CREATE INDEX "inbox_filters_org_id_idx" ON "inbox_filters"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "receiving_configs_uuid_key" ON "receiving_configs"("uuid");

-- CreateIndex
CREATE UNIQUE INDEX "receiving_configs_org_id_key" ON "receiving_configs"("org_id");

-- AddForeignKey
ALTER TABLE "users" ADD CONSTRAINT "users_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "oauth_connections" ADD CONSTRAINT "oauth_connections_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "api_keys" ADD CONSTRAINT "api_keys_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "domains" ADD CONSTRAINT "domains_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "domain_dns_records" ADD CONSTRAINT "domain_dns_records_domain_id_fkey" FOREIGN KEY ("domain_id") REFERENCES "domains"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "identities" ADD CONSTRAINT "identities_domain_id_fkey" FOREIGN KEY ("domain_id") REFERENCES "domains"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "identities" ADD CONSTRAINT "identities_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "message_metadata" ADD CONSTRAINT "message_metadata_campaign_id_fkey" FOREIGN KEY ("campaign_id") REFERENCES "campaigns"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "message_metadata" ADD CONSTRAINT "message_metadata_contact_id_fkey" FOREIGN KEY ("contact_id") REFERENCES "contacts"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "message_metadata" ADD CONSTRAINT "message_metadata_identity_id_fkey" FOREIGN KEY ("identity_id") REFERENCES "identities"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "message_metadata" ADD CONSTRAINT "message_metadata_thread_group_id_fkey" FOREIGN KEY ("thread_group_id") REFERENCES "thread_groups"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "thread_groups" ADD CONSTRAINT "thread_groups_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "contacts" ADD CONSTRAINT "contacts_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "lists" ADD CONSTRAINT "lists_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "list_contacts" ADD CONSTRAINT "list_contacts_contact_id_fkey" FOREIGN KEY ("contact_id") REFERENCES "contacts"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "list_contacts" ADD CONSTRAINT "list_contacts_list_id_fkey" FOREIGN KEY ("list_id") REFERENCES "lists"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "campaigns" ADD CONSTRAINT "campaigns_list_id_fkey" FOREIGN KEY ("list_id") REFERENCES "lists"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "campaigns" ADD CONSTRAINT "campaigns_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "campaigns" ADD CONSTRAINT "campaigns_template_id_fkey" FOREIGN KEY ("template_id") REFERENCES "templates"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "templates" ADD CONSTRAINT "templates_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "transactional_emails" ADD CONSTRAINT "transactional_emails_template_id_fkey" FOREIGN KEY ("template_id") REFERENCES "email_templates"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "transactional_delivery_events" ADD CONSTRAINT "transactional_delivery_events_email_id_fkey" FOREIGN KEY ("email_id") REFERENCES "transactional_emails"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "emails" ADD CONSTRAINT "emails_campaign_id_fkey" FOREIGN KEY ("campaign_id") REFERENCES "campaigns"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "emails" ADD CONSTRAINT "emails_contact_id_fkey" FOREIGN KEY ("contact_id") REFERENCES "contacts"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "emails" ADD CONSTRAINT "emails_domain_id_fkey" FOREIGN KEY ("domain_id") REFERENCES "domains"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "emails" ADD CONSTRAINT "emails_identity_id_fkey" FOREIGN KEY ("identity_id") REFERENCES "identities"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "emails" ADD CONSTRAINT "emails_template_id_fkey" FOREIGN KEY ("template_id") REFERENCES "templates"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "delivery_events" ADD CONSTRAINT "delivery_events_email_id_fkey" FOREIGN KEY ("email_id") REFERENCES "emails"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "webhooks" ADD CONSTRAINT "webhooks_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "webhook_calls" ADD CONSTRAINT "webhook_calls_webhook_id_fkey" FOREIGN KEY ("webhook_id") REFERENCES "webhooks"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "auto_reply_senders" ADD CONSTRAINT "auto_reply_senders_auto_reply_id_fkey" FOREIGN KEY ("auto_reply_id") REFERENCES "auto_replies"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "audit_logs" ADD CONSTRAINT "audit_logs_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "user_sessions" ADD CONSTRAINT "user_sessions_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "webauthn_credentials" ADD CONSTRAINT "webauthn_credentials_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "shared_mailboxes" ADD CONSTRAINT "shared_mailboxes_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "shared_mailbox_members" ADD CONSTRAINT "shared_mailbox_members_shared_mailbox_id_fkey" FOREIGN KEY ("shared_mailbox_id") REFERENCES "shared_mailboxes"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "shared_mailbox_members" ADD CONSTRAINT "shared_mailbox_members_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "sieve_scripts" ADD CONSTRAINT "sieve_scripts_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "push_subscriptions" ADD CONSTRAINT "push_subscriptions_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "tenant_brandings" ADD CONSTRAINT "tenant_brandings_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "automation_enrollments" ADD CONSTRAINT "automation_enrollments_automation_id_fkey" FOREIGN KEY ("automation_id") REFERENCES "automations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "received_emails" ADD CONSTRAINT "received_emails_domain_id_fkey" FOREIGN KEY ("domain_id") REFERENCES "domains"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "received_emails" ADD CONSTRAINT "received_emails_identity_id_fkey" FOREIGN KEY ("identity_id") REFERENCES "identities"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "email_attachments" ADD CONSTRAINT "email_attachments_received_email_id_fkey" FOREIGN KEY ("received_email_id") REFERENCES "received_emails"("id") ON DELETE CASCADE ON UPDATE CASCADE;

