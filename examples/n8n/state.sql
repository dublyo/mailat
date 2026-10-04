-- Run in a dedicated automation state database, not the Mailat application schema.
CREATE TABLE IF NOT EXISTS mailat_n8n_events (
  event_id uuid PRIMARY KEY,
  message_uuid uuid NOT NULL,
  lease_owner text NOT NULL,
  lease_until timestamptz,
  completed_at timestamptz
);
-- Keep completed receipts for at least as long as webhook replay is allowed.
