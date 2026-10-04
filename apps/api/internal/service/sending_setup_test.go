package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type fakeSendingSetup struct {
	db                  *sql.DB
	org                 int64
	creates, subscribes int
	feedbackErr         error
	secretPersisted     bool
}

func (f *fakeSendingSetup) SetupSendingResources(context.Context, string) (*provider.SendingSetupResources, error) {
	f.creates++
	return &provider.SendingSetupResources{Bucket: "private-sending", Region: "us-east-1", TopicARN: "arn:aws:sns:us-east-1:123456789012:sending"}, nil
}
func (f *fakeSendingSetup) EnsureSendingFeedback(context.Context, string, string, []string) error {
	return f.feedbackErr
}
func (f *fakeSendingSetup) WebhookURL(secret string) string {
	return "https://api.example.test/api/v1/webhooks/ses/incoming?secret=" + secret
}
func (f *fakeSendingSetup) SubscribeWebhook(ctx context.Context, topic, endpoint string) error {
	f.subscribes++
	u, _ := url.Parse(endpoint)
	var secret string
	if err := f.db.QueryRowContext(ctx, `SELECT webhook_secret FROM sending_configs WHERE org_id=$1 AND sns_topic_arn=$2`, f.org, topic).Scan(&secret); err != nil {
		return err
	}
	f.secretPersisted = secret != "" && secret == u.Query().Get("secret")
	if !f.secretPersisted {
		return fmt.Errorf("secret was not committed before Subscribe")
	}
	return nil
}
func (f *fakeSendingSetup) SendingSubscriptionActive(context.Context, string, string) (bool, error) {
	return false, nil
}
func TestSendingOnlySetupAuthorizationConfirmationAndRetry(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, _, identity := mailboxFixture(t, db, "send-only.test")
	var domain int64
	var id string
	if err := db.QueryRow(`SELECT d.id,d.uuid FROM domains d JOIN identities i ON i.domain_id=d.id WHERE i.id=$1`, identity).Scan(&domain, &id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE domains SET receiving_s3_bucket=NULL,receiving_enabled=false`); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSendingSetup{db: db, org: org}
	svc := &DomainService{db: db, sendingProvider: fake}
	for i := 0; i < 2; i++ {
		status, err := svc.EnsureDomainSendingResources(ctx, org, domain)
		if err != nil || !status.StorageReady || !status.FeedbackConfigured || status.FeedbackReady || status.SubscriptionStatus != "pending" {
			t.Fatal(status, err)
		}
	}
	if fake.creates != 1 || !fake.secretPersisted {
		t.Fatal("non-retryable setup or confirmation race")
	}
	var receiving bool
	var records int
	if err := db.QueryRow(`SELECT receiving_enabled,(SELECT count(*) FROM domain_dns_records WHERE record_type='MX') FROM domains WHERE id=$1`, domain).Scan(&receiving, &records); err != nil || receiving || records != 0 {
		t.Fatal("receiving/DNS changed", receiving, records, err)
	}
	var secret, topic string
	if err := db.QueryRow(`SELECT webhook_secret,sns_topic_arn FROM sending_configs WHERE org_id=$1`, org).Scan(&secret, &topic); err != nil {
		t.Fatal(err)
	}
	receive := &ReceivingService{db: db}
	auth, err := receive.AuthorizeNotification(ctx, topic, secret)
	if err != nil || !auth.SendingOnly {
		t.Fatal("sending topic not authorized", auth, err)
	}
	if err = receive.ProcessIncomingEmail(ctx, auth, &model.SESNotification{}); err == nil || !strings.Contains(err.Error(), "cannot deliver incoming") {
		t.Fatal("sending topic became an inbound route", err)
	}
	if err = receive.ConfirmNotificationSubscription(ctx, auth); err != nil {
		t.Fatal(err)
	}
	status, err := svc.GetSendingStatus(ctx, org, id)
	if err != nil || !status.FeedbackReady {
		t.Fatal("confirmed subscription not reflected", status, err)
	}
	if _, err = svc.EnsureDomainSendingResources(ctx, org+1, domain); err == nil || fake.creates != 1 {
		t.Fatal("foreign domain allowed", err)
	}
	fake.feedbackErr = provider.ErrSendingFeedbackConflict
	status, err = svc.EnsureDomainSendingResources(ctx, org, domain)
	if err != nil || status.FeedbackReady || !status.StorageReady || status.Reason != provider.ErrSendingFeedbackConflict.Error() {
		t.Fatal("feedback conflict not surfaced", status, err)
	}
}
