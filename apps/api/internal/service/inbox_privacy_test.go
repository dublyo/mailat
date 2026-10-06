package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func blockedFilter(value, operator, folder string) *model.InboxFilter {
	return &model.InboxFilter{Kind: FilterKindBlockedSender, Active: true, ConditionLogic: "all",
		Conditions: []model.FilterCondition{{Field: "from", Operator: operator, Value: value}}, ActionFolder: folder, ActionMarkRead: true}
}

func TestBlockedSenderValidationMatrix(t *testing.T) {
	cases := []struct {
		name string
		f    *model.InboxFilter
		ok   bool
	}{
		{"address", blockedFilter("Bad@Example.com", "equals", "spam"), true},
		{"domain", blockedFilter("@Example.co.uk", "endsWith", "trash"), true},
		{"display name", blockedFilter("Bad <bad@example.com>", "equals", "spam"), false},
		{"domain without at", blockedFilter("example.com", "endsWith", "spam"), false},
		{"domain via equals", blockedFilter("@example.com", "equals", "spam"), false},
		{"contains", blockedFilter("example", "contains", "spam"), false},
		{"inbox destination", blockedFilter("a@example.com", "equals", "inbox"), false},
		{"no destination", blockedFilter("a@example.com", "equals", ""), false},
		{"subject field", &model.InboxFilter{Kind: FilterKindBlockedSender, Conditions: []model.FilterCondition{{Field: "subject", Operator: "equals", Value: "a@example.com"}}, ActionFolder: "spam"}, false},
		{"two conditions", &model.InboxFilter{Kind: FilterKindBlockedSender, Conditions: []model.FilterCondition{{Field: "from", Operator: "equals", Value: "a@example.com"}, {Field: "from", Operator: "equals", Value: "b@example.com"}}, ActionFolder: "spam"}, false},
	}
	for _, tc := range cases {
		err := normalizeBlockedSender(tc.f)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v want ok=%t", tc.name, err, tc.ok)
		}
	}
	f := blockedFilter("Bad@Example.com", "equals", "spam")
	f.ActionStar = true
	if err := normalizeBlockedSender(f); err == nil {
		t.Error("star action accepted on blocked sender")
	}
	f = blockedFilter(" Bad@Example.com ", "equals", "spam")
	f.Priority = 500
	if err := normalizeBlockedSender(f); err != nil || f.Conditions[0].Value != "bad@example.com" || f.Priority != 0 || f.Name != "Blocked: bad@example.com" {
		t.Fatalf("not normalized: %+v %v", f, err)
	}
}

func TestBlockedSenderIdempotentAndAppliedLast(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := &InboxService{db: db}
	first, err := s.SaveFilter(ctx, 1, 1, "", blockedFilter("@Example.test", "endsWith", "spam"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SaveFilter(ctx, 1, 1, "", blockedFilter("@example.test", "endsWith", "spam"))
	if err != nil || again.UUID != first.UUID {
		t.Fatalf("duplicate block not idempotent: %+v %v", again, err)
	}
	// A user rule with higher priority that routes the same sender to the inbox
	// must not override the block.
	for _, priority := range []int{100, -100} {
		if _, err = s.SaveFilter(ctx, 1, 1, "", &model.InboxFilter{Name: fmt.Sprintf("Keep example %d", priority), Priority: priority, Active: true, ConditionLogic: "all", Conditions: []model.FilterCondition{{Field: "from", Operator: "contains", Value: "example"}}, ActionFolder: "inbox"}); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := s.ListFilters(ctx, 1, FilterKindBlockedSender)
	if err != nil || len(blocked) != 1 || blocked[0].Kind != FilterKindBlockedSender {
		t.Fatalf("kind filter: %+v %v", blocked, err)
	}
	all, err := s.ListFilters(ctx, 1, "")
	if err != nil || len(all) != 3 || all[2].Kind != FilterKindBlockedSender {
		t.Fatalf("blocked sender not listed last: %+v %v", all, err)
	}
	if _, err = s.ListFilters(ctx, 1, "bogus"); !errors.Is(err, ErrInvalidMailboxInput) {
		t.Fatal("unknown kind accepted", err)
	}
	// kind is immutable on update.
	conv := blocked[0]
	conv.Kind = FilterKindFilter
	if _, err = s.SaveFilter(ctx, 1, 1, conv.UUID, &conv); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatal("kind changed on update", err)
	}

	receiving := &ReceivingService{db: db, storage: &fakeIncomingStorage{raw: []byte("From: a@example.test\r\nTo: a@one.test\r\nSubject: Hi\r\nMessage-ID: <blocked@example.test>\r\n\r\nBody")}}
	auth, err := receiving.AuthorizeNotification(ctx, "arn:aws:sns:us-east-1:123456789012:test", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId = "blocked-sender"
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: []string{"a@one.test"}, Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/one.test/blocked-sender"}}
	if err = receiving.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	var folder string
	var read bool
	if err = db.QueryRow(`SELECT folder,is_read FROM received_emails WHERE message_id='<blocked@example.test>' AND identity_id=1`).Scan(&folder, &read); err != nil {
		t.Fatal(err)
	}
	if folder != "spam" || !read {
		t.Fatalf("blocked sender delivered to %s read=%t", folder, read)
	}
}

func TestTrustedSendersAndRemoteImageFlags(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := &InboxService{db: db}
	insert := func(from, dmarc, folder, direction string) string {
		var id string
		if err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,dmarc_verdict,folder,is_spam,direction,received_at,updated_at) VALUES(1,1,1,$1,$2,ARRAY['a@one.test'],'Images',NULLIF($3,''),$4::text,$4::text='spam',$5,NOW(),NOW()) RETURNING uuid`, fmt.Sprintf("<%d@x>", time.Now().UnixNano()), from, dmarc, folder, direction).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	check := func(id, want string, trusted bool) {
		t.Helper()
		e, err := s.GetReceivedEmail(ctx, 1, id)
		if err != nil || e.RemoteImages != want || e.TrustedSender != trusted {
			t.Fatalf("got %q trusted=%t err=%v, want %q trusted=%t", e.RemoteImages, e.TrustedSender, err, want, trusted)
		}
	}
	pass := insert("News@Brand.test", "pass", "inbox", "inbound")
	fail := insert("news@brand.test", "FAIL", "inbox", "inbound")
	spam := insert("news@brand.test", "PASS", "spam", "inbound")
	sent := insert("a@one.test", "", "sent", "outbound")
	// No user_settings row: "ask".
	check(pass, "blocked", false)
	check(sent, "allowed", false)

	if _, _, err := s.AddTrustedSender(ctx, 1, 1, "not an address"); !errors.Is(err, ErrInvalidMailboxInput) {
		t.Fatal("invalid sender accepted", err)
	}
	ts, created, err := s.AddTrustedSender(ctx, 1, 1, "@Brand.test")
	if err != nil || !created || ts.Sender != "@brand.test" {
		t.Fatalf("add: %+v %t %v", ts, created, err)
	}
	again, created, err := s.AddTrustedSender(ctx, 1, 1, "@brand.test")
	if err != nil || created || again.UUID != ts.UUID {
		t.Fatalf("add not idempotent: %+v %t %v", again, created, err)
	}
	check(pass, "allowed", true)
	check(fail, "blocked", true)
	check(spam, "blocked", true)

	if err = s.DeleteTrustedSender(ctx, 2, ts.UUID); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatal("foreign delete", err)
	}
	if err = s.DeleteTrustedSender(ctx, 1, ts.UUID); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTrustedSenders(ctx, 1)
	if err != nil || len(list) != 0 {
		t.Fatal("not deleted", err)
	}

	settings := NewSettingsService(db, nil)
	bad := "sometimes"
	if _, err = settings.UpdateSettings(ctx, 1, &UpdateSettingsRequest{RemoteImages: &bad}); !errors.Is(err, ErrInvalidSettings) {
		t.Fatal("invalid remoteImages accepted", err)
	}
	always := "always"
	got, err := settings.UpdateSettings(ctx, 1, &UpdateSettingsRequest{RemoteImages: &always})
	if err != nil || got.RemoteImages != "always" {
		t.Fatalf("settings: %+v %v", got, err)
	}
	check(fail, "allowed", false)

	// The per-user cap is enforced.
	if _, err = db.Exec(`INSERT INTO mailbox_trusted_senders(user_id,org_id,sender) SELECT 1,1,'s'||g||'@cap.test' FROM generate_series(1,500) g`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AddTrustedSender(ctx, 1, 1, "late@cap.test"); !errors.Is(err, ErrMailboxConflict) {
		t.Fatal("cap not enforced", err)
	}
}
