package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// senderTestProvider records every SES call and returns scripted outcomes.
type senderTestProvider struct {
	provider.EmailProvider
	mu       sync.Mutex
	calls    []string // recipient per call, in order
	raw      [][]byte
	msgs     []*provider.EmailMessage
	script   func(call int, to string) error
	quota    *provider.SendQuota
	quotaErr error
}

func (p *senderTestProvider) SendEmail(_ context.Context, m *provider.EmailMessage) (*provider.SendResult, error) {
	raw, _, err := provider.BuildMailMIME(m)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.calls = append(p.calls, m.To[0])
	p.raw = append(p.raw, raw)
	p.msgs = append(p.msgs, m)
	n, script := len(p.calls), p.script
	p.mu.Unlock()
	if script != nil {
		if err := script(n, m.To[0]); err != nil {
			return &provider.SendResult{ProviderName: "ses", Error: err}, err
		}
	}
	return &provider.SendResult{MessageID: fmt.Sprintf("ses-%d", n), ProviderName: "ses", Success: true}, nil
}

func (p *senderTestProvider) GetSendQuota(context.Context) (*provider.SendQuota, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.quotaErr != nil {
		return nil, p.quotaErr
	}
	q := *p.quota
	return &q, nil
}

func (p *senderTestProvider) Name() string { return "ses" }
func (p *senderTestProvider) Close() error { return nil }

func (p *senderTestProvider) callCount(to string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if to == "" || c == to {
			n++
		}
	}
	return n
}

// senderFixture is the campaign fixture (org 1, creator 2, list 1 with
// a@ and b@example.net) plus extra list members and a runner whose clock
// the test controls.
func senderFixture(t *testing.T, extra ...string) (*sql.DB, *CampaignService, *campaignRunner, *senderTestProvider) {
	t.Helper()
	db, svc, _ := newCampaignFixture(t)
	for i, email := range extra {
		addContact(t, db, 10+i, 1, email, "active", 1)
	}
	svc.cfg.CampaignSendConcurrency = 1
	fake := &senderTestProvider{quota: &provider.SendQuota{Max24HourSend: -1, MaxSendRate: 100}}
	r := newCampaignRunner(db, svc.cfg, fake)
	now := time.Now()
	r.now = func() time.Time { return now }
	return db, svc, r, fake
}

func startCampaign(t *testing.T, svc *CampaignService) int64 {
	t.Helper()
	c := createTestCampaign(t, svc, campaignCreator, nil)
	if _, err := svc.SendCampaignNow(context.Background(), 1, campaignCreator, c.UUID); err != nil {
		t.Fatal(err)
	}
	return int64(c.ID)
}

// drain runs ticks until the runner reports no work.
func drain(t *testing.T, r *campaignRunner) {
	t.Helper()
	for i := 0; i < 30; i++ {
		worked, err := r.runOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("runner never went idle")
}

func runTick(t *testing.T, r *campaignRunner) {
	t.Helper()
	if _, err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func wantCampaign(t *testing.T, db *sql.DB, id int64, status, reason string) {
	t.Helper()
	var gotStatus string
	var gotReason sql.NullString
	if err := db.QueryRow(`SELECT status, status_reason FROM campaigns WHERE id=$1`, id).Scan(&gotStatus, &gotReason); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotReason.String != reason {
		t.Fatalf("campaign %d: status=%s reason=%q, want %s %q", id, gotStatus, gotReason.String, status, reason)
	}
}

func apiErr(code string) error {
	msg := code
	switch code {
	case "MessageRejected":
		return &types.MessageRejected{Message: &msg}
	case "TooManyRequestsException":
		return &types.TooManyRequestsException{Message: &msg}
	case "SendingPausedException":
		return &types.SendingPausedException{Message: &msg}
	case "MailFromDomainNotVerifiedException":
		return &types.MailFromDomainNotVerifiedException{Message: &msg}
	}
	panic(code)
}

func TestCampaignSenderSendsWithHeadersAndCompletes(t *testing.T) {
	db, svc, r, fake := senderFixture(t)
	svc.cfg.DisableAppLimits = false
	id := startCampaign(t, svc)
	drain(t, r)

	wantCampaign(t, db, id, "sent", "")
	count(t, db, 2, `SELECT total_recipients FROM campaigns WHERE id=$1 AND sent_count=2 AND failed_count=0 AND unknown_count=0 AND completed_at IS NOT NULL AND prepared_at IS NOT NULL`, id)
	count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='sent' AND provider_message_id LIKE 'ses-%' AND sent_at IS NOT NULL AND lease_owner IS NULL AND quota_reserved`)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.sent'`)
	if fake.callCount("a@example.net") != 1 || fake.callCount("b@example.net") != 1 {
		t.Fatalf("calls %v", fake.calls)
	}
	for i, m := range fake.msgs {
		var messageUUID string
		if err := db.QueryRow(`SELECT message_uuid::text FROM campaign_recipients WHERE email=$1`, m.To[0]).Scan(&messageUUID); err != nil {
			t.Fatal(err)
		}
		raw := string(fake.raw[i])
		for _, want := range []string{"List-Unsubscribe: <https://api.test/api/v1/unsubscribe/", "List-Unsubscribe-Post: List-Unsubscribe=One-Click",
			"X-Mailat-Message-ID: " + messageUUID} {
			if !strings.Contains(raw, want) {
				t.Fatalf("message %d lacks %q:\n%s", i, want, raw)
			}
		}
		if !strings.Contains(m.HTMLBody, "1 Main St") || !strings.Contains(m.HTMLBody, "/api/v1/tracking/open/") {
			t.Fatalf("message %d lacks footer or pixel: %s", i, m.HTMLBody)
		}
		if strings.Contains(raw, "mailto:") {
			t.Fatal("List-Unsubscribe must not offer mailto")
		}
	}

	// A further tick neither resends nor re-emits.
	drain(t, r)
	if fake.callCount("") != 2 {
		t.Fatalf("resent: %v", fake.calls)
	}
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.sent'`)
}

func TestCampaignSenderPromotesDueSchedules(t *testing.T) {
	db, svc, r, fake := senderFixture(t)
	ctx := context.Background()
	due := createTestCampaign(t, svc, campaignCreator, nil)
	later := createTestCampaign(t, svc, campaignCreator, nil)
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	for _, c := range []string{due.UUID, later.UUID} {
		if _, err := svc.ScheduleCampaign(ctx, 1, campaignCreator, c, &model.ScheduleCampaignRequest{ScheduledAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, db, `UPDATE campaigns SET scheduled_at=now()-interval '1 second' WHERE id=$1`, due.ID)
	drain(t, r)
	wantCampaign(t, db, int64(due.ID), "sent", "")
	wantCampaign(t, db, int64(later.ID), "scheduled", "")
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.started'`)
	if fake.callCount("") != 2 {
		t.Fatalf("calls %v", fake.calls)
	}
}

func TestCampaignSenderPrepareRevalidates(t *testing.T) {
	t.Run("no eligible recipients", func(t *testing.T) {
		db, svc, r, fake := senderFixture(t)
		id := startCampaign(t, svc)
		mustExec(t, db, `UPDATE contacts SET status='unsubscribed'`)
		drain(t, r)
		wantCampaign(t, db, id, "sent", "no_eligible_recipients")
		count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.sent'`)
		if fake.callCount("") != 0 {
			t.Fatal("sent to nobody expected")
		}
	})
	for name, tc := range map[string]struct{ break_, reason string }{
		"sender":   {`UPDATE identities SET can_send=false WHERE id=1`, "sender_unavailable"},
		"feedback": {`UPDATE domains SET sending_feedback_ready=false WHERE id=1`, "sender_unavailable"},
		"postal":   {`UPDATE organizations SET postal_address='' WHERE id=1`, "no_postal_address"},
		"segment":  {`UPDATE lists SET type='dynamic', segment_rules='{"match":"all","conditions":[{"field":"nope","op":"eq","value":1}]}' WHERE id=1`, "invalid_segment"},
	} {
		t.Run(name, func(t *testing.T) {
			db, svc, r, fake := senderFixture(t)
			id := startCampaign(t, svc)
			mustExec(t, db, tc.break_)
			drain(t, r)
			wantCampaign(t, db, id, "paused", tc.reason)
			count(t, db, 0, `SELECT count(*) FROM campaign_recipients`)
			count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.paused'`)
			if fake.callCount("") != 0 {
				t.Fatal("paused campaign sent")
			}
		})
	}
	t.Run("sender lost after prepare", func(t *testing.T) {
		db, svc, r, fake := senderFixture(t)
		id := startCampaign(t, svc)
		if err := r.prepareDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, `UPDATE domains SET ses_verified=false WHERE id=1`)
		drain(t, r)
		wantCampaign(t, db, id, "paused", "sender_unavailable")
		count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='pending'`)
		if fake.callCount("") != 0 {
			t.Fatal("sent with an invalid sender")
		}
	})
}

func TestCampaignSenderRechecksBeforeSending(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net")
	id := startCampaign(t, svc)
	if err := r.prepareDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE contacts SET status='unsubscribed' WHERE email='a@example.net';
		INSERT INTO suppression_list(org_id,email,reason,source) VALUES(1,'C@EXAMPLE.NET','bounce','ses')`)
	drain(t, r)
	wantCampaign(t, db, id, "sent", "")
	if fake.callCount("") != 1 || fake.callCount("b@example.net") != 1 {
		t.Fatalf("calls %v", fake.calls)
	}
	count(t, db, 2, `SELECT skipped_count FROM campaigns WHERE id=$1`, id)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='a@example.net' AND status='skipped' AND skip_reason='inactive' AND NOT quota_reserved`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='c@example.net' AND status='skipped' AND skip_reason='suppressed'`)
}

// A crash leaves an expired claim, a send that may have reached SES and a send
// SNS already confirmed. The next runner resends none of the latter two and
// does not charge the reclaimed row's quota twice.
func TestCampaignSenderRecoversFromCrash(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net", "d@example.net")
	svc.cfg.DisableAppLimits = false
	mustExec(t, db, `UPDATE organizations SET monthly_email_limit=100 WHERE id=1`)
	id := startCampaign(t, svc)
	if err := r.prepareDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	dead := uuid.NewString()
	mustExec(t, db, `UPDATE campaign_recipients SET status='claimed', lease_owner=$1, lease_expires_at=now()-interval '1 second', quota_reserved=true WHERE email='a@example.net'`, dead)
	mustExec(t, db, `UPDATE campaign_recipients SET status='sending', attempt_started_at=now()-interval '11 minutes', quota_reserved=true WHERE email='b@example.net';
		UPDATE campaign_recipients SET status='sending', attempt_started_at=now()-interval '11 minutes', quota_reserved=true, delivery_status='delivered' WHERE email='c@example.net';
		INSERT INTO organization_send_usage(org_id,month,attempts) VALUES(1,`+monthlyUsageMonth+`,3)`)

	drain(t, r)
	wantCampaign(t, db, id, "sent", "")
	if fake.callCount("b@example.net") != 0 || fake.callCount("c@example.net") != 0 {
		t.Fatalf("possibly delivered rows were resent: %v", fake.calls)
	}
	if fake.callCount("a@example.net") != 1 || fake.callCount("d@example.net") != 1 {
		t.Fatalf("calls %v", fake.calls)
	}
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='b@example.net' AND status='unknown'`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='c@example.net' AND status='sent' AND sent_at IS NOT NULL`)
	count(t, db, 3, `SELECT sent_count FROM campaigns WHERE id=$1 AND unknown_count=1`, id)
	// Only d@ was newly reserved.
	count(t, db, 4, `SELECT attempts FROM organization_send_usage WHERE org_id=1`)
}

func TestCampaignSenderRunnersClaimDisjointRows(t *testing.T) {
	db, svc, r1, fake := senderFixture(t, "c@example.net")
	id := startCampaign(t, svc)
	ctx := context.Background()
	if err := r1.prepareDue(ctx); err != nil {
		t.Fatal(err)
	}
	r2 := newCampaignRunner(db, svc.cfg, fake)
	c, err := r1.pickCampaign(ctx)
	if err != nil || c == nil || c.ID != id {
		t.Fatalf("pick %+v %v", c, err)
	}
	first, err := r1.claimBatch(ctx, c, 1)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := r2.claimBatch(ctx, c, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(rest) != 2 || first[0] == rest[0] || first[0] == rest[1] {
		t.Fatalf("claims overlap: %v %v", first, rest)
	}
	// The guarded start refuses a row leased to another runner.
	snap, err := loadCampaignSnapshot(ctx, db, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	opts := renderOptions{APIURL: "https://api.test", WebURL: "https://app.test", Secret: svc.cfg.JWTSecret, Mode: renderSend}
	if _, cont := r2.sendOne(ctx, c, snap, eligibleRecipient{ID: first[0], Email: "x@example.net", Attributes: map[string]any{}}, opts); cont {
		t.Fatal("started a row owned by another runner")
	}
	if fake.callCount("") != 0 {
		t.Fatal("provider called for a foreign lease")
	}
}

func TestCampaignSenderLeaderLockPerSchema(t *testing.T) {
	db, svc, r1, fake := senderFixture(t)
	ctx := context.Background()
	conn, ok, err := r1.tryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	r2 := newCampaignRunner(db, svc.cfg, fake)
	if _, ok, err := r2.tryLock(ctx); ok || err != nil {
		t.Fatalf("second runner in the same schema got the lock: %v %v", ok, err)
	}
	other := newCampaignRunner(testutil.EmptyDatabase(t), svc.cfg, fake)
	otherConn, ok, err := other.tryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("another schema must have its own lock: %v %v", ok, err)
	}
	other.unlock(otherConn)
	r1.unlock(conn)
	conn2, ok, err := r2.tryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("lock not handed over: %v %v", ok, err)
	}
	r2.unlock(conn2)
}

func TestCampaignSenderPauseMidBatchAndResume(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net", "d@example.net")
	id := startCampaign(t, svc)
	uuidOf := func() string {
		var u string
		if err := db.QueryRow(`SELECT uuid::text FROM campaigns WHERE id=$1`, id).Scan(&u); err != nil {
			t.Fatal(err)
		}
		return u
	}()
	fake.script = func(call int, _ string) error {
		if call == 1 {
			if _, err := svc.PauseCampaign(context.Background(), 1, campaignCreator, uuidOf); err != nil {
				t.Error(err)
			}
		}
		return nil
	}
	runTick(t, r)
	wantCampaign(t, db, id, "paused", "user_paused")
	if fake.callCount("") != 1 {
		t.Fatalf("sends after pause: %v", fake.calls)
	}
	count(t, db, 3, `SELECT count(*) FROM campaign_recipients WHERE status='pending' AND lease_owner IS NULL`)
	count(t, db, 1, `SELECT sent_count FROM campaigns WHERE id=$1`, id)

	fake.script = nil
	if _, err := svc.ResumeCampaign(context.Background(), 1, campaignCreator, uuidOf); err != nil {
		t.Fatal(err)
	}
	drain(t, r)
	wantCampaign(t, db, id, "sent", "")
	for _, e := range []string{"a@example.net", "b@example.net", "c@example.net", "d@example.net"} {
		if fake.callCount(e) != 1 {
			t.Fatalf("%s sent %d times", e, fake.callCount(e))
		}
	}
}

func TestCampaignSenderMonthlyQuota(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net", "d@example.net", "e@example.net")
	svc.cfg.DisableAppLimits = false
	mustExec(t, db, `UPDATE organizations SET monthly_email_limit=3 WHERE id=1`)
	id := startCampaign(t, svc)
	drain(t, r)
	wantCampaign(t, db, id, "paused", "monthly_quota_exceeded")
	if fake.callCount("") != 3 {
		t.Fatalf("calls %v", fake.calls)
	}
	count(t, db, 3, `SELECT count(*) FROM campaign_recipients WHERE status='sent' AND quota_reserved`)
	count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='pending' AND NOT quota_reserved AND lease_owner IS NULL`)
	count(t, db, 3, `SELECT attempts FROM organization_send_usage WHERE org_id=1`)
	count(t, db, 1, `SELECT count(*) FROM webhook_events WHERE event_type='campaign.paused' AND payload->'data'->>'statusReason'='monthly_quota_exceeded'`)
}

func TestCampaignSenderOutcomesAndThrottle(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net", "d@example.net", "e@example.net")
	id := startCampaign(t, svc)
	fake.script = func(_ int, to string) error {
		switch to {
		case "b@example.net":
			return apiErr("MessageRejected")
		case "c@example.net":
			return context.DeadlineExceeded
		case "d@example.net":
			return apiErr("TooManyRequestsException")
		}
		return nil
	}
	runTick(t, r)
	wantCampaign(t, db, id, "sending", "ses_throttled")
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND sent_count=1 AND failed_count=1 AND unknown_count=1
		AND throttled_until BETWEEN now()+interval '50 seconds' AND now()+interval '70 seconds'`, id)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='a@example.net' AND status='sent' AND provider_message_id='ses-1'`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='b@example.net' AND status='failed' AND error LIKE '%MessageRejected%'`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email='c@example.net' AND status='unknown'`)
	count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='pending' AND attempt_started_at IS NULL AND lease_owner IS NULL`)

	// Throttled campaigns are not picked until the deadline passes.
	if worked, err := r.runOnce(context.Background()); worked || err != nil {
		t.Fatalf("throttled campaign worked: %v %v", worked, err)
	}
	fake.script = nil
	mustExec(t, db, `UPDATE campaigns SET throttled_until=now()-interval '1 second' WHERE id=$1`, id)
	drain(t, r)
	wantCampaign(t, db, id, "sent", "")
	count(t, db, 3, `SELECT sent_count FROM campaigns WHERE id=$1 AND failed_count=1 AND unknown_count=1 AND throttled_until IS NULL`, id)
	if fake.callCount("c@example.net") != 1 || fake.callCount("b@example.net") != 1 || fake.callCount("d@example.net") != 2 {
		t.Fatalf("calls %v", fake.calls)
	}
}

func TestCampaignSenderProviderRefusalsPause(t *testing.T) {
	for code, reason := range map[string]string{
		"SendingPausedException":             "provider_paused",
		"MailFromDomainNotVerifiedException": "sender_unavailable",
	} {
		t.Run(code, func(t *testing.T) {
			db, svc, r, fake := senderFixture(t)
			id := startCampaign(t, svc)
			fake.script = func(int, string) error { return apiErr(code) }
			drain(t, r)
			wantCampaign(t, db, id, "paused", reason)
			if fake.callCount("") != 1 {
				t.Fatalf("calls %v", fake.calls)
			}
			count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='pending' AND lease_owner IS NULL`)
			count(t, db, 0, `SELECT failed_count+unknown_count+sent_count FROM campaigns WHERE id=$1`, id)
		})
	}
}

func TestCampaignSenderRejectionStreakPauses(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net", "d@example.net", "e@example.net", "f@example.net")
	id := startCampaign(t, svc)
	fake.script = func(int, string) error { return apiErr("MessageRejected") }
	drain(t, r)
	wantCampaign(t, db, id, "paused", "provider_rejected")
	count(t, db, 5, `SELECT failed_count FROM campaigns WHERE id=$1`, id)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE status='pending'`)
}

func TestCampaignSenderBounceBreaker(t *testing.T) {
	db, svc, r, fake := senderFixture(t)
	id := startCampaign(t, svc)
	if err := r.prepareDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE campaigns SET sent_count=100, bounce_count=5 WHERE id=$1`, id)
	drain(t, r)
	wantCampaign(t, db, id, "paused", "bounce_rate_high")
	if fake.callCount("") != 0 {
		t.Fatal("sent past the breaker")
	}
}

func TestCampaignBreakerReason(t *testing.T) {
	for _, tc := range []struct {
		sent, bounces, complaints int64
		want                      string
	}{
		{99, 50, 0, ""},
		{100, 4, 0, ""},
		{100, 5, 0, "bounce_rate_high"},
		{199, 0, 10, ""},
		{200, 0, 0, ""},
		{1000, 0, 2, ""},
		{1000, 0, 3, "complaint_rate_high"},
	} {
		if got := breakerReason(tc.sent, tc.bounces, tc.complaints); got != tc.want {
			t.Errorf("%+v: got %q", tc, got)
		}
	}
}

func TestCampaignSenderDailyQuota(t *testing.T) {
	db, svc, r, fake := senderFixture(t)
	fake.quota = &provider.SendQuota{Max24HourSend: 200, SentLast24Hours: 200, MaxSendRate: 14}
	id := startCampaign(t, svc)
	drain(t, r)
	wantCampaign(t, db, id, "sending", "ses_daily_quota")
	count(t, db, 1, `SELECT count(*) FROM campaigns WHERE id=$1 AND throttled_until BETWEEN now()+interval '29 minutes' AND now()+interval '31 minutes'`, id)
	if fake.callCount("") != 0 {
		t.Fatal("sent past the SES daily quota")
	}
}

// Shutdown mid-batch: the in-flight send completes and is recorded as sent;
// nothing becomes unknown and unstarted rows return to pending.
func TestCampaignSenderShutdownDoesNotCreateUnknowns(t *testing.T) {
	db, svc, r, fake := senderFixture(t, "c@example.net")
	id := startCampaign(t, svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.script = func(call int, _ string) error {
		if call == 1 {
			cancel()
		}
		return nil
	}
	if _, err := r.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r.releaseAll()
	if fake.callCount("") != 1 {
		t.Fatalf("calls %v", fake.calls)
	}
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE status='sent'`)
	count(t, db, 2, `SELECT count(*) FROM campaign_recipients WHERE status='pending' AND lease_owner IS NULL`)
	count(t, db, 0, `SELECT count(*) FROM campaign_recipients WHERE status IN ('unknown','sending','claimed')`)
	count(t, db, 1, `SELECT sent_count FROM campaigns WHERE id=$1 AND unknown_count=0`, id)
}

func TestCampaignSendRateAndBatchSize(t *testing.T) {
	for _, tc := range []struct {
		override float64
		quota    *provider.SendQuota
		want     float64
	}{
		{0, &provider.SendQuota{MaxSendRate: 14}, 11},
		{5, &provider.SendQuota{MaxSendRate: 14}, 5},
		{50, &provider.SendQuota{MaxSendRate: 14}, 11},
		{0, &provider.SendQuota{MaxSendRate: 1}, 1},
		{0.5, &provider.SendQuota{MaxSendRate: 14}, 1},
		{0, nil, 1},
		{20, nil, 1},
	} {
		if got := campaignSendRate(tc.override, tc.quota); got != tc.want {
			t.Errorf("rate(%v,%+v)=%v want %v", tc.override, tc.quota, got, tc.want)
		}
	}
	for perSecond, want := range map[float64]int{1: 20, 2: 40, 11: 50, 1000: 50} {
		if got := campaignBatchSize(perSecond); got != want {
			t.Errorf("batch(%v)=%d want %d", perSecond, got, want)
		}
	}
}

func TestCampaignSenderQuotaCacheKeepsLastKnown(t *testing.T) {
	fake := &senderTestProvider{quota: &provider.SendQuota{Max24HourSend: -1, MaxSendRate: 14}}
	r := newCampaignRunner(nil, &config.Config{}, fake)
	now := time.Now()
	r.now = func() time.Time { return now }
	ctx := context.Background()
	if q := r.sendQuota(ctx); q == nil || r.applyRate(q) != 11 {
		t.Fatalf("quota %+v", q)
	}
	fake.quota = &provider.SendQuota{MaxSendRate: 50}
	if q := r.sendQuota(ctx); q.MaxSendRate != 14 {
		t.Fatal("quota refreshed inside the cache window")
	}
	now = now.Add(6 * time.Minute)
	fake.quotaErr = errors.New("aws down")
	if q := r.sendQuota(ctx); q == nil || q.MaxSendRate != 14 || r.applyRate(q) != 11 {
		t.Fatalf("failed refresh lost the last known quota: %+v", q)
	}
	fresh := newCampaignRunner(nil, &config.Config{}, fake)
	if q := fresh.sendQuota(ctx); q != nil || fresh.applyRate(q) != 1 {
		t.Fatal("unknown quota must fall back to 1/s")
	}
}
