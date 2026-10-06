package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Users: 1 owner, 2 member (creator), 3 member, 4 admin, 9 member of org 2.
// Identities: 1 news@one.test (user 2), 2 b@one.test (user 3), 3 nosend@one.test
// (user 2, can_send=false), 4 x@unverified.test (user 2, domain not SES-verified),
// 5 x@inactive.test (user 2, domain inactive), 6 admin@one.test (user 4).
var (
	campaignCreator = CampaignActor{UserID: 2}
	campaignMember  = CampaignActor{UserID: 3}
	campaignAdmin   = CampaignActor{UserID: 4}
)

func newCampaignFixture(t *testing.T) (*sql.DB, *CampaignService, *mailboxTestProvider) {
	t.Helper()
	db := testutil.Database(t)
	mustExec(t, db, `INSERT INTO organizations(id,name,slug,postal_address,updated_at) VALUES(1,'QA','qa','1 Main St',now()),(2,'Other','other','2 Side St',now());
		INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES
			(1,1,'owner@one.test','x','Owner','owner',now()),(2,1,'creator@one.test','x','Cora','member',now()),
			(3,1,'member@one.test','x','Mem','member',now()),(4,1,'admin@one.test','x','Ada','admin',now()),
			(9,2,'other@two.test','x','Other','owner',now());
		INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,sending_feedback_ready,dmarc_verified,updated_at) VALUES
			(1,1,'one.test','t','active',true,true,true,now()),(2,1,'unverified.test','t','active',false,true,false,now()),
			(3,1,'inactive.test','t','pending',true,true,false,now()),(4,2,'two.test','t','active',true,true,true,now());
		INSERT INTO identities(id,user_id,domain_id,email,can_send,updated_at) VALUES
			(1,2,1,'news@one.test',true,now()),(2,3,1,'b@one.test',true,now()),(3,2,1,'nosend@one.test',false,now()),
			(4,2,2,'x@unverified.test',true,now()),(5,2,3,'x@inactive.test',true,now()),(6,4,1,'admin@one.test',true,now()),
			(7,9,4,'o@two.test',true,now());
		INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'Readers',now()),(2,1,'Empty',now()),(3,2,'Foreign',now());
		INSERT INTO lists(id,org_id,name,type,segment_rules,updated_at) VALUES(4,1,'Broken','dynamic','{"match":"all","conditions":[{"field":"nope","op":"eq","value":"x"}]}',now());`)
	addContact(t, db, 1, 1, "a@example.net", "active", 1)
	addContact(t, db, 2, 1, "b@example.net", "active", 1)
	fake := &mailboxTestProvider{}
	cfg := &config.Config{EmailProvider: "ses", DisableAppLimits: true, JWTSecret: "campaign-test-secret-0123456789abcdef", APIUrl: "https://api.test", WebUrl: "https://app.test"}
	return db, NewCampaignService(db, cfg, fake), fake
}

func createTestCampaign(t *testing.T, svc *CampaignService, actor CampaignActor, mutate func(*model.CreateCampaignRequest)) *model.Campaign {
	t.Helper()
	req := &model.CreateCampaignRequest{Name: "Launch", Subject: "Hi {{firstName}}", HTMLContent: "<p>Hello {{first_name}}</p>", FromName: "News", FromEmail: "news@one.test", ListID: 1}
	if mutate != nil {
		mutate(req)
	}
	c, err := svc.CreateCampaign(context.Background(), 1, actor, req)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wantValidation(t *testing.T, err error, contains string) {
	t.Helper()
	var v *provider.MailValidationError
	if !errors.As(err, &v) || !strings.Contains(v.Error(), contains) {
		t.Fatalf("got %v, want validation error containing %q", err, contains)
	}
}

func wantErrIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got %v, want %v", err, target)
	}
}

func TestCampaignCreateValidatesSender(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	base := model.CreateCampaignRequest{Name: "Launch", Subject: "Hi", HTMLContent: "<p>x</p>", FromName: "News", FromEmail: "news@one.test", ListID: 1}
	for name, tc := range map[string]struct {
		mutate func(*model.CreateCampaignRequest)
		want   string
	}{
		"other users identity": {func(r *model.CreateCampaignRequest) { r.FromEmail = "b@one.test" }, "sending identities"},
		"cannot send":          {func(r *model.CreateCampaignRequest) { r.FromEmail = "nosend@one.test" }, "sending identities"},
		"unverified domain":    {func(r *model.CreateCampaignRequest) { r.FromEmail = "x@unverified.test" }, "sending identities"},
		"inactive domain":      {func(r *model.CreateCampaignRequest) { r.FromEmail = "x@inactive.test" }, "sending identities"},
		"missing identity":     {func(r *model.CreateCampaignRequest) { r.FromEmail = "gone@one.test" }, "sending identities"},
		"display name":         {func(r *model.CreateCampaignRequest) { r.FromEmail = "News <news@one.test>" }, "plain email"},
		"foreign list":         {func(r *model.CreateCampaignRequest) { r.ListID = 3 }, "list not found"},
		"empty body":           {func(r *model.CreateCampaignRequest) { r.HTMLContent = "" }, "content is required"},
		"subject header":       {func(r *model.CreateCampaignRequest) { r.Subject = "Hi\r\nBcc: x@y.test" }, "single line"},
		"long subject":         {func(r *model.CreateCampaignRequest) { r.Subject = strings.Repeat("s", 501) }, "single line"},
		"oversize":             {func(r *model.CreateCampaignRequest) { r.TextContent = strings.Repeat("t", maxComposeBodyBytes) }, "maximum size"},
	} {
		req := base
		tc.mutate(&req)
		_, err := svc.CreateCampaign(ctx, 1, campaignCreator, &req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	// Feedback readiness is not required to save a draft.
	mustExec(t, db, `UPDATE domains SET sending_feedback_ready=false WHERE id=1`)
	off := false
	c := createTestCampaign(t, svc, campaignCreator, func(r *model.CreateCampaignRequest) {
		r.FromEmail = "NEWS@one.test"
		r.TrackClicks = &off
		r.TextContent = "Hi {{ coupon }}"
	})
	if c.Status != "draft" || !c.TrackOpens || c.TrackClicks || c.CreatedByUserID == nil || *c.CreatedByUserID != 2 || c.ListType != "static" {
		t.Fatalf("created %+v", c)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "{{coupon}}") {
		t.Fatalf("warnings %v", c.Warnings)
	}
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE identity_id=1 AND created_by_user_id=2`)
	mustExec(t, db, `UPDATE contacts SET attributes='{"coupon":"X1"}' WHERE id=1`)
	if c = createTestCampaign(t, svc, campaignCreator, func(r *model.CreateCampaignRequest) { r.TextContent = "{{coupon}}" }); len(c.Warnings) != 0 {
		t.Fatalf("known attribute warned: %v", c.Warnings)
	}
}

func TestCampaignSendLifecycleAndOutbox(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	c := createTestCampaign(t, svc, campaignCreator, nil)

	mustExec(t, db, `UPDATE domains SET sending_feedback_ready=false WHERE id=1`)
	_, err := svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID)
	wantValidation(t, err, "bounce/complaint feedback")
	mustExec(t, db, `UPDATE domains SET sending_feedback_ready=true WHERE id=1; UPDATE organizations SET postal_address=NULL WHERE id=1`)
	_, err = svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID)
	wantValidation(t, err, "postal address")
	mustExec(t, db, `UPDATE organizations SET postal_address='1 Main St' WHERE id=1`)
	svc.cfg.EmailProvider = "smtp"
	_, err = svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID)
	wantValidation(t, err, "EMAIL_PROVIDER=ses")
	svc.cfg.EmailProvider = "ses"

	sent, err := svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Status != "sending" || sent.StartedAt == nil || sent.PreparedAt != nil {
		t.Fatalf("send: %+v", sent)
	}
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE org_id=1 AND event_type='campaign.started' AND user_id=2 AND payload->'data'->>'campaignUuid'=$1 AND payload->'data'->>'identityId'='1'`, c.UUID)
	_, err = svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID)
	wantErrIs(t, err, ErrCampaignState)
	_, err = svc.ResumeCampaign(ctx, 1, campaignCreator, c.UUID)
	wantErrIs(t, err, ErrCampaignState)

	paused, err := svc.PauseCampaign(ctx, 1, campaignCreator, c.UUID)
	if err != nil || paused.Status != "paused" || paused.StatusReason == nil || *paused.StatusReason != "user_paused" {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	_, err = svc.PauseCampaign(ctx, 1, campaignCreator, c.UUID)
	wantErrIs(t, err, ErrCampaignState)
	if r, err := svc.ResumeCampaign(ctx, 1, campaignCreator, c.UUID); err != nil || r.Status != "sending" || r.StatusReason != nil {
		t.Fatalf("resume: %+v %v", r, err)
	}
	if _, err = svc.PauseCampaign(ctx, 1, campaignCreator, c.UUID); err != nil {
		t.Fatal(err)
	}
	count(t, db, 2, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.paused' AND payload->'data'->>'statusReason'='user_paused'`)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.started'`)

	// Resume revalidates the stored sender.
	mustExec(t, db, `UPDATE identities SET can_send=false WHERE id=1`)
	_, err = svc.ResumeCampaign(ctx, 1, campaignCreator, c.UUID)
	wantValidation(t, err, "sending identities")
	mustExec(t, db, `UPDATE identities SET can_send=true WHERE id=1`)

	// Prepared rows: content is locked and the list cannot change; cancel refunds reserved quota.
	mustExec(t, db, `UPDATE campaigns SET prepared_at=now(), total_recipients=3 WHERE id=$1`, c.ID)
	mustExec(t, db, `INSERT INTO campaign_recipients(campaign_id,org_id,contact_id,email,status,quota_reserved) VALUES
			($1,1,1,'a@example.net','pending',true),($1,1,2,'b@example.net','claimed',true),($1,1,NULL,'c@example.net','sent',true)`, c.ID)
	mustExec(t, db, `INSERT INTO organization_send_usage(org_id,month,attempts) VALUES(1,`+monthlyUsageMonth+`,5)`)
	svc.cfg.DisableAppLimits = false
	subject := "changed"
	_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{Subject: &subject})
	wantErrIs(t, err, ErrCampaignState)
	name := "Renamed"
	if u, err := svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{Name: &name, Subject: &c.Subject}); err != nil || u.Name != "Renamed" {
		t.Fatalf("rename paused prepared: %+v %v", u, err)
	}
	cancelled, err := svc.CancelCampaign(ctx, 1, campaignCreator, c.UUID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='cancelled' AND lease_owner IS NULL`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE status='sent'`)
	count(t, db, 3, `SELECT attempts FROM organization_send_usage WHERE org_id=1`)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.cancelled' AND dedupe_key=$1`, "campaign.cancelled:"+c.UUID)
	_, err = svc.CancelCampaign(ctx, 1, campaignCreator, c.UUID)
	wantErrIs(t, err, ErrCampaignState)
	_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{Name: &name})
	wantErrIs(t, err, ErrCampaignState)
	wantErrIs(t, svc.DeleteCampaign(ctx, 1, campaignCreator, c.UUID), ErrCampaignState)

	// A cancelled campaign that never prepared can be deleted, as can a draft.
	d := createTestCampaign(t, svc, campaignCreator, nil)
	if _, err = svc.CancelCampaign(ctx, 1, campaignCreator, d.UUID); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteCampaign(ctx, 1, campaignCreator, d.UUID); err != nil {
		t.Fatal(err)
	}
	wantErrIs(t, svc.DeleteCampaign(ctx, 1, campaignCreator, d.UUID), ErrCampaignNotFound)
	_, err = svc.GetCampaign(ctx, 1, "not-a-uuid")
	wantErrIs(t, err, ErrCampaignNotFound)
	_, err = svc.GetCampaign(ctx, 2, c.UUID)
	wantErrIs(t, err, ErrCampaignNotFound)
}

func TestCampaignSendRequiresAudience(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	empty := createTestCampaign(t, svc, campaignCreator, func(r *model.CreateCampaignRequest) { r.ListID = 2 })
	_, err := svc.SendCampaignNow(ctx, 1, campaignCreator, empty.UUID)
	wantValidation(t, err, "no eligible recipients")
	broken := createTestCampaign(t, svc, campaignCreator, func(r *model.CreateCampaignRequest) { r.ListID = 4 })
	_, err = svc.SendCampaignNow(ctx, 1, campaignCreator, broken.UUID)
	if !IsSegmentError(err) {
		t.Fatalf("invalid segment: %v", err)
	}
	// Scheduling does not require a non-empty audience.
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	s, err := svc.ScheduleCampaign(ctx, 1, campaignCreator, empty.UUID, &model.ScheduleCampaignRequest{ScheduledAt: at})
	if err != nil || s.Status != "scheduled" || s.ScheduledAt == nil {
		t.Fatalf("schedule: %+v %v", s, err)
	}
	count(t, db, 0, `SELECT count(*) FROM webhook_events`)
}

func TestCampaignScheduleWindow(t *testing.T) {
	_, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	c := createTestCampaign(t, svc, campaignCreator, nil)
	for _, at := range []string{"tomorrow", time.Now().Add(30 * time.Second).Format(time.RFC3339), time.Now().Add(366 * 24 * time.Hour).Format(time.RFC3339)} {
		_, err := svc.ScheduleCampaign(ctx, 1, campaignCreator, c.UUID, &model.ScheduleCampaignRequest{ScheduledAt: at})
		wantValidation(t, err, "scheduledAt")
	}
	first := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if _, err := svc.ScheduleCampaign(ctx, 1, campaignCreator, c.UUID, &model.ScheduleCampaignRequest{ScheduledAt: first.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Hour).In(time.FixedZone("UTC+3", 3*3600))
	s, err := svc.ScheduleCampaign(ctx, 1, campaignCreator, c.UUID, &model.ScheduleCampaignRequest{ScheduledAt: second.Format(time.RFC3339)})
	if err != nil || !s.ScheduledAt.Equal(second) {
		t.Fatalf("reschedule: %+v %v", s, err)
	}
	// A scheduled campaign can still be sent now; afterwards it cannot be scheduled.
	if _, err = svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ScheduleCampaign(ctx, 1, campaignCreator, c.UUID, &model.ScheduleCampaignRequest{ScheduledAt: first.Format(time.RFC3339)})
	wantErrIs(t, err, ErrCampaignState)
}

func TestCampaignSendStatePermissions(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	c := createTestCampaign(t, svc, campaignCreator, nil)
	name := "x"

	_, err := svc.SendCampaignNow(ctx, 1, campaignMember, c.UUID)
	wantErrIs(t, err, ErrCampaignForbidden)
	_, err = svc.UpdateCampaign(ctx, 1, campaignMember, c.UUID, &model.UpdateCampaignRequest{Name: &name})
	wantErrIs(t, err, ErrCampaignForbidden)
	wantErrIs(t, svc.DeleteCampaign(ctx, 1, campaignMember, c.UUID), ErrCampaignForbidden)
	_, err = svc.CancelCampaign(ctx, 1, campaignMember, c.UUID)
	wantErrIs(t, err, ErrCampaignForbidden)
	_, err = svc.SendTestEmail(ctx, 1, campaignMember, c.UUID, []string{"t@example.net"}, "test-key-0001")
	wantErrIs(t, err, ErrCampaignForbidden)
	// An admin's API key is bound to the admin user but cannot act on another user's campaign.
	_, err = svc.SendCampaignNow(ctx, 1, CampaignActor{UserID: 4, APIKey: true}, c.UUID)
	wantErrIs(t, err, ErrCampaignForbidden)
	_, err = svc.PauseCampaign(ctx, 1, CampaignActor{UserID: 1, APIKey: true}, c.UUID)
	wantErrIs(t, err, ErrCampaignForbidden)
	// A demoted or deactivated admin loses the override immediately.
	mustExec(t, db, `UPDATE users SET status='suspended' WHERE id=4`)
	_, err = svc.SendCampaignNow(ctx, 1, campaignAdmin, c.UUID)
	wantErrIs(t, err, ErrCampaignForbidden)
	mustExec(t, db, `UPDATE users SET status='active' WHERE id=4`)

	// A session admin can send another member's campaign; it still sends from the creator's identity.
	s, err := svc.SendCampaignNow(ctx, 1, campaignAdmin, c.UUID)
	if err != nil || s.Status != "sending" || *s.CreatedByUserID != 2 {
		t.Fatalf("admin send: %+v %v", s, err)
	}
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND identity_id=1`, c.ID)
	if _, err = svc.PauseCampaign(ctx, 1, CampaignActor{UserID: 2, APIKey: true}, c.UUID); err != nil {
		t.Fatal("creator API key pause:", err)
	}
	if _, err = svc.ResumeCampaign(ctx, 1, CampaignActor{UserID: 1}, c.UUID); err != nil {
		t.Fatal("owner resume:", err)
	}
	// Another org's owner cannot see the campaign at all.
	_, err = svc.PauseCampaign(ctx, 2, CampaignActor{UserID: 9}, c.UUID)
	wantErrIs(t, err, ErrCampaignNotFound)
}

func TestCampaignUpdateRules(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	c := createTestCampaign(t, svc, campaignCreator, func(r *model.CreateCampaignRequest) {
		r.ReplyTo = "reply@one.test"
		r.TextContent = "plain"
	})
	empty, list, off := "", 2, false
	u, err := svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{ReplyTo: &empty, TextContent: &empty, ListID: &list, TrackOpens: &off})
	if err != nil || u.ReplyTo != "" || u.TextContent != "" || u.ListID != 2 || u.TrackOpens || u.HTMLContent != c.HTMLContent {
		t.Fatalf("draft update: %+v %v", u, err)
	}
	html := ""
	_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{HTMLContent: &html})
	wantValidation(t, err, "content is required")
	bad := 3
	_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{ListID: &bad})
	wantValidation(t, err, "list not found")
	reply := "x\r\nBcc: y@z.test"
	_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{ReplyTo: &reply})
	wantValidation(t, err, "line breaks")

	// An admin changing From resolves against the creator's identities, not their own.
	from := "admin@one.test"
	_, err = svc.UpdateCampaign(ctx, 1, campaignAdmin, c.UUID, &model.UpdateCampaignRequest{FromEmail: &from})
	wantValidation(t, err, "sending identities")
	mustExec(t, db, `INSERT INTO identities(id,user_id,domain_id,email,can_send,updated_at) VALUES(8,2,1,'promo@one.test',true,now())`)
	from = "promo@one.test"
	if u, err = svc.UpdateCampaign(ctx, 1, campaignAdmin, c.UUID, &model.UpdateCampaignRequest{FromEmail: &from}); err != nil || u.FromEmail != from {
		t.Fatalf("admin from change: %+v %v", u, err)
	}
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND identity_id=8 AND created_by_user_id=2`, c.ID)

	// Paused before preparing: full edits except the list.
	list = 1
	mustExec(t, db, `UPDATE campaigns SET status='paused', status_reason='sender_unavailable', list_id=1 WHERE id=$1`, c.ID)
	list = 2
	_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{ListID: &list})
	wantErrIs(t, err, ErrCampaignState)
	subject := "New subject"
	if u, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{Subject: &subject}); err != nil || u.Subject != subject {
		t.Fatalf("paused unprepared edit: %+v %v", u, err)
	}
	for _, status := range []string{"sending", "sent"} {
		mustExec(t, db, `UPDATE campaigns SET status=$2 WHERE id=$1`, c.ID, status)
		name := "x"
		_, err = svc.UpdateCampaign(ctx, 1, campaignCreator, c.UUID, &model.UpdateCampaignRequest{Name: &name})
		wantErrIs(t, err, ErrCampaignState)
	}

	// Legacy campaigns without a creator adopt the editor when From is re-resolved.
	mustExec(t, db, `INSERT INTO campaigns(org_id,name,subject,html_content,from_name,from_email,list_id,status,status_reason,updated_at)
		VALUES(1,'Legacy','S','<p>x</p>','F','someone@else.test',1,'draft','legacy_requires_review',now())`)
	var legacy string
	if err = db.QueryRow(`SELECT uuid FROM campaigns WHERE name='Legacy'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateCampaign(ctx, 1, campaignMember, legacy, &model.UpdateCampaignRequest{Name: &subject})
	wantErrIs(t, err, ErrCampaignForbidden)
	from = "admin@one.test"
	if _, err = svc.UpdateCampaign(ctx, 1, campaignAdmin, legacy, &model.UpdateCampaignRequest{FromEmail: &from}); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE uuid=$1 AND identity_id=6 AND created_by_user_id=4`, legacy)
}

func TestCampaignSettingsPostalAddress(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	for _, actor := range []CampaignActor{campaignMember, {UserID: 1, APIKey: true}} {
		_, err := svc.UpdateCampaignSettings(ctx, 1, actor, &model.CampaignSettings{PostalAddress: "x"})
		wantErrIs(t, err, ErrCampaignForbidden)
	}
	_, err := svc.UpdateCampaignSettings(ctx, 1, campaignAdmin, &model.CampaignSettings{PostalAddress: strings.Repeat("a", 501)})
	wantValidation(t, err, "500")
	_, err = svc.UpdateCampaignSettings(ctx, 1, campaignAdmin, &model.CampaignSettings{PostalAddress: "a\x00b"})
	wantValidation(t, err, "invalid characters")
	got, err := svc.UpdateCampaignSettings(ctx, 1, CampaignActor{UserID: 1}, &model.CampaignSettings{PostalAddress: "  QA Inc\r\n1 Main St\rSpringfield  "})
	if err != nil || got.PostalAddress != "QA Inc\n1 Main St\nSpringfield" {
		t.Fatalf("settings: %+v %v", got, err)
	}
	if read, err := svc.GetCampaignSettings(ctx, 1); err != nil || read.PostalAddress != got.PostalAddress {
		t.Fatalf("read: %+v %v", read, err)
	}
	count(t, db, 1, `SELECT count(*) FROM organizations WHERE id=2 AND postal_address='2 Side St'`)
	if _, err = svc.UpdateCampaignSettings(ctx, 1, campaignAdmin, &model.CampaignSettings{PostalAddress: " "}); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM organizations WHERE id=1 AND postal_address IS NULL`)
}

func TestCampaignReadModels(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	mustExec(t, db, `UPDATE contacts SET first_name='<b>Ann</b>', attributes='{"plan":"pro"}' WHERE id=1`)
	addContact(t, db, 3, 1, "gone@example.net", "unsubscribed", 1)
	c := createTestCampaign(t, svc, campaignCreator, func(r *model.CreateCampaignRequest) { r.TextContent = "Plan {{plan}} {{missing}}" })

	est, err := svc.EstimateAudience(ctx, 1, c.UUID)
	if err != nil || est.Eligible != 2 || est.ExcludedInactive != 1 || len(est.Warnings) != 0 {
		t.Fatalf("estimate: %+v %v", est, err)
	}
	mustExec(t, db, `UPDATE domains SET dmarc_verified=false, sending_feedback_ready=false WHERE id=1; UPDATE organizations SET postal_address='' WHERE id=1`)
	if est, err = svc.EstimateAudience(ctx, 1, c.UUID); err != nil || strings.Join(est.Warnings, ",") != "dmarc_missing,no_postal_address,feedback_not_ready" {
		t.Fatalf("warnings: %+v %v", est, err)
	}

	var contactUUID string
	if err = db.QueryRow(`SELECT uuid FROM contacts WHERE id=1`).Scan(&contactUUID); err != nil {
		t.Fatal(err)
	}
	p, err := svc.PreviewCampaign(ctx, 1, c.UUID, contactUUID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "Hi <b>Ann</b>" || !strings.Contains(p.HTML, "&lt;b&gt;Ann&lt;/b&gt;") || !strings.Contains(p.Text, "Plan pro ") ||
		strings.Contains(p.HTML, "/tracking/") || strings.Join(p.UnknownVariables, ",") != "missing" {
		t.Fatalf("preview: %+v", p)
	}
	if p, err = svc.PreviewCampaign(ctx, 1, c.UUID, ""); err != nil || len(p.UnknownVariables) != 2 {
		t.Fatalf("sample preview: %+v %v", p, err)
	}
	_, err = svc.PreviewCampaign(ctx, 2, c.UUID, contactUUID)
	wantErrIs(t, err, ErrCampaignNotFound)
	other := createTestCampaign(t, svc, campaignCreator, nil)
	mustExec(t, db, `INSERT INTO contacts(id,org_id,email,status,updated_at) VALUES(50,2,'x@two.test','active',now())`)
	var foreign string
	if err = db.QueryRow(`SELECT uuid FROM contacts WHERE id=50`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	_, err = svc.PreviewCampaign(ctx, 1, other.UUID, foreign)
	wantValidation(t, err, "contact not found")

	mustExec(t, db, `UPDATE campaigns SET status='sending', started_at=now()-interval '2 hours', prepared_at=now(), sent_count=4, open_count=2, click_count=1, delivered_count=3 WHERE id=$1`, c.ID)
	mustExec(t, db, `INSERT INTO campaign_recipients(id,campaign_id,org_id,contact_id,email,status) VALUES
			(1,$1,1,1,'a@example.net','sent'),(2,$1,1,2,'b@example.net','pending'),(3,$1,1,NULL,'c@example.net','claimed'),
			(4,$1,1,NULL,'d@example.net','skipped'),(5,$1,1,NULL,'e@example.net','unknown')`, c.ID)
	mustExec(t, db, `INSERT INTO campaign_events(campaign_id,recipient_id,event_type,url,occurred_at) VALUES
			($1,1,'open',NULL,now()-interval '90 minutes'),($1,1,'open',NULL,now()-interval '80 minutes'),($1,2,'open',NULL,now()),
			($1,1,'click','https://a.test',now()),($1,1,'click','https://a.test',now()),($1,2,'click','https://b.test',now())`, c.ID)
	pr, err := svc.GetProgress(ctx, 1, c.UUID)
	if err != nil || pr.Total != 5 || pr.Pending != 1 || pr.InFlight != 1 || pr.Sent != 1 || pr.Skipped != 1 || pr.Unknown != 1 || pr.Percent != 60 || pr.Preparing {
		t.Fatalf("progress: %+v %v", pr, err)
	}
	st, err := svc.GetCampaignStats(ctx, 1, c.UUID)
	if err != nil || st.OpenRate != 50 || st.ClickToOpenRate != 50 || st.DeliveredRate != 75 {
		t.Fatalf("stats: %+v %v", st, err)
	}
	if len(st.ClicksByLink) != 2 || st.ClicksByLink[0] != (model.LinkClicks{URL: "https://a.test", Clicks: 2, UniqueClicks: 1}) {
		t.Fatalf("clicks: %+v", st.ClicksByLink)
	}
	opens := 0
	for _, h := range st.OpensByHour {
		opens += h.Opens
	}
	if opens != 3 || len(st.OpensByHour) < 2 {
		t.Fatalf("opens by hour: %+v", st.OpensByHour)
	}
	rs, err := svc.ListRecipients(ctx, 1, c.UUID, "skipped", 1, 500)
	if err != nil || rs.Total != 1 || len(rs.Recipients) != 1 || rs.Recipients[0].Email != "d@example.net" {
		t.Fatalf("recipients: %+v %v", rs, err)
	}
	if rs, err = svc.ListRecipients(ctx, 1, c.UUID, "", 2, 2); err != nil || rs.Total != 5 || len(rs.Recipients) != 2 || rs.Recipients[0].Email != "c@example.net" {
		t.Fatalf("recipients page: %+v %v", rs, err)
	}
	if list, err := svc.ListCampaigns(ctx, 1, 1, 20, "sending"); err != nil || list.Total != 1 || list.Campaigns[0].UUID != c.UUID {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestCampaignTestSend(t *testing.T) {
	db, svc, fake := newCampaignFixture(t)
	ctx := context.Background()
	c := createTestCampaign(t, svc, campaignCreator, nil)
	mustExec(t, db, `UPDATE domains SET sending_feedback_ready=false WHERE id=1`) // tests may run before feedback setup

	_, err := svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"t@example.net"}, "short")
	wantValidation(t, err, "Idempotency-Key")
	for _, emails := range [][]string{nil, {"Name <t@example.net>"}, {"a@x.test", "b@x.test", "c@x.test", "d@x.test", "e@x.test", "f@x.test"}} {
		_, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, emails, "test-key-0001")
		wantValidation(t, err, "")
	}
	mustExec(t, db, `INSERT INTO suppression_list(org_id,email,reason,source) VALUES(1,'BLOCKED@example.net','bounce','ses')`)
	_, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"ok@example.net", "blocked@EXAMPLE.net"}, "test-key-0001")
	wantValidation(t, err, "suppressed")

	res, err := svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"A@example.net", "t@example.net"}, "test-key-0001")
	if err != nil || res.Status != "sent" || len(res.Results) != 2 || fake.calls != 2 {
		t.Fatalf("test send: %+v %v calls=%d", res, err, fake.calls)
	}
	if !strings.HasPrefix(fake.last.Subject, "[Test] Hi ") || strings.Contains(fake.last.HTMLBody, "/tracking/") || fake.last.Headers["List-Unsubscribe"] == "" {
		t.Fatalf("test message: %+v", fake.last)
	}
	// Same key and addresses (any order or case): stored outcome, no second send.
	again, err := svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"T@example.net", "a@example.net"}, "test-key-0001")
	if err != nil || again.Status != "sent" || len(again.Results) != 2 || fake.calls != 2 {
		t.Fatalf("replay: %+v %v calls=%d", again, err, fake.calls)
	}
	_, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"other@example.net"}, "test-key-0001")
	wantErrIs(t, err, ErrCampaignTestConflict)

	// A provider rejection is reported per address.
	fake.err = &smithy.GenericAPIError{Code: "MessageRejected", Message: "Email address is not verified.", Fault: smithy.FaultClient}
	res, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"sandbox@example.net"}, "test-key-0002")
	if err != nil || res.Status != "failed" || !strings.Contains(res.Results[0].Error, "not verified") {
		t.Fatalf("rejected: %+v %v", res, err)
	}
	fake.err = nil
	count(t, db, 1, `SELECT count(*) FROM campaign_test_sends WHERE idempotency_key='test-key-0002' AND status='failed' AND results->0->>'status'='failed'`)

	// A stuck row is reported as unknown after 2 minutes.
	mustExec(t, db, `UPDATE campaign_test_sends SET status='sending', created_at=now()-interval '3 minutes' WHERE idempotency_key='test-key-0002'`)
	if res, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"sandbox@example.net"}, "test-key-0002"); err != nil || res.Status != "unknown" {
		t.Fatalf("stale: %+v %v", res, err)
	}

	// Monthly quota must cover every address.
	svc.cfg.DisableAppLimits = false
	mustExec(t, db, `UPDATE organizations SET monthly_email_limit=1 WHERE id=1`)
	_, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"q1@example.net", "q2@example.net"}, "test-key-0003")
	wantValidation(t, err, "monthly quota")
	svc.cfg.DisableAppLimits = true

	// At most 10 test sends per campaign per hour.
	mustExec(t, db, `INSERT INTO campaign_test_sends(campaign_id,user_id,idempotency_key,request_hash,recipients,status)
		SELECT $1,2,'filler-'||g,'h',ARRAY['f@example.net'],'sent' FROM generate_series(1,8) g`, c.ID)
	_, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"late@example.net"}, "test-key-0004")
	wantErrIs(t, err, ErrCampaignTestRateLimited)
	if _, err = svc.SendTestEmail(ctx, 1, campaignCreator, c.UUID, []string{"T@example.net", "a@example.net"}, "test-key-0001"); err != nil {
		t.Fatal("replay must not be rate limited:", err)
	}
	if fake.calls != 3 {
		t.Fatalf("provider calls=%d", fake.calls)
	}
}

// Outbox emits must not fail the transaction when the identity was deleted
// or moved to another user.
func TestCampaignEventWithoutOwnedIdentity(t *testing.T) {
	db, svc, _ := newCampaignFixture(t)
	ctx := context.Background()
	c := createTestCampaign(t, svc, campaignCreator, nil)
	if _, err := svc.SendCampaignNow(ctx, 1, campaignCreator, c.UUID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE identities SET user_id=3 WHERE id=1`)
	if _, err := svc.PauseCampaign(ctx, 1, campaignCreator, c.UUID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `DELETE FROM identities WHERE id=1`)
	if _, err := svc.CancelCampaign(ctx, 1, campaignCreator, c.UUID); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.paused' AND NOT (payload->'data' ? 'identityId')`)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.cancelled' AND user_id=2`)
}
