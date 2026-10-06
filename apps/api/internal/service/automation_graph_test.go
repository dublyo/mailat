package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
)

type graphFixture struct {
	Name              string          `json:"name"`
	TriggerType       string          `json:"triggerType"`
	ExpectTriggerType string          `json:"expectTriggerType"`
	Workflow          json.RawMessage `json:"workflow"`
	NormalizeError    bool            `json:"normalizeError"`
	Structure         [][2]string     `json:"structure"`
	Publish           *[][2]string    `json:"publish"`
}

func loadGraphFixtures(t *testing.T) []graphFixture {
	t.Helper()
	b, err := os.ReadFile("../../../web/tests/fixtures/automation-graphs.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Cases []graphFixture }
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 30 {
		t.Fatalf("only %d fixtures", len(f.Cases))
	}
	return f.Cases
}

func errorPairs(errs []model.AutomationValidationError) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range errs {
		if e.Message == "" {
			out = append(out, "missing message for "+e.NodeID+"/"+e.Field)
		}
		k := e.NodeID + "/" + e.Field
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func fixturePairs(p [][2]string) []string {
	var out []string
	for _, x := range p {
		out = append(out, x[0]+"/"+x[1])
	}
	sort.Strings(out)
	return out
}

func TestAutomationGraphSharedFixtures(t *testing.T) {
	for _, tc := range loadGraphFixtures(t) {
		t.Run(tc.Name, func(t *testing.T) {
			var w model.Workflow
			if err := json.Unmarshal(tc.Workflow, &w); err != nil {
				t.Fatal(err)
			}
			norm, trigger, _, err := NormalizeWorkflow(&w, tc.TriggerType, nil)
			if tc.NormalizeError {
				if !errors.Is(err, ErrTriggerTypeMismatch) {
					t.Fatalf("normalize error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.ExpectTriggerType != "" && trigger != tc.ExpectTriggerType {
				t.Fatalf("trigger type %q", trigger)
			}
			if got, want := errorPairs(ValidateStructure(norm)), fixturePairs(tc.Structure); !reflect.DeepEqual(got, want) {
				t.Fatalf("structure errors %v, want %v", got, want)
			}
			if tc.Publish == nil {
				return
			}
			g, errs := CompileForPublish(norm, trigger, nil)
			if got, want := errorPairs(errs), fixturePairs(*tc.Publish); !reflect.DeepEqual(got, want) {
				t.Fatalf("publish errors %v, want %v", got, want)
			}
			if len(*tc.Publish) == 0 && (g == nil || g.Hash == "") {
				t.Fatal("valid graph did not compile")
			}
		})
	}
}

func TestNormalizeWorkflow(t *testing.T) {
	raw := `{"nodes":[
		{"id":"t","type":"trigger","position":{"x":1,"y":2},"data":{"label":"Old","config":{"event":"subscribed","listUuid":"11111111-1111-4111-8111-111111111111"}}},
		{"id":"m","type":"email","position":{"x":1,"y":2},"data":{"label":"Mail","type":"email","config":{"templateId":"welcome","identityId":2}}}],
	  "edges":[{"id":"e1","source":"t","target":"m","label":"go","style":{"stroke":"red"},"markerEnd":"arrow","animated":true,"type":"smoothstep"}]}`
	var w model.Workflow
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	norm, trigger, cfg, err := NormalizeWorkflow(&w, "", map[string]any{"ignored": true})
	if err != nil {
		t.Fatal(err)
	}
	if trigger != AutomationTriggerSubscribed || cfg["event"] != AutomationTriggerSubscribed || cfg["listUuid"] == nil || cfg["ignored"] != nil {
		t.Fatalf("trigger %q cfg %v", trigger, cfg)
	}
	for _, n := range norm.Nodes {
		if n.Type != "workflow" {
			t.Fatalf("node %s renderer type %q", n.ID, n.Type)
		}
	}
	if norm.Nodes[0].Data.Type != "trigger" || norm.Nodes[1].Data.Type != "email" || norm.SchemaVersion != 1 {
		t.Fatalf("kinds not moved into data.type: %+v", norm.Nodes)
	}
	if norm.Nodes[1].Data.Config["templateId"] != "welcome" {
		t.Fatal("legacy email keys must be kept for validation")
	}
	if w.Nodes[0].Type != "trigger" {
		t.Fatal("input was mutated")
	}
	b, _ := json.Marshal(norm.Edges)
	if s := string(b); strings.Contains(s, "label") || strings.Contains(s, "style") || strings.Contains(s, "marker") || !strings.Contains(s, `"animated":true`) {
		t.Fatalf("edge not stripped: %s", s)
	}

	if _, _, _, err := NormalizeWorkflow(&w, "contact.created", nil); !errors.Is(err, ErrTriggerTypeMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, tt, _, err := NormalizeWorkflow(&w, "contact_subscribed", nil); err != nil || tt != AutomationTriggerSubscribed {
		t.Fatalf("alias request trigger: %q %v", tt, err)
	}

	// No workflow: a trigger node is built from the request.
	built, tt, cfg, err := NormalizeWorkflow(nil, "contact_added", map[string]any{"sources": []any{"api"}})
	if err != nil || tt != AutomationTriggerCreated || len(built.Nodes) != 1 || built.Nodes[0].Data.Config["event"] != AutomationTriggerCreated || cfg["sources"] == nil {
		t.Fatalf("built %+v %q %v %v", built, tt, cfg, err)
	}
	if _, tt, _, _ := NormalizeWorkflow(nil, "", nil); tt != AutomationTriggerSubscribed {
		t.Fatalf("default trigger %q", tt)
	}
}

func TestWorkflowHashIgnoresLayout(t *testing.T) {
	base := func() *model.Workflow {
		f := loadGraphFixtures(t)[0]
		var w model.Workflow
		if err := json.Unmarshal(f.Workflow, &w); err != nil {
			t.Fatal(err)
		}
		n, _, _, err := NormalizeWorkflow(&w, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, b := base(), base()
	b.Nodes[0].Position = model.WorkflowPosition{X: 9, Y: 9}
	b.Nodes[1].Data.Label = "Renamed"
	b.Edges[0].ID, b.Edges[0].Type, b.Edges[0].Animated = "other", "default", false
	b.Nodes[0], b.Nodes[1] = b.Nodes[1], b.Nodes[0]
	b.Edges[0], b.Edges[1] = b.Edges[1], b.Edges[0]
	if WorkflowHash(a) != WorkflowHash(b) {
		t.Fatal("layout changed the hash")
	}
	b.Nodes[0].Data.Config["subject"] = "Changed"
	if WorkflowHash(a) == WorkflowHash(b) {
		t.Fatal("config change kept the hash")
	}
	c := base()
	c.Edges[3].SourceHandle, c.Edges[4].SourceHandle = "no", "yes"
	if WorkflowHash(a) == WorkflowHash(c) {
		t.Fatal("swapped branches kept the hash")
	}
}

func TestCompileForPublishGraph(t *testing.T) {
	f := loadGraphFixtures(t)[0]
	var w model.Workflow
	if err := json.Unmarshal(f.Workflow, &w); err != nil {
		t.Fatal(err)
	}
	g, errs := CompileForPublish(&w, "", nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if g.TriggerID != "trigger-1" || g.Successor("trigger-1", "") != "email-1" || g.Successor("email-1", "") != "delay-1" ||
		g.Successor("cond-1", "yes") != "action-yes" || g.Successor("cond-1", "no") != "action-no" || g.Successor("action-yes", "") != "" {
		t.Fatalf("successors: next=%v yes=%v no=%v", g.Next, g.Yes, g.No)
	}
	if got := g.Trigger(); got.ListUUID != "11111111-1111-4111-8111-111111111111" || !reflect.DeepEqual(got.Sources, []string{"signup_form"}) {
		t.Fatalf("trigger %+v", got)
	}
	if e := g.Nodes["email-1"].Email; !e.TrackOpens || !e.TrackClicks || e.TemplateUUID == "" {
		t.Fatalf("email %+v", e)
	}
	if d := g.Nodes["delay-1"].Delay; d.Total().Minutes() != 1 {
		t.Fatalf("delay %+v", d)
	}
	if len(g.Refs) != 4 || !sort.StringsAreSorted(g.Refs) {
		t.Fatalf("refs %v", g.Refs)
	}

	// Sources default to every source except import, automation and unknown.
	w.Nodes[0].Data.Config = map[string]any{"event": "contact.created"}
	w.Nodes[1].Data.Config["trackOpens"] = false
	g, errs = CompileForPublish(&w, "", nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, s := range g.Trigger().Sources {
		if s == "import" || s == "automation" || s == "unknown" {
			t.Fatalf("default sources include %s", s)
		}
	}
	if len(g.Trigger().Sources) != len(DefaultAutomationTriggerSources) || g.Nodes["email-1"].Email.TrackOpens {
		t.Fatalf("defaults: %v %+v", g.Trigger().Sources, g.Nodes["email-1"].Email)
	}
}

func TestValidateStructureLimits(t *testing.T) {
	w := &model.Workflow{}
	for i := 0; i <= maxWorkflowNodes; i++ {
		w.Nodes = append(w.Nodes, model.WorkflowNode{ID: fmt.Sprintf("d%d", i), Type: "workflow", Data: model.WorkflowNodeData{Type: "delay"}})
	}
	if got := errorPairs(ValidateStructure(w)); !reflect.DeepEqual(got, []string{"/nodes"}) {
		t.Fatalf("node limit: %v", got)
	}
	w = &model.Workflow{Nodes: []model.WorkflowNode{{ID: "d", Data: model.WorkflowNodeData{Type: "delay", Config: map[string]any{"unit": strings.Repeat("x", maxWorkflowJSONBytes)}}}}}
	if got := errorPairs(ValidateStructure(w)); !reflect.DeepEqual(got, []string{"/workflow"}) {
		t.Fatalf("size limit: %v", got)
	}
	w = &model.Workflow{Edges: make([]model.WorkflowEdge, maxWorkflowEdges+1)}
	if got := errorPairs(ValidateStructure(w)); !reflect.DeepEqual(got, []string{"/edges"}) {
		t.Fatalf("edge limit: %v", got)
	}
	w = &model.Workflow{Nodes: []model.WorkflowNode{{ID: "t", Data: model.WorkflowNodeData{Type: "trigger", Label: strings.Repeat("é", maxNodeLabelRunes+1),
		Config: map[string]any{"sources": []any{"api", 3}, "unknownKey": map[string]any{"x": 1}}}}}}
	if got := errorPairs(ValidateStructure(w)); !reflect.DeepEqual(got, []string{"t/label", "t/sources"}) {
		t.Fatalf("label/sources: %v", got)
	}
}

func TestEvalCondition(t *testing.T) {
	facts := ContactFacts{EngagementScore: 12.5, Attributes: map[string]any{"plan": "Pro Annual", "seats": float64(10), "beta": true, "zip": "02134", "empty": nil}}
	for _, tc := range []struct {
		c               CondCfg
		opened, clicked bool
		want            bool
	}{
		{CondCfg{Field: "email_opened"}, true, false, true},
		{CondCfg{Field: "email_opened"}, false, true, false},
		{CondCfg{Field: "email_clicked"}, false, true, true},
		{CondCfg{Field: "engagement_score", Operator: "greater_than", Value: "10"}, false, false, true},
		{CondCfg{Field: "engagement_score", Operator: "less_than", Value: "10"}, false, false, false},
		{CondCfg{Field: "engagement_score", Operator: "equals", Value: "12.50"}, false, false, true},
		{CondCfg{Field: "engagement_score", Operator: "not_equals", Value: "12.5"}, false, false, false},
		{CondCfg{Field: "custom_field", Attribute: "plan", Operator: "contains", Value: "pro"}, false, false, true},
		{CondCfg{Field: "custom_field", Attribute: "plan", Operator: "equals", Value: "PRO ANNUAL"}, false, false, true},
		{CondCfg{Field: "custom_field", Attribute: "plan", Operator: "not_equals", Value: "pro annual"}, false, false, false},
		{CondCfg{Field: "custom_field", Attribute: "seats", Operator: "greater_than", Value: "9"}, false, false, true},
		{CondCfg{Field: "custom_field", Attribute: "seats", Operator: "less_than", Value: "9.5"}, false, false, false},
		// Numeric when both sides parse: "02134" equals "2134".
		{CondCfg{Field: "custom_field", Attribute: "zip", Operator: "equals", Value: "2134"}, false, false, true},
		{CondCfg{Field: "custom_field", Attribute: "seats", Operator: "greater_than", Value: "abc"}, false, false, false},
		{CondCfg{Field: "custom_field", Attribute: "beta", Operator: "equals", Value: "TRUE"}, false, false, true},
		{CondCfg{Field: "custom_field", Attribute: "missing", Operator: "not_equals", Value: "x"}, false, false, false},
		{CondCfg{Field: "custom_field", Attribute: "empty", Operator: "not_equals", Value: "x"}, false, false, false},
		{CondCfg{Field: "custom_field", Attribute: "plan", Operator: "unknown", Value: "x"}, false, false, false},
		{CondCfg{Field: "tag_exists"}, true, true, false},
	} {
		if got := EvalCondition(&tc.c, facts, tc.opened, tc.clicked); got != tc.want {
			t.Errorf("%+v opened=%v clicked=%v: got %v", tc.c, tc.opened, tc.clicked, got)
		}
	}
}
