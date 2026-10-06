package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
)

// TestAutomationErasureExportAndGuards: erasure removes everything automation
// holds about a contact, export lists enrollments, and lists/templates used by
// a live automation cannot be deleted.
func TestAutomationErasureExportAndGuards(t *testing.T) {
	db, s, x, _, _ := automationMailFixture(t)
	ctx := context.Background()
	graph := func(withAction bool) *model.Workflow {
		nodes := []graphNode{
			{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
			{"e", "email", map[string]any{"templateUuid": autoTemplate, "identityUuid": autoIdentity}},
			{"h", "webhook", map[string]any{"webhookUuid": autoWebhook}},
			{"w", "delay", map[string]any{"duration": 1, "unit": "days"}},
		}
		edges := [][3]string{{"t", "e", ""}, {"e", "h", ""}, {"h", "w", ""}}
		if withAction {
			nodes = append(nodes, graphNode{"a", "action", map[string]any{"action": "add_to_list", "listUuid": autoListB}})
			edges = append(edges, [3]string{"w", "a", ""})
		}
		return buildGraph(t, nodes, edges)
	}
	a := publishGraph(t, s, graph(true), "never")
	addAutoContact(t, db, 100, "active", `{}`)
	addAutoContact(t, db, 101, "active", `{}`)
	autoSubscribe(t, db, 1, 100, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	if got := stepRuns(t, db, 100); got != "e:succeeded:queued,h:succeeded:queued,w:waiting:" {
		t.Fatalf("steps: %s", got)
	}

	compliance := NewComplianceService(db, s.cfg)
	c100 := contactUUID(t, s, 100)
	export, err := compliance.ExportContactData(ctx, 1, c100)
	if err != nil {
		t.Fatal(err)
	}
	if got := export["automations"].([]map[string]interface{}); len(got) != 1 || got[0]["automationName"] != "Flow" || got[0]["status"] != "active" {
		t.Fatalf("export: %+v", got)
	}

	lists := NewListService(db, s.cfg)
	templates := &TransactionalService{db: db, cfg: s.cfg}
	refused := func(err error) {
		t.Helper()
		var ae *AutomationError
		if !errors.As(err, &ae) || !strings.Contains(ae.Message, "Used by automation “Flow”; archive it first") {
			t.Fatalf("delete was not refused: %v", err)
		}
	}
	refused(lists.DeleteList(ctx, 1, autoListA))
	refused(lists.DeleteList(ctx, 1, autoListB))
	refused(templates.DeleteTemplate(ctx, 1, autoTemplate))
	if _, err := s.PauseAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	refused(lists.DeleteList(ctx, 1, autoListB))
	if _, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, false); err != nil {
		t.Fatal(err)
	}

	// Erasure mid-flow leaves nothing behind.
	if err := compliance.DeleteContactData(ctx, 1, ContactActor{UserID: 1}, c100); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM automation_enrollments`,
		`SELECT count(*) FROM automation_step_runs`,
		`SELECT count(*) FROM automation_trigger_events`,
		`SELECT count(*) FROM automation_messages`,
		`SELECT count(*) FROM webhook_events WHERE event_type='automation.webhook'`,
		`SELECT count(*) FROM webhook_deliveries`,
	} {
		count(t, db, 0, q)
	}

	// A version that is no longer published still guards its resources
	// while enrollments run on it.
	autoSubscribe(t, db, 1, 101, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	if _, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{Workflow: graph(false)}); err != nil {
		t.Fatal(err)
	}
	if act, err := s.ActivateAutomation(ctx, 1, 1, a.UUID, true); err != nil || act.Version != 2 {
		t.Fatalf("publish v2: %+v %v", act, err)
	}
	refused(lists.DeleteList(ctx, 1, autoListB))

	if _, _, err := s.ArchiveAutomation(ctx, 1, a.UUID); err != nil {
		t.Fatal(err)
	}
	if err := lists.DeleteList(ctx, 1, autoListB); err != nil {
		t.Fatalf("delete after archive: %v", err)
	}
	if err := templates.DeleteTemplate(ctx, 1, autoTemplate); err != nil {
		t.Fatalf("delete template after archive: %v", err)
	}
}
