-- 019_domain_ready_backfill.sql: 018 stamped only domains that were active and
-- SES verified at upgrade. ses_verified can drop to false for a while (a failed
-- SES check or a lapsed identity), so stamp every domain that has ever been
-- active too: the one-time ready automation is only for a domain's first
-- verification, never for one that was already in use.
-- The stamp is the epoch, never a recent time, so the readiness checklist
-- never reports these domains as running the setup.
UPDATE domains SET ready_automation_at = timestamptz 'epoch'
WHERE ready_automation_at IS NULL AND (status = 'active' OR verified_at IS NOT NULL);
