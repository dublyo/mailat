package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

func TestAutomationRetryDelay(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}
	for i, w := range want {
		if d, fail := automationRetryDelay(i); d != w || fail {
			t.Fatalf("retry %d: %v %v", i, d, fail)
		}
	}
	if _, fail := automationRetryDelay(5); !fail {
		t.Fatal("the 6th failure must fail the enrollment")
	}
}

func TestClassifyStepError(t *testing.T) {
	for _, c := range []struct {
		err       error
		permanent bool
	}{
		{permanentStep("gone"), true},
		{fmt.Errorf("wrap: %w", &provider.MailValidationError{Message: "bad"}), true},
		{errors.New("connection reset"), false},
		{sql.ErrConnDone, false},
	} {
		if _, p := classifyStepError(c.err); p != c.permanent {
			t.Fatalf("%v: permanent=%v", c.err, p)
		}
	}
	if msg, _ := classifyStepError(errors.New("pq: secret@example.net")); msg != "Temporary error; the step will be retried" {
		t.Fatalf("raw error leaked into the stored message: %q", msg)
	}
}

type graphNode struct {
	id, kind string
	cfg      map[string]any
}

// buildGraph makes a decoded workflow; edges are {source, target, handle}.
func buildGraph(t *testing.T, nodes []graphNode, edges [][3]string) *model.Workflow {
	t.Helper()
	w := &model.Workflow{}
	for i, n := range nodes {
		w.Nodes = append(w.Nodes, model.WorkflowNode{ID: n.id, Type: "workflow", Position: model.WorkflowPosition{Y: float64(i)},
			Data: model.WorkflowNodeData{Label: n.id, Type: n.kind, Config: n.cfg}})
	}
	for _, e := range edges {
		w.Edges = append(w.Edges, model.WorkflowEdge{ID: e[0] + e[1], Source: e[0], Target: e[1], SourceHandle: e[2]})
	}
	b, _ := json.Marshal(w)
	out := &model.Workflow{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func publishGraph(t *testing.T, s *AutomationService, w *model.Workflow, reentry string) *model.Automation {
	t.Helper()
	ctx := context.Background()
	a, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: "Flow", Workflow: w, ReentryPolicy: &reentry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err != nil {
		t.Fatal(err)
	}
	return a
}

func addAutoContact(t *testing.T, db *sql.DB, id int, status string, attrs string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO contacts(id,org_id,email,status,attributes,updated_at) VALUES($1,1,$2,$3,$4,now())`,
		id, fmt.Sprintf("c%d@example.net", id), status, attrs)
}

func autoSubscribe(t *testing.T, db *sql.DB, listID, contactID int, source string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO list_contacts(list_id,contact_id,source) VALUES($1,$2,$3)`, listID, contactID, source)
}

func mustRun(t *testing.T, f func(context.Context) (int, error)) int {
	t.Helper()
	n, err := f(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func stepRuns(t *testing.T, db *sql.DB, contactID int) string {
	t.Helper()
	rows, err := db.Query(`SELECT r.node_id||':'||r.status||':'||COALESCE(r.outcome,'') FROM automation_step_runs r
		JOIN automation_enrollments e ON e.id=r.enrollment_id WHERE e.contact_id=$1 ORDER BY r.id`, contactID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if out != "" {
			out += ","
		}
		out += s
	}
	return out
}

func enrollmentState(t *testing.T, db *sql.DB, contactID int) string {
	t.Helper()
	var status, node, reason string
	if err := db.QueryRow(`SELECT status, COALESCE(current_node_id,''), COALESCE(exit_reason,'') FROM automation_enrollments
		WHERE contact_id=$1 ORDER BY id DESC LIMIT 1`, contactID).Scan(&status, &node, &reason); err != nil {
		t.Fatal(err)
	}
	return status + "/" + node + "/" + reason
}

func TestAutomationEnroller(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"u", "action", map[string]any{"action": "update_field", "attribute": "seen", "value": "1"}},
	}, [][3]string{{"t", "u", ""}})
	a := publishGraph(t, s, w, "never")

	addAutoContact(t, db, 100, "active", `{}`)
	addAutoContact(t, db, 101, "active", `{}`)
	addAutoContact(t, db, 102, "active", `{}`)
	addAutoContact(t, db, 103, "unsubscribed", `{}`)
	addAutoContact(t, db, 104, "active", `{}`)
	addAutoContact(t, db, 105, "active", `{}`)
	mustExec(t, db, `INSERT INTO suppression_list(org_id,email) VALUES(1,'C104@example.net')`)
	autoSubscribe(t, db, 1, 100, "api")    // enrolls
	autoSubscribe(t, db, 1, 101, "import") // import is opt-in
	autoSubscribe(t, db, 2, 102, "api")    // another list
	autoSubscribe(t, db, 1, 103, "api")    // inactive
	autoSubscribe(t, db, 1, 104, "api")    // suppressed
	// An event older than the activation is ignored.
	mustExec(t, db, `INSERT INTO automation_trigger_events(org_id,event_type,contact_id,list_id,source,occurred_at)
		VALUES(1,'contact.subscribed',105,1,'api',now()-interval '1 day')`)

	if n := mustRun(t, x.EnrollPending); n != 6 {
		t.Fatalf("processed %d events", n)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments`)
	var v1 int64
	if err := db.QueryRow(`SELECT published_version_id FROM automations WHERE id=$1`, a.ID).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=100 AND status='active' AND current_node_id='u' AND version_id=$1 AND trigger_event_id IS NOT NULL`, v1)
	count(t, db, 0, `SELECT count(*) FROM automation_trigger_events WHERE processed_at IS NULL`)

	// Replaying the same events enrolls nobody twice.
	mustExec(t, db, `UPDATE automation_trigger_events SET processed_at=NULL`)
	mustRun(t, x.EnrollPending)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments`)

	// never: a finished contact who rejoins is not enrolled again.
	mustExec(t, db, `UPDATE automation_enrollments SET status='completed' WHERE contact_id=100`)
	mustExec(t, db, `DELETE FROM list_contacts WHERE list_id=1 AND contact_id=100`)
	autoSubscribe(t, db, 1, 100, "api")
	mustRun(t, x.EnrollPending)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=100`)

	// after_exit (published as v2): blocked during the 24h cooldown, then allowed on v2.
	reentry := "after_exit"
	if _, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{ReentryPolicy: &reentry}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err != nil {
		t.Fatal(err)
	}
	rejoin := func() {
		mustExec(t, db, `DELETE FROM list_contacts WHERE list_id=1 AND contact_id=100`)
		autoSubscribe(t, db, 1, 100, "api")
		mustRun(t, x.EnrollPending)
	}
	rejoin()
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=100`)
	mustExec(t, db, `UPDATE automation_enrollments SET enrolled_at=now()-interval '25 hours' WHERE contact_id=100`)
	rejoin()
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=100 AND status='active' AND version_id<>$1`, v1)

	// Paused: the trigger records nothing and nothing is enrolled.
	if _, err := s.PauseAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	autoSubscribe(t, db, 1, 105, "api")
	count(t, db, 0, `SELECT count(*) FROM automation_trigger_events WHERE processed_at IS NULL`)

	// Cleanup removes events processed more than a week ago.
	mustExec(t, db, `UPDATE automation_trigger_events SET processed_at=now()-interval '8 days' WHERE contact_id<>100`)
	x.cleanup(ctx)
	count(t, db, 0, `SELECT count(*) FROM automation_trigger_events WHERE contact_id<>100`)
	count(t, db, 4, `SELECT count(*) FROM automation_trigger_events WHERE contact_id=100`)
}

// TestAutomationExecutorEndToEnd runs trigger -> wait 1m -> if plan=pro:
// add to list B, else set nurture=true, with no email step.
func TestAutomationExecutorEndToEnd(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"w", "delay", map[string]any{"duration": 1, "unit": "minutes"}},
		{"c", "condition", map[string]any{"field": "custom_field", "attribute": "plan", "operator": "equals", "value": "PRO"}},
		{"a", "action", map[string]any{"action": "add_to_list", "listUuid": autoListB}},
		{"u", "action", map[string]any{"action": "update_field", "attribute": "nurture", "value": "true"}},
	}, [][3]string{{"t", "w", ""}, {"w", "c", ""}, {"c", "a", "yes"}, {"c", "u", "no"}})
	a := publishGraph(t, s, w, "never")
	addAutoContact(t, db, 100, "active", `{"plan":"pro"}`)
	addAutoContact(t, db, 101, "active", `{"plan":"free"}`)
	autoSubscribe(t, db, 1, 100, "signup_form")
	autoSubscribe(t, db, 1, 101, "signup_form")
	mustRun(t, x.EnrollPending)

	if n := mustRun(t, x.StepDue); n != 2 {
		t.Fatalf("claimed %d", n)
	}
	if got := enrollmentState(t, db, 100); got != "active/w/" {
		t.Fatalf("after first step: %s", got)
	}
	count(t, db, 2, `SELECT count(*) FROM automation_step_runs WHERE node_id='w' AND status='waiting' AND resume_at > now()`)
	count(t, db, 0, `SELECT count(*) FROM automation_enrollments WHERE claim_token IS NOT NULL OR locked_until IS NOT NULL`)
	if n := mustRun(t, x.StepDue); n != 0 {
		t.Fatalf("claimed %d before the wait ended", n)
	}

	// Time passes.
	x.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	mustExec(t, db, `UPDATE automation_enrollments SET next_run_at=now()-interval '1 second'`)
	mustRun(t, x.StepDue)

	if got := stepRuns(t, db, 100); got != "w:succeeded:,c:succeeded:yes,a:succeeded:added" {
		t.Fatalf("pro runs: %s", got)
	}
	if got := stepRuns(t, db, 101); got != "w:succeeded:,c:succeeded:no,u:succeeded:updated" {
		t.Fatalf("free runs: %s", got)
	}
	count(t, db, 2, `SELECT count(*) FROM automation_enrollments WHERE status='completed' AND completed_at IS NOT NULL AND claim_token IS NULL`)
	count(t, db, 1, `SELECT contact_count FROM lists WHERE id=2`)
	count(t, db, 1, `SELECT count(*) FROM list_contacts WHERE list_id=2 AND contact_id=100 AND source='automation'`)
	count(t, db, 1, `SELECT count(*) FROM contacts WHERE id=101 AND attributes->>'nurture'='true' AND attributes->>'plan'='free'`)

	stats, err := s.GetAutomationStats(ctx, 1, a.UUID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 2 || stats.Nodes["c"].Yes != 1 || stats.Nodes["c"].No != 1 || stats.Nodes["w"].Succeeded != 2 || stats.Nodes["a"].Entered != 1 {
		t.Fatalf("stats: %+v", stats)
	}

	// Replay safety: a committed run is advanced past, never repeated.
	mustExec(t, db, `UPDATE automation_enrollments SET status='active', current_node_id='a', next_run_at=now()-interval '1 second' WHERE contact_id=100`)
	mustExec(t, db, `DELETE FROM list_contacts WHERE list_id=2`)
	mustRun(t, x.StepDue)
	count(t, db, 0, `SELECT count(*) FROM list_contacts WHERE list_id=2`)
	if got := enrollmentState(t, db, 100); got != "completed/a/" {
		t.Fatalf("replay: %s", got)
	}
}

func TestAutomationExecutorExitsAndLifecycle(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.created"}},
		{"u", "action", map[string]any{"action": "update_field", "attribute": "k", "value": "v"}},
		{"r", "action", map[string]any{"action": "remove_from_list", "listUuid": autoListB}},
	}, [][3]string{{"t", "u", ""}, {"u", "r", ""}})
	a := publishGraph(t, s, w, "never")
	for id := 100; id < 104; id++ {
		mustExec(t, db, `INSERT INTO contacts(id,org_id,email,created_source,updated_at) VALUES($1,1,$2,'api',now())`, id, fmt.Sprintf("c%d@example.net", id))
	}
	mustRun(t, x.EnrollPending)
	count(t, db, 4, `SELECT count(*) FROM automation_enrollments WHERE status='active'`)

	// Paused: nothing is claimed; resume continues.
	if _, err := s.PauseAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	if n := mustRun(t, x.StepDue); n != 0 {
		t.Fatalf("claimed %d while paused", n)
	}
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, false); err != nil {
		t.Fatal(err)
	}

	mustExec(t, db, `UPDATE contacts SET status='unsubscribed' WHERE id=100`)
	mustExec(t, db, `INSERT INTO suppression_list(org_id,email) VALUES(1,'c101@example.net')`)
	mustExec(t, db, `UPDATE contacts SET status='bounced' WHERE id=102`)
	autoSubscribe(t, db, 2, 103, "api")
	mustRun(t, x.StepDue)
	for id, want := range map[int]string{100: "exited/u/unsubscribed", 101: "exited/u/suppressed", 102: "exited/u/inactive", 103: "completed/r/"} {
		if got := enrollmentState(t, db, id); got != want {
			t.Fatalf("contact %d: %s, want %s", id, got, want)
		}
	}
	if got := stepRuns(t, db, 103); got != "u:succeeded:updated,r:succeeded:removed" {
		t.Fatalf("runs: %s", got)
	}
	count(t, db, 0, `SELECT contact_count FROM lists WHERE id=2`)
}

func TestAutomationExecutorFailures(t *testing.T) {
	db, s := newAutomationFixture(t)
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
	}, [][3]string{{"t", "e", ""}})
	publishGraph(t, s, w, "never")
	addAutoContact(t, db, 100, "active", `{}`)
	addAutoContact(t, db, 101, "active", `{}`)
	addAutoContact(t, db, 102, "active", `{}`)
	autoSubscribe(t, db, 1, 100, "api")
	mustRun(t, x.EnrollPending)

	// Without a sender the email step fails permanently.
	mustRun(t, x.StepDue)
	if got := enrollmentState(t, db, 100); got != "failed/e/" {
		t.Fatalf("nil sender: %s", got)
	}
	if got := stepRuns(t, db, 100); got != "e:failed:" {
		t.Fatalf("runs: %s", got)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_step_runs WHERE error='Email sending is not configured'`)

	// A transient error retries with backoff and fails after the last retry.
	fake := &fakeAutomationSender{err: errors.New("connection reset")}
	x.sender = fake
	autoSubscribe(t, db, 1, 101, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=101 AND status='active' AND retry_count=1
		AND next_run_at BETWEEN now()+interval '50 seconds' AND now()+interval '70 seconds' AND claim_token IS NULL`)
	count(t, db, 0, `SELECT count(*) FROM automation_step_runs r JOIN automation_enrollments e ON e.id=r.enrollment_id WHERE e.contact_id=101`)
	mustExec(t, db, `UPDATE automation_enrollments SET retry_count=5, next_run_at=now()-interval '1 second' WHERE contact_id=101`)
	mustRun(t, x.StepDue)
	if got := enrollmentState(t, db, 101); got != "failed/e/" {
		t.Fatalf("exhausted: %s", got)
	}

	// Invalid mail fails at once; a suppressed recipient exits.
	fake.err = &provider.MailValidationError{Message: "template has no body"}
	autoSubscribe(t, db, 1, 102, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=102 AND status='failed' AND retry_count=0 AND error_message LIKE '%template has no body'`)
	mustExec(t, db, `DELETE FROM automation_enrollments WHERE contact_id=102`)
	mustExec(t, db, `UPDATE automation_trigger_events SET processed_at=NULL WHERE contact_id=102`)
	fake.err = ErrAutomationRecipientSuppressed
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	if got := enrollmentState(t, db, 102); got != "exited/e/suppressed" {
		t.Fatalf("suppressed: %s", got)
	}

	// Leaving the trigger list skips the email.
	fake.err = nil
	mustExec(t, db, `DELETE FROM automation_enrollments`)
	mustExec(t, db, `UPDATE automation_trigger_events SET processed_at=NULL WHERE contact_id=100`)
	mustRun(t, x.EnrollPending)
	mustExec(t, db, `DELETE FROM list_contacts WHERE contact_id=100`)
	mustRun(t, x.StepDue)
	if got := enrollmentState(t, db, 100) + " " + stepRuns(t, db, 100); got != "exited/e/left_list e:skipped:left_list" {
		t.Fatalf("left list: %s", got)
	}

	// The identity changing hands after publish fails the step.
	mustExec(t, db, `DELETE FROM automation_enrollments`)
	mustExec(t, db, `UPDATE automation_trigger_events SET processed_at=NULL WHERE contact_id=101`)
	mustRun(t, x.EnrollPending)
	mustExec(t, db, `UPDATE identities SET user_id=2 WHERE uuid=$1`, autoIdentity)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=101 AND status='failed' AND error_message LIKE '%own sending identities'`)
	if len(fake.keys()) != 0 {
		t.Fatalf("queued %v", fake.keys())
	}
}

// fakeAutomationSender queues a row in the step transaction and records keys.
type fakeAutomationSender struct {
	mu    sync.Mutex
	err   error
	calls map[string]int
}

func (f *fakeAutomationSender) EnqueueAutomationEmail(ctx context.Context, tx *sql.Tx, m AutomationEmail) (int64, error) {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO automation_messages(org_id,automation_id,version_id,enrollment_id,node_id,contact_id,email,sender_user_id,identity_id,template_id)
		SELECT $1,$2,$3,$4,$5,c.id,c.email,$6,$7,$8 FROM contacts c WHERE c.id=$9
		ON CONFLICT (enrollment_id,node_id) DO UPDATE SET updated_at=now() RETURNING id`,
		m.OrgID, m.AutomationID, m.VersionID, m.EnrollmentID, m.NodeID, m.SenderUserID, m.IdentityID, m.TemplateID, m.ContactID).Scan(&id)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[m.IdempotencyKey]++
	f.mu.Unlock()
	return id, nil
}

func (f *fakeAutomationSender) keys() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.calls {
		out[k] = v
	}
	return out
}

func TestAutomationExecutorConcurrency(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()
	fake := &fakeAutomationSender{}
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
		{"a", "action", map[string]any{"action": "add_to_list", "listUuid": autoListB}},
	}, [][3]string{{"t", "e", ""}, {"e", "a", ""}})
	publishGraph(t, s, w, "never")
	mustExec(t, db, `INSERT INTO contacts(id,org_id,email,updated_at) SELECT g,1,'n'||g||'@example.net',now() FROM generate_series(100,199) g;
		INSERT INTO list_contacts(list_id,contact_id,source) SELECT 1,g,'api' FROM generate_series(100,199) g`)

	// Two enrollers and two steppers race over the same 100 contacts.
	x1, x2 := NewAutomationExecutor(db, s.cfg, fake), NewAutomationExecutor(db, s.cfg, fake)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for _, x := range []*AutomationExecutor{x1, x2} {
		wg.Add(1)
		go func(x *AutomationExecutor) {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				if _, err := x.EnrollPending(ctx); err != nil {
					errs <- err
				}
				if _, err := x.StepDue(ctx); err != nil {
					errs <- err
				}
			}
		}(x)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	count(t, db, 100, `SELECT count(*) FROM automation_enrollments WHERE status='completed'`)
	count(t, db, 100, `SELECT count(*) FROM automation_messages`)
	count(t, db, 100, `SELECT contact_count FROM lists WHERE id=2`)
	calls := fake.keys()
	if len(calls) != 100 {
		t.Fatalf("%d distinct emails", len(calls))
	}
	for k, n := range calls {
		if n != 1 {
			t.Fatalf("%s queued %d times", k, n)
		}
	}

	// An expired lease is reclaimed and resumes without repeating the email.
	mustExec(t, db, `UPDATE automation_enrollments SET status='active', current_node_id='e', next_run_at=now()-interval '1 minute',
		claim_token=gen_random_uuid(), locked_until=now()-interval '1 second' WHERE contact_id=100`)
	mustExec(t, db, `UPDATE automation_enrollments SET status='active', current_node_id='e', next_run_at=now()-interval '1 minute',
		claim_token=gen_random_uuid(), locked_until=now()+interval '1 minute' WHERE contact_id=101`)
	if n := mustRun(t, x1.StepDue); n != 1 {
		t.Fatalf("reclaimed %d, want only the expired lease", n)
	}
	if got := enrollmentState(t, db, 100); got != "completed/a/" {
		t.Fatalf("reclaimed: %s", got)
	}
	if calls := fake.keys(); len(calls) != 100 {
		t.Fatalf("replay queued again: %d", len(calls))
	}

	// A stale worker can neither release, fail nor step the new claim.
	var id int64
	var token string
	if err := db.QueryRow(`SELECT id, claim_token FROM automation_enrollments WHERE contact_id=101`).Scan(&id, &token); err != nil {
		t.Fatal(err)
	}
	stale := "00000000-0000-4000-8000-000000000000"
	x2.release(id, stale)
	if err := x2.fail(ctx, &enrollmentStep{id: id, nodeID: "e", kind: KindEmail}, stale, permanentStep("stale")); err != nil {
		t.Fatal(err)
	}
	if more, _, err := x2.step(ctx, id, stale); err != nil || more {
		t.Fatalf("stale step: %v %v", more, err)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE id=$1 AND claim_token=$2 AND status='active' AND current_node_id='e'`, id, token)
	x2.release(id, token)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE id=$1 AND claim_token IS NULL AND locked_until IS NULL`, id)
}

// TestAutomationLegacyEnrollmentsAndContacts: enrollments from before the
// executor (version_id NULL, cancelled by migration 014) do not block
// re-entry under 'never', and a legacy NULL engagement_score is read as 0.
func TestAutomationLegacyEnrollmentsAndContacts(t *testing.T) {
	db, s := newAutomationFixture(t)
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"c", "condition", map[string]any{"field": "engagement_score", "operator": "less_than", "value": "1"}},
		{"y", "action", map[string]any{"action": "update_field", "attribute": "low", "value": "yes"}},
		{"n", "action", map[string]any{"action": "update_field", "attribute": "low", "value": "no"}},
	}, [][3]string{{"t", "c", ""}, {"c", "y", "yes"}, {"c", "n", "no"}})
	a := publishGraph(t, s, w, "never")
	mustExec(t, db, `ALTER TABLE contacts ALTER COLUMN engagement_score DROP NOT NULL`)
	addAutoContact(t, db, 100, "active", `{}`)
	mustExec(t, db, `UPDATE contacts SET engagement_score=NULL WHERE id=100`)
	mustExec(t, db, `INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status,error_message,updated_at)
		SELECT id,100,1,'cancelled','Created before the automation executor existed',now() FROM automations WHERE uuid=$1`, a.UUID)
	autoSubscribe(t, db, 1, 100, "api")
	if n := mustRun(t, x.EnrollPending); n != 1 {
		t.Fatalf("enrolled %d", n)
	}
	mustRun(t, x.StepDue)
	if got := enrollmentState(t, db, 100); got != "completed/y/" {
		t.Fatalf("state: %s", got)
	}
	count(t, db, 1, `SELECT count(*) FROM contacts WHERE id=100 AND attributes->>'low'='yes'`)
}
