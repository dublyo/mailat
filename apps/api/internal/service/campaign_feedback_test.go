package service

import (
	"context"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
)

func campaignFeedback(kind, messageID, headerUUID, bounceType string, to ...string) *model.SESNotification {
	n := &model.SESNotification{NotificationType: kind, Mail: model.SESMail{MessageId: messageID, Timestamp: time.Now().UTC().Format(time.RFC3339)}}
	if headerUUID != "" {
		n.Mail.Headers = []model.SESHeader{{Name: "X-Mailat-Message-ID", Value: headerUUID}}
	}
	switch kind {
	case "Delivery":
		n.Delivery = &model.SESDelivery{Recipients: to}
	case "Bounce":
		n.Bounce = &model.SESBounce{BounceType: bounceType}
		for _, r := range to {
			n.Bounce.BouncedRecipients = append(n.Bounce.BouncedRecipients, model.SESBouncedRecipient{EmailAddress: r})
		}
	case "Complaint":
		n.Complaint = &model.SESComplaint{ComplaintFeedbackType: "abuse"}
		for _, r := range to {
			n.Complaint.ComplainedRecipients = append(n.Complaint.ComplainedRecipients, model.SESComplainedRecipient{EmailAddress: r})
		}
	}
	return n
}

func TestCampaignSESFeedback(t *testing.T) {
	db, svc, r, _ := senderFixture(t)
	ctx := context.Background()
	id := startCampaign(t, svc)
	drain(t, r)
	wantCampaign(t, db, id, "sent", "")
	mustExec(t, db, `INSERT INTO webhooks(org_id,user_id,name,url,secret,events,updated_at) VALUES(1,2,'sink','https://hook.example.com/r','s',ARRAY['email.bounced','email.complained'],now())`)
	var aID string
	if err := db.QueryRow(`SELECT provider_message_id FROM campaign_recipients WHERE email='a@example.net'`).Scan(&aID); err != nil || aID == "" {
		t.Fatalf("provider id %q %v", aID, err)
	}
	recv := &ReceivingService{db: db}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test"}
	process := func(notification string, n *model.SESNotification) {
		t.Helper()
		if err := recv.ProcessDeliveryEvent(ctx, auth, notification, n); err != nil {
			t.Fatal(notification, err)
		}
	}
	counters := func(delivered, bounced, complained int) {
		t.Helper()
		count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND delivered_count=$2 AND bounce_count=$3 AND complaint_count=$4 AND sent_count=2`, id, delivered, bounced, complained)
	}

	// Precedence delivered < bounced < complained; each counter moves once.
	process("n1", campaignFeedback("Delivery", aID, "", "", "a@example.net"))
	process("n1", campaignFeedback("Delivery", aID, "", "", "a@example.net")) // replay
	process("n2", campaignFeedback("Delivery", aID, "", "", "a@example.net"))
	counters(1, 0, 0)
	process("n3", campaignFeedback("Bounce", aID, "", "Transient", "a@example.net"))
	counters(1, 0, 0)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='a@example.net' AND delivery_status='delivered' AND soft_bounced_at IS NOT NULL`)
	count(t, db, 0, `SELECT count(*) FROM suppressions WHERE org_id=1`)
	process("n4", campaignFeedback("Bounce", aID, "", "Permanent", "A@example.net"))
	process("n5", campaignFeedback("Bounce", aID, "", "Permanent", "a@example.net"))
	counters(1, 1, 0)
	process("n6", campaignFeedback("Complaint", aID, "", "", "a@example.net"))
	process("n7", campaignFeedback("Delivery", aID, "", "", "a@example.net"))
	process("n8", campaignFeedback("Bounce", aID, "", "Permanent", "a@example.net"))
	counters(1, 1, 1)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='a@example.net' AND delivery_status='complained'
		AND delivered_at IS NOT NULL AND bounced_at IS NOT NULL AND complained_at IS NOT NULL AND status='sent'`)
	count(t, db, 1, `SELECT count(*) FROM suppressions WHERE org_id=1 AND email='a@example.net'`)
	count(t, db, 1, `SELECT count(*) FROM suppression_list WHERE org_id=1 AND email='a@example.net'`)

	// Only the first permanent bounce and complaint reach the creator's webhook.
	var campaignUUID string
	if err := db.QueryRow(`SELECT uuid::text FROM campaigns WHERE id=$1`, id).Scan(&campaignUUID); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM webhook_events e JOIN webhook_deliveries d ON d.event_id=e.id WHERE e.event_type='email.bounced' AND e.user_id=2
		AND e.dedupe_key LIKE 'n4:%' AND e.payload->'data'->>'campaignUuid'=$1 AND e.payload->'data'->>'recipient'='a@example.net'
		AND e.payload->'data'->>'bounceType'='Permanent' AND e.payload->'data'->>'identityId'='1'`, campaignUUID)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='email.complained' AND payload->'data'->>'complaintType'='abuse'`)
	count(t, db, 0, `SELECT count(*) FROM webhook_events WHERE event_type='email.delivered'`)
	count(t, db, 2, `SELECT count(*) FROM webhook_events WHERE event_type LIKE 'email.%'`)
}

// Feedback for a send whose finish was never recorded upgrades the row, matched
// by the X-Mailat-Message-ID header alone, and fills in the provider ID.
func TestCampaignSESFeedbackUpgradesUnrecordedSends(t *testing.T) {
	db, svc, r, _ := senderFixture(t)
	ctx := context.Background()
	id := startCampaign(t, svc)
	if err := r.prepareDue(ctx); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE campaign_recipients SET status='unknown' WHERE email='a@example.net';
		UPDATE campaign_recipients SET status='sending', attempt_started_at=now() WHERE email='b@example.net'`)
	mustExec(t, db, `UPDATE campaigns SET unknown_count=1 WHERE id=$1`, id)
	var aUUID, bUUID string
	if err := db.QueryRow(`SELECT (SELECT message_uuid::text FROM campaign_recipients WHERE email='a@example.net'), (SELECT message_uuid::text FROM campaign_recipients WHERE email='b@example.net')`).Scan(&aUUID, &bUUID); err != nil {
		t.Fatal(err)
	}
	recv := &ReceivingService{db: db}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test"}
	if err := recv.ProcessDeliveryEvent(ctx, auth, "d1", campaignFeedback("Delivery", "ses-late-a", aUUID, "", "a@example.net")); err != nil {
		t.Fatal(err)
	}
	if err := recv.ProcessDeliveryEvent(ctx, auth, "d2", campaignFeedback("Bounce", "ses-late-b", bUUID, "Transient", "b@example.net")); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='a@example.net' AND status='sent' AND sent_at IS NOT NULL AND provider_message_id='ses-late-a' AND delivery_status='delivered'`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='b@example.net' AND status='sent' AND provider_message_id='ses-late-b' AND delivery_status IS NULL AND soft_bounced_at IS NOT NULL`)
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND sent_count=2 AND unknown_count=0 AND delivered_count=1 AND bounce_count=0`, id)

	// The runner's late finish for b neither recounts nor resends it.
	drain(t, r)
	wantCampaign(t, db, id, "sent", "")
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND sent_count=2 AND unknown_count=0`, id)
}

// A campaign whose creator's identity is gone still records feedback: the
// outbox event is written without an identity instead of failing the SNS
// transaction.
func TestCampaignSESFeedbackWithoutIdentity(t *testing.T) {
	db, svc, r, _ := senderFixture(t)
	ctx := context.Background()
	id := startCampaign(t, svc)
	drain(t, r)
	mustExec(t, db, `DELETE FROM identities WHERE id=1`)
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND identity_id IS NULL`, id)
	var bID string
	if err := db.QueryRow(`SELECT provider_message_id FROM campaign_recipients WHERE email='b@example.net'`).Scan(&bID); err != nil {
		t.Fatal(err)
	}
	recv := &ReceivingService{db: db}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test"}
	if err := recv.ProcessDeliveryEvent(ctx, auth, "c1", campaignFeedback("Complaint", bID, "", "", "b@example.net")); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT complaint_count FROM campaigns WHERE id=$1`, id)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='email.complained' AND user_id=2 AND NOT (payload->'data' ? 'identityId')`)
	// Another org's feedback never touches this campaign.
	other := &ReceivingAuthorization{OrgID: 2, TopicARN: auth.TopicARN}
	_ = recv.ProcessDeliveryEvent(ctx, other, "c2", campaignFeedback("Bounce", bID, "", "Permanent", "b@example.net"))
	count(t, db, 0, `SELECT bounce_count FROM campaigns WHERE id=$1`, id)
}
