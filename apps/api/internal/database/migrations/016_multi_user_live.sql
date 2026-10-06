-- 016_multi_user_live.sql: M5 invites and roles, shared mailbox identities and live-update wakeups.
-- Applies on fresh and legacy installs.

ALTER TABLE users ADD COLUMN removed_at timestamptz;
-- NOT VALID: legacy rows are never rejected; every new write is checked.
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('owner','admin','member')) NOT VALID;

CREATE TABLE org_invites (
  id bigserial PRIMARY KEY,
  uuid uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  org_id integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email text NOT NULL CHECK (email = lower(email) AND length(email) <= 255),
  role text NOT NULL CHECK (role IN ('member','admin')),
  token_hash char(64) NOT NULL UNIQUE,
  invited_by integer REFERENCES users(id) ON DELETE SET NULL,
  sender_identity_id integer REFERENCES identities(id) ON DELETE SET NULL,
  expires_at timestamptz NOT NULL,
  send_count integer NOT NULL DEFAULT 1,
  last_sent_at timestamptz NOT NULL DEFAULT now(),
  failed_attempts integer NOT NULL DEFAULT 0,
  accepted_at timestamptz,
  accepted_user_id integer REFERENCES users(id) ON DELETE SET NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX org_invites_open_email_idx ON org_invites(org_id,email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Shared mailbox = shared identity whose mail is copied to members.
ALTER TABLE identities ADD COLUMN kind text NOT NULL DEFAULT 'personal' CHECK (kind IN ('personal','shared'));
ALTER TABLE identities ADD CONSTRAINT identities_shared_flags CHECK (kind='personal' OR (NOT is_catch_all AND NOT is_default));
ALTER TABLE shared_mailboxes ADD COLUMN identity_id integer UNIQUE REFERENCES identities(id) ON DELETE CASCADE;
ALTER TABLE shared_mailboxes ALTER COLUMN updated_at SET DEFAULT now();
CREATE INDEX shared_mailbox_members_user_idx ON shared_mailbox_members(user_id);
-- Legacy shared_mailboxes rows remain unlinked (identity_id NULL) and are shown as "Not active".

-- One copy per member: the SES message id is unique per identity and mailbox owner.
CREATE UNIQUE INDEX received_emails_identity_owner_ses_key ON received_emails(identity_id, mailbox_owner_id, ses_message_id);
DROP INDEX IF EXISTS received_emails_identity_ses_key;
CREATE INDEX received_emails_owner_folder_time_idx ON received_emails(mailbox_owner_id, folder, received_at DESC, id DESC);

CREATE OR REPLACE FUNCTION mailat_mailbox_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE steward integer; ident_kind text;
BEGIN
  SELECT user_id, kind INTO steward, ident_kind FROM identities WHERE id=NEW.identity_id;
  IF ident_kind='shared' THEN
    IF NEW.mailbox_owner_id IS NULL OR NOT EXISTS (
       SELECT 1 FROM shared_mailboxes sm JOIN shared_mailbox_members m ON m.shared_mailbox_id=sm.id
       WHERE sm.identity_id=NEW.identity_id AND m.user_id=NEW.mailbox_owner_id AND (m.can_read OR m.can_send)) THEN
      RAISE EXCEPTION 'shared mailbox copy requires a member owner' USING ERRCODE='42501';
    END IF;
  ELSE
    NEW.mailbox_owner_id := steward;  -- personal mail is always owned by the identity owner at write time
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS mailbox_owner ON received_emails;
CREATE TRIGGER mailbox_owner BEFORE INSERT OR UPDATE OF identity_id, mailbox_owner_id ON received_emails
FOR EACH ROW EXECUTE FUNCTION mailat_mailbox_owner();

-- Same body as 008 plus a commit-time wakeup (identical payloads collapse per transaction).
CREATE OR REPLACE FUNCTION mailat_mailbox_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mail received_emails%ROWTYPE; next_cursor bigint;
BEGIN
  IF TG_OP='DELETE' THEN mail:=OLD; ELSE mail:=NEW; END IF;
  IF mail.mailbox_owner_id IS NULL OR NOT EXISTS(SELECT 1 FROM users WHERE id=mail.mailbox_owner_id) THEN RETURN NULL; END IF;
  INSERT INTO mailbox_change_counters(user_id,cursor) VALUES(mail.mailbox_owner_id,1)
  ON CONFLICT(user_id) DO UPDATE SET cursor=mailbox_change_counters.cursor+1 RETURNING cursor INTO next_cursor;
  INSERT INTO mailbox_changes(user_id,cursor,message_uuid,identity_id,domain_id,operation)
  VALUES(mail.mailbox_owner_id,next_cursor,mail.uuid,mail.identity_id,mail.domain_id,
         CASE TG_OP WHEN 'INSERT' THEN 'created' WHEN 'DELETE' THEN 'deleted' ELSE 'updated' END);
  PERFORM pg_notify('mailat_mailbox', mail.mailbox_owner_id::text);
  RETURN NULL;
END $$;

-- Stream tickets are single use.
CREATE TABLE stream_ticket_redemptions (
  jti uuid PRIMARY KEY,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL
);
CREATE INDEX stream_ticket_redemptions_exp_idx ON stream_ticket_redemptions(expires_at);
