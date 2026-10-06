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

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/internal/worker"
)

type arrivalFixture struct {
	db                  *sql.DB
	org, user, identity int64
	receive             *ReceivingService
	storage             *fakeIncomingStorage
	auth                *ReceivingAuthorization
	runner              *ArrivalRunner
	provider            *mailboxTestProvider
	mu                  sync.Mutex
	dispatched          []*worker.EmailSendPayload
	replies             *AutoReplyService
	ruleID              int
}

const arrivalTopic = "arn:aws:sns:us-east-1:123456789012:arrival"

func newArrivalFixture(t *testing.T) *arrivalFixture {
	t.Helper()
	db := testutil.Database(t)
	f := &arrivalFixture{db: db, storage: &fakeIncomingStorage{}, provider: &mailboxTestProvider{}}
	f.org, f.user, f.identity = mailboxFixture(t, db, "arrival.test")
	if _, err := db.Exec(`UPDATE domains SET receiving_enabled=true WHERE org_id=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO receiving_configs(org_id,s3_bucket,s3_region,sns_topic_arn,ses_rule_set_name,webhook_secret,status,updated_at) VALUES($1,'arrival-bucket','us-east-1',$2,'rs','arrival-secret','active',now())`, f.org, arrivalTopic); err != nil {
		t.Fatal(err)
	}
	f.receive = &ReceivingService{db: db, storage: f.storage}
	auth, err := f.receive.AuthorizeNotification(context.Background(), arrivalTopic, "arrival-secret")
	if err != nil {
		t.Fatal(err)
	}
	f.auth = auth
	cfg := &config.Config{EmailProvider: "ses", DisableAppLimits: true}
	f.runner = NewArrivalRunner(db, cfg, &TransactionalService{db: db, cfg: cfg, emailProvider: f.provider})
	f.runner.dispatch = func(p *worker.EmailSendPayload) {
		f.mu.Lock()
		f.dispatched = append(f.dispatched, p)
		f.mu.Unlock()
	}
	f.replies = NewAutoReplyService(db, cfg)
	return f
}

// deliver ingests one SES notification for owner@arrival.test.
func (f *arrivalFixture) deliver(t *testing.T, sesID, from string, headers ...string) {
	t.Helper()
	f.deliverWith(t, sesID, from, func(*model.SESReceipt) {}, headers...)
}

func (f *arrivalFixture) deliverWith(t *testing.T, sesID, from string, receipt func(*model.SESReceipt), headers ...string) {
	t.Helper()
	extra := ""
	for _, h := range headers {
		extra += h + "\r\n"
	}
	f.storage.raw = []byte("From: Sender <" + from + ">\r\nTo: Owner <owner@arrival.test>\r\nMessage-ID: <" + sesID + "@sender.test>\r\nReferences: <root@sender.test>\r\nSubject: Question " + sesID + "\r\n" + extra + "\r\nHello\r\n")
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId = sesID
	n.Mail.Source = "bounce-1@mail.sender.test"
	pass := model.SESVerdict{Status: "PASS"}
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: []string{"owner@arrival.test"},
		SpamVerdict: pass, VirusVerdict: pass, SPFVerdict: pass, DKIMVerdict: pass, DMARCVerdict: pass,
		Action: model.SESAction{Type: "S3", BucketName: f.auth.Bucket, ObjectKey: "incoming/arrival.test/" + sesID}}
	receipt(n.Receipt)
	if err := f.receive.ProcessIncomingEmail(context.Background(), f.auth, n); err != nil {
		t.Fatal(err)
	}
}

func (f *arrivalFixture) createRule(t *testing.T, in CreateAutoReplyInput) {
	t.Helper()
	if in.HTMLContent == "" {
		in.HTMLContent = "<p>I am away</p>"
		in.TextContent = "I am away"
	}
	in.Active = true
	in.StartDate = time.Now().Add(-time.Hour)
	rule, err := f.replies.CreateAutoReply(context.Background(), f.user, f.org, &in)
	if err != nil {
		t.Fatal(err)
	}
	f.ruleID = rule.ID
}

func (f *arrivalFixture) job(t *testing.T, sesID, kind string) (status, result string) {
	t.Helper()
	var r sql.NullString
	if err := f.db.QueryRow(`SELECT status,result FROM mail_arrival_jobs WHERE ses_message_id=$1 AND kind=$2`, sesID, kind).Scan(&status, &r); err != nil {
		t.Fatalf("job %s/%s: %v", sesID, kind, err)
	}
	return status, r.String
}

func (f *arrivalFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *arrivalFixture) runOnce(t *testing.T) {
	t.Helper()
	if _, err := f.runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestArrivalJobsEnqueuedOnIngest(t *testing.T) {
	f := newArrivalFixture(t)
	ctx := context.Background()

	f.deliver(t, "no-rule", "alice@sender.test")
	if n := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs`); n != 0 {
		t.Fatalf("jobs without a rule or subscription: %d", n)
	}

	f.createRule(t, CreateAutoReplyInput{Subject: "Away"})
	f.deliver(t, "ses-1", "alice@sender.test")
	var ruleID, userID, emailID int64
	var payload string
	if err := f.db.QueryRow(`SELECT rule_id,user_id,received_email_id,payload::text FROM mail_arrival_jobs WHERE kind='auto_reply' AND ses_message_id='ses-1'`).Scan(&ruleID, &userID, &emailID, &payload); err != nil {
		t.Fatal(err)
	}
	if ruleID != int64(f.ruleID) || userID != f.user || emailID == 0 || !strings.Contains(payload, `"sender": "alice@sender.test"`) || !strings.Contains(payload, "<ses-1@sender.test>") {
		t.Fatalf("job rule=%d user=%d email=%d payload=%s", ruleID, userID, emailID, payload)
	}

	// Enqueueing is idempotent per message and identity.
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received", Receipt: &model.SESReceipt{}}
	n.Mail.MessageId, n.Mail.Source = "ses-1", "a@sender.test"
	header := guardHeader()
	header["To"] = []string{"owner@arrival.test"}
	in := ArrivalInput{OrgID: f.org, IdentityID: f.identity, IdentityEmail: "owner@arrival.test", Recipients: []string{"owner@arrival.test"},
		Copies: []ArrivalCopy{{OwnerID: f.user, EmailID: emailID, Folder: "inbox"}}, Header: header, Notification: n}
	if err = EnqueueArrivalJobs(ctx, tx, in); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE ses_message_id='ses-1'`); c != 1 {
		t.Fatalf("duplicate jobs: %d", c)
	}

	// Cheap guards stop the job at enqueue time.
	f.deliver(t, "bulk", "alice@sender.test", "Precedence: bulk")
	f.deliver(t, "daemon", "mailer-daemon@sender.test")
	f.deliverWith(t, "spoofed", "alice@sender.test", func(r *model.SESReceipt) { r.DMARCVerdict.Status = "FAIL" })
	f.deliver(t, "self", "owner@arrival.test")
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE ses_message_id IN ('bulk','daemon','spoofed','self')`); c != 0 {
		t.Fatalf("guarded mail queued %d jobs", c)
	}

	// Push jobs exist only for subscribed owners whose copy stayed in the inbox.
	if _, err = f.db.Exec(`INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key) VALUES($1,'https://fcm.googleapis.com/x','k','a')`, f.user); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "push-1", "bob@sender.test")
	f.deliverWith(t, "spam-1", "bob@sender.test", func(r *model.SESReceipt) { r.SpamVerdict.Status = "FAIL" })
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE kind='push' AND ses_message_id='push-1'`); c != 1 {
		t.Fatalf("push jobs: %d", c)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE ses_message_id='spam-1'`); c != 0 {
		t.Fatalf("spam queued %d jobs", c)
	}

	// A rule naming other identities does not cover this one.
	other := 0
	if err = f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) SELECT $1,domain_id,'second@arrival.test',true,now() FROM identities WHERE id=$2 RETURNING id`, f.user, f.identity).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE auto_replies SET identity_ids=ARRAY[$1::int]`, other); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "scoped", "carol@sender.test")
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE kind='auto_reply' AND ses_message_id='scoped'`); c != 0 {
		t.Fatal("rule for another identity applied")
	}
}

func TestArrivalRunnerAutoReply(t *testing.T) {
	f := newArrivalFixture(t)
	f.createRule(t, CreateAutoReplyInput{Subject: "", ReplyIntervalDays: 7})
	var ruleUUID string
	if err := f.db.QueryRow(`SELECT uuid::text FROM auto_replies WHERE id=$1`, f.ruleID).Scan(&ruleUUID); err != nil {
		t.Fatal(err)
	}

	f.deliver(t, "m1", "alice@sender.test")
	f.runOnce(t)
	if status, result := f.job(t, "m1", "auto_reply"); status != "done" || result != "done" {
		t.Fatal(status, result)
	}
	var kind, ref, to, subject, payload string
	var systemUser int64
	if err := f.db.QueryRow(`SELECT system_kind,system_ref::text,system_user_id,to_addresses,subject,send_payload FROM transactional_emails`).Scan(&kind, &ref, &systemUser, &to, &subject, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "auto_reply" || ref != ruleUUID || systemUser != f.user || !strings.Contains(to, "alice@sender.test") || subject != "Re: Question m1" {
		t.Fatalf("send kind=%s ref=%s user=%d to=%s subject=%q", kind, ref, systemUser, to, subject)
	}
	for _, want := range []string{`"Auto-Submitted": "auto-replied"`, `"X-Auto-Response-Suppress": "All"`, `"In-Reply-To": "<m1@sender.test>"`, `"References": "<root@sender.test> <m1@sender.test>"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload lacks %s: %s", want, payload)
		}
	}
	if c := f.count(t, `SELECT COUNT(*) FROM received_emails WHERE direction='outbound'`); c != 0 {
		t.Fatal("auto-reply wrote a Sent copy")
	}
	if len(f.dispatched) != 1 {
		t.Fatalf("dispatched %d", len(f.dispatched))
	}
	var replyCount, windowCount int
	if err := f.db.QueryRow(`SELECT reply_count,window_count FROM auto_replies WHERE id=$1`, f.ruleID).Scan(&replyCount, &windowCount); err != nil || replyCount != 1 || windowCount != 1 {
		t.Fatal(replyCount, windowCount, err)
	}

	// Within the interval the same sender gets nothing.
	f.deliver(t, "m2", "Alice@Sender.test")
	f.runOnce(t)
	if status, result := f.job(t, "m2", "auto_reply"); status != "skipped" || result != "skipped:recently-replied" {
		t.Fatal(status, result)
	}
	// After the interval the sender is answered again.
	if _, err := f.db.Exec(`UPDATE auto_reply_senders SET replied_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "m3", "alice@sender.test")
	f.runOnce(t)
	if status, _ := f.job(t, "m3", "auto_reply"); status != "done" {
		t.Fatal(status)
	}
	// Reply-once is never refreshed.
	if _, err := f.replies.UpdateAutoReply(context.Background(), f.user, f.ruleID, &UpdateAutoReplyInput{ReplyOnce: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE auto_reply_senders SET replied_at=now()-interval '30 days'`); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "m4", "alice@sender.test")
	f.runOnce(t)
	if _, result := f.job(t, "m4", "auto_reply"); result != "skipped:recently-replied" {
		t.Fatal(result)
	}

	// The daily cap rolls back the sender claim it would have made.
	if _, err := f.db.Exec(`UPDATE auto_replies SET window_day=current_date, window_count=200 WHERE id=$1`, f.ruleID); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "m5", "bob@sender.test")
	f.runOnce(t)
	if _, result := f.job(t, "m5", "auto_reply"); result != "skipped:daily-cap" {
		t.Fatal(result)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM auto_reply_senders WHERE sender_email='bob@sender.test'`); c != 0 {
		t.Fatal("capped job kept its sender claim")
	}
	if _, err := f.db.Exec(`UPDATE auto_replies SET window_count=0 WHERE id=$1`, f.ruleID); err != nil {
		t.Fatal(err)
	}

	// Suppressed and excluded senders are skipped at run time.
	if _, err := f.db.Exec(`INSERT INTO suppression_list(org_id,email,reason) VALUES($1,'carol@sender.test','bounce')`, f.org); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "m6", "carol@sender.test")
	if _, err := f.replies.UpdateAutoReply(context.Background(), f.user, f.ruleID, &UpdateAutoReplyInput{ExcludePatterns: &[]string{"@partner.test"}}); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "m7", "dave@partner.test")
	f.runOnce(t)
	if _, result := f.job(t, "m6", "auto_reply"); result != "skipped:suppressed" {
		t.Fatal(result)
	}
	if _, result := f.job(t, "m7", "auto_reply"); result != "skipped:excluded" {
		t.Fatal(result)
	}

	// A rule deleted after enqueue is skipped.
	f.deliver(t, "m8", "erin@sender.test")
	if err := f.replies.DeleteAutoReply(context.Background(), f.user, f.ruleID); err != nil {
		t.Fatal(err)
	}
	f.runOnce(t)
	if _, result := f.job(t, "m8", "auto_reply"); result != "skipped:rule-gone" {
		t.Fatal(result)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM transactional_emails`); c != 2 {
		t.Fatalf("expected 2 replies in total, got %d", c)
	}
}

func ptr[T any](v T) *T { return &v }

func TestArrivalRunnerCrashRetryAndLease(t *testing.T) {
	f := newArrivalFixture(t)
	ctx := context.Background()
	f.createRule(t, CreateAutoReplyInput{Subject: "Away"})

	// Crash after the claim and the handler's writes, before commit.
	f.deliver(t, "c1", "alice@sender.test")
	jobs, err := f.runner.claim(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if res, err := f.runner.runAutoReply(ctx, tx, &jobs[0]); err != nil || res.Status != "done" {
		t.Fatal(res, err)
	}
	_ = tx.Rollback()
	f.runOnce(t) // the lease is still held
	if status, _ := f.job(t, "c1", "auto_reply"); status != "running" {
		t.Fatal(status)
	}
	if _, err = f.db.Exec(`UPDATE mail_arrival_jobs SET lease_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	f.runOnce(t)
	var attempts int
	if err = f.db.QueryRow(`SELECT attempts FROM mail_arrival_jobs WHERE ses_message_id='c1'`).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatal(attempts, err)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM transactional_emails`); c != 1 {
		t.Fatalf("crash retry sent %d", c)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM auto_reply_senders`); c != 1 {
		t.Fatalf("sender claims %d", c)
	}

	// A runner whose lease expired cannot complete the job; its writes roll back.
	f.deliver(t, "c2", "bob@sender.test")
	stale, err := f.runner.claim(ctx)
	if err != nil || len(stale) != 1 {
		t.Fatal(stale, err)
	}
	if _, err = f.db.Exec(`UPDATE mail_arrival_jobs SET lease_until=now()-interval '1 second' WHERE ses_message_id='c2'`); err != nil {
		t.Fatal(err)
	}
	other := NewArrivalRunner(f.db, f.runner.cfg, f.runner.tx)
	other.dispatch = f.runner.dispatch
	fresh, err := other.claim(ctx)
	if err != nil || len(fresh) != 1 {
		t.Fatal(fresh, err)
	}
	if err = f.runner.processOne(ctx, &stale[0]); !errors.Is(err, errArrivalLeaseLost) {
		t.Fatalf("stale runner: %v", err)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM transactional_emails`); c != 1 {
		t.Fatal("stale runner's send committed")
	}
	if err = other.processOne(ctx, &fresh[0]); err != nil {
		t.Fatal(err)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM transactional_emails`); c != 2 {
		t.Fatalf("fresh runner sends %d", c)
	}

	// Concurrent jobs for one sender: the sender claim lets one through.
	f.deliver(t, "c3", "carol@sender.test")
	f.deliver(t, "c4", "carol@sender.test")
	both, err := f.runner.claim(ctx)
	if err != nil || len(both) != 2 {
		t.Fatal(both, err)
	}
	var wg sync.WaitGroup
	for i := range both {
		wg.Add(1)
		go func(j *arrivalJob) {
			defer wg.Done()
			if err := f.runner.processOne(ctx, j); err != nil {
				t.Error(err)
			}
		}(&both[i])
	}
	wg.Wait()
	if c := f.count(t, `SELECT COUNT(*) FROM transactional_emails WHERE to_addresses LIKE '%carol@%'`); c != 1 {
		t.Fatalf("concurrent sender replies %d", c)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE ses_message_id IN ('c3','c4') AND result='skipped:recently-replied'`); c != 1 {
		t.Fatal("one concurrent job should be skipped")
	}

	// Two runners racing over a batch run every job exactly once.
	for i := 0; i < 6; i++ {
		f.deliver(t, fmt.Sprintf("r%d", i), fmt.Sprintf("racer%d@sender.test", i))
	}
	before := f.count(t, `SELECT COUNT(*) FROM transactional_emails`)
	for _, r := range []*ArrivalRunner{f.runner, other} {
		wg.Add(1)
		go func(r *ArrivalRunner) {
			defer wg.Done()
			if _, err := r.runOnce(ctx); err != nil {
				t.Error(err)
			}
		}(r)
	}
	wg.Wait()
	if c := f.count(t, `SELECT COUNT(*) FROM transactional_emails`) - before; c != 6 {
		t.Fatalf("racing runners sent %d", c)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE status<>'done' AND ses_message_id LIKE 'r%'`); c != 0 {
		t.Fatal("racing runners left jobs unfinished")
	}
}

type failingPusher struct{ calls int }

func (p *failingPusher) SendNewEmailNotification(context.Context, int64, string, string, string, string) error {
	p.calls++
	return errors.New("push endpoint unavailable")
}

func TestArrivalRunnerRetryFailureAndRetention(t *testing.T) {
	f := newArrivalFixture(t)
	ctx := context.Background()
	insert := func(kind, dedupe string) {
		t.Helper()
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = insertArrivalJob(ctx, tx, f.org, f.identity, f.user, 0, 0, kind, dedupe, dedupe, arrivalPush{UUID: "u", From: "a@b.test", Subject: "s"}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// Without a pusher, push jobs finish skipped.
	insert("push", "p0")
	f.runOnce(t)
	if _, result := f.job(t, "p0", "push"); result != "skipped:push-not-configured" {
		t.Fatal(result)
	}

	// A transient error retries with backoff, then fails after the last attempt.
	pusher := &failingPusher{}
	f.runner.SetPusher(pusher)
	insert("push", "p1")
	f.runOnce(t)
	var status, result string
	var attempts int
	var wait float64
	if err := f.db.QueryRow(`SELECT status,result,attempts,EXTRACT(EPOCH FROM next_attempt_at-now()) FROM mail_arrival_jobs WHERE dedupe_key='p1'`).Scan(&status, &result, &attempts, &wait); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || wait < 20 || wait > 31 || !strings.Contains(result, "push endpoint unavailable") {
		t.Fatal(status, result, attempts, wait)
	}
	if _, err := f.db.Exec(`UPDATE mail_arrival_jobs SET attempts=$1, next_attempt_at=now() WHERE dedupe_key='p1'`, arrivalMaxAttempts-1); err != nil {
		t.Fatal(err)
	}
	f.runOnce(t)
	if status, _ := f.job(t, "p1", "push"); status != "failed" || pusher.calls != 2 {
		t.Fatal(status, pusher.calls)
	}
	// An expired lease on the last attempt is failed, never claimed again.
	insert("push", "p2")
	if _, err := f.db.Exec(`UPDATE mail_arrival_jobs SET status='running', attempts=$1, lease_until=now()-interval '1 second' WHERE dedupe_key='p2'`, arrivalMaxAttempts); err != nil {
		t.Fatal(err)
	}
	f.runOnce(t)
	if _, result := f.job(t, "p2", "push"); result != "failed:lease-expired" || pusher.calls != 2 {
		t.Fatal(result, pusher.calls)
	}

	// No mail provider: final failure, no retry.
	f.createRule(t, CreateAutoReplyInput{Subject: "Away"})
	f.deliver(t, "np", "alice@sender.test")
	f.runner.tx = &TransactionalService{db: f.db, cfg: f.runner.cfg}
	f.runOnce(t)
	if status, result := f.job(t, "np", "auto_reply"); status != "failed" || result != "failed:provider-not-configured" {
		t.Fatal(status, result)
	}

	// Unknown kinds (forwards until they are executed) finish without effect.
	insert("forward", "fw")
	f.runOnce(t)
	if status, _ := f.job(t, "fw", "forward"); status != "skipped" {
		t.Fatal(status)
	}

	if _, err := f.db.Exec(`UPDATE mail_arrival_jobs SET updated_at=now()-interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.retention(ctx); err != nil {
		t.Fatal(err)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs`); c != 3 {
		t.Fatalf("retention kept %d rows; want the three failed ones", c)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE status<>'failed'`); c != 0 {
		t.Fatal("retention kept a finished job")
	}
}

func TestArrivalBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: 30 * time.Second, 2: 2 * time.Minute, 3: 8 * time.Minute, 5: 128 * time.Minute, 6: 6 * time.Hour, 9: 6 * time.Hour} {
		if got := arrivalBackoff(attempts); got != want {
			t.Errorf("attempt %d: %v want %v", attempts, got, want)
		}
	}
	if got := systemDedupeKey("mailat:ar:1:", strings.Repeat("x", 200)); len(got) > 128 || !strings.HasPrefix(got, "mailat:ar:1:") {
		t.Fatal(got)
	}
}

func TestAutoReplyServiceValidation(t *testing.T) {
	f := newArrivalFixture(t)
	ctx := context.Background()
	var stranger, strangerIdentity int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,'stranger@arrival.test','x',now()) RETURNING id`, f.org).Scan(&stranger); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,updated_at) SELECT $1,domain_id,'stranger@arrival.test',now() FROM identities WHERE id=$2 RETURNING id`, stranger, f.identity).Scan(&strangerIdentity); err != nil {
		t.Fatal(err)
	}
	base := func() *CreateAutoReplyInput {
		return &CreateAutoReplyInput{Name: "Vacation", Subject: "Away", HTMLContent: "<p>x</p>", Active: true}
	}
	var invalid *AutoReplyValidationError
	for name, mutate := range map[string]func(*CreateAutoReplyInput){
		"foreign identity": func(in *CreateAutoReplyInput) { in.IdentityIDs = []int{int(f.identity), int(strangerIdentity)} },
		"interval":         func(in *CreateAutoReplyInput) { in.ReplyIntervalDays = 31 },
		"negative":         func(in *CreateAutoReplyInput) { in.ReplyIntervalDays = -1 },
		"no html":          func(in *CreateAutoReplyInput) { in.HTMLContent = " " },
		"long subject":     func(in *CreateAutoReplyInput) { in.Subject = strings.Repeat("s", 501) },
		"subject newline":  func(in *CreateAutoReplyInput) { in.Subject = "a\r\nBcc: x" },
		"big body":         func(in *CreateAutoReplyInput) { in.TextContent = strings.Repeat("t", autoReplyMaxBody+1) },
		"many patterns":    func(in *CreateAutoReplyInput) { in.ExcludePatterns = make([]string, 51) },
		"long pattern":     func(in *CreateAutoReplyInput) { in.ExcludePatterns = []string{strings.Repeat("p", 201)} },
		"end before start": func(in *CreateAutoReplyInput) {
			in.StartDate = time.Now()
			end := in.StartDate.Add(-time.Hour)
			in.EndDate = &end
		},
	} {
		in := base()
		mutate(in)
		if _, err := f.replies.CreateAutoReply(ctx, f.user, f.org, in); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	in := base()
	in.ExcludePatterns = []string{"a,b@x.test", `quote"d`, " ", "*@spam.test"}
	in.IdentityIDs = []int{int(f.identity), int(f.identity)}
	created, err := f.replies.CreateAutoReply(ctx, f.user, f.org, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.ReplyIntervalDays != 7 || len(created.ExcludePatterns) != 3 || created.ExcludePatterns[0] != "a,b@x.test" || created.ExcludePatterns[1] != `quote"d` ||
		len(created.IdentityIDs) != 1 || created.IdentityIDs[0] != int(f.identity) {
		t.Fatalf("%+v", created)
	}
	updated, err := f.replies.UpdateAutoReply(ctx, f.user, created.ID, &UpdateAutoReplyInput{ReplyIntervalDays: ptr(14), IdentityIDs: &[]int{}})
	if err != nil || updated.ReplyIntervalDays != 14 || len(updated.IdentityIDs) != 0 || updated.Subject != "Away" {
		t.Fatalf("%+v %v", updated, err)
	}
	if _, err = f.replies.UpdateAutoReply(ctx, f.user, created.ID, &UpdateAutoReplyInput{IdentityIDs: &[]int{int(strangerIdentity)}}); !errors.As(err, &invalid) {
		t.Fatal("foreign identity accepted on update", err)
	}
	if _, err = f.replies.UpdateAutoReply(ctx, stranger, created.ID, &UpdateAutoReplyInput{Active: ptr(false)}); !errors.Is(err, ErrAutoReplyNotFound) {
		t.Fatal("another user's rule updated", err)
	}
	list, err := f.replies.ListAutoReplies(ctx, f.user)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if list, err = f.replies.ListAutoReplies(ctx, stranger); err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	if err = f.replies.DeleteAutoReply(ctx, stranger, created.ID); !errors.Is(err, ErrAutoReplyNotFound) {
		t.Fatal(err)
	}
}
