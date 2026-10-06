package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
)

func (s *ReceivingService) ProcessDeliveryEvent(ctx context.Context, auth *ReceivingAuthorization, notificationID string, n *model.SESNotification) error {
	status := ""
	source := ""
	recipients := []string{}
	switch n.NotificationType {
	case "Delivery":
		if n.Delivery == nil {
			return fmt.Errorf("missing delivery event")
		}
		status = "delivered"
	case "Bounce":
		if n.Bounce == nil {
			return fmt.Errorf("missing bounce event")
		}
		status = "bounced"
		if n.Bounce.BounceType == "Permanent" {
			source = "bounce"
			for _, r := range n.Bounce.BouncedRecipients {
				recipients = append(recipients, strings.ToLower(r.EmailAddress))
			}
		}
	case "Complaint":
		if n.Complaint == nil {
			return fmt.Errorf("missing complaint event")
		}
		status = "complained"
		source = "complaint"
		for _, r := range n.Complaint.ComplainedRecipients {
			recipients = append(recipients, strings.ToLower(r.EmailAddress))
		}
	default:
		return nil
	}
	if notificationID == "" || n.Mail.MessageId == "" {
		return fmt.Errorf("missing event identifier")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO ses_delivery_events(topic_arn,notification_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, auth.TopicARN, notificationID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return nil
	}
	mailboxUUID := ""
	for _, h := range n.Mail.Headers {
		if strings.EqualFold(h.Name, "X-Mailat-Message-ID") {
			mailboxUUID = h.Value
			break
		}
	}
	var known bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM received_emails WHERE org_id=$1 AND (ses_message_id=$2 OR (ses_message_id IS NULL AND uuid::text=$3)) AND direction='outbound') OR EXISTS(SELECT 1 FROM transactional_emails WHERE org_id=$1 AND provider_message_id=$2) OR EXISTS(SELECT 1 FROM emails WHERE org_id=$1 AND provider_message_id=$2) OR EXISTS(SELECT 1 FROM compose_submission_keys k JOIN users u ON u.id=k.user_id WHERE u.org_id=$1 AND (k.ses_message_id=$2 OR (k.ses_message_id IS NULL AND k.email_uuid::text=$3))) OR EXISTS(SELECT 1 FROM campaign_recipients WHERE org_id=$1 AND (provider_message_id=$2 OR (provider_message_id IS NULL AND message_uuid=$4::uuid))) OR EXISTS(SELECT 1 FROM automation_messages WHERE org_id=$1 AND (provider_message_id=$2 OR (provider_message_id IS NULL AND message_uuid=$4::uuid)))`, auth.OrgID, n.Mail.MessageId, mailboxUUID, headerUUIDParam(mailboxUUID)).Scan(&known)
	if err != nil {
		return err
	}
	if !known {
		// A young event may race the send being recorded: fail so SNS retries.
		sentAt, parseErr := time.Parse(time.RFC3339, n.Mail.Timestamp)
		if parseErr != nil || time.Since(sentAt) < unmatchedFeedbackGrace {
			return fmt.Errorf("provider send has not been recorded yet")
		}
		// Older feedback is for mail Mailat never recorded (another app on the same
		// identity, or a lost record). Ack it so SNS stops retrying, but still honor
		// permanent bounces and complaints. No status updates and no webhook.
		if err = insertFeedbackSuppressions(ctx, tx, auth.OrgID, recipients, source, n.Mail.MessageId); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		log.Printf("unmatched SES %s for org=%d", n.NotificationType, auth.OrgID)
		return nil
	}
	// Complaints/bounces are terminal; a late delivery notification must not undo them.
	if _, err = tx.ExecContext(ctx, `UPDATE received_emails SET send_status=$1::varchar,ses_message_id=COALESCE(ses_message_id,$3),updated_at=NOW() WHERE org_id=$2 AND (ses_message_id=$3 OR (ses_message_id IS NULL AND uuid::text=$4)) AND direction='outbound' AND send_status!='complained' AND ($1::varchar='complained' OR send_status!='bounced')`, status, auth.OrgID, n.Mail.MessageId, mailboxUUID); err != nil {
		return err
	}
	// The immutable submission receipt remains after deleting the Sent copy.
	// A later bounce must still suppress the address and update the send outcome.
	if _, err = tx.ExecContext(ctx, `UPDATE compose_submission_keys k SET status=$1::text,ses_message_id=COALESCE(k.ses_message_id,$3),updated_at=NOW()
 FROM users u WHERE u.id=k.user_id AND u.org_id=$2 AND (k.ses_message_id=$3 OR (k.ses_message_id IS NULL AND k.email_uuid::text=$4))
 AND k.status!='complained' AND ($1::text='complained' OR k.status!='bounced')`, status, auth.OrgID, n.Mail.MessageId, mailboxUUID); err != nil {
		return err
	}
	for _, table := range []string{"transactional_emails", "emails"} {
		if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET status=$1::varchar,updated_at=NOW() WHERE org_id=$2 AND provider_message_id=$3 AND status!='complained' AND ($1::varchar='complained' OR status!='bounced')`, status, auth.OrgID, n.Mail.MessageId); err != nil {
			return err
		}
	}
	if err = insertFeedbackSuppressions(ctx, tx, auth.OrgID, recipients, source, n.Mail.MessageId); err != nil {
		return err
	}
	if err = applyCampaignFeedback(ctx, tx, auth.OrgID, notificationID, n, mailboxUUID); err != nil {
		return err
	}
	if err = applyAutomationFeedback(ctx, tx, auth.OrgID, n, mailboxUUID); err != nil {
		return err
	}
	// Resolve the immutable public mailbox ID even if the Sent copy was deleted.
	rows, err := tx.QueryContext(ctx, `SELECT e.uuid::text,i.user_id,e.identity_id FROM received_emails e JOIN identities i ON i.id=e.identity_id WHERE e.org_id=$1 AND e.direction='outbound' AND(e.ses_message_id=$2 OR e.uuid::text=$3)
 UNION SELECT t.uuid::text,i.user_id,t.identity_id FROM transactional_emails t JOIN identities i ON i.id=t.identity_id WHERE t.org_id=$1 AND(t.provider_message_id=$2 OR t.uuid::text=$3)
 UNION SELECT k.email_uuid::text,k.user_id,COALESCE(k.identity_id,0)::bigint FROM compose_submission_keys k JOIN users u ON u.id=k.user_id WHERE u.org_id=$1 AND(k.ses_message_id=$2 OR k.email_uuid::text=$3)`, auth.OrgID, n.Mail.MessageId, mailboxUUID)
	if err != nil {
		return err
	}
	type target struct {
		uuid           string
		user, identity int64
	}
	targets := []target{}
	seen := map[string]int{}
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.uuid, &t.user, &t.identity); err != nil {
			rows.Close()
			return err
		}
		if position, ok := seen[t.uuid]; !ok {
			seen[t.uuid] = len(targets)
			targets = append(targets, t)
		} else if targets[position].identity == 0 && t.identity != 0 {
			targets[position] = t
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range targets {
		data := map[string]any{"providerMessageId": n.Mail.MessageId, "status": status}
		if n.Bounce != nil {
			data["bounceType"] = n.Bounce.BounceType
		}
		if n.Complaint != nil {
			data["complaintType"] = n.Complaint.ComplaintFeedbackType
		}
		if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email." + status, OrgID: auth.OrgID, UserID: t.user, IdentityID: t.identity, MessageUUID: t.uuid, DedupeKey: notificationID + ":" + t.uuid, Data: data}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	return nil
}

// campaignDeliveryRank orders delivery_status; it only ever moves up.
var campaignDeliveryRank = map[string]int{"": 0, "delivered": 1, "bounced": 2, "complained": 3}

// applyCampaignFeedback records SES feedback on the matching campaign
// recipient. delivery_status follows delivered < bounced < complained and each
// campaign counter moves only when the row first reaches that value; transient
// bounces only stamp soft_bounced_at. Any feedback proves SES accepted the
// message, so a sending or unknown row becomes sent. A first permanent bounce
// or complaint notifies the campaign creator's webhooks; deliveries do not.
func applyCampaignFeedback(ctx context.Context, tx *sql.Tx, orgID int64, notificationID string, n *model.SESNotification, headerUUID string) error {
	target := ""
	switch {
	case n.NotificationType == "Delivery":
		target = "delivered"
	case n.NotificationType == "Bounce" && n.Bounce.BounceType == "Permanent":
		target = "bounced"
	case n.NotificationType == "Complaint":
		target = "complained"
	}
	var (
		id, campaignID, userID, identityID int64
		status, email, messageUUID         string
		campaignUUID                       string
		current                            sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT r.id, r.campaign_id, r.status, r.delivery_status, r.email, r.message_uuid::text, c.uuid::text,
			COALESCE(c.created_by_user_id,0),
			CASE WHEN EXISTS(SELECT 1 FROM identities i JOIN users u ON u.id=i.user_id
				WHERE i.id=c.identity_id AND i.user_id=c.created_by_user_id AND u.org_id=c.org_id) THEN c.identity_id ELSE 0 END
		FROM campaign_recipients r JOIN campaigns c ON c.id=r.campaign_id AND c.org_id=r.org_id
		WHERE r.org_id=$1 AND (r.provider_message_id=$2 OR (r.provider_message_id IS NULL AND r.message_uuid=$3::uuid))
		ORDER BY r.id LIMIT 1 FOR UPDATE OF r`, orgID, n.Mail.MessageId, headerUUIDParam(headerUUID)).
		Scan(&id, &campaignID, &status, &current, &email, &messageUUID, &campaignUUID, &userID, &identityID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load campaign recipient: %w", err)
	}
	moved := target != "" && campaignDeliveryRank[target] > campaignDeliveryRank[current.String]
	promote := status == "sending" || status == "unknown"
	newStatus := status
	if promote {
		newStatus = "sent"
	}
	setTarget := ""
	if moved {
		setTarget = target
	}
	if _, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET
			provider_message_id=COALESCE(provider_message_id,$3),
			status=$4,
			sent_at=CASE WHEN $5 THEN COALESCE(sent_at,now()) ELSE sent_at END,
			error=CASE WHEN $5 THEN NULL ELSE error END,
			lease_owner=CASE WHEN $5 THEN NULL ELSE lease_owner END,
			lease_expires_at=CASE WHEN $5 THEN NULL ELSE lease_expires_at END,
			delivery_status=COALESCE(NULLIF($6,''),delivery_status),
			delivered_at=CASE WHEN $6='delivered' THEN now() ELSE delivered_at END,
			bounced_at=CASE WHEN $6='bounced' THEN now() ELSE bounced_at END,
			complained_at=CASE WHEN $6='complained' THEN now() ELSE complained_at END,
			soft_bounced_at=CASE WHEN $7 THEN now() ELSE soft_bounced_at END,
			updated_at=now()
		WHERE id=$1 AND org_id=$2`, id, orgID, n.Mail.MessageId, newStatus, promote, setTarget,
		n.NotificationType == "Bounce" && target == ""); err != nil {
		return fmt.Errorf("update campaign recipient: %w", err)
	}
	var sent, unknown, delivered, bounced, complained int
	if promote {
		sent = 1
		if status == "unknown" {
			unknown = -1
		}
	}
	switch setTarget {
	case "delivered":
		delivered = 1
	case "bounced":
		bounced = 1
	case "complained":
		complained = 1
	}
	if promote || setTarget != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE campaigns SET sent_count=sent_count+$3, unknown_count=unknown_count+$4,
				delivered_count=delivered_count+$5, bounce_count=bounce_count+$6, complaint_count=complaint_count+$7, updated_at=now()
			WHERE id=$1 AND org_id=$2`, campaignID, orgID, sent, unknown, delivered, bounced, complained); err != nil {
			return fmt.Errorf("update campaign counters: %w", err)
		}
	}
	if setTarget == "bounced" || setTarget == "complained" {
		data := map[string]any{"campaignUuid": campaignUUID, "recipient": email, "providerMessageId": n.Mail.MessageId, "status": target}
		if n.Bounce != nil {
			data["bounceType"] = n.Bounce.BounceType
		}
		if n.Complaint != nil {
			data["complaintType"] = n.Complaint.ComplaintFeedbackType
		}
		if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email." + target, OrgID: orgID, UserID: userID, IdentityID: identityID,
			MessageUUID: messageUUID, DedupeKey: notificationID + ":" + messageUUID, Data: data}); err != nil {
			return fmt.Errorf("record campaign feedback event: %w", err)
		}
	}
	return nil
}

// applyAutomationFeedback records SES feedback on the matching automation
// message: delivery_status only moves up (delivered < bounced < complained)
// and any feedback proves SES accepted it, so sending or unknown becomes sent.
// Suppression of bounced and complained addresses is handled by the caller.
func applyAutomationFeedback(ctx context.Context, tx *sql.Tx, orgID int64, n *model.SESNotification, headerUUID string) error {
	target := ""
	switch {
	case n.NotificationType == "Delivery":
		target = "delivered"
	case n.NotificationType == "Bounce" && n.Bounce.BounceType == "Permanent":
		target = "bounced"
	case n.NotificationType == "Complaint":
		target = "complained"
	}
	var id int64
	var status string
	var current sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, status, delivery_status FROM automation_messages
		WHERE org_id=$1 AND (provider_message_id=$2 OR (provider_message_id IS NULL AND message_uuid=$3::uuid))
		ORDER BY id LIMIT 1 FOR UPDATE`, orgID, n.Mail.MessageId, headerUUIDParam(headerUUID)).Scan(&id, &status, &current)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load automation message: %w", err)
	}
	if campaignDeliveryRank[target] <= campaignDeliveryRank[current.String] {
		target = ""
	}
	promote := status == "sending" || status == "unknown"
	if _, err = tx.ExecContext(ctx, `UPDATE automation_messages SET
			provider_message_id=COALESCE(provider_message_id,$3),
			status=CASE WHEN $4 THEN 'sent' ELSE status END,
			sent_at=CASE WHEN $4 THEN COALESCE(sent_at,now()) ELSE sent_at END,
			error=CASE WHEN $4 THEN NULL ELSE error END,
			lease_owner=CASE WHEN $4 THEN NULL ELSE lease_owner END,
			lease_expires_at=CASE WHEN $4 THEN NULL ELSE lease_expires_at END,
			delivery_status=COALESCE(NULLIF($5,''),delivery_status),
			delivered_at=CASE WHEN $5='delivered' THEN now() ELSE delivered_at END,
			bounced_at=CASE WHEN $5='bounced' THEN now() ELSE bounced_at END,
			complained_at=CASE WHEN $5='complained' THEN now() ELSE complained_at END,
			updated_at=now()
		WHERE id=$1 AND org_id=$2`, id, orgID, n.Mail.MessageId, promote, target); err != nil {
		return fmt.Errorf("update automation message: %w", err)
	}
	return nil
}

// headerUUIDParam is the X-Mailat-Message-ID header as a uuid parameter, or
// NULL when it is missing or malformed. Comparing the uuid column directly
// (not message_uuid::text) lets both match branches use an index.
func headerUUIDParam(header string) any {
	u, err := uuid.Parse(strings.TrimSpace(header))
	if err != nil {
		return nil
	}
	return u.String()
}

// unmatchedFeedbackGrace is how long feedback for an unknown send keeps failing
// (so SNS redelivers) before it is treated as mail Mailat never recorded.
const unmatchedFeedbackGrace = time.Hour

// insertFeedbackSuppressions records permanent bounces and complaints in both the
// transactional (suppression_list) and marketing (suppressions) lists.
func insertFeedbackSuppressions(ctx context.Context, tx *sql.Tx, orgID int64, recipients []string, source, providerMessageID string) error {
	for _, address := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO suppression_list(org_id,email,reason,source) VALUES($1,$2,$3::text,$3::text) ON CONFLICT(org_id,email) DO NOTHING`, orgID, address, source); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO suppressions(org_id,email,reason,source_type,source_id) VALUES($1,$2,$3,'ses',$4) ON CONFLICT(org_id,email) DO NOTHING`, orgID, address, source, providerMessageID); err != nil {
			return err
		}
	}
	return nil
}

// Cleanup is durable and reference-aware: deleting one recipient's copy never
// removes objects still used by another recipient or a forwarded message.
func (s *ReceivingService) RunStorageCleanup(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.cleanupStorage(ctx); err != nil {
				log.Printf("Storage cleanup will retry: %v", err)
			}
		}
	}
}
func (s *ReceivingService) cleanupStorage(ctx context.Context) error {
	for index := 0; index < 25; index++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var id int64
		var bucket, key string
		err = tx.QueryRowContext(ctx, `SELECT id,bucket,object_key FROM storage_cleanup_jobs WHERE next_attempt_at<=NOW() ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &bucket, &key)
		if err == sql.ErrNoRows {
			tx.Rollback()
			return nil
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		var referenced bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM received_emails WHERE raw_s3_bucket=$1 AND raw_s3_key=$2) OR EXISTS(SELECT 1 FROM email_attachments WHERE s3_bucket=$1 AND s3_key=$2) OR EXISTS(SELECT 1 FROM compose_uploads WHERE s3_bucket=$1 AND s3_key=$2) OR EXISTS(SELECT 1 FROM send_attachment_refs WHERE s3_bucket=$1 AND s3_key=$2)`, bucket, key).Scan(&referenced)
		if err != nil {
			tx.Rollback()
			return err
		}
		if referenced {
			_, err = tx.ExecContext(ctx, `UPDATE storage_cleanup_jobs SET next_attempt_at=NOW()+INTERVAL '1 hour' WHERE id=$1`, id)
		} else {
			requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			deleteErr := s.storage.DeleteObject(requestCtx, bucket, key)
			cancel()
			if deleteErr != nil {
				_, err = tx.ExecContext(ctx, `UPDATE storage_cleanup_jobs SET attempts=attempts+1,next_attempt_at=NOW()+INTERVAL '5 minutes',last_error='S3 deletion failed' WHERE id=$1`, id)
			} else {
				_, err = tx.ExecContext(ctx, `DELETE FROM storage_cleanup_jobs WHERE id=$1`, id)
			}
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// AttachmentDownload proxies bounded private bytes through the authenticated API.
// Returning bytes avoids requiring permissive S3 CORS or exposing storage keys.
func (s *ReceivingService) AttachmentDownload(ctx context.Context, userID int64, emailUUID, attachmentUUID string) ([]byte, string, string, error) {
	var bucket, key, filename, contentType string
	err := s.db.QueryRowContext(ctx, `SELECT a.s3_bucket,a.s3_key,a.filename,a.content_type FROM email_attachments a JOIN received_emails e ON e.id=a.received_email_id JOIN identities i ON i.id=e.identity_id WHERE i.user_id=$1 AND e.uuid=$2 AND a.uuid=$3`, userID, emailUUID, attachmentUUID).Scan(&bucket, &key, &filename, &contentType)
	if err != nil || bucket == "" || key == "" {
		return nil, "", "", fmt.Errorf("attachment not found")
	}
	data, err := s.storage.GetEmailFromS3(ctx, bucket, key)
	if err != nil {
		return nil, "", "", fmt.Errorf("attachment storage unavailable: %w", err)
	}
	return data, filename, contentType, nil
}
