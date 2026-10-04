-- Existing and future accounts organize reports by default. The classifier
-- treats a missing settings row the same way; an explicit opt-out is retained.
ALTER TABLE user_settings
    ADD COLUMN auto_organize_dmarc_reports BOOLEAN NOT NULL DEFAULT true;
