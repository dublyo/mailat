package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestAutomationDraftSaveNormalizes(t *testing.T) {
	db := testutil.Database(t)
	mustExec(t, db, `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'QA','qa',now())`)
	s := NewAutomationService(db, &config.Config{})
	ctx := context.Background()

	var legacy model.Workflow
	if err := json.Unmarshal([]byte(`{"nodes":[
		{"id":"t","type":"trigger","position":{"x":0,"y":0},"data":{"label":"Contact Added","config":{"event":"contact_added"}}},
		{"id":"w","type":"delay","position":{"x":0,"y":1},"data":{"label":"Wait","config":{"duration":1,"unit":"days"}}}],
		"edges":[{"id":"e","source":"t","target":"w","label":"x"}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAutomation(ctx, 1, 0, &model.CreateAutomationRequest{Name: "Legacy", TriggerType: "contact_added", Workflow: &legacy})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "draft" || a.TriggerType != AutomationTriggerCreated || a.ReentryPolicy != "never" || a.Workflow.SchemaVersion != 1 ||
		a.Workflow.Nodes[0].Type != "workflow" || a.Workflow.Nodes[1].Data.Type != "delay" || a.TriggerConfig["event"] != AutomationTriggerCreated {
		t.Fatalf("stored draft: %+v %+v", a, a.Workflow)
	}

	// An incomplete draft saves; structural problems and trigger mismatches do not.
	if _, err := s.CreateAutomation(ctx, 1, 0, &model.CreateAutomationRequest{Name: "Empty"}); err != nil {
		t.Fatal("empty draft:", err)
	}
	var invalid *AutomationInvalidError
	bad := &model.Workflow{Nodes: []model.WorkflowNode{{ID: "bad id", Data: model.WorkflowNodeData{Type: "sms"}}}}
	if _, err := s.CreateAutomation(ctx, 1, 0, &model.CreateAutomationRequest{Name: "Bad", Workflow: bad}); !errors.As(err, &invalid) || invalid.Errors[0].Field != "id" {
		t.Fatalf("structural error: %v", err)
	}
	if _, err := s.CreateAutomation(ctx, 1, 0, &model.CreateAutomationRequest{Name: "Mismatch", TriggerType: "manual", Workflow: &legacy}); !errors.As(err, &invalid) {
		t.Fatalf("mismatch: %v", err)
	}
	policy := "always"
	if _, err := s.CreateAutomation(ctx, 1, 0, &model.CreateAutomationRequest{Name: "Policy", ReentryPolicy: &policy}); err == nil {
		t.Fatal("unknown reentry policy accepted")
	}

	manual := "manual"
	if _, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{TriggerType: &manual}); err == nil {
		t.Fatal("trigger change without the workflow accepted")
	}
	legacy.Nodes[0].Data.Config = map[string]any{"event": "subscribed"}
	policy = "after_exit"
	u, err := s.UpdateAutomation(ctx, 1, a.UUID, &model.UpdateAutomationRequest{Workflow: &legacy, ReentryPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	if u.TriggerType != AutomationTriggerSubscribed || u.ReentryPolicy != "after_exit" || u.Workflow.Nodes[0].Type != "workflow" {
		t.Fatalf("updated draft: %+v", u)
	}
}
