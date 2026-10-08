-- 018_domain_ready_automation.sql: who added a domain, and the one-time setup
-- that runs when it first becomes active and SES verified.
ALTER TABLE domains ADD COLUMN IF NOT EXISTS created_by integer REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS ready_automation_at timestamptz;
-- Domains that are already ready keep their current setup: the automation is
-- only for a domain's first transition to active + SES verified.
UPDATE domains SET ready_automation_at = COALESCE(verified_at, updated_at, now())
WHERE status = 'active' AND COALESCE(ses_verified, false) AND ready_automation_at IS NULL;
