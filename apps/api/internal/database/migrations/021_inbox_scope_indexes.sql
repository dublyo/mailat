-- 021_inbox_scope_indexes.sql: the mailbox sidebar splits each user's Inbox by
-- domain and by address. These indexes serve the per-domain and per-address
-- list pages and counts (mailbox_owner_id scoping, newest first). The runner
-- wraps each migration in a transaction, so CONCURRENTLY is not available;
-- the table is small enough on current installs for a brief lock.
CREATE INDEX IF NOT EXISTS received_emails_owner_domain_folder_time_idx
  ON received_emails (mailbox_owner_id, domain_id, folder, received_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS received_emails_owner_identity_folder_time_idx
  ON received_emails (mailbox_owner_id, identity_id, folder, received_at DESC, id DESC);
