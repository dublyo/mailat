package service

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

// automationMailFixture is the automation fixture with SES configured, a
// postal address, the real mailer as sender and a fake SES provider.
func automationMailFixture(t *testing.T) (*sql.DB, *AutomationService, *AutomationExecutor, *automationMailRunner, *senderTestProvider) {
	t.Helper()
	db, s := newAutomationFixture(t)
	s.cfg.EmailProvider, s.cfg.APIUrl, s.cfg.WebUrl, s.cfg.JWTSecret = "ses", "https://api.one.test", "https://app.one.test", "test-secret"
	mustExec(t, db, `UPDATE organizations SET postal_address='1 Main St' WHERE id=1;
		UPDATE email_templates SET subject='Hi {{firstName}}', html_body='<p>Hello {{firstName}} <a href="https://one.test/x">x</a></p>' WHERE id=1`)
	fake := &senderTestProvider{quota: &provider.SendQuota{Max24HourSend: -1, MaxSendRate: 100}}
	return db, s, NewAutomationExecutor(db, s.cfg, NewAutomationMailer(db, s.cfg)), newAutomationMailRunner(db, s.cfg, fake), fake
}

func mailOnce(t *testing.T, r *automationMailRunner) int {
	t.Helper()
	n, err := r.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var (
	openPixelRe = regexp.MustCompile(`/api/v1/tracking/open/([A-Za-z0-9_-]+)\.gif`)
	unsubHdrRe  = regexp.MustCompile(`/api/v1/unsubscribe/([A-Za-z0-9_=-]+)>`)
)

// TestAutomationEmailEndToEnd: trigger -> email -> wait 1m -> if opened:
// set opened=yes, else set opened=no. Email is queued in the step
// transaction and only sent by the mailer after commit.
func TestAutomationEmailEndToEnd(t *testing.T) {
	db, s, x, r, fake := automationMailFixture(t)
	ctx := context.Background()
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
		{"w", "delay", map[string]any{"duration": 1, "unit": "minutes"}},
		{"c", "condition", map[string]any{"field": "email_opened", "emailNodeId": "e"}},
		{"y", "action", map[string]any{"action": "update_field", "attribute": "opened", "value": "yes"}},
		{"n", "action", map[string]any{"action": "update_field", "attribute": "opened", "value": "no"}},
	}, [][3]string{{"t", "e", ""}, {"e", "w", ""}, {"w", "c", ""}, {"c", "y", "yes"}, {"c", "n", "no"}})
	a := publishGraph(t, s, w, "never")
	mustExec(t, db, `INSERT INTO contacts(id,org_id,email,first_name,status,updated_at) VALUES(100,1,'c100@example.net','<b>Ann</b>','active',now()),
		(101,1,'c101@example.net','Bo','active',now())`)
	autoSubscribe(t, db, 1, 100, "api")
	autoSubscribe(t, db, 1, 101, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)

	// Queued with the step, nothing sent before the mailer runs.
	count(t, db, 2, `SELECT count(*) FROM automation_messages WHERE status='pending' AND node_id='e'`)
	count(t, db, 2, `SELECT count(*) FROM automation_step_runs r JOIN automation_messages m ON m.id=r.message_id WHERE r.node_id='e' AND r.status='succeeded' AND r.outcome='queued'`)
	if fake.callCount("") != 0 {
		t.Fatal("SES was called inside the step")
	}

	// Replaying the email step (lost step run) returns the same queued row.
	var msgID int64
	if err := db.QueryRow(`SELECT id FROM automation_messages WHERE contact_id=100`).Scan(&msgID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `DELETE FROM automation_step_runs WHERE node_id IN ('e','w') AND enrollment_id=(SELECT id FROM automation_enrollments WHERE contact_id=100)`)
	mustExec(t, db, `UPDATE automation_enrollments SET current_node_id='e', next_run_at=now()-interval '1 second' WHERE contact_id=100`)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=100`)
	count(t, db, 1, `SELECT count(*) FROM automation_step_runs WHERE node_id='e' AND message_id=$1`, msgID)

	// The mailer sends each message once, through the M3 renderer.
	if n := mailOnce(t, r); n != 2 {
		t.Fatalf("claimed %d", n)
	}
	mailOnce(t, r)
	if fake.callCount("c100@example.net") != 1 || fake.callCount("c101@example.net") != 1 {
		t.Fatalf("calls: %v", fake.calls)
	}
	count(t, db, 2, `SELECT count(*) FROM automation_messages WHERE status='sent' AND provider_message_id LIKE 'ses-%' AND quota_reserved AND attempts=1`)
	var msg *provider.EmailMessage
	for _, m := range fake.msgs {
		if m.To[0] == "c100@example.net" {
			msg = m
		}
	}
	if msg.From != `<news@one.test>` || msg.Subject != "Hi <b>Ann</b>" || !strings.Contains(msg.HTMLBody, "Hello &lt;b&gt;Ann&lt;/b&gt;") ||
		!strings.Contains(msg.HTMLBody, "1 Main St") || !strings.Contains(msg.HTMLBody, "/api/v1/tracking/click/") ||
		msg.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || msg.Headers["X-Mailat-Message-ID"] == "" {
		t.Fatalf("rendered message: %+v", msg)
	}

	// An open recorded through the pixel feeds the condition.
	tracking := NewTrackingService(db, s.cfg)
	pixel := openPixelRe.FindStringSubmatch(msg.HTMLBody)
	if pixel == nil {
		t.Fatal("no open pixel")
	}
	if err := tracking.ProcessOpenEvent(ctx, pixel[1], "203.0.113.1", "UA"); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT open_count FROM automation_messages WHERE id=$1 AND first_opened_at IS NOT NULL`, msgID)
	count(t, db, 1, `SELECT count(*) FROM automation_message_events WHERE message_id=$1 AND event_type='open'`, msgID)
	count(t, db, 0, `SELECT count(*) FROM campaign_events`)

	x.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	mustExec(t, db, `UPDATE automation_enrollments SET next_run_at=now()-interval '1 second'`)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM contacts WHERE id=100 AND attributes->>'opened'='yes'`)
	count(t, db, 1, `SELECT count(*) FROM contacts WHERE id=101 AND attributes->>'opened'='no'`)
	stats, err := s.GetAutomationStats(ctx, 1, a.UUID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := stats.Nodes["e"]; n.Sent != 2 || n.Opened != 1 || stats.Completed != 2 {
		t.Fatalf("stats: %+v", stats)
	}

	// One-click unsubscribe links the message and suppresses the address.
	hdr := unsubHdrRe.FindStringSubmatch(msg.Headers["List-Unsubscribe"])
	if hdr == nil {
		t.Fatalf("List-Unsubscribe: %q", msg.Headers["List-Unsubscribe"])
	}
	if err := NewComplianceService(db, s.cfg).ProcessOneClickUnsubscribe(ctx, hdr[1], "203.0.113.1", "UA"); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE id=$1 AND unsubscribed_at IS NOT NULL`, msgID)
	count(t, db, 1, `SELECT count(*) FROM suppressions WHERE email='c100@example.net' AND source_type='automation'`)
	count(t, db, 1, `SELECT count(*) FROM contacts WHERE id=100 AND status='unsubscribed'`)
}

// TestAutomationEmailExitsAndRecheck: unsubscribing mid-flow exits the
// enrollment, and a queued message whose recipient became ineligible is
// skipped by the mailer without reaching SES.
func TestAutomationEmailExitsAndRecheck(t *testing.T) {
	db, s, x, r, fake := automationMailFixture(t)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"w", "delay", map[string]any{"duration": 1, "unit": "minutes"}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
	}, [][3]string{{"t", "w", ""}, {"w", "e", ""}})
	publishGraph(t, s, w, "never")
	for _, id := range []int{100, 101, 102, 103} {
		addAutoContact(t, db, id, "active", `{}`)
		autoSubscribe(t, db, 1, id, "api")
	}
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)

	// Mid-flow: 100 unsubscribes and 101 is suppressed during the wait.
	mustExec(t, db, `UPDATE contacts SET status='unsubscribed' WHERE id=100;
		INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'c101@example.net','unsubscribe','manual')`)
	x.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	mustExec(t, db, `UPDATE automation_enrollments SET next_run_at=now()-interval '1 second'`)
	mustRun(t, x.StepDue)
	if got := enrollmentState(t, db, 100); got != "exited/w/unsubscribed" {
		t.Fatalf("100: %s", got)
	}
	if got := enrollmentState(t, db, 101); got != "exited/w/suppressed" {
		t.Fatalf("101: %s", got)
	}
	count(t, db, 2, `SELECT count(*) FROM automation_messages WHERE status='pending'`)

	// After queueing: 102 is suppressed (transactional list), 103 changes address.
	mustExec(t, db, `INSERT INTO suppression_list(org_id,email) VALUES(1,'C102@example.net');
		UPDATE contacts SET email='new103@example.net' WHERE id=103`)
	mailOnce(t, r)
	if fake.callCount("") != 0 {
		t.Fatalf("sent to ineligible recipients: %v", fake.calls)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=102 AND status='skipped' AND skip_reason='suppressed' AND NOT quota_reserved`)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=103 AND status='skipped' AND skip_reason='email_changed'`)
}

// TestAutomationMailerCrashRecovery: an expired claim is sent once; a send
// interrupted after the start is never repeated; archive cancels the queue.
func TestAutomationMailerCrashRecovery(t *testing.T) {
	db, s, x, r, fake := automationMailFixture(t)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
	}, [][3]string{{"t", "e", ""}})
	a := publishGraph(t, s, w, "never")
	for _, id := range []int{100, 101, 102} {
		addAutoContact(t, db, id, "active", `{}`)
		autoSubscribe(t, db, 1, id, "api")
	}
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	count(t, db, 3, `SELECT count(*) FROM automation_messages WHERE status='pending'`)

	// 100: a crashed worker's claim expired. 101: a worker crashed mid-send.
	mustExec(t, db, `UPDATE automation_messages SET status='claimed', lease_owner=gen_random_uuid(), lease_expires_at=now()-interval '1 second' WHERE contact_id=100;
		UPDATE automation_messages SET status='sending', quota_reserved=true, attempts=1, attempt_started_at=now()-interval '11 minutes' WHERE contact_id=101`)
	// 102: another worker holds a live claim.
	mustExec(t, db, `UPDATE automation_messages SET status='claimed', lease_owner=gen_random_uuid(), lease_expires_at=now()+interval '1 minute' WHERE contact_id=102`)
	mailOnce(t, r)
	mailOnce(t, r)
	if fake.callCount("c100@example.net") != 1 || fake.callCount("c101@example.net") != 0 || fake.callCount("c102@example.net") != 0 {
		t.Fatalf("calls: %v", fake.calls)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=100 AND status='sent'`)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=101 AND status='unknown' AND error LIKE '%interrupted%'`)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=102 AND status='claimed'`)

	// SES feedback for the unknown send proves acceptance.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var uuid101 string
	if err := tx.QueryRow(`SELECT message_uuid::text FROM automation_messages WHERE contact_id=101`).Scan(&uuid101); err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Delivery"}
	n.Mail.MessageId = "ses-late"
	if err := applyAutomationFeedback(context.Background(), tx, 1, n, uuid101); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=101 AND status='sent' AND delivery_status='delivered' AND provider_message_id='ses-late'`)

	// A throttled send goes back to pending; archive then cancels it.
	mustExec(t, db, `UPDATE automation_messages SET status='pending', lease_owner=NULL, lease_expires_at=NULL WHERE contact_id=102`)
	fake.script = func(int, string) error { return apiErr("TooManyRequestsException") }
	mailOnce(t, r)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=102 AND status='pending' AND next_attempt_at>now() AND quota_reserved`)
	if _, _, err := s.ArchiveAutomation(context.Background(), 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=102 AND status='cancelled'`)
}

// TestAutomationEmailPermanentFailures: missing SES or postal address fail
// the email step at once without queueing anything.
func TestAutomationEmailPermanentFailures(t *testing.T) {
	db, s, x, _, _ := automationMailFixture(t)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
	}, [][3]string{{"t", "e", ""}})
	publishGraph(t, s, w, "never")

	mustExec(t, db, `UPDATE organizations SET postal_address=NULL WHERE id=1`)
	addAutoContact(t, db, 100, "active", `{}`)
	autoSubscribe(t, db, 1, 100, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=100 AND status='failed' AND error_message LIKE '%postal address%'`)

	mustExec(t, db, `UPDATE organizations SET postal_address='1 Main St' WHERE id=1`)
	s.cfg.EmailProvider = "smtp"
	addAutoContact(t, db, 101, "active", `{}`)
	autoSubscribe(t, db, 1, 101, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=101 AND status='failed' AND error_message LIKE '%Amazon SES%'`)
	count(t, db, 0, `SELECT count(*) FROM automation_messages`)
}
