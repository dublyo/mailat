ALTER TABLE organizations ADD COLUMN IF NOT EXISTS max_identities integer NOT NULL DEFAULT 0;
ALTER TABLE organizations ALTER COLUMN max_domains SET DEFAULT 0;
ALTER TABLE organizations ALTER COLUMN max_contacts SET DEFAULT 0;
ALTER TABLE organizations ALTER COLUMN monthly_email_limit SET DEFAULT 0;

-- Serialize identity changes on their user/domain rows in the service as well;
-- these constraints protect against direct concurrent writes.
CREATE UNIQUE INDEX IF NOT EXISTS identities_one_catch_all_per_domain ON identities(domain_id) WHERE is_catch_all;
CREATE UNIQUE INDEX IF NOT EXISTS identities_one_default_per_user ON identities(user_id) WHERE is_default;

CREATE TABLE IF NOT EXISTS organization_send_usage (
  org_id integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  month date NOT NULL,
  attempts bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (org_id, month)
);
