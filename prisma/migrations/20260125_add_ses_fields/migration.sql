-- Add SES fields to domains table
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "ses_verified" BOOLEAN DEFAULT false;
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "ses_dkim_tokens" TEXT[] DEFAULT '{}';
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "ses_identity_arn" VARCHAR(255);
ALTER TABLE "domains" ADD COLUMN IF NOT EXISTS "email_provider" VARCHAR(20) DEFAULT 'ses';

-- Add provider fields to transactional_emails table
ALTER TABLE "transactional_emails" ADD COLUMN IF NOT EXISTS "provider_message_id" VARCHAR(255);
ALTER TABLE "transactional_emails" ADD COLUMN IF NOT EXISTS "email_provider" VARCHAR(20) DEFAULT 'ses';

-- Add provider fields to emails table
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "provider_message_id" VARCHAR(255);
ALTER TABLE "emails" ADD COLUMN IF NOT EXISTS "email_provider" VARCHAR(20) DEFAULT 'ses';

-- Add index for provider message ID lookup
CREATE INDEX IF NOT EXISTS "transactional_emails_provider_message_id_idx" ON "transactional_emails" ("provider_message_id");
CREATE INDEX IF NOT EXISTS "emails_provider_message_id_idx" ON "emails" ("provider_message_id");
