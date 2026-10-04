-- Snapshot only pre-feature mail. No object downloads or mailbox updates run
-- during migration; the bounded application worker performs classification.
CREATE TABLE dmarc_report_backfills (
    name text PRIMARY KEY,
    cutoff_id bigint NOT NULL,
    last_id bigint NOT NULL DEFAULT 0,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    scanned bigint NOT NULL DEFAULT 0,
    moved bigint NOT NULL DEFAULT 0,
    skipped bigint NOT NULL DEFAULT 0,
    failed bigint NOT NULL DEFAULT 0,
    retry_message_id bigint,
    retry_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    last_error text NOT NULL DEFAULT ''
);
INSERT INTO dmarc_report_backfills(name,cutoff_id)
SELECT 'aggregate-v1',COALESCE(max(id),0) FROM received_emails;
