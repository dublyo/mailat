package service

import (
	"context"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestUnmatchedSESFeedbackAcksAfterGraceAndSuppresses(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	svc := &ReceivingService{db: db}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test"}
	bounce := func(age time.Duration, address string) *model.SESNotification {
		return &model.SESNotification{NotificationType: "Bounce",
			Mail:   model.SESMail{MessageId: "never-recorded-" + address, Timestamp: time.Now().Add(-age).UTC().Format("2006-01-02T15:04:05.000Z")},
			Bounce: &model.SESBounce{BounceType: "Permanent", BouncedRecipients: []model.SESBouncedRecipient{{EmailAddress: address}}}}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Recent: the send may not be recorded yet, so SNS must retry.
	if err := svc.ProcessDeliveryEvent(ctx, auth, "young", bounce(time.Minute, "young@example.test")); err == nil {
		t.Fatal("young unmatched event was acknowledged")
	}
	if n := count(`SELECT count(*) FROM ses_delivery_events WHERE notification_id='young'`) + count(`SELECT count(*) FROM suppressions WHERE email='young@example.test'`); n != 0 {
		t.Fatalf("young event left %d rows", n)
	}

	// Old: acknowledged, suppressed in both lists, deduplicated.
	old := bounce(2*time.Hour, "Old@Example.test")
	if err := svc.ProcessDeliveryEvent(ctx, auth, "old", old); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM suppression_list WHERE org_id=1 AND email='old@example.test'`) != 1 ||
		count(`SELECT count(*) FROM suppressions WHERE org_id=1 AND email='old@example.test'`) != 1 ||
		count(`SELECT count(*) FROM ses_delivery_events WHERE notification_id='old'`) != 1 {
		t.Fatal("old unmatched bounce not suppressed or not deduplicated")
	}
	if count(`SELECT count(*) FROM webhook_events WHERE org_id=1`) != 0 {
		t.Fatal("unmatched feedback emitted a webhook event")
	}
	if err := svc.ProcessDeliveryEvent(ctx, auth, "old", old); err != nil {
		t.Fatal("redelivery:", err)
	}
}
