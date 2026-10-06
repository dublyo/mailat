-- 013_campaign_sending.sql: M3 durable SES campaign sender. Applies on fresh and legacy installs.

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS postal_address varchar(500);

ALTER TABLE campaigns
  ADD COLUMN IF NOT EXISTS track_opens boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS track_clicks boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS created_by_user_id integer REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS identity_id integer REFERENCES identities(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS status_reason text,
  ADD COLUMN IF NOT EXISTS prepared_at timestamptz,
  ADD COLUMN IF NOT EXISTS throttled_until timestamptz,
  ADD COLUMN IF NOT EXISTS last_batch_at timestamptz,
  ADD COLUMN IF NOT EXISTS failed_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS skipped_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS unknown_count integer NOT NULL DEFAULT 0;

-- Nothing was ever sent before M3. Never auto-blast legacy rows after deploy:
-- they return to draft so the owner re-confirms the sender (identity/creator) and sends.
UPDATE campaigns SET status='draft', status_reason='legacy_requires_review',
       started_at=NULL, scheduled_at=NULL, total_recipients=0, updated_at=now()
 WHERE status IS NULL OR status NOT IN ('draft','sent','cancelled');
ALTER TABLE campaigns ALTER COLUMN status SET DEFAULT 'draft';
ALTER TABLE campaigns ALTER COLUMN status SET NOT NULL;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
  CHECK (status IN ('draft','scheduled','sending','paused','sent','cancelled'));
CREATE INDEX IF NOT EXISTS campaigns_due_idx ON campaigns(scheduled_at) WHERE status='scheduled';
CREATE INDEX IF NOT EXISTS campaigns_active_idx ON campaigns(last_batch_at NULLS FIRST, id) WHERE status='sending';

CREATE TABLE campaign_recipients (
  id bigserial PRIMARY KEY,
  campaign_id integer NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  org_id integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  contact_id bigint REFERENCES contacts(id) ON DELETE SET NULL,
  email varchar(255) NOT NULL,
  message_uuid uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','claimed','sending','sent','failed','unknown','skipped','cancelled')),
  skip_reason text CHECK (skip_reason IN ('inactive','not_member','suppressed','contact_deleted','email_changed')),
  quota_reserved boolean NOT NULL DEFAULT false,
  lease_owner uuid,
  lease_expires_at timestamptz,
  attempt_started_at timestamptz,
  provider_message_id varchar(255),
  error varchar(500),
  sent_at timestamptz,
  delivery_status text CHECK (delivery_status IN ('delivered','bounced','complained')),
  delivered_at timestamptz,
  bounced_at timestamptz,
  complained_at timestamptz,
  soft_bounced_at timestamptz,
  open_count integer NOT NULL DEFAULT 0,
  first_opened_at timestamptz,
  click_count integer NOT NULL DEFAULT 0,
  first_clicked_at timestamptz,
  unsubscribed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (campaign_id, contact_id)
);
CREATE UNIQUE INDEX campaign_recipients_campaign_email_key ON campaign_recipients(campaign_id, lower(email));
CREATE INDEX campaign_recipients_pending_idx ON campaign_recipients(campaign_id, id) WHERE status='pending';
CREATE INDEX campaign_recipients_claimed_idx ON campaign_recipients(lease_expires_at) WHERE status='claimed';
CREATE INDEX campaign_recipients_sending_idx ON campaign_recipients(attempt_started_at) WHERE status='sending';
CREATE INDEX campaign_recipients_status_idx ON campaign_recipients(campaign_id, status);
CREATE INDEX campaign_recipients_provider_idx ON campaign_recipients(org_id, provider_message_id) WHERE provider_message_id IS NOT NULL;
CREATE INDEX campaign_recipients_contact_idx ON campaign_recipients(org_id, contact_id);

CREATE TABLE campaign_events (
  id bigserial PRIMARY KEY,
  campaign_id integer NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  recipient_id bigint NOT NULL REFERENCES campaign_recipients(id) ON DELETE CASCADE,
  event_type text NOT NULL CHECK (event_type IN ('open','click')),
  url varchar(2048),
  link_index integer,
  user_agent varchar(512),
  ip_address varchar(45),
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX campaign_events_campaign_idx ON campaign_events(campaign_id, event_type, occurred_at);
CREATE INDEX campaign_events_recipient_idx ON campaign_events(recipient_id);

CREATE TABLE campaign_test_sends (
  id bigserial PRIMARY KEY,
  campaign_id integer NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_key varchar(128) NOT NULL,
  request_hash text NOT NULL,
  recipients text[] NOT NULL,
  status text NOT NULL DEFAULT 'sending' CHECK (status IN ('sending','sent','failed','unknown','partial')),
  results jsonb NOT NULL DEFAULT '[]',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (campaign_id, user_id, idempotency_key)
);
CREATE INDEX campaign_test_sends_rate_idx ON campaign_test_sends(campaign_id, created_at);

-- Case-insensitive transactional suppression lookups in campaign eligibility.
-- suppressions (hash index) and contacts (lower(email)) are already indexed by 012.
CREATE INDEX IF NOT EXISTS suppression_list_org_lower_email_idx ON suppression_list(org_id, lower(email));
