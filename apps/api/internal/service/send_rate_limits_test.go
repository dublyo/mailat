package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type fakeSendQuota struct {
	mu    sync.Mutex
	quota *provider.SendQuota
	err   error
	calls int
}

func (f *fakeSendQuota) GetSendQuota(context.Context) (*provider.SendQuota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.quota, f.err
}

func (f *fakeSendQuota) set(q *provider.SendQuota, err error) {
	f.mu.Lock()
	f.quota, f.err = q, err
	f.mu.Unlock()
}

func (f *fakeSendQuota) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestSendLimiter(db *sql.DB, cfg *config.Config, quota sendQuotaReader) *SendLimiter {
	cfg.JWTSecret = "send-limit-fixture-secret-0123456789"
	return NewSendLimiter(NewRateLimiter(db, cfg), cfg, quota)
}

func asSendLimit(t *testing.T, err error) *SendLimitError {
	t.Helper()
	var limit *SendLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("expected a send limit refusal, got %v", err)
	}
	return limit
}

// shiftWindow moves a subject's current window into the past, as if it ended.
func shiftWindow(t *testing.T, db *sql.DB, l *SendLimiter, rule, subject string, by time.Duration) {
	t.Helper()
	window := time.Hour
	if strings.HasSuffix(rule, "-day") {
		window = 24 * time.Hour
	}
	key := l.rates.storageKey(RateRule{Name: rule, Window: window}, subject)
	if _, err := db.Exec(`UPDATE auth_rate_limits SET window_start=window_start-make_interval(secs => $2) WHERE key=$1`, key, by.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimiterAllowNIsAllOrNothing(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	l := NewRateLimiter(db, &config.Config{JWTSecret: "rate-limit-fixture-secret-0123456789"})
	rule := RateRule{Name: "n-fixture", Limit: 5, Window: time.Hour}
	if ok, _, err := l.allowN(ctx, db, rule, "s", 3); err != nil || !ok {
		t.Fatal("first 3 refused", err)
	}
	if ok, retry, err := l.allowN(ctx, db, rule, "s", 3); err != nil || ok || retry <= 0 {
		t.Fatalf("3 more must be refused whole: ok=%v retry=%v err=%v", ok, retry, err)
	}
	if ok, _, err := l.allowN(ctx, db, rule, "s", 2); err != nil || !ok {
		t.Fatal("the remaining 2 refused", err)
	}
	if ok, _, err := l.allowN(ctx, db, rule, "other", 6); err != nil || ok {
		t.Fatal("more than the limit in one request allowed", err)
	}
	var rows int
	db.QueryRow(`SELECT count(*) FROM auth_rate_limits WHERE key=$1`, l.storageKey(rule, "other")).Scan(&rows)
	if rows != 0 {
		t.Fatal("an oversized request was recorded")
	}
	if ok, _, err := l.Allow(ctx, rule, "s"); err != nil || ok {
		t.Fatal("Allow ignores hits counted by allowN", err)
	}
}

func TestSendLimiterUserAndKeyWindows(t *testing.T) {
	db := testutil.Database(t)
	l := newTestSendLimiter(db, &config.Config{SendLimitUserPerHour: 5, SendLimitUserPerDay: 8, SendLimitKeyPerHour: 2}, nil)
	user := WithSendQuota(context.Background(), SendQuotaSubject{UserID: 7})
	key := WithSendQuota(context.Background(), SendQuotaSubject{UserID: 7, APIKeyID: 3})

	// System mail carries no subject and is never counted.
	if err := l.Reserve(context.Background(), 100000); err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve(user, 3); err != nil {
		t.Fatal(err)
	}
	limit := asSendLimit(t, l.Reserve(user, 3))
	if limit.Message != "Sending limit reached: 5 recipients per hour. Try again "+retryPhrase(limit.RetryAfter)+"." || limit.RetryAfter <= 0 || limit.RetryAfter > time.Hour {
		t.Fatalf("hourly refusal %+v", limit)
	}
	if err := l.Reserve(user, 2); err != nil {
		t.Fatal("refused send was counted", err)
	}

	// API keys count per key, separately from the owner's sessions; a zero
	// daily key limit means no daily window.
	if err := l.Reserve(key, 2); err != nil {
		t.Fatal("key shares the user's count", err)
	}
	asSendLimit(t, l.Reserve(key, 1))
	if err := l.Reserve(WithSendQuota(context.Background(), SendQuotaSubject{UserID: 7, APIKeyID: 4}), 2); err != nil {
		t.Fatal("keys share a count", err)
	}

	// A new hour still respects the day; a refused day leaves the hour untouched.
	shiftWindow(t, db, l, "send-user-hour", "7", time.Hour)
	limit = asSendLimit(t, l.Reserve(user, 4))
	if !strings.Contains(limit.Message, "8 recipients per day") || limit.RetryAfter <= 0 {
		t.Fatalf("daily refusal %+v", limit)
	}
	if err := l.Reserve(user, 3); err != nil {
		t.Fatal("refused daily reservation consumed the hour", err)
	}
	asSendLimit(t, l.Reserve(user, 1))

	// More recipients than a window allows: waiting would not help.
	limit = asSendLimit(t, l.Reserve(WithSendQuota(context.Background(), SendQuotaSubject{UserID: 8}), 6))
	if limit.RetryAfter != 0 || !strings.Contains(limit.Message, "This send has 6 recipients") {
		t.Fatalf("oversized refusal %+v", limit)
	}
	if err := l.Reserve(WithSendQuota(context.Background(), SendQuotaSubject{UserID: 8}), 5); err != nil {
		t.Fatal("oversized refusal was counted", err)
	}
}

func TestSendLimiterZeroMeansUnlimited(t *testing.T) {
	db := testutil.Database(t)
	l := newTestSendLimiter(db, &config.Config{}, nil)
	for _, subject := range []SendQuotaSubject{{UserID: 1}, {APIKeyID: 2}} {
		if err := l.Reserve(WithSendQuota(context.Background(), subject), 1000000); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM auth_rate_limits`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("unlimited windows stored %d rows %v", rows, err)
	}
	var nilLimiter *SendLimiter
	if err := nilLimiter.Reserve(WithSendQuota(context.Background(), SendQuotaSubject{UserID: 1}), 10); err != nil {
		t.Fatal(err)
	}
}

func TestSendLimiterConcurrentReservationsNeverOvershoot(t *testing.T) {
	db := testutil.Database(t)
	l := newTestSendLimiter(db, &config.Config{SendLimitUserPerHour: 5, SendLimitUserPerDay: 100}, nil)
	ctx := WithSendQuota(context.Background(), SendQuotaSubject{UserID: 9})
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := l.Reserve(ctx, 1)
			var limit *SendLimitError
			if err != nil && !errors.As(err, &limit) {
				t.Error(err)
			}
			if err == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 5 {
		t.Fatalf("granted %d of a 5-recipient window", granted)
	}
}

func TestSendLimiterSESHeadroomGuard(t *testing.T) {
	db := testutil.Database(t)
	quota := &fakeSendQuota{quota: &provider.SendQuota{Max24HourSend: 1000, SentLast24Hours: 899}}
	l := newTestSendLimiter(db, &config.Config{}, quota)
	now := time.Now()
	l.now = func() time.Time { return now }
	user := WithSendQuota(context.Background(), SendQuotaSubject{UserID: 1})

	if err := l.Reserve(user, 1); err != nil {
		t.Fatal(err)
	}
	quota.set(&provider.SendQuota{Max24HourSend: 1000, SentLast24Hours: 900}, nil)
	if err := l.Reserve(user, 1); err != nil || quota.count() != 1 {
		t.Fatalf("quota must be cached for five minutes: err=%v calls=%d", err, quota.count())
	}
	now = now.Add(sendQuotaCacheTTL)
	limit := asSendLimit(t, l.Reserve(WithSendQuota(context.Background(), SendQuotaSubject{APIKeyID: 5}), 1))
	if !strings.Contains(limit.Message, "90%") || limit.RetryAfter != sendQuotaCacheTTL || quota.count() != 2 {
		t.Fatalf("headroom refusal %+v calls=%d", limit, quota.count())
	}
	// System mail is still allowed and does not read the quota.
	if err := l.Reserve(context.Background(), 1); err != nil || quota.count() != 2 {
		t.Fatalf("system mail blocked: err=%v calls=%d", err, quota.count())
	}

	// A failed lookup fails open until the next refresh.
	quota.set(nil, errors.New("ses unavailable"))
	now = now.Add(sendQuotaCacheTTL)
	if err := l.Reserve(user, 1); err != nil || quota.count() != 3 {
		t.Fatalf("quota failure must fail open: err=%v calls=%d", err, quota.count())
	}
	// A provider without a 24-hour cap (SMTP reports -1) never trips the guard.
	quota.set(&provider.SendQuota{Max24HourSend: -1, SentLast24Hours: 5000}, nil)
	now = now.Add(sendQuotaCacheTTL)
	if err := l.Reserve(user, 1); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionalSendLimitsBatchAndKeys(t *testing.T) {
	db := testutil.Database(t)
	org, user, _ := mailboxFixture(t, db, "limits.test")
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &mailboxTestProvider{}}
	svc.SetSendLimiter(newTestSendLimiter(db, &config.Config{SendLimitUserPerHour: 4, SendLimitKeyPerHour: 4}, nil))
	actor := SendActor{UserID: user, Admin: true}
	session := WithSendQuota(context.Background(), SendQuotaSubject{UserID: user})
	item := func(to ...string) model.SendEmailRequest {
		return model.SendEmailRequest{From: "owner@limits.test", To: to, Subject: "s", Text: "t"}
	}
	batch := func(key string, emails ...model.SendEmailRequest) *model.BatchSendRequest {
		return &model.BatchSendRequest{IdempotencyKey: key, Emails: emails}
	}
	recorded := func(key string) (batches, emails int) {
		t.Helper()
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM email_batch_submissions WHERE submission_key=$1),(SELECT count(*) FROM transactional_emails WHERE idempotency_key LIKE $2)`, key, "batch:%").Scan(&batches, &emails); err != nil {
			t.Fatal(err)
		}
		return
	}

	// Five recipients across the batch exceed the hourly four: nothing is recorded.
	oversized := batch("oversized-batch", item("a@ext.test", "b@ext.test"), model.SendEmailRequest{From: "owner@limits.test", To: []string{"c@ext.test"}, Cc: []string{"d@ext.test"}, Bcc: []string{"e@ext.test"}, Subject: "s", Text: "t"})
	_, err := svc.BatchSendEmailForUser(session, org, actor, oversized)
	asSendLimit(t, err)
	if b, e := recorded("oversized-batch"); b != 0 || e != 0 {
		t.Fatalf("refused batch recorded %d batches %d emails", b, e)
	}

	// Four recipients fit; the items are counted once, as a batch.
	ok := batch("fitting-batch", item("a@ext.test", "b@ext.test"), model.SendEmailRequest{From: "owner@limits.test", To: []string{"c@ext.test"}, Cc: []string{"d@ext.test"}, Subject: "s", Text: "t"})
	res, err := svc.BatchSendEmailForUser(session, org, actor, ok)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Results {
		if r.ID == "" {
			t.Fatalf("batch item failed: %+v", r)
		}
		waitForTransactionalSend(t, db, r.ID)
	}
	// A retry of the recorded batch replays without being counted again.
	replay, err := svc.BatchSendEmailForUser(session, org, actor, ok)
	if err != nil || replay.Results[0].ID != res.Results[0].ID {
		t.Fatalf("batch replay refused: %v", err)
	}

	single := &model.SendEmailRequest{From: "owner@limits.test", To: []string{"f@ext.test"}, Subject: "s", Text: "t", IdempotencyKey: "single-over-limit"}
	_, err = svc.SendEmailForUser(session, org, actor, single)
	asSendLimit(t, err)
	var rows int
	if err = db.QueryRow(`SELECT count(*) FROM transactional_emails WHERE idempotency_key='single-over-limit'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused send recorded %d rows %v", rows, err)
	}
	// The same request through an API key counts against that key instead.
	sent, err := svc.SendEmailForUser(WithSendQuota(context.Background(), SendQuotaSubject{UserID: user, APIKeyID: 77}), org, actor, single)
	if err != nil {
		t.Fatal("API key shares the session count", err)
	}
	waitForTransactionalSend(t, db, sent.ID)
	// System sends (no subject) are not limited.
	system := &model.SendEmailRequest{From: "owner@limits.test", To: []string{"g@ext.test"}, Subject: "s", Text: "t", IdempotencyKey: "system-send"}
	if sent, err = svc.SendEmailForUser(context.Background(), org, actor, system); err != nil {
		t.Fatal("system send limited", err)
	}
	waitForTransactionalSend(t, db, sent.ID)
}

func TestComposeSendLimitKeepsKeyUsable(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	_, user, identity := mailboxFixture(t, db, "compose-limit.test")
	fake := &mailboxTestProvider{}
	svc := &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: mailboxTestStorage{}}
	limits := newTestSendLimiter(db, &config.Config{SendLimitUserPerHour: 2}, nil)
	svc.SetSendLimiter(limits)
	session := WithSendQuota(ctx, SendQuotaSubject{UserID: user})
	mail := func(key string, to ...string) *ComposeEmail {
		m := &ComposeEmail{IdentityID: identity, Subject: "limited", TextBody: "hello", SubmissionKey: key}
		for _, addr := range to {
			m.To = append(m.To, EmailAddress{Email: addr})
		}
		return m
	}

	first, err := svc.SendEmail(session, user, &ComposeEmail{IdentityID: identity, Subject: "limited", TextBody: "hello", SubmissionKey: "compose-limit-1", To: []EmailAddress{{Email: "a@ext.test"}}, Bcc: []EmailAddress{{Email: "b@ext.test"}}})
	if err != nil || first.Status != "sent" {
		t.Fatal(first, err)
	}
	// A replay of the accepted key returns its receipt without being counted.
	replay, err := svc.SendEmail(session, user, &ComposeEmail{IdentityID: identity, Subject: "limited", TextBody: "hello", SubmissionKey: "compose-limit-1", To: []EmailAddress{{Email: "a@ext.test"}}, Bcc: []EmailAddress{{Email: "b@ext.test"}}})
	if err != nil || replay.EmailID != first.EmailID || fake.calls != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", replay, err, fake.calls)
	}
	_, err = svc.SendEmail(session, user, mail("compose-limit-2", "c@ext.test"))
	if limit := asSendLimit(t, err); !strings.Contains(limit.Message, "2 recipients per hour") {
		t.Fatal(limit.Message)
	}
	var claimed int
	if err = db.QueryRow(`SELECT count(*) FROM compose_submission_keys WHERE submission_key='compose-limit-2'`).Scan(&claimed); err != nil || claimed != 0 {
		t.Fatalf("refused send claimed its key: %d %v", claimed, err)
	}
	// Once the window has passed, the same key and content send normally.
	shiftWindow(t, db, limits, "send-user-hour", strconv.FormatInt(user, 10), time.Hour)
	sent, err := svc.SendEmail(session, user, mail("compose-limit-2", "c@ext.test"))
	if err != nil || sent.Status != "sent" || fake.calls != 2 {
		t.Fatalf("retry after window: %+v %v calls=%d", sent, err, fake.calls)
	}
}

func TestCampaignTestSendCountsTowardSendLimits(t *testing.T) {
	db, svc, fake := newCampaignFixture(t)
	c := createTestCampaign(t, svc, campaignCreator, nil)
	mustExec(t, db, `UPDATE domains SET sending_feedback_ready=false WHERE id=1`)
	svc.SetSendLimiter(newTestSendLimiter(db, &config.Config{SendLimitUserPerHour: 3}, nil))
	session := WithSendQuota(context.Background(), SendQuotaSubject{UserID: campaignCreator.UserID})

	if res, err := svc.SendTestEmail(session, 1, campaignCreator, c.UUID, []string{"a@example.net", "b@example.net"}, "limit-test-0001"); err != nil || res.Status != "sent" {
		t.Fatalf("test send: %+v %v", res, err)
	}
	_, err := svc.SendTestEmail(session, 1, campaignCreator, c.UUID, []string{"c@example.net", "d@example.net"}, "limit-test-0002")
	asSendLimit(t, err)
	var stored int
	if err = db.QueryRow(`SELECT count(*) FROM campaign_test_sends WHERE idempotency_key='limit-test-0002'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("refused test send stored %d rows %v", stored, err)
	}
	// A replay returns the stored outcome without being counted.
	if _, err = svc.SendTestEmail(session, 1, campaignCreator, c.UUID, []string{"b@example.net", "a@example.net"}, "limit-test-0001"); err != nil {
		t.Fatal("replay limited:", err)
	}
	if _, err = svc.SendTestEmail(session, 1, campaignCreator, c.UUID, []string{"e@example.net"}, "limit-test-0003"); err != nil || fake.calls != 3 {
		t.Fatalf("remaining recipient refused: %v calls=%d", err, fake.calls)
	}
}

// Concurrent requests with one idempotency key count their recipients once:
// only the request that claims the key reserves, inside the claim transaction.
func TestSameKeyConcurrentSendsCountOnce(t *testing.T) {
	db := testutil.Database(t)
	org, user, identity := mailboxFixture(t, db, "same-key.test")
	limits := newTestSendLimiter(db, &config.Config{SendLimitUserPerHour: 100}, nil)
	session := WithSendQuota(context.Background(), SendQuotaSubject{UserID: user})
	hits := func() int {
		t.Helper()
		var n int
		key := limits.rates.storageKey(RateRule{Name: "send-user-hour", Window: time.Hour}, strconv.FormatInt(user, 10))
		if err := db.QueryRow(`SELECT COALESCE(SUM(hits),0) FROM auth_rate_limits WHERE key=$1`, key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// race holds the key's claim row in an open transaction until all n
	// requests passed their replay checks and wait on the claim, so every one
	// of them races for the key; the blocker then rolls back.
	race := func(n int, table string, block func(*sql.Tx) error, send func() (string, error)) string {
		t.Helper()
		blocker, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if err = block(blocker); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ids[i], errs[i] = send()
			}(i)
		}
		for deadline := time.Now().Add(10 * time.Second); ; {
			var waiting int
			if err = db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'INSERT INTO '||$1||'%'`, table).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting >= n {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d of %d requests reached the %s claim", waiting, n, table)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err = blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		for i := range ids {
			if errs[i] != nil || ids[i] == "" || ids[i] != ids[0] {
				t.Fatalf("request %d: id=%q (first %q) err=%v", i, ids[i], ids[0], errs[i])
			}
		}
		return ids[0]
	}

	fake := &mailboxTestProvider{}
	compose := &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: mailboxTestStorage{}}
	compose.SetSendLimiter(limits)
	race(4, "compose_submission_keys", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO compose_submission_keys(user_id,submission_key,request_hash,email_uuid,message_id,identity_id) VALUES($1,'same-key-compose','blocker',gen_random_uuid(),'<blocker@same-key.test>',$2)`, user, identity)
		return err
	}, func() (string, error) {
		res, err := compose.SendEmail(session, user, &ComposeEmail{IdentityID: identity, Subject: "same key", TextBody: "hello", SubmissionKey: "same-key-compose",
			To: []EmailAddress{{Email: "a@ext.test"}}, Cc: []EmailAddress{{Email: "b@ext.test"}}})
		if err != nil {
			return "", err
		}
		return res.EmailID, nil
	})
	if got := hits(); got != 2 || fake.calls != 1 {
		t.Fatalf("compose: counted %d recipients (want 2), provider calls %d", got, fake.calls)
	}

	// One provider for both services, so provider message ids stay unique.
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake}
	svc.SetSendLimiter(limits)
	actor := SendActor{UserID: user, Admin: true}
	sentID := race(4, "email_submission_keys", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO email_submission_keys(org_id,submission_key,request_hash) VALUES($1,'same-key-single','blocker')`, org)
		return err
	}, func() (string, error) {
		res, err := svc.SendEmailForUser(session, org, actor, &model.SendEmailRequest{From: "owner@same-key.test", To: []string{"c@ext.test", "d@ext.test"}, Bcc: []string{"e@ext.test"}, Subject: "s", Text: "t", IdempotencyKey: "same-key-single"})
		if err != nil {
			return "", err
		}
		return res.ID, nil
	})
	waitForTransactionalSend(t, db, sentID)
	if got := hits(); got != 5 {
		t.Fatalf("single /emails: counted %d recipients in all, want 5", got)
	}

	batch := &model.BatchSendRequest{IdempotencyKey: "same-key-batch", Emails: []model.SendEmailRequest{
		{From: "owner@same-key.test", To: []string{"f@ext.test"}, Subject: "s", Text: "t"},
		{From: "owner@same-key.test", To: []string{"g@ext.test"}, Cc: []string{"h@ext.test"}, Subject: "s", Text: "t"},
	}}
	var batchIDs []string
	var mu sync.Mutex
	race(4, "email_batch_submissions", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO email_batch_submissions(org_id,submission_key,user_id,request_hash) VALUES($1,'same-key-batch',$2,'blocker')`, org, user)
		return err
	}, func() (string, error) {
		res, err := svc.BatchSendEmailForUser(session, org, actor, batch)
		if err != nil {
			return "", err
		}
		mu.Lock()
		defer mu.Unlock()
		batchIDs = nil
		for _, r := range res.Results {
			if r.ID == "" {
				return "", errors.New("batch item failed: " + r.Error)
			}
			batchIDs = append(batchIDs, r.ID)
		}
		return res.Results[0].ID, nil
	})
	for _, id := range batchIDs {
		waitForTransactionalSend(t, db, id)
	}
	if got := hits(); got != 8 {
		t.Fatalf("batch: counted %d recipients in all, want 8", got)
	}
}
