-- 014_automation_executor.sql: M4 versioned, DB-leased automation executor.
-- Applies on fresh and legacy installs. Data cleanup runs before every CHECK/NOT NULL.

-- Nothing ever executed; no row may start sending merely because it said 'active'.
UPDATE automations SET status='paused', updated_at=now() WHERE status='active';
UPDATE automations SET status='draft', updated_at=now() WHERE status IS NULL OR status NOT IN ('draft','paused','archived');
DELETE FROM automations a WHERE NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id=a.org_id);
UPDATE automations SET trigger_type='contact.created' WHERE trigger_type='contact_added';
UPDATE automations SET trigger_type='contact.subscribed' WHERE trigger_type IN ('contact_subscribed','subscribed');

ALTER TABLE automations
  ALTER COLUMN updated_at SET DEFAULT now(),
  ALTER COLUMN status SET DEFAULT 'draft',
  ALTER COLUMN status SET NOT NULL,
  ADD COLUMN IF NOT EXISTS created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS reentry_policy VARCHAR(20) NOT NULL DEFAULT 'never',
  ADD COLUMN IF NOT EXISTS published_version_id BIGINT,
  ADD COLUMN IF NOT EXISTS activated_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='automations_org_id_fkey') THEN
    ALTER TABLE automations ADD CONSTRAINT automations_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE;
  END IF;
END $$;
ALTER TABLE automations
  ADD CONSTRAINT automations_status_check CHECK (status IN ('draft','active','paused','archived')),
  ADD CONSTRAINT automations_reentry_check CHECK (reentry_policy IN ('never','after_exit'));

CREATE TABLE automation_versions (
  id BIGSERIAL PRIMARY KEY,
  automation_id INTEGER NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
  version INTEGER NOT NULL,
  trigger_type VARCHAR(50) NOT NULL,
  trigger_config JSONB NOT NULL DEFAULT '{}',
  workflow JSONB NOT NULL,
  graph_hash TEXT NOT NULL,
  -- Published with the graph so editing the draft never changes live re-entry.
  reentry_policy VARCHAR(20) NOT NULL DEFAULT 'never' CHECK (reentry_policy IN ('never','after_exit')),
  resource_refs TEXT[] NOT NULL DEFAULT '{}',
  published_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (automation_id, version)
);
CREATE INDEX automation_versions_refs_idx ON automation_versions USING GIN (resource_refs);
ALTER TABLE automations ADD CONSTRAINT automations_published_version_fkey
  FOREIGN KEY (published_version_id) REFERENCES automation_versions(id) ON DELETE SET NULL;

-- Pre-executor enrollments have no version and cannot run safely.
UPDATE automation_enrollments SET status='cancelled', error_message='Created before the automation executor existed', updated_at=now()
  WHERE status IS NULL OR status NOT IN ('completed','exited','failed','cancelled');
DELETE FROM automation_enrollments e WHERE NOT EXISTS (SELECT 1 FROM contacts c WHERE c.id=e.contact_id);
-- Legacy installs have a table constraint, fresh installs a bare unique index.
ALTER TABLE automation_enrollments DROP CONSTRAINT IF EXISTS automation_enrollments_automation_id_contact_id_key;
DROP INDEX IF EXISTS automation_enrollments_automation_id_contact_id_key;
ALTER TABLE automation_enrollments
  ALTER COLUMN updated_at SET DEFAULT now(),
  ALTER COLUMN status SET NOT NULL,
  ADD COLUMN IF NOT EXISTS version_id BIGINT REFERENCES automation_versions(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS current_node_id VARCHAR(100),
  ADD COLUMN IF NOT EXISTS trigger_event_id BIGINT,
  ADD COLUMN IF NOT EXISTS exit_reason VARCHAR(50),
  ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS claim_token UUID,
  ADD CONSTRAINT automation_enrollments_contact_fkey FOREIGN KEY (contact_id) REFERENCES contacts(id) ON DELETE CASCADE,
  ADD CONSTRAINT automation_enrollments_status_check CHECK (status IN ('active','completed','exited','failed','cancelled')),
  ADD CONSTRAINT automation_enrollments_versioned CHECK (status <> 'active' OR (version_id IS NOT NULL AND current_node_id IS NOT NULL));
CREATE UNIQUE INDEX automation_enrollments_one_active ON automation_enrollments(automation_id, contact_id) WHERE status='active';
CREATE UNIQUE INDEX automation_enrollments_trigger_event ON automation_enrollments(automation_id, trigger_event_id) WHERE trigger_event_id IS NOT NULL;
CREATE INDEX automation_enrollments_due_idx ON automation_enrollments(next_run_at, id) WHERE status='active';
CREATE INDEX automation_enrollments_contact_idx ON automation_enrollments(contact_id);
CREATE INDEX automation_enrollments_reentry_idx ON automation_enrollments(automation_id, contact_id, enrolled_at DESC);

-- Durable automation email queue (M3 has no tx-joinable sender). The step
-- transaction inserts one row per (enrollment, email node) and commits it with
-- the step advance; a leased runner sends it after commit through M3's renderer,
-- eligibility, quota and SES classification. Status/lease columns mirror
-- campaign_recipients.
CREATE TABLE automation_messages (
  id BIGSERIAL PRIMARY KEY,
  message_uuid UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  automation_id INTEGER NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
  version_id BIGINT NOT NULL REFERENCES automation_versions(id) ON DELETE CASCADE,
  enrollment_id BIGINT NOT NULL REFERENCES automation_enrollments(id) ON DELETE CASCADE,
  node_id VARCHAR(100) NOT NULL,
  contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  email VARCHAR(255) NOT NULL,
  sender_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  identity_id INTEGER REFERENCES identities(id) ON DELETE SET NULL,
  template_id INTEGER REFERENCES email_templates(id) ON DELETE SET NULL,
  subject_override VARCHAR(998),
  track_opens BOOLEAN NOT NULL DEFAULT true,
  track_clicks BOOLEAN NOT NULL DEFAULT true,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','claimed','sending','sent','failed','unknown','skipped','cancelled')),
  skip_reason TEXT CHECK (skip_reason IN ('inactive','suppressed','contact_deleted','email_changed','left_list')),
  quota_reserved BOOLEAN NOT NULL DEFAULT false,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_owner UUID,
  lease_expires_at TIMESTAMPTZ,
  attempt_started_at TIMESTAMPTZ,
  provider_message_id VARCHAR(255),
  error VARCHAR(500),                -- never contains the recipient address
  sent_at TIMESTAMPTZ,
  delivery_status TEXT CHECK (delivery_status IN ('delivered','bounced','complained')),
  delivered_at TIMESTAMPTZ,
  bounced_at TIMESTAMPTZ,
  complained_at TIMESTAMPTZ,
  open_count INTEGER NOT NULL DEFAULT 0,
  first_opened_at TIMESTAMPTZ,
  click_count INTEGER NOT NULL DEFAULT 0,
  first_clicked_at TIMESTAMPTZ,
  unsubscribed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (enrollment_id, node_id)
);
CREATE INDEX automation_messages_pending_idx ON automation_messages(next_attempt_at, id) WHERE status='pending';
CREATE INDEX automation_messages_claimed_idx ON automation_messages(lease_expires_at) WHERE status='claimed';
CREATE INDEX automation_messages_sending_idx ON automation_messages(attempt_started_at) WHERE status='sending';
CREATE INDEX automation_messages_provider_idx ON automation_messages(org_id, provider_message_id) WHERE provider_message_id IS NOT NULL;
CREATE INDEX automation_messages_contact_idx ON automation_messages(org_id, contact_id);
CREATE INDEX automation_messages_node_idx ON automation_messages(automation_id, version_id, node_id);

CREATE TABLE automation_message_events (
  id BIGSERIAL PRIMARY KEY,
  message_id BIGINT NOT NULL REFERENCES automation_messages(id) ON DELETE CASCADE,
  automation_id INTEGER NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL CHECK (event_type IN ('open','click')),
  url VARCHAR(2048),
  link_index INTEGER,
  user_agent VARCHAR(512),
  ip_address VARCHAR(45),
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automation_message_events_message_idx ON automation_message_events(message_id);
CREATE INDEX automation_message_events_automation_idx ON automation_message_events(automation_id, event_type, occurred_at);

CREATE TABLE automation_step_runs (
  id BIGSERIAL PRIMARY KEY,
  enrollment_id BIGINT NOT NULL REFERENCES automation_enrollments(id) ON DELETE CASCADE,
  automation_id INTEGER NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
  version_id BIGINT NOT NULL REFERENCES automation_versions(id) ON DELETE CASCADE,
  node_id VARCHAR(100) NOT NULL,
  node_type VARCHAR(20) NOT NULL,
  status VARCHAR(20) NOT NULL CHECK (status IN ('waiting','succeeded','skipped','failed')),
  outcome VARCHAR(30),               -- yes/no, suppressed, left_list, added, already_member …
  resume_at TIMESTAMPTZ,
  message_id BIGINT REFERENCES automation_messages(id) ON DELETE SET NULL,
  webhook_event_id UUID,
  error TEXT,                        -- never contains the recipient address
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  UNIQUE (enrollment_id, node_id)
);
CREATE INDEX automation_step_runs_node_idx ON automation_step_runs(automation_id, version_id, node_id, status);

-- Where a membership or contact came from; trigger sources filter on it.
-- NULL or any unlisted value is recorded as 'unknown'.
ALTER TABLE list_contacts ADD COLUMN IF NOT EXISTS source VARCHAR(30);
-- contacts.consent_source is free text (up to 100 chars) supplied by callers,
-- so contact.created reads this code-set column instead.
ALTER TABLE contacts ADD COLUMN IF NOT EXISTS created_source VARCHAR(30);

CREATE TABLE automation_trigger_events (
  id BIGSERIAL PRIMARY KEY,
  org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  event_type VARCHAR(30) NOT NULL CHECK (event_type IN ('contact.created','contact.subscribed')),
  contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  list_id INTEGER REFERENCES lists(id) ON DELETE CASCADE,
  source VARCHAR(30) NOT NULL DEFAULT 'unknown',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at TIMESTAMPTZ
);
CREATE INDEX automation_trigger_events_pending_idx ON automation_trigger_events(id) WHERE processed_at IS NULL;
CREATE INDEX automation_trigger_events_contact_idx ON automation_trigger_events(contact_id);
CREATE INDEX automation_trigger_events_processed_idx ON automation_trigger_events(processed_at) WHERE processed_at IS NOT NULL;

CREATE OR REPLACE FUNCTION automation_trigger_source(src TEXT) RETURNS TEXT AS $$
  SELECT CASE WHEN src IN ('signup_form','api','import','manual','preference_center','double_opt_in','automation')
              THEN src ELSE 'unknown' END
$$ LANGUAGE sql IMMUTABLE;

CREATE OR REPLACE FUNCTION automation_record_subscribed() RETURNS TRIGGER AS $$
DECLARE v_org INTEGER;
BEGIN
  SELECT c.org_id INTO v_org FROM contacts c JOIN lists l ON l.id=NEW.list_id AND l.org_id=c.org_id WHERE c.id=NEW.contact_id;
  IF v_org IS NOT NULL AND EXISTS (SELECT 1 FROM automations a JOIN automation_versions v ON v.id=a.published_version_id
       WHERE a.org_id=v_org AND a.status='active' AND v.trigger_type='contact.subscribed') THEN
    INSERT INTO automation_trigger_events(org_id,event_type,contact_id,list_id,source)
    VALUES (v_org,'contact.subscribed',NEW.contact_id,NEW.list_id,automation_trigger_source(NEW.source));
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER list_contacts_automation_trigger AFTER INSERT ON list_contacts
  FOR EACH ROW EXECUTE FUNCTION automation_record_subscribed();

CREATE OR REPLACE FUNCTION automation_record_created() RETURNS TRIGGER AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM automations a JOIN automation_versions v ON v.id=a.published_version_id
       WHERE a.org_id=NEW.org_id AND a.status='active' AND v.trigger_type='contact.created') THEN
    INSERT INTO automation_trigger_events(org_id,event_type,contact_id,source)
    VALUES (NEW.org_id,'contact.created',NEW.id,automation_trigger_source(NEW.created_source));
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER contacts_automation_trigger AFTER INSERT ON contacts
  FOR EACH ROW EXECUTE FUNCTION automation_record_created();
