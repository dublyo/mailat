-- Retain identity filtering after the mailbox copy is permanently removed.
ALTER TABLE compose_submission_keys ADD COLUMN IF NOT EXISTS identity_id BIGINT;
UPDATE compose_submission_keys k SET identity_id=e.identity_id FROM received_emails e WHERE e.uuid=k.email_uuid AND k.identity_id IS NULL;

-- Keep accepted send work durable even if Redis is unavailable after commit.
ALTER TABLE transactional_emails ADD COLUMN IF NOT EXISTS send_payload JSONB;
CREATE INDEX IF NOT EXISTS transactional_emails_pending_idx ON transactional_emails(scheduled_for,id) WHERE status='queued';

-- Bind a whole batch before processing its items; item receipts survive partial retries.
CREATE TABLE IF NOT EXISTS email_batch_submissions (
 org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 submission_key TEXT NOT NULL,
 user_id INTEGER,
 request_hash TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(org_id,submission_key)
);

-- All status producers (including SNS) update the same public UUID in Sent.
-- Deliberately update only: a deleted Sent copy must not reappear on a late event.
CREATE OR REPLACE FUNCTION sync_transactional_mailbox() RETURNS TRIGGER AS $$
BEGIN
 UPDATE received_emails SET
  send_status=NEW.status,
  ses_message_id=COALESCE(NEW.provider_message_id,ses_message_id),
  sent_at=COALESCE(NEW.sent_at,sent_at),
  folder=CASE WHEN NEW.status IN ('sent','delivered','bounced','complained','opened','clicked') AND folder='outbox' THEN 'sent' ELSE folder END,
  updated_at=NOW()
 WHERE uuid=NEW.uuid AND org_id=NEW.org_id AND direction='outbound';
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS transactional_mailbox_status ON transactional_emails;
CREATE TRIGGER transactional_mailbox_status AFTER UPDATE OF status,provider_message_id,sent_at ON transactional_emails
FOR EACH ROW EXECUTE FUNCTION sync_transactional_mailbox();
