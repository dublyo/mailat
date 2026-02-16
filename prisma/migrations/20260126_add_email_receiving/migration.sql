-- Add email receiving feature

-- Add isCatchAll to identities
ALTER TABLE "identities" ADD COLUMN IF NOT EXISTS "is_catch_all" BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS "identities_domain_id_is_catch_all_idx" ON "identities"("domain_id", "is_catch_all");

-- Add receiving configuration to domains
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "receiving_enabled" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "receiving_s3_bucket" VARCHAR(255);
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "receiving_sns_topic_arn" VARCHAR(255);
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "receiving_rule_set_name" VARCHAR(255);
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "receiving_rule_name" VARCHAR(255);
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "receiving_setup_at" TIMESTAMPTZ(6);

-- Create received_emails table
CREATE TABLE IF NOT EXISTS "received_emails" (
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
    "to_emails" TEXT[] NOT NULL,
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
    "updated_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "received_emails_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX IF NOT EXISTS "received_emails_uuid_key" ON "received_emails"("uuid");
CREATE UNIQUE INDEX IF NOT EXISTS "received_emails_message_id_key" ON "received_emails"("message_id");
CREATE INDEX IF NOT EXISTS "received_emails_org_id_identity_id_folder_received_at_idx" ON "received_emails"("org_id", "identity_id", "folder", "received_at" DESC);
CREATE INDEX IF NOT EXISTS "received_emails_org_id_identity_id_is_read_idx" ON "received_emails"("org_id", "identity_id", "is_read");
CREATE INDEX IF NOT EXISTS "received_emails_identity_id_folder_is_trashed_received_at_idx" ON "received_emails"("identity_id", "folder", "is_trashed", "received_at" DESC);
CREATE INDEX IF NOT EXISTS "received_emails_identity_id_is_starred_idx" ON "received_emails"("identity_id", "is_starred");
CREATE INDEX IF NOT EXISTS "received_emails_thread_id_idx" ON "received_emails"("thread_id");
CREATE INDEX IF NOT EXISTS "received_emails_ses_message_id_idx" ON "received_emails"("ses_message_id");

ALTER TABLE "received_emails" ADD CONSTRAINT "received_emails_domain_id_fkey" FOREIGN KEY ("domain_id") REFERENCES "domains"("id") ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE "received_emails" ADD CONSTRAINT "received_emails_identity_id_fkey" FOREIGN KEY ("identity_id") REFERENCES "identities"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- Create email_attachments table
CREATE TABLE IF NOT EXISTS "email_attachments" (
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

CREATE UNIQUE INDEX IF NOT EXISTS "email_attachments_uuid_key" ON "email_attachments"("uuid");
CREATE INDEX IF NOT EXISTS "email_attachments_received_email_id_idx" ON "email_attachments"("received_email_id");

ALTER TABLE "email_attachments" ADD CONSTRAINT "email_attachments_received_email_id_fkey" FOREIGN KEY ("received_email_id") REFERENCES "received_emails"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- Create email_labels table
CREATE TABLE IF NOT EXISTS "email_labels" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    "name" VARCHAR(100) NOT NULL,
    "color" VARCHAR(7) NOT NULL DEFAULT '#6366f1',
    "created_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "email_labels_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX IF NOT EXISTS "email_labels_uuid_key" ON "email_labels"("uuid");
CREATE UNIQUE INDEX IF NOT EXISTS "email_labels_user_id_name_key" ON "email_labels"("user_id", "name");
CREATE INDEX IF NOT EXISTS "email_labels_org_id_user_id_idx" ON "email_labels"("org_id", "user_id");

-- Create inbox_filters table
CREATE TABLE IF NOT EXISTS "inbox_filters" (
    "id" SERIAL NOT NULL,
    "uuid" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    "identity_id" INTEGER,
    "name" VARCHAR(255) NOT NULL,
    "priority" INTEGER NOT NULL DEFAULT 0,
    "active" BOOLEAN NOT NULL DEFAULT true,
    "conditions" JSONB NOT NULL DEFAULT '[]'::JSONB,
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
    "updated_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "inbox_filters_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX IF NOT EXISTS "inbox_filters_uuid_key" ON "inbox_filters"("uuid");
CREATE INDEX IF NOT EXISTS "inbox_filters_user_id_active_priority_idx" ON "inbox_filters"("user_id", "active", "priority");
CREATE INDEX IF NOT EXISTS "inbox_filters_org_id_idx" ON "inbox_filters"("org_id");

-- Create receiving_configs table
CREATE TABLE IF NOT EXISTS "receiving_configs" (
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
    "updated_at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "receiving_configs_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX IF NOT EXISTS "receiving_configs_uuid_key" ON "receiving_configs"("uuid");
CREATE UNIQUE INDEX IF NOT EXISTS "receiving_configs_org_id_key" ON "receiving_configs"("org_id");
