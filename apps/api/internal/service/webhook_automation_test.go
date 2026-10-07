package service

import (
	"context"
	"encoding/json"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWebhookTriggerLifecycleAndOwnership(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := NewWebhookTriggerService(db, &config.Config{}).ForUser(1)
	empty, err := s.List(ctx, 1)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list=%v %v", empty, err)
	}
	tr, err := s.Create(ctx, 1, 1, &CreateWebhookTriggerInput{Name: "n8n", TriggerType: "email.received", WebhookURL: "https://hooks.example.com/incoming", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Secret == "" || tr.Filters == nil || tr.UpdatedAt.IsZero() {
		t.Fatalf("missing creation fields %+v", tr)
	}
	for _, ref := range []string{tr.UUID} {
		id, err := s.ResolveID(ctx, 1, ref)
		if err != nil || id != tr.ID {
			t.Fatalf("UUID reference failed %d %v", id, err)
		}
	}
	got, err := s.Get(ctx, 1, tr.ID)
	if err != nil || got.Secret != "" {
		t.Fatalf("read leaked secret %+v %v", got, err)
	}
	other := s.ForUser(2)
	if _, err = other.Get(ctx, 1, tr.ID); err == nil {
		t.Fatal("cross-user get accepted")
	}
	if err = other.Delete(ctx, 1, tr.ID); err == nil {
		t.Fatal("cross-user delete accepted")
	}
	if _, err = other.RotateSecret(ctx, 1, tr.ID); err == nil {
		t.Fatal("cross-user rotation accepted")
	}
	secret, err := s.RotateSecret(ctx, 1, tr.ID)
	if err != nil || secret == tr.Secret || secret == "" {
		t.Fatal("rotation failed", err)
	}
	inactive := false
	filters := map[string]interface{}{"folder": "inbox"}
	updated, err := s.Update(ctx, 1, tr.ID, nil, nil, nil, &filters, &inactive)
	if err != nil || updated.Active || updated.Filters["folder"] != "inbox" {
		t.Fatalf("update failed %+v %v", updated, err)
	}
	if _, err = s.TestDelivery(ctx, 1, tr.ID); err == nil {
		t.Fatal("inactive test accepted")
	}
	bad := "https://127.0.0.1/private"
	if _, err = s.Update(ctx, 1, tr.ID, nil, nil, &bad, nil, nil); err == nil {
		t.Fatal("unsafe destination accepted")
	}
	if err = s.Delete(ctx, 1, tr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, 1, tr.ID); err == nil {
		t.Fatal("deleted trigger found")
	}
	regular := NewWebhookService(db, &config.Config{}).ForUser(1)
	w, err := regular.CreateWebhook(ctx, 1, &model.CreateWebhookRequest{Name: "events", URL: "https://hooks.example.com/all", Events: []string{"email.received", "email.sent"}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Secret == "" {
		t.Fatal("creation secret omitted")
	}
	if _, err = regular.ForUser(2).GetWebhook(ctx, 1, w.UUID); err == nil {
		t.Fatal("regular endpoint cross-user get")
	}
	if got, err := regular.GetWebhook(ctx, 1, w.UUID); err != nil || got.Secret != "" {
		t.Fatal("regular secret leaked")
	}
	if _, err = regular.GetWebhookCalls(ctx, 1, w.UUID, 50); err != nil {
		t.Fatal(err)
	}
}

func TestIncomingEventDurablePublicUUIDAndFilter(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := NewWebhookTriggerService(db, &config.Config{}).ForUser(1)
	if _, err := s.Create(ctx, 1, 1, &CreateWebhookTriggerInput{Name: "n8n", TriggerType: "email_received", WebhookURL: "https://hooks.example.com/mail", Filters: map[string]interface{}{"from": "sender@example.test"}, Active: true}); err != nil {
		t.Fatal(err)
	}
	storage := &fakeIncomingStorage{raw: []byte("From: sender@example.test\r\nTo: a@one.test, b@one.test\r\nSubject: Test\r\n\r\nhello")}
	receive := &ReceivingService{db: db, storage: storage}
	auth, err := receive.AuthorizeNotification(ctx, "arn:aws:sns:us-east-1:123456789012:test", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: "webhook-mail"}, Receipt: &model.SESReceipt{Recipients: []string{"a@one.test", "b@one.test"}, Action: model.SESAction{Type: "SNS", TopicArn: auth.TopicARN}}}
	for i := 0; i < 2; i++ {
		if err = receive.ProcessIncomingEmail(ctx, auth, n); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("owner/filter/dedupe deliveries=%d %v", count, err)
	}
	var raw []byte
	var publicID string
	if err = db.QueryRow(`SELECT e.payload,r.uuid FROM webhook_events e JOIN webhook_deliveries d ON d.event_id=e.id JOIN received_emails r ON r.uuid::text=e.payload->'data'->>'messageUuid' WHERE e.user_id=1`).Scan(&raw, &publicID); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	data := payload["data"].(map[string]any)
	if data["messageUuid"] != publicID || data["email_id"] != nil {
		t.Fatalf("bad public event %s", raw)
	}
}

type hookTransport func(*http.Request) (*http.Response, error)

func (f hookTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWebhookTestUsesRealProtocolAndReports500(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	cfg := &config.Config{}
	trigger := NewWebhookTriggerService(db, cfg).ForUser(1)
	tr, err := trigger.Create(ctx, 1, 1, &CreateWebhookTriggerInput{Name: "test", TriggerType: "email.received", WebhookURL: "https://hooks.example.com/mail", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	regular := NewWebhookService(db, cfg).ForUser(1)
	wh, err := regular.CreateWebhook(ctx, 1, &model.CreateWebhookRequest{Name: "test", URL: "https://hooks.example.com/mail", Events: []string{"email.received"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, secret string
		run          func() (*eventoutbox.Result, error)
		set          func(*http.Client)
	}{{"trigger", tr.Secret, func() (*eventoutbox.Result, error) { return trigger.TestDelivery(ctx, 1, tr.ID) }, func(c *http.Client) { trigger.httpClient = c }}, {"webhook", wh.Secret, func() (*eventoutbox.Result, error) { return regular.TestDelivery(ctx, 1, wh.UUID) }, func(c *http.Client) { regular.httpClient = c }}} {
		t.Run(item.name, func(t *testing.T) {
			item.set(&http.Client{Transport: hookTransport(func(r *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(r.Body)
				if !eventoutbox.Verify(raw, r.Header.Get("X-Webhook-Signature"), item.secret, time.Minute) {
					t.Fatal("test signature differs from real engine")
				}
				var e eventoutbox.Envelope
				if err = json.Unmarshal(raw, &e); err != nil || e.Version != "1" || e.Type != "webhook.test" || e.ID != r.Header.Get("X-Webhook-ID") {
					t.Fatalf("wrong test envelope %s", raw)
				}
				return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("fixture failure")), Header: http.Header{}}, nil
			})})
			result, err := item.run()
			if err != nil || result.Status != "retry" || result.HTTPStatus != 500 || result.Error == "" {
				t.Fatalf("false positive test result %+v %v", result, err)
			}
			_, attempts, err := regular.Delivery(ctx, 1, 1, result.DeliveryID)
			if err != nil || len(attempts) != 1 {
				t.Fatal("test attempt not recorded", err)
			}
		})
	}
	var count int
	if err = db.QueryRow(`SELECT trigger_count FROM webhook_triggers WHERE id=$1`, tr.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed test counted as trigger success", count, err)
	}
	calls, err := regular.GetWebhookCalls(ctx, 1, wh.UUID, 50)
	if err != nil || len(calls) != 1 || calls[0].ResponseStatus != 500 {
		t.Fatalf("actual attempt missing in compatibility calls %v %v", calls, err)
	}
}
func TestTransactionalFeedbackEventAfterSentDelete(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, _ := mailboxFixture(t, db, "event-feedback.test")
	fake := &mailboxTestProvider{}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake}
	result, err := svc.SendEmailForUser(ctx, org, SendActor{UserID: user, Admin: true}, &model.SendEmailRequest{From: "owner@event-feedback.test", To: []string{"to@external.test"}, Subject: "Feedback", Text: "test", IdempotencyKey: "event-feedback"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTransactionalSend(t, db, result.ID)
	var providerID string
	if err = db.QueryRow(`SELECT provider_message_id FROM transactional_emails WHERE uuid=$1`, result.ID).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if err = (&InboxService{db: db}).TrashReceivedEmails(ctx, user, []string{result.ID}, true); err != nil {
		t.Fatal(err)
	}
	receive := &ReceivingService{db: db}
	n := &model.SESNotification{NotificationType: "Bounce", Mail: model.SESMail{MessageId: providerID}, Bounce: &model.SESBounce{BounceType: "Permanent", BouncedRecipients: []model.SESBouncedRecipient{{EmailAddress: "to@external.test"}}}}
	for i := 0; i < 2; i++ {
		if err = receive.ProcessDeliveryEvent(ctx, &ReceivingAuthorization{OrgID: org, TopicARN: "test:feedback"}, "feedback-1", n); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	var owner, count int64
	if err = db.QueryRow(`SELECT payload->'data'->>'messageUuid',user_id FROM webhook_events WHERE event_type='email.bounced'`).Scan(&id, &owner); err != nil || id != result.ID || owner != user {
		t.Fatalf("deleted transactional feedback lost public owner id=%s owner=%d %v", id, owner, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM webhook_events WHERE event_type='email.bounced'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate feedback event", count, err)
	}
}
