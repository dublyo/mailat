-- 017_mailbox_users.sql: M5.1 mailbox users, send-as aliases, wildcard sender.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('owner','admin','member','mailbox')) NOT VALID;
ALTER TABLE users ADD CONSTRAINT users_status_check CHECK (status IN ('active','pending','suspended','disabled')) NOT VALID;
-- Only mailbox users can be pending. 'suspended' stays allowed for any role;
-- the API suspends mailboxes only.
ALTER TABLE users ADD CONSTRAINT users_pending_mailbox_only CHECK (status <> 'pending' OR role = 'mailbox') NOT VALID;
CREATE INDEX users_org_role_idx ON users(org_id, role);
CREATE INDEX IF NOT EXISTS identities_lower_email_idx ON identities(lower(email));

CREATE TABLE mailbox_accounts (
  user_id     integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  org_id      integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  identity_id integer NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
  domain_id   integer NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  recovery_email text CHECK (recovery_email IS NULL OR (recovery_email = lower(recovery_email) AND length(recovery_email) <= 255)),
  created_by  integer REFERENCES users(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  removed_at  timestamptz
);
-- A removed mailbox keeps its row (for ?removed=true); re-creating the address
-- reuses the identity, so uniqueness covers live rows only.
CREATE UNIQUE INDEX mailbox_accounts_live_identity_key ON mailbox_accounts(identity_id) WHERE removed_at IS NULL;
CREATE INDEX mailbox_accounts_domain_idx ON mailbox_accounts(domain_id) WHERE removed_at IS NULL;

ALTER TABLE identities ADD COLUMN wildcard_sender boolean NOT NULL DEFAULT false;
ALTER TABLE identities ADD CONSTRAINT identities_wildcard_personal CHECK (kind='personal' OR NOT wildcard_sender);

CREATE TABLE identity_send_aliases (
  id          bigserial PRIMARY KEY,
  uuid        uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  identity_id integer NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
  address     text NOT NULL CHECK (address = lower(address) AND length(address) <= 255),
  created_by  integer REFERENCES users(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX identity_send_aliases_address_key ON identity_send_aliases(address);
CREATE INDEX identity_send_aliases_identity_idx ON identity_send_aliases(identity_id);

-- An address is an identity or a send-as alias, never both. The per-address
-- advisory lock makes the check race-free across both tables under READ COMMITTED;
-- services also hold the domain row lock. Raised as 23505 without a constraint
-- name, so services map any 23505 here to 409.
CREATE OR REPLACE FUNCTION mailat_address_exclusive() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE addr text;
BEGIN
  IF TG_TABLE_NAME = 'identity_send_aliases' THEN
    addr := NEW.address;
    IF (SELECT kind FROM identities WHERE id = NEW.identity_id) IS DISTINCT FROM 'personal' THEN
      RAISE EXCEPTION 'send-as aliases belong to personal identities' USING ERRCODE = '23514';
    END IF;
  ELSE
    addr := lower(NEW.email);
  END IF;
  PERFORM pg_advisory_xact_lock(20261017, hashtext(addr));
  IF TG_TABLE_NAME = 'identity_send_aliases' THEN
    IF EXISTS (SELECT 1 FROM identities WHERE lower(email) = addr) THEN
      RAISE EXCEPTION 'address is already an identity' USING ERRCODE = '23505';
    END IF;
  ELSIF EXISTS (SELECT 1 FROM identity_send_aliases WHERE address = addr) THEN
    RAISE EXCEPTION 'address is already a send-as alias' USING ERRCODE = '23505';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER identity_send_aliases_exclusive BEFORE INSERT OR UPDATE OF address, identity_id ON identity_send_aliases
  FOR EACH ROW EXECUTE FUNCTION mailat_address_exclusive();
CREATE TRIGGER identities_alias_exclusive BEFORE INSERT OR UPDATE OF email ON identities
  FOR EACH ROW EXECUTE FUNCTION mailat_address_exclusive();

-- Mailbox setup and admin password-reset links reuse org_invites.
ALTER TABLE org_invites DROP CONSTRAINT IF EXISTS org_invites_role_check;
ALTER TABLE org_invites ADD CONSTRAINT org_invites_role_check CHECK (role IN ('member','admin','mailbox'));
ALTER TABLE org_invites ADD COLUMN purpose text NOT NULL DEFAULT 'join'
  CHECK (purpose IN ('join','mailbox_setup','password_reset'));
ALTER TABLE org_invites ADD COLUMN user_id integer REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE org_invites ADD COLUMN delivery_email text
  CHECK (delivery_email IS NULL OR (delivery_email = lower(delivery_email) AND length(delivery_email) <= 255));
ALTER TABLE org_invites ADD CONSTRAINT org_invites_purpose_shape CHECK (
  (purpose = 'join' AND user_id IS NULL AND role <> 'mailbox')
  OR (purpose <> 'join' AND user_id IS NOT NULL AND role = 'mailbox' AND delivery_email IS NOT NULL));
CREATE INDEX org_invites_open_user_idx ON org_invites(user_id) WHERE accepted_at IS NULL AND revoked_at IS NULL;
-- org_invites_open_email_idx (016) keeps one open link per address, so a
-- setup link, a reset link and a join invite for the same address exclude each other.
