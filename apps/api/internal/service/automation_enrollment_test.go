package service

import (
	"context"
	"errors"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
)

func contactUUID(t *testing.T, s *AutomationService, id int) string {
	t.Helper()
	var u string
	if err := s.db.QueryRow(`SELECT uuid::text FROM contacts WHERE id=$1`, id).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

func enrollmentUUID(t *testing.T, s *AutomationService, contactID int) string {
	t.Helper()
	var u string
	if err := s.db.QueryRow(`SELECT uuid::text FROM automation_enrollments WHERE contact_id=$1 ORDER BY id DESC LIMIT 1`, contactID).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

// TestAutomationManualEnrollment: manual enroll of a contact or a list,
// enrollment list/detail, cancel and retry.
func TestAutomationManualEnrollment(t *testing.T) {
	db, s, x, _, _ := automationMailFixture(t)
	ctx := context.Background()
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "manual"}},
		{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
		{"w", "delay", map[string]any{"duration": 1, "unit": "days"}},
		{"u", "action", map[string]any{"action": "update_field", "attribute": "seen", "value": "1"}},
	}, [][3]string{{"t", "e", ""}, {"e", "w", ""}, {"w", "u", ""}})
	a, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: "Manual", Workflow: w})
	if err != nil {
		t.Fatal(err)
	}
	c1 := contactUUID(t, s, 1)
	_, err = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ContactUUID: c1})
	wantAutomationErr(t, err, "Only active automations")
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err != nil {
		t.Fatal(err)
	}
	_, err = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{})
	wantAutomationErr(t, err, "exactly one")
	_, err = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ContactUUID: c1, ListUUID: autoListA})
	wantAutomationErr(t, err, "exactly one")
	_, err = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ListUUID: autoListOther})
	wantAutomationErr(t, err, "List not found")
	if _, err = s.Enroll(ctx, 2, a.UUID, &model.EnrollRequest{ContactUUID: c1}); !errors.Is(err, ErrAutomationNotFound) {
		t.Fatalf("another org enrolled: %v", err)
	}

	res, err := s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ContactUUID: c1})
	if err != nil || res.Enrolled != 1 || res.Skipped != 0 {
		t.Fatalf("contact enroll: %+v %v", res, err)
	}
	if res, _ = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ContactUUID: c1}); res.Enrolled != 0 || res.Skipped != 1 {
		t.Fatalf("an active contact was enrolled twice: %+v", res)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=1 AND current_node_id='e' AND trigger_event_id IS NULL
		AND version_id=(SELECT published_version_id FROM automations WHERE uuid=$1)`, a.UUID)

	// List: one new, one already enrolled, one unsubscribed, one suppressed.
	addAutoContact(t, db, 100, "active", `{}`)
	addAutoContact(t, db, 101, "unsubscribed", `{}`)
	addAutoContact(t, db, 102, "active", `{}`)
	mustExec(t, db, `INSERT INTO suppression_list(org_id,email) VALUES(1,'C102@example.net')`)
	for _, id := range []int{1, 100, 101, 102} {
		autoSubscribe(t, db, 1, id, "manual")
	}
	maxManualListEnroll = 1
	_, err = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ListUUID: autoListA})
	maxManualListEnroll = 50000
	wantAutomationErr(t, err, "more than 1 eligible")
	if res, err = s.Enroll(ctx, 1, a.UUID, &model.EnrollRequest{ListUUID: autoListA}); err != nil || res.Enrolled != 1 || res.Skipped != 3 {
		t.Fatalf("list enroll: %+v %v", res, err)
	}
	count(t, db, 2, `SELECT count(*) FROM automation_enrollments`)

	mustRun(t, x.StepDue)
	if got := stepRuns(t, db, 1); got != "e:succeeded:queued,w:waiting:" {
		t.Fatalf("steps: %s", got)
	}

	// List and filters.
	list, err := s.ListEnrollments(ctx, 1, a.UUID, "", 0, 1000)
	if err != nil || list.Total != 2 || len(list.Enrollments) != 2 || list.Page != 1 || list.PageSize != 100 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if e := list.Enrollments[1]; e.ContactEmail != "a@example.net" || e.Status != "active" || *e.CurrentNodeID != "w" || e.NextRunAt == nil || *e.Version != 1 {
		t.Fatalf("enrollment view: %+v", e)
	}
	for status, want := range map[string]int{"waiting": 2, "active": 2, "completed": 0} {
		if l, err := s.ListEnrollments(ctx, 1, a.UUID, status, 1, 20); err != nil || l.Total != want {
			t.Fatalf("status %s: %+v %v", status, l, err)
		}
	}
	_, err = s.ListEnrollments(ctx, 1, a.UUID, "bogus", 1, 20)
	wantAutomationErr(t, err, "status must be")
	if _, err = s.ListEnrollments(ctx, 2, a.UUID, "", 1, 20); !errors.Is(err, ErrAutomationNotFound) {
		t.Fatalf("another org listed enrollments: %v", err)
	}

	// Detail with the step timeline and the queued email's public id.
	e1 := enrollmentUUID(t, s, 1)
	d, err := s.GetEnrollment(ctx, 1, a.UUID, e1)
	if err != nil || len(d.Steps) != 2 || d.Steps[0].NodeID != "e" || d.Steps[0].MessageUUID == nil || d.Steps[1].Status != "waiting" || d.Steps[1].ResumeAt == nil {
		t.Fatalf("detail: %+v %v", d, err)
	}
	other, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: "Other", Workflow: w})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not-a-uuid", "e0000000-0000-4000-8000-000000000000"} {
		if _, err := s.GetEnrollment(ctx, 1, a.UUID, bad); !errors.Is(err, ErrAutomationEnrollmentNotFound) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, err := s.GetEnrollment(ctx, 1, other.UUID, e1); !errors.Is(err, ErrAutomationEnrollmentNotFound) {
		t.Fatalf("enrollment found through another automation: %v", err)
	}
	if _, err := s.GetEnrollment(ctx, 2, a.UUID, e1); !errors.Is(err, ErrAutomationNotFound) {
		t.Fatalf("enrollment found from another org: %v", err)
	}

	// Cancel: the unclaimed queued email is cancelled and its quota refunded.
	mustExec(t, db, `UPDATE organizations SET monthly_email_limit=10 WHERE id=1;
		INSERT INTO organization_send_usage(org_id,month,attempts) VALUES(1,`+monthlyUsageMonth+`,1);
		UPDATE automation_messages SET quota_reserved=true WHERE contact_id=1`)
	c, err := s.CancelEnrollment(ctx, 1, a.UUID, e1)
	if err != nil || c.Status != "cancelled" || *c.ExitReason != "manual" || c.NextRunAt != nil {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=1 AND status='cancelled' AND NOT quota_reserved`)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE contact_id=100 AND status='pending'`)
	count(t, db, 0, `SELECT attempts FROM organization_send_usage WHERE org_id=1`)
	_, err = s.CancelEnrollment(ctx, 1, a.UUID, e1)
	wantAutomationErr(t, err, "Only active")

	// Retry: a failed enrollment resumes at its failed step on the same version.
	e100 := enrollmentUUID(t, s, 100)
	mustExec(t, db, `UPDATE automation_enrollments SET status='failed', retry_count=5, error_message='Step w: boom' WHERE contact_id=100;
		UPDATE automation_step_runs SET status='failed', error='boom', resume_at=NULL WHERE node_id='w'
		  AND enrollment_id=(SELECT id FROM automation_enrollments WHERE contact_id=100)`)
	_, err = s.RetryEnrollment(ctx, 1, a.UUID, e1)
	wantAutomationErr(t, err, "Only failed")
	if _, err := s.PauseAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	_, err = s.RetryEnrollment(ctx, 1, a.UUID, e100)
	wantAutomationErr(t, err, "Activate the automation")
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, false); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status,version_id,current_node_id,next_run_at,updated_at)
		SELECT automation_id,contact_id,org_id,'active',version_id,'u',now()+interval '1 day',now() FROM automation_enrollments WHERE uuid=$1`, e100)
	if _, err := s.RetryEnrollment(ctx, 1, a.UUID, e100); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("retry beside an active enrollment: %v", err)
	}
	mustExec(t, db, `DELETE FROM automation_enrollments WHERE contact_id=100 AND status='active'`)
	r, err := s.RetryEnrollment(ctx, 1, a.UUID, e100)
	if err != nil || r.Status != "active" || r.RetryCount != 0 || r.Error != nil || *r.CurrentNodeID != "w" {
		t.Fatalf("retry: %+v %v", r, err)
	}
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_step_runs r JOIN automation_enrollments e ON e.id=r.enrollment_id
		WHERE e.uuid=$1 AND r.node_id='w' AND r.status='waiting' AND r.error IS NULL AND r.resume_at > now()`, e100)
	if got := enrollmentState(t, db, 100); got != "active/w/" {
		t.Fatalf("retried enrollment: %s", got)
	}
}
