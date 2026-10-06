package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

const (
	autoListA      = "a0000000-0000-4000-8000-000000000001"
	autoListB      = "a0000000-0000-4000-8000-000000000002"
	autoListDyn    = "a0000000-0000-4000-8000-000000000003"
	autoListOther  = "a0000000-0000-4000-8000-000000000004"
	autoTemplate   = "b0000000-0000-4000-8000-000000000001"
	autoTplOff     = "b0000000-0000-4000-8000-000000000002"
	autoIdentity   = "c0000000-0000-4000-8000-000000000001"
	autoIdentityU2 = "c0000000-0000-4000-8000-000000000002"
	autoIdentityNV = "c0000000-0000-4000-8000-000000000003"
	autoWebhook    = "d0000000-0000-4000-8000-000000000001"
	autoWebhookU2  = "d0000000-0000-4000-8000-000000000002"
	autoWebhookOff = "d0000000-0000-4000-8000-000000000003"
)

func newAutomationFixture(t *testing.T) (*sql.DB, *AutomationService) {
	t.Helper()
	db := testutil.Database(t)
	mustExec(t, db, `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'QA','qa',now()),(2,'Other','other',now());
		INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'owner@one.test','x',now()),(2,1,'member@one.test','x',now()),(9,2,'o@two.test','x',now());
		INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,sending_feedback_ready,updated_at) VALUES
			(1,1,'one.test','t','active',true,true,now()),(2,1,'unverified.test','t','active',false,true,now());
		INSERT INTO identities(id,uuid,user_id,domain_id,email,can_send,updated_at) VALUES
			(1,'`+autoIdentity+`',1,1,'news@one.test',true,now()),(2,'`+autoIdentityU2+`',2,1,'m@one.test',true,now()),
			(3,'`+autoIdentityNV+`',1,2,'x@unverified.test',true,now());
		INSERT INTO email_templates(id,uuid,org_id,name,subject,html_body,is_active,updated_at) VALUES
			(1,'`+autoTemplate+`',1,'Welcome','Hi','<p>Hi</p>',true,now()),(2,'`+autoTplOff+`',1,'Old','Hi','<p>Hi</p>',false,now());
		INSERT INTO lists(id,uuid,org_id,name,type,updated_at) VALUES
			(1,'`+autoListA+`',1,'A','static',now()),(2,'`+autoListB+`',1,'B','static',now()),
			(3,'`+autoListDyn+`',1,'Dyn','dynamic',now()),(4,'`+autoListOther+`',2,'Foreign','static',now());
		INSERT INTO webhooks(id,uuid,org_id,user_id,name,url,secret,active,updated_at) VALUES
			(1,'`+autoWebhook+`',1,1,'Mine','https://hooks.example.net/a','s',true,now()),
			(2,'`+autoWebhookU2+`',1,2,'Theirs','https://hooks.example.net/b','s',true,now()),
			(3,'`+autoWebhookOff+`',1,1,'Off','https://hooks.example.net/c','s',false,now());
		INSERT INTO contacts(id,org_id,email,first_name,last_name,status,updated_at) VALUES(1,1,'a@example.net','A','A','active',now()),(2,1,'b@example.net','B','B','active',now());`)
	return db, NewAutomationService(db, &config.Config{})
}

// welcomeGraph: trigger(list A) -> email -> wait -> add to list B -> webhook.
func welcomeGraph(t *testing.T, delayDays int, mutate func(map[string]map[string]any)) *model.Workflow {
	t.Helper()
	cfg := map[string]map[string]any{
		"t": {"event": "contact.subscribed", "listUuid": autoListA},
		"e": {"templateUuid": autoTemplate, "identityUuid": autoIdentity},
		"w": {"duration": delayDays, "unit": "days"},
		"a": {"action": "add_to_list", "listUuid": autoListB},
		"h": {"webhookUuid": autoWebhook},
	}
	if mutate != nil {
		mutate(cfg)
	}
	kinds := map[string]string{"t": "trigger", "e": "email", "w": "delay", "a": "action", "h": "webhook"}
	w := &model.Workflow{}
	for i, id := range []string{"t", "e", "w", "a", "h"} {
		w.Nodes = append(w.Nodes, model.WorkflowNode{ID: id, Type: "workflow", Position: model.WorkflowPosition{X: 0, Y: float64(i)},
			Data: model.WorkflowNodeData{Label: id, Type: kinds[id], Config: cfg[id]}})
	}
	for _, e := range [][2]string{{"t", "e"}, {"e", "w"}, {"w", "a"}, {"a", "h"}} {
		w.Edges = append(w.Edges, model.WorkflowEdge{ID: e[0] + e[1], Source: e[0], Target: e[1]})
	}
	// Round-trip through JSON so numbers are float64 like a decoded request.
	b, _ := json.Marshal(w)
	out := &model.Workflow{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantAutomationErr(t *testing.T, err error, contains string) {
	t.Helper()
	var e *AutomationError
	if !errors.As(err, &e) || !strings.Contains(e.Message, contains) {
		t.Fatalf("want AutomationError %q, got %v", contains, err)
	}
}

func wantInvalid(t *testing.T, err error, nodeID, field string) {
	t.Helper()
	var invalid *AutomationInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("want validation errors, got %v", err)
	}
	for _, e := range invalid.Errors {
		if e.NodeID == nodeID && e.Field == field {
			return
		}
	}
	t.Fatalf("no %s/%s in %+v", nodeID, field, invalid.Errors)
}

func addEnrollment(t *testing.T, db *sql.DB, automationID int, contactID int, status string, versionID any, nextRun string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status,version_id,current_node_id,next_run_at,updated_at)
		VALUES($1,$2,1,$3,$4,'w',now()+$5::interval,now())`, automationID, contactID, status, versionID, nextRun)
}

func TestAutomationLifecycleAndVersioning(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()

	a, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: "  Welcome  ", Workflow: welcomeGraph(t, 1, nil)})
	if err != nil {
		t.Fatal(err)
	}
	var createdBy sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM automations WHERE id=$1`, a.ID).Scan(&createdBy); err != nil || createdBy.Int64 != 1 {
		t.Fatalf("created_by %v %v", createdBy, err)
	}
	if a.Name != "Welcome" || a.Status != "draft" || a.PublishedVersion != nil || a.HasUnpublishedChanges {
		t.Fatalf("draft: %+v", a)
	}

	// Validate is per publishing user: user 2 does not own the identity or webhook.
	if errs, err := s.ValidateAutomation(ctx, 1, 1, a.UUID); err != nil || len(errs) != 0 {
		t.Fatalf("valid for owner: %v %+v", err, errs)
	}
	errs, err := s.ValidateAutomation(ctx, 1, 2, a.UUID)
	if err != nil || len(errs) != 2 || errs[0].NodeID != "e" || errs[1].NodeID != "h" {
		t.Fatalf("other user: %v %+v", err, errs)
	}
	if _, err := s.ActivateAutomation(ctx, 1, 2, a.UUID, true); err == nil {
		t.Fatal("activated with another user's identity")
	}

	res, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Published || res.Version != 1 || res.Automation.Status != "active" || res.Automation.ActivatedAt == nil ||
		*res.Automation.PublishedVersion != 1 || res.Automation.HasUnpublishedChanges {
		t.Fatalf("activate: %+v %+v", res, res.Automation)
	}
	var refs, triggerType, publishedBy string
	if err := db.QueryRow(`SELECT array_to_string(resource_refs,','), trigger_type, published_by::text FROM automation_versions WHERE automation_id=$1`, a.ID).Scan(&refs, &triggerType, &publishedBy); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{autoListA, autoListB, autoTemplate, autoIdentity, autoWebhook} {
		if !strings.Contains(refs, ref) {
			t.Fatalf("refs %s missing %s", refs, ref)
		}
	}
	if triggerType != AutomationTriggerSubscribed || publishedBy != "1" {
		t.Fatal(triggerType, publishedBy)
	}

	// Re-activating an unchanged graph publishes nothing; layout edits are not changes.
	moved := welcomeGraph(t, 1, nil)
	moved.Nodes[2].Position.X, moved.Nodes[2].Data.Label = 500, "Wait a day"
	if u, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{Workflow: moved}); err != nil || u.HasUnpublishedChanges || u.Status != "active" {
		t.Fatalf("layout edit: %v %+v", err, u)
	}
	if res, err = s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err != nil || res.Published || res.Version != 1 {
		t.Fatalf("unchanged activate: %v %+v", err, res)
	}

	var v1 int64
	if err := db.QueryRow(`SELECT id FROM automation_versions WHERE automation_id=$1 AND version=1`, a.ID).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	addEnrollment(t, db, a.ID, 1, "active", v1, "1 hour")
	if _, err := s.PauseAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseAutomation(ctx, 1, a.UUID); err == nil {
		t.Fatal("paused twice")
	}
	// Editing a paused automation changes only the draft, even when invalid.
	broken := welcomeGraph(t, 2, func(c map[string]map[string]any) { c["e"]["templateUuid"] = autoTplOff })
	u, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{Workflow: broken})
	if err != nil || u.Status != "paused" || !u.HasUnpublishedChanges || *u.PublishedVersion != 1 {
		t.Fatalf("paused edit: %v %+v", err, u)
	}
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err == nil {
		t.Fatal("published an inactive template")
	} else {
		wantInvalid(t, err, "e", "templateUuid")
	}
	if status, _ := s.automationStatus(ctx, 1, a.UUID); status != "paused" {
		t.Fatal("failed publish changed status", status)
	}
	// Resume without publishing keeps v1 although the draft is invalid.
	if res, err = s.ActivateAutomation(ctx, 1, 1, a.UUID, false); err != nil || res.Published || res.Version != 1 ||
		res.Automation.Status != "active" || !res.Automation.HasUnpublishedChanges {
		t.Fatalf("resume: %v %+v", err, res)
	}
	// Reentry policy is versioned: a policy-only change publishes v2.
	policy := "after_exit"
	if _, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{Workflow: welcomeGraph(t, 2, nil), ReentryPolicy: &policy}); err != nil {
		t.Fatal(err)
	}
	if res, err = s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err != nil || !res.Published || res.Version != 2 || res.Automation.HasUnpublishedChanges {
		t.Fatalf("publish v2: %v %+v", err, res)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE automation_id=$1 AND version_id=$2 AND status='active'`, a.ID, v1)
	count(t, db, 1, `SELECT count(*) FROM automation_versions WHERE automation_id=$1 AND version=2 AND reentry_policy='after_exit'`, a.ID)

	// Live automations cannot be deleted; archive cancels active enrollments.
	wantAutomationErr(t, s.DeleteAutomation(ctx, 1, a.UUID), "Archive the automation before deleting it")
	addEnrollment(t, db, a.ID, 2, "completed", v1, "0 seconds")
	mustExec(t, db, `INSERT INTO automation_messages(org_id,automation_id,version_id,enrollment_id,node_id,contact_id,email)
		SELECT 1,automation_id,version_id,id,'e',contact_id,'a@example.net' FROM automation_enrollments WHERE automation_id=$1 AND contact_id=1`, a.ID)
	archived, cancelled, err := s.ArchiveAutomation(ctx, 1, a.UUID)
	if err != nil || cancelled != 1 || archived.Status != "archived" || archived.ArchivedAt == nil || archived.Stats.Cancelled != 1 || archived.Stats.Completed != 1 {
		t.Fatalf("archive: %v %d %+v", err, cancelled, archived)
	}
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE automation_id=$1 AND status='cancelled' AND exit_reason='archived' AND claim_token IS NULL`, a.ID)
	count(t, db, 1, `SELECT count(*) FROM automation_messages WHERE automation_id=$1 AND status='cancelled'`, a.ID)
	_, _, err = s.ArchiveAutomation(ctx, 1, a.UUID)
	wantAutomationErr(t, err, "already archived")
	_, err = s.ActivateAutomation(ctx, 1, 1, a.UUID, false)
	wantAutomationErr(t, err, "cannot be activated")
	name := "Renamed"
	_, err = s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{Name: &name})
	wantAutomationErr(t, err, "cannot be edited")

	if err := s.DeleteAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	count(t, db, 0, `SELECT count(*) FROM automation_versions`)
	count(t, db, 0, `SELECT count(*) FROM automation_enrollments`)
	if _, err := s.GetAutomation(ctx, 1, a.UUID); !errors.Is(err, ErrAutomationNotFound) {
		t.Fatal(err)
	}
}

func TestAutomationActivateRejectsBadReferences(t *testing.T) {
	_, s := newAutomationFixture(t)
	ctx := context.Background()
	cases := []struct {
		name          string
		mutate        func(map[string]map[string]any)
		nodeID, field string
	}{
		{"inactive template", func(c map[string]map[string]any) { c["e"]["templateUuid"] = autoTplOff }, "e", "templateUuid"},
		{"missing template", func(c map[string]map[string]any) { c["e"]["templateUuid"] = autoListA }, "e", "templateUuid"},
		{"unverified domain", func(c map[string]map[string]any) { c["e"]["identityUuid"] = autoIdentityNV }, "e", "identityUuid"},
		{"other user's identity", func(c map[string]map[string]any) { c["e"]["identityUuid"] = autoIdentityU2 }, "e", "identityUuid"},
		{"dynamic trigger list", func(c map[string]map[string]any) { c["t"]["listUuid"] = autoListDyn }, "t", "listUuid"},
		{"foreign action list", func(c map[string]map[string]any) { c["a"]["listUuid"] = autoListOther }, "a", "listUuid"},
		{"other user's webhook", func(c map[string]map[string]any) { c["h"]["webhookUuid"] = autoWebhookU2 }, "h", "webhookUuid"},
		{"inactive webhook", func(c map[string]map[string]any) { c["h"]["webhookUuid"] = autoWebhookOff }, "h", "webhookUuid"},
		{"graph error", func(c map[string]map[string]any) { c["w"]["duration"] = 0 }, "w", "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: tc.name, Workflow: welcomeGraph(t, 1, tc.mutate)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.ActivateAutomation(ctx, 1, 1, a.UUID, true)
			wantInvalid(t, err, tc.nodeID, tc.field)
			// publishDraft=false without a published version still validates.
			_, err = s.ActivateAutomation(ctx, 1, 1, a.UUID, false)
			wantInvalid(t, err, tc.nodeID, tc.field)
			got, err := s.GetAutomation(ctx, 1, a.UUID)
			if err != nil || got.Status != "draft" || got.PublishedVersion != nil {
				t.Fatalf("failed activation changed the automation: %v %+v", err, got)
			}
		})
	}
}

func TestAutomationListPagingStatusAndCounts(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: fmt.Sprintf("A%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: "Live", Workflow: welcomeGraph(t, 1, nil)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true)
	if err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := db.QueryRow(`SELECT id FROM automation_versions WHERE automation_id=$1`, a.ID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	addEnrollment(t, db, a.ID, 1, "active", versionID, "1 day")        // waiting
	addEnrollment(t, db, a.ID, 2, "active", versionID, "-1 minute")    // due
	addEnrollment(t, db, a.ID, 1, "completed", versionID, "0 seconds") // history
	addEnrollment(t, db, a.ID, 2, "failed", versionID, "0 seconds")
	if _, err := s.CreateAutomation(ctx, 2, 9, &model.CreateAutomationRequest{Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListAutomations(ctx, 1, 0, 1000, "")
	if err != nil || list.Page != 1 || list.PageSize != 100 || list.Total != 4 || len(list.Automations) != 4 {
		t.Fatalf("clamped list: %v %+v", err, list)
	}
	list, err = s.ListAutomations(ctx, 1, 2, 3, "")
	if err != nil || len(list.Automations) != 1 || list.Total != 4 {
		t.Fatalf("page 2: %v %+v", err, list)
	}
	list, err = s.ListAutomations(ctx, 1, -5, -1, "active")
	if err != nil || list.Total != 1 || list.PageSize != 20 {
		t.Fatalf("active: %v %+v", err, list)
	}
	got := list.Automations[0]
	want := model.AutomationCounts{Enrolled: 4, Active: 2, Waiting: 1, Completed: 1, Failed: 1}
	if got.Stats != want || got.EnrolledCount != 4 || got.InProgressCount != 2 || got.CompletedCount != 1 || *got.PublishedVersion != res.Version {
		t.Fatalf("live counts: %+v", got)
	}
	_, err = s.ListAutomations(ctx, 1, 1, 20, "running")
	wantAutomationErr(t, err, "status must be")

	// Per-node stats for the published version.
	var enrollmentID int64
	if err := db.QueryRow(`SELECT id FROM automation_enrollments WHERE automation_id=$1 AND status='completed'`, a.ID).Scan(&enrollmentID); err != nil {
		t.Fatal(err)
	}
	var messageID int64
	if err := db.QueryRow(`INSERT INTO automation_messages(org_id,automation_id,version_id,enrollment_id,node_id,contact_id,email,status,open_count)
		VALUES(1,$1,$2,$3,'e',1,'a@example.net','sent',2) RETURNING id`, a.ID, versionID, enrollmentID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO automation_step_runs(enrollment_id,automation_id,version_id,node_id,node_type,status,outcome,message_id) VALUES
		($1,$2,$3,'e','email','succeeded',NULL,$4),($1,$2,$3,'w','delay','waiting',NULL,NULL)`, enrollmentID, a.ID, versionID, messageID)
	stats, err := s.GetAutomationStats(ctx, 1, a.UUID, nil)
	if err != nil || stats.Version != 1 || stats.Enrolled != 4 || stats.CompletionRate != 25 || stats.InProgress != 2 {
		t.Fatalf("stats: %v %+v", err, stats)
	}
	if e := stats.Nodes["e"]; e.Entered != 1 || e.Succeeded != 1 || e.Sent != 1 || e.Opened != 1 || e.Clicked != 0 || stats.Nodes["w"].Waiting != 1 {
		t.Fatalf("node stats: %+v", stats.Nodes)
	}
	missing := 7
	_, err = s.GetAutomationStats(ctx, 1, a.UUID, &missing)
	wantAutomationErr(t, err, "does not exist")

	for _, bad := range []string{"not-a-uuid", "e0000000-0000-4000-8000-000000000000"} {
		if _, err := s.GetAutomation(ctx, 1, bad); !errors.Is(err, ErrAutomationNotFound) {
			t.Fatal(bad, err)
		}
		if _, err := s.PauseAutomation(ctx, 1, bad); !errors.Is(err, ErrAutomationNotFound) {
			t.Fatal(bad, err)
		}
	}
	// Org scoping: org 2 cannot see org 1's automation.
	if _, err := s.GetAutomation(ctx, 2, a.UUID); !errors.Is(err, ErrAutomationNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteAutomation(ctx, 2, a.UUID); !errors.Is(err, ErrAutomationNotFound) {
		t.Fatal(err)
	}
}
