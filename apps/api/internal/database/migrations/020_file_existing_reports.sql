-- 020_file_existing_reports.sql: arrival now files bounce notices (delivery
-- status notifications from MAILER-DAEMON or postmaster) under Reports
-- (folder 'dmarc-reports'). Apply the same rule once to bounce notices already
-- in the Inbox, only for mailbox owners who keep automatic filing on (the
-- default). Nothing is deleted: the mail stays in All Mail and Reports.
-- Replies and forwards (Re:/Fwd:), spam and archived mail stay where they are.
UPDATE received_emails re
SET folder = 'dmarc-reports', updated_at = NOW()
WHERE re.folder = 'inbox'
  AND re.direction = 'inbound'
  AND NOT re.is_trashed AND NOT re.is_archived AND NOT re.is_spam
  AND COALESCE(re.subject, '') !~* '^\s*(re|fw|fwd)\s*(\[[0-9]+\])?\s*:'
  AND lower(COALESCE(re.from_email, '')) ~ '^(mailer-daemon|postmaster)@'
  AND COALESCE(re.subject, '') ~* '^\s*(delivery status notification|undeliver(ed|able)|mail delivery (failed|failure|subsystem)|returned mail|delivery (failure|has failed|incomplete|delayed|notification)|failure notice|non[- ]?delivery)'
  AND COALESCE((SELECT us.auto_organize_dmarc_reports FROM user_settings us WHERE us.user_id = re.mailbox_owner_id), true);
