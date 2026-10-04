-- Endpoint ownership and durable event delivery replace process-local goroutines.
ALTER TABLE webhooks ADD COLUMN IF NOT EXISTS user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;
-- Legacy endpoints had organization ownership only. Attribute them to the
-- earliest administrator; subsequent events remain bound to that user's mailbox.
UPDATE webhooks w SET user_id=(SELECT u.id FROM users u WHERE u.org_id=w.org_id ORDER BY CASE WHEN u.role IN ('owner','admin') THEN 0 ELSE 1 END,u.id LIMIT 1) WHERE user_id IS NULL;
ALTER TABLE webhook_triggers ALTER COLUMN updated_at SET DEFAULT NOW();
CREATE TABLE webhook_events (
  id UUID PRIMARY KEY,
  org_id BIGINT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(org_id,event_type,dedupe_key)
);
CREATE TABLE webhook_deliveries (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES webhook_events(id) ON DELETE CASCADE,
  webhook_id BIGINT REFERENCES webhooks(id) ON DELETE CASCADE,
  trigger_id BIGINT REFERENCES webhook_triggers(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivering','retry','delivered','dead_letter','cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0,
  replay_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  locked_until TIMESTAMPTZ,
  claim_token UUID,
  last_http_status INTEGER,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CHECK((webhook_id IS NULL) <> (trigger_id IS NULL)),
  UNIQUE(event_id,webhook_id),
  UNIQUE(event_id,trigger_id)
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries(status,next_attempt_at);
CREATE INDEX webhook_events_owner ON webhook_events(org_id,user_id,created_at DESC);
CREATE TABLE webhook_delivery_attempts (
  id BIGSERIAL PRIMARY KEY,
  delivery_id UUID NOT NULL REFERENCES webhook_deliveries(id) ON DELETE CASCADE,
  attempt INTEGER NOT NULL,
  replay INTEGER NOT NULL,
  http_status INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  response_body TEXT NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX webhook_attempts_delivery ON webhook_delivery_attempts(delivery_id,id);
