-- Outbound copies share the mailbox table so SES drafts and Sent do not depend on JMAP.
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT 'inbound';
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS send_status TEXT NOT NULL DEFAULT 'received';
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS send_error TEXT;
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS sent_at TIMESTAMPTZ;
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS draft_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS submission_key TEXT;
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS submission_hash TEXT;
ALTER TABLE received_emails ADD COLUMN IF NOT EXISTS submitter_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX IF NOT EXISTS received_emails_submission_key_idx
  ON received_emails(submitter_user_id, submission_key) WHERE submission_key IS NOT NULL;

-- Keep the submission receipt after a user permanently removes the mailbox copy.
-- Deliberately no foreign key to received_emails: deleting mail must not permit a resend.
CREATE TABLE IF NOT EXISTS compose_submission_keys (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  submission_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  email_uuid UUID NOT NULL,
  message_id TEXT NOT NULL,
  ses_message_id TEXT,
  status TEXT NOT NULL DEFAULT 'sending',
  send_error TEXT,
  sent_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY(user_id, submission_key)
);
ALTER TABLE compose_submission_keys ADD COLUMN IF NOT EXISTS ses_message_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS compose_submission_keys_email_uuid_idx ON compose_submission_keys(email_uuid);
CREATE INDEX IF NOT EXISTS compose_submission_keys_ses_message_id_idx ON compose_submission_keys(ses_message_id);
INSERT INTO compose_submission_keys(user_id,submission_key,request_hash,email_uuid,message_id,ses_message_id,status,send_error,sent_at)
SELECT submitter_user_id,submission_key,submission_hash,uuid,message_id,ses_message_id,send_status,send_error,sent_at
FROM received_emails WHERE submitter_user_id IS NOT NULL AND submission_key IS NOT NULL AND submission_hash IS NOT NULL
ON CONFLICT DO NOTHING;

-- SNS delivery updates and permanent deletion preserve the latest known outcome.
CREATE OR REPLACE FUNCTION sync_compose_submission_receipt() RETURNS TRIGGER AS $$
DECLARE mailbox received_emails%ROWTYPE;
BEGIN
  IF TG_OP = 'DELETE' THEN mailbox := OLD; ELSE mailbox := NEW; END IF;
  IF mailbox.submission_key IS NOT NULL AND mailbox.submitter_user_id IS NOT NULL THEN
    UPDATE compose_submission_keys SET status=mailbox.send_status,send_error=mailbox.send_error,
      sent_at=mailbox.sent_at,ses_message_id=COALESCE(mailbox.ses_message_id,ses_message_id),updated_at=NOW()
    WHERE user_id=mailbox.submitter_user_id AND submission_key=mailbox.submission_key;
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS received_emails_compose_receipt ON received_emails;
CREATE TRIGGER received_emails_compose_receipt BEFORE UPDATE OR DELETE ON received_emails
FOR EACH ROW EXECUTE FUNCTION sync_compose_submission_receipt();

-- The upload endpoint may persist a file before a draft exists. Ownership remains explicit.
CREATE TABLE IF NOT EXISTS compose_uploads (
  uuid UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  filename TEXT NOT NULL,
  content_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
  s3_bucket TEXT NOT NULL,
  s3_key TEXT NOT NULL,
  checksum TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS compose_uploads_owner_idx ON compose_uploads(user_id, created_at);

-- Scope API idempotency to the organization. Reserve the key atomically before queueing.
CREATE TABLE IF NOT EXISTS email_submission_keys (
  org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  submission_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  email_uuid UUID,
  status TEXT NOT NULL DEFAULT 'reserved',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY(org_id, submission_key)
);
