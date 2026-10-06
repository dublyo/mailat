-- 015_mail_arrival.sql: M5 auto-replies, forwarding, system sends and the arrival job queue.
-- Applies on fresh and legacy installs. Data cleanup runs before every new constraint.

-- 001 never constrained these tables; remove orphans before adding FKs.
DELETE FROM auto_replies a WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id=a.user_id)
   OR NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id=a.org_id);
DELETE FROM email_forwards f WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id=f.user_id)
   OR NOT EXISTS (SELECT 1 FROM identities i WHERE i.id=f.identity_id)
   OR NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id=f.org_id);
ALTER TABLE auto_replies ADD CONSTRAINT auto_replies_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE auto_replies ADD CONSTRAINT auto_replies_org_fk FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE email_forwards ADD CONSTRAINT email_forwards_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE email_forwards ADD CONSTRAINT email_forwards_org_fk FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE email_forwards ADD CONSTRAINT email_forwards_identity_fk FOREIGN KEY (identity_id) REFERENCES identities(id) ON DELETE CASCADE;

ALTER TABLE auto_replies
  ADD COLUMN reply_interval_days integer NOT NULL DEFAULT 7 CHECK (reply_interval_days BETWEEN 1 AND 30),
  ADD COLUMN window_day date,
  ADD COLUMN window_count integer NOT NULL DEFAULT 0,
  ADD COLUMN last_error text,
  ALTER COLUMN updated_at SET DEFAULT now();

ALTER TABLE email_forwards
  ADD COLUMN status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','paused','suspended')),
  ADD COLUMN verify_token_hash char(64),
  ADD COLUMN verify_expires_at timestamptz,
  ADD COLUMN verify_send_day date,
  ADD COLUMN verify_send_count integer NOT NULL DEFAULT 0,
  ADD COLUMN verify_last_sent_at timestamptz,
  ADD COLUMN verify_failures integer NOT NULL DEFAULT 0,
  ADD COLUMN last_error text,
  ADD COLUMN window_day date,
  ADD COLUMN window_count integer NOT NULL DEFAULT 0,
  ALTER COLUMN updated_at SET DEFAULT now();
-- No verification mail was ever sent (TODO in auto_reply.go); never trust prior state.
UPDATE email_forwards SET verify_token=NULL, verified=false, verified_at=NULL, active=false, status='pending',
  last_error='Send a new verification email to activate this forward', updated_at=now();
CREATE UNIQUE INDEX email_forwards_verify_hash_idx ON email_forwards(verify_token_hash) WHERE verify_token_hash IS NOT NULL;

-- Sieve is unsupported; rows are retained.
ALTER TABLE sieve_scripts ADD COLUMN unsupported_at timestamptz;
UPDATE sieve_scripts SET active=false, unsupported_at=now(),
  last_error='Sieve is not supported. Use inbox filters.', updated_at=now();

-- Existing subscriptions were bound to per-boot ephemeral VAPID keys and are undeliverable.
ALTER TABLE push_subscriptions ADD COLUMN failure_count integer NOT NULL DEFAULT 0, ADD COLUMN vapid_key_id text;
UPDATE push_subscriptions SET active=false;

-- System sends (auto-reply, forward, verification, invite). metadata is TEXT, so use typed columns.
ALTER TABLE transactional_emails
  ADD COLUMN system_kind text CHECK (system_kind IN ('auto_reply','forward','forward_verify','invite')),
  ADD COLUMN system_ref uuid,
  ADD COLUMN system_user_id integer REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX transactional_emails_system_idx ON transactional_emails(system_kind, system_ref) WHERE system_kind IS NOT NULL;

-- S3 objects referenced by queued sends; storage cleanup must not delete them.
CREATE TABLE send_attachment_refs (
  transactional_email_id bigint NOT NULL REFERENCES transactional_emails(id) ON DELETE CASCADE,
  s3_bucket text NOT NULL,
  s3_key text NOT NULL,
  PRIMARY KEY (transactional_email_id, s3_bucket, s3_key)
);
CREATE INDEX send_attachment_refs_object_idx ON send_attachment_refs(s3_bucket, s3_key);

CREATE TABLE mail_arrival_jobs (
  id bigserial PRIMARY KEY,
  org_id integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('auto_reply','forward','push')),
  identity_id integer NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
  user_id integer REFERENCES users(id) ON DELETE CASCADE,
  rule_id integer,
  received_email_id bigint REFERENCES received_emails(id) ON DELETE SET NULL,
  ses_message_id text NOT NULL,
  dedupe_key text NOT NULL UNIQUE,
  payload jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','done','skipped','failed')),
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_until timestamptz,
  claim_token uuid,
  result text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_arrival_jobs_due_idx ON mail_arrival_jobs(next_attempt_at) WHERE status IN ('pending','running');
CREATE INDEX mail_arrival_jobs_retention_idx ON mail_arrival_jobs(updated_at) WHERE status IN ('done','skipped','failed');
