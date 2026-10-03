-- RFC Message-ID is sender-controlled and may address several mailbox owners.
ALTER TABLE received_emails DROP CONSTRAINT IF EXISTS received_emails_message_id_key;
DROP INDEX IF EXISTS received_emails_message_id_key;
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS envelope_recipients TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS received_emails_message_id_idx ON received_emails(message_id);
CREATE UNIQUE INDEX IF NOT EXISTS received_emails_identity_ses_key ON received_emails(identity_id, ses_message_id);
CREATE INDEX IF NOT EXISTS identities_user_domain_id_idx ON identities(user_id, domain_id, id);
CREATE INDEX IF NOT EXISTS received_emails_identity_time_id_idx ON received_emails(identity_id, received_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS received_emails_identity_folder_time_id_idx ON received_emails(identity_id, folder, received_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS received_emails_raw_object_idx ON received_emails(raw_s3_bucket, raw_s3_key);
CREATE INDEX IF NOT EXISTS email_attachments_object_idx ON email_attachments(s3_bucket, s3_key);
-- Kept after mailbox deletion, so an SNS retry cannot resurrect deleted mail.
CREATE TABLE IF NOT EXISTS received_ingestions (
 id BIGSERIAL PRIMARY KEY,
 org_id INTEGER NOT NULL,
 topic_arn TEXT NOT NULL,
 ses_message_id TEXT NOT NULL,
 identity_id INTEGER NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(topic_arn, ses_message_id, identity_id)
);
-- Receipt recipients are scoped to one SES rule. Different domain rules can
-- legitimately publish the same SES message ID to this organization topic.
-- Seed historical mailbox copies so deleting pre-upgrade mail cannot revive it.
INSERT INTO received_ingestions(org_id,topic_arn,ses_message_id,identity_id)
SELECT DISTINCT re.org_id,rc.sns_topic_arn,re.ses_message_id,re.identity_id
FROM received_emails re JOIN receiving_configs rc ON rc.org_id=re.org_id
WHERE COALESCE(re.ses_message_id,'')!=''
ON CONFLICT DO NOTHING;
-- Written in the same transaction as deletion; retries survive process restarts.
CREATE TABLE IF NOT EXISTS storage_cleanup_jobs (
 id BIGSERIAL PRIMARY KEY,
 bucket TEXT NOT NULL,
 object_key TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 last_error TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(bucket, object_key)
);
CREATE INDEX IF NOT EXISTS storage_cleanup_jobs_due_idx ON storage_cleanup_jobs(next_attempt_at);
CREATE TABLE IF NOT EXISTS ses_delivery_events (
 topic_arn TEXT NOT NULL,
 notification_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(topic_arn, notification_id)
);
