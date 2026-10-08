package service

import (
	"context"
	"net/mail"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// POST /emails accepts "Name <address>" and the display name reaches the
// outgoing message, so API clients can send as "Support <support@...>".
func TestTransactionalSendKeepsFromDisplayName(t *testing.T) {
	db := testutil.Database(t)
	org, user, _ := mailboxFixture(t, db, "named.test")
	fake := &mailboxTestProvider{}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake}
	resp, err := svc.SendEmailForUser(context.Background(), org, SendActor{UserID: user, Admin: true}, &model.SendEmailRequest{
		From: "Support Team <owner@named.test>", To: []string{"customer@example.net"}, Subject: "Hello", Text: "body", IdempotencyKey: "named-from-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForTransactionalSend(t, db, resp.ID)
	fake.mu.Lock()
	last := fake.last
	fake.mu.Unlock()
	from, err := mail.ParseAddress(last.From)
	if err != nil || from.Name != "Support Team" || from.Address != "owner@named.test" {
		t.Fatalf("from %q %v", last.From, err)
	}
}
