-- Add AWS provisioning fields to organizations table
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_provisioned" BOOLEAN DEFAULT false;
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_region" VARCHAR(20);
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_access_key_id" VARCHAR(100);
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_secret_access_key" VARCHAR(100);
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_s3_bucket" VARCHAR(100);
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_lambda_arn" VARCHAR(255);
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_sns_topic_arn" VARCHAR(255);
ALTER TABLE "organizations" ADD COLUMN IF NOT EXISTS "aws_receipt_rule_set" VARCHAR(100);

-- Add email_attachments table if not exists
CREATE TABLE IF NOT EXISTS "email_attachments" (
    "id" BIGSERIAL PRIMARY KEY,
    "uuid" UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    "email_id" BIGINT NOT NULL,
    "filename" VARCHAR(255) NOT NULL,
    "content_type" VARCHAR(100),
    "size" BIGINT DEFAULT 0,
    "s3_key" VARCHAR(500),
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT "email_attachments_email_id_fkey"
        FOREIGN KEY ("email_id") REFERENCES "emails"("id") ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS "email_attachments_email_id_idx" ON "email_attachments" ("email_id");

-- Add folder and received_at to emails if not exists
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "folder" VARCHAR(50) DEFAULT 'inbox';
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "received_at" TIMESTAMPTZ;
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "is_read" BOOLEAN DEFAULT false;
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "is_starred" BOOLEAN DEFAULT false;
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "has_attachments" BOOLEAN DEFAULT false;
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "text_body" TEXT;
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "snippet" VARCHAR(500);

-- Index for folder queries
CREATE INDEX IF NOT EXISTS "emails_org_folder_idx" ON "emails" ("org_id", "folder");
CREATE INDEX IF NOT EXISTS "emails_received_at_idx" ON "emails" ("received_at" DESC);
