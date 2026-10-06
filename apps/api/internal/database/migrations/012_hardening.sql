-- 012_hardening.sql: M2 hardening. Additive only.

-- A1: throttle bookkeeping (scheduled_for keeps its user meaning)
ALTER TABLE transactional_emails
  ADD COLUMN IF NOT EXISTS send_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_deferral_reason text;
CREATE INDEX IF NOT EXISTS transactional_emails_retry_idx
  ON transactional_emails(next_attempt_at, id) WHERE status = 'queued';

-- A: delivery-tracking NOTIFY triggers (formerly service.SetupTriggers at boot; same bodies)
CREATE OR REPLACE FUNCTION notify_email_status_change() RETURNS TRIGGER AS $$
DECLARE payload JSON;
BEGIN
  payload := json_build_object('table',TG_TABLE_NAME,'action',TG_OP,'email_id',NEW.id,'org_id',NEW.org_id,
                               'old_status',COALESCE(OLD.status,''),'new_status',NEW.status);
  PERFORM pg_notify('email_status_changed', payload::text);
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS email_status_changed_trigger ON transactional_emails;
CREATE TRIGGER email_status_changed_trigger AFTER UPDATE OF status ON transactional_emails
  FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION notify_email_status_change();

CREATE OR REPLACE FUNCTION notify_delivery_event() RETURNS TRIGGER AS $$
DECLARE payload JSON; org_id INT;
BEGIN
  SELECT te.org_id INTO org_id FROM transactional_emails te WHERE te.id = NEW.email_id;
  payload := json_build_object('table',TG_TABLE_NAME,'action',TG_OP,'email_id',NEW.email_id,'org_id',org_id,
    'event_type',NEW.event_type,'data',json_build_object('details',NEW.details,'ip_address',NEW.ip_address,'user_agent',NEW.user_agent));
  PERFORM pg_notify('delivery_event_created', payload::text);
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS delivery_event_created_trigger ON transactional_delivery_events;
CREATE TRIGGER delivery_event_created_trigger AFTER INSERT ON transactional_delivery_events
  FOR EACH ROW EXECUTE FUNCTION notify_delivery_event();

-- C2: DB-backed auth/public rate limits (HMAC keys; no raw IP/email)
CREATE TABLE auth_rate_limits (key text PRIMARY KEY, window_start timestamptz NOT NULL, hits integer NOT NULL);
CREATE INDEX auth_rate_limits_window ON auth_rate_limits(window_start);

-- C1: OAuth state, link intents and link-confirm tickets (hashes only)
CREATE TABLE oauth_states (
  state_hash       text PRIMARY KEY,
  purpose          text NOT NULL CHECK (purpose IN ('login','link','link_confirm')),
  provider         text NOT NULL,
  user_id          integer REFERENCES users(id) ON DELETE CASCADE,
  provider_user_id text,
  provider_email   text,
  provider_name    text,
  expires_at       timestamptz NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CHECK ((purpose = 'login') = (user_id IS NULL)),
  CHECK ((purpose = 'link_confirm') = (provider_user_id IS NOT NULL))
);
CREATE INDEX oauth_states_expiry ON oauth_states(expires_at);

-- C1: provider tokens were never used; stop retaining them
UPDATE oauth_connections SET access_token = NULL, refresh_token = NULL, token_expiry = NULL
 WHERE access_token IS NOT NULL OR refresh_token IS NOT NULL OR token_expiry IS NOT NULL;

-- D1: remote content
ALTER TABLE user_settings
  ADD COLUMN remote_images text NOT NULL DEFAULT 'ask' CHECK (remote_images IN ('ask','always'));
CREATE TABLE mailbox_trusted_senders (
  id         bigserial PRIMARY KEY,
  uuid       uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  user_id    integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id     integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  sender     text NOT NULL CHECK (sender = lower(sender) AND length(sender) BETWEEN 3 AND 320),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, sender)
);

-- D2: blocked senders are inbox filters, applied last
ALTER TABLE inbox_filters
  ADD COLUMN kind text NOT NULL DEFAULT 'filter' CHECK (kind IN ('filter','blocked_sender'));
CREATE UNIQUE INDEX inbox_filters_blocked_sender_unique
  ON inbox_filters(user_id, ((conditions->0->>'value'))) WHERE kind = 'blocked_sender';

-- B6: hashed suppression matching (erased rows keep only the hash)
ALTER TABLE suppressions ADD COLUMN email_sha256 text;
UPDATE suppressions SET email_sha256 = encode(sha256(convert_to(lower(trim(email)),'UTF8')),'hex');
ALTER TABLE suppressions ALTER COLUMN email_sha256 SET NOT NULL;
CREATE INDEX suppressions_org_email_sha256 ON suppressions(org_id, email_sha256);
CREATE OR REPLACE FUNCTION suppressions_fill_sha256() RETURNS TRIGGER AS $$
BEGIN
  IF NEW.email NOT LIKE 'erased:%' THEN
    NEW.email_sha256 := encode(sha256(convert_to(lower(trim(NEW.email)),'UTF8')),'hex');
  END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER suppressions_fill_sha256 BEFORE INSERT OR UPDATE OF email ON suppressions
  FOR EACH ROW EXECUTE FUNCTION suppressions_fill_sha256();

-- B3/B4: normalize contact email case where it cannot collide; colliding groups stay for manual merge
WITH g AS (SELECT id, count(*) OVER (PARTITION BY org_id, lower(email)) AS n FROM contacts)
UPDATE contacts c SET email = lower(c.email), updated_at = now()
FROM g WHERE g.id = c.id AND g.n = 1 AND c.email <> lower(c.email);
CREATE INDEX contacts_org_lower_email ON contacts(org_id, lower(email));
