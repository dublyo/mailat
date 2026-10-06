package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/model"
)

// The canonical automation graph: nodes are saved with the vue-flow renderer
// key type:'workflow' and their kind in data.type. Normalization maps legacy
// shapes onto it; ValidateStructure runs on save and CompileForPublish on
// validate/activate. Everything here is pure (no database).

type NodeKind string

const (
	KindTrigger   NodeKind = "trigger"
	KindEmail     NodeKind = "email"
	KindDelay     NodeKind = "delay"
	KindCondition NodeKind = "condition"
	KindAction    NodeKind = "action"
	KindWebhook   NodeKind = "webhook"
)

const (
	AutomationTriggerSubscribed = "contact.subscribed"
	AutomationTriggerCreated    = "contact.created"
	AutomationTriggerManual     = "manual"

	maxWorkflowNodes     = 100
	maxWorkflowEdges     = 200
	maxWorkflowJSONBytes = 256 << 10
	maxNodeLabelRunes    = 200
	maxAutomationValue   = 1000
)

var (
	nodeKinds = map[NodeKind]bool{KindTrigger: true, KindEmail: true, KindDelay: true, KindCondition: true, KindAction: true, KindWebhook: true}

	triggerAliases = map[string]string{
		"contact_added":      AutomationTriggerCreated,
		"contact_created":    AutomationTriggerCreated,
		"contact_subscribed": AutomationTriggerSubscribed,
		"subscribed":         AutomationTriggerSubscribed,
	}

	// AutomationTriggerSources are the list_contacts/contacts source values a
	// trigger can filter on (the 014 trigger maps anything else to unknown).
	AutomationTriggerSources = []string{"signup_form", "api", "import", "manual", "preference_center", "double_opt_in", "automation", "unknown"}
	// DefaultAutomationTriggerSources: imports, other automations and unknown
	// paths enroll only when an automation opts in.
	DefaultAutomationTriggerSources = []string{"signup_form", "api", "manual", "preference_center", "double_opt_in"}

	nodeIDRe        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	attributeKeyRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	delayUnits      = map[string]time.Duration{"minutes": time.Minute, "hours": time.Hour, "days": 24 * time.Hour, "weeks": 7 * 24 * time.Hour}
	scoreOperators  = map[string]bool{"equals": true, "not_equals": true, "greater_than": true, "less_than": true}
	fieldOperators  = map[string]bool{"equals": true, "not_equals": true, "contains": true, "greater_than": true, "less_than": true}
	reservedAttrKey = map[string]bool{"email": true, "status": true}

	ErrTriggerTypeMismatch = errors.New("triggerType does not match the trigger step's event")
)

type TriggerCfg struct {
	Event    string
	ListUUID string
	Sources  []string // defaulted; empty for manual
}

type EmailCfg struct {
	TemplateUUID string
	Subject      string
	IdentityUUID string
	TrackOpens   bool
	TrackClicks  bool
}

type DelayCfg struct {
	Duration int
	Unit     string
}

func (d DelayCfg) Total() time.Duration { return time.Duration(d.Duration) * delayUnits[d.Unit] }

type CondCfg struct {
	Field       string // email_opened, email_clicked, engagement_score, custom_field
	Mode        string // if_else, filter
	EmailNodeID string
	Attribute   string
	Operator    string
	Value       string
}

type ActionCfg struct {
	Action    string // add_to_list, remove_from_list, update_field
	ListUUID  string
	Attribute string
	Value     string
}

type WebhookCfg struct {
	WebhookUUID string
}

type CompiledNode struct {
	ID      string
	Kind    NodeKind
	Trigger *TriggerCfg
	Email   *EmailCfg
	Delay   *DelayCfg
	Cond    *CondCfg
	Action  *ActionCfg
	Webhook *WebhookCfg
}

type CompiledGraph struct {
	TriggerID string
	Nodes     map[string]*CompiledNode
	Next      map[string]string // non-condition successor ("" = exit)
	Yes, No   map[string]string // condition successors ("" = exit)
	Refs      []string          // list/template/identity/webhook UUIDs
	Hash      string            // sha256 of canonical JSON; positions, labels, edge styling excluded
}

// Trigger returns the compiled trigger config.
func (g *CompiledGraph) Trigger() *TriggerCfg { return g.Nodes[g.TriggerID].Trigger }

// Successor is where an enrollment goes after nodeID; outcome is yes/no for conditions.
func (g *CompiledGraph) Successor(nodeID, outcome string) string {
	if g.Nodes[nodeID] != nil && g.Nodes[nodeID].Kind == KindCondition {
		if outcome == "yes" {
			return g.Yes[nodeID]
		}
		return g.No[nodeID]
	}
	return g.Next[nodeID]
}

// ContactFacts are the contact fields a condition can read.
type ContactFacts struct {
	EngagementScore float64
	Attributes      map[string]any
}

// AutomationInvalidError carries validation errors for the API's data.errors.
type AutomationInvalidError struct {
	Errors []model.AutomationValidationError
}

func (e *AutomationInvalidError) Error() string {
	if len(e.Errors) == 0 {
		return "Automation is not valid"
	}
	return "Automation is not valid: " + e.Errors[0].Message
}

// NormalizeTriggerType maps legacy trigger names onto canonical ones.
func NormalizeTriggerType(t string) string {
	t = strings.TrimSpace(t)
	if alias, ok := triggerAliases[t]; ok {
		return alias
	}
	return t
}

// NormalizeWorkflow returns a canonical copy of w and the draft trigger type
// and config derived from its trigger node. The trigger node's event is the
// source of truth: a non-empty triggerType that differs is rejected. Without a
// workflow, a single trigger node is built from triggerType and cfg.
func NormalizeWorkflow(w *model.Workflow, triggerType string, cfg map[string]any) (*model.Workflow, string, map[string]any, error) {
	triggerType = NormalizeTriggerType(triggerType)
	if w == nil {
		event := triggerType
		if event == "" {
			event = AutomationTriggerSubscribed
		}
		config := map[string]any{}
		for k, v := range cfg {
			config[k] = v
		}
		config["event"] = event
		w = &model.Workflow{Nodes: []model.WorkflowNode{{ID: "trigger-1", Position: model.WorkflowPosition{X: 400, Y: 100},
			Data: model.WorkflowNodeData{Label: "Trigger", Type: string(KindTrigger), Config: config}}}}
	}
	out := &model.Workflow{SchemaVersion: model.WorkflowSchemaVersion, Nodes: make([]model.WorkflowNode, len(w.Nodes)), Edges: make([]model.WorkflowEdge, len(w.Edges))}
	triggerSeen := false
	derivedType, derivedCfg := triggerType, cfg
	for i, n := range w.Nodes {
		n.ID = strings.TrimSpace(n.ID)
		if nodeKinds[NodeKind(n.Type)] && (n.Data.Type == "" || n.Data.Type == n.Type) {
			n.Data.Type = n.Type
		}
		n.Type = "workflow"
		config := make(map[string]any, len(n.Data.Config))
		for k, v := range n.Data.Config {
			config[k] = v
		}
		n.Data.Config = config
		if NodeKind(n.Data.Type) == KindTrigger && !triggerSeen {
			triggerSeen = true
			event, _ := config["event"].(string)
			event = NormalizeTriggerType(event)
			if event == "" {
				event = triggerType
			}
			if event == "" {
				event = AutomationTriggerSubscribed
			}
			if triggerType != "" && triggerType != event {
				return nil, "", nil, ErrTriggerTypeMismatch
			}
			config["event"] = event
			derivedType = event
			derivedCfg = make(map[string]any, len(config))
			for k, v := range config {
				derivedCfg[k] = v
			}
		}
		out.Nodes[i] = n
	}
	for i, e := range w.Edges {
		out.Edges[i] = model.WorkflowEdge{ID: e.ID, Source: e.Source, Target: e.Target, SourceHandle: e.SourceHandle,
			TargetHandle: e.TargetHandle, Type: e.Type, Animated: e.Animated}
	}
	if derivedCfg == nil {
		derivedCfg = map[string]any{}
	}
	return out, derivedType, derivedCfg, nil
}

// configTypes are the primitive types each kind's known config keys must have.
// "scalar" accepts a string or a number.
var configTypes = map[NodeKind]map[string]string{
	KindTrigger:   {"event": "string", "listUuid": "string", "sources": "strings"},
	KindEmail:     {"templateUuid": "string", "subject": "string", "identityUuid": "string", "trackOpens": "bool", "trackClicks": "bool", "templateId": "scalar", "identityId": "scalar"},
	KindDelay:     {"duration": "number", "unit": "string"},
	KindCondition: {"field": "string", "mode": "string", "emailNodeId": "string", "attribute": "string", "operator": "string", "value": "scalar"},
	KindAction:    {"action": "string", "listUuid": "string", "attribute": "string", "value": "scalar"},
	KindWebhook:   {"webhookUuid": "string", "url": "string", "method": "string"},
}

func errAt(nodeID, field, format string, args ...any) model.AutomationValidationError {
	return model.AutomationValidationError{NodeID: nodeID, Field: field, Message: fmt.Sprintf(format, args...)}
}

// ValidateStructure enforces the limits checked on every save. Required
// values may be missing so an incomplete draft can be stored.
func ValidateStructure(w *model.Workflow) []model.AutomationValidationError {
	var errs []model.AutomationValidationError
	if w == nil {
		return []model.AutomationValidationError{errAt("", "workflow", "Workflow is required")}
	}
	if len(w.Nodes) > maxWorkflowNodes {
		errs = append(errs, errAt("", "nodes", "A workflow can have at most %d steps", maxWorkflowNodes))
	}
	if len(w.Edges) > maxWorkflowEdges {
		errs = append(errs, errAt("", "edges", "A workflow can have at most %d connections", maxWorkflowEdges))
	}
	if b, err := json.Marshal(w); err != nil || len(b) > maxWorkflowJSONBytes {
		errs = append(errs, errAt("", "workflow", "Workflow is larger than 256 KiB"))
	}
	if len(errs) > 0 {
		return errs
	}
	seen := map[string]bool{}
	for _, n := range w.Nodes {
		if !nodeIDRe.MatchString(n.ID) {
			errs = append(errs, errAt(n.ID, "id", "Step ids may contain only letters, digits, '-' and '_' (1-100 characters)"))
			continue
		}
		if seen[n.ID] {
			errs = append(errs, errAt(n.ID, "id", "Step id is used more than once"))
			continue
		}
		seen[n.ID] = true
		if len([]rune(n.Data.Label)) > maxNodeLabelRunes {
			errs = append(errs, errAt(n.ID, "label", "Step name is longer than %d characters", maxNodeLabelRunes))
		}
		kind := NodeKind(n.Data.Type)
		if !nodeKinds[kind] {
			errs = append(errs, errAt(n.ID, "type", "Unknown step type %q", truncateRunes(n.Data.Type, 40)))
			continue
		}
		keys := make([]string, 0, len(n.Data.Config))
		for k := range n.Data.Config {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := n.Data.Config[k]
			want, known := configTypes[kind][k]
			if !known || v == nil || hasType(v, want) {
				continue
			}
			errs = append(errs, errAt(n.ID, k, "%s must be %s", k, typeName(want)))
		}
	}
	for _, e := range w.Edges {
		if len(e.ID) > 200 || len(e.Source) > 100 || len(e.Target) > 100 || len(e.SourceHandle) > 100 || len(e.TargetHandle) > 100 {
			errs = append(errs, errAt("", "edges", "Connection fields are too long"))
			break
		}
	}
	return errs
}

func hasType(v any, want string) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	case "scalar":
		switch v.(type) {
		case string, float64:
			return true
		}
		return false
	case "strings":
		list, ok := v.([]any)
		if !ok {
			return false
		}
		for _, s := range list {
			if _, ok := s.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}

func typeName(t string) string {
	return map[string]string{"string": "text", "number": "a number", "bool": "true or false", "scalar": "text or a number", "strings": "a list of text values"}[t]
}

// CompileForPublish runs full validation (structure, graph shape, typed
// configs) on a normalized workflow and compiles it for the executor.
func CompileForPublish(w *model.Workflow, triggerType string, cfg map[string]any) (*CompiledGraph, []model.AutomationValidationError) {
	w, _, _, err := NormalizeWorkflow(w, triggerType, cfg)
	if err != nil {
		return nil, []model.AutomationValidationError{errAt("", "triggerType", "%s", err.Error())}
	}
	if errs := ValidateStructure(w); len(errs) > 0 {
		return nil, errs
	}
	var errs []model.AutomationValidationError
	g := &CompiledGraph{Nodes: map[string]*CompiledNode{}, Next: map[string]string{}, Yes: map[string]string{}, No: map[string]string{}}
	order := make([]string, 0, len(w.Nodes))
	byID := map[string]*model.WorkflowNode{}
	var triggers []string
	for i := range w.Nodes {
		n := &w.Nodes[i]
		byID[n.ID] = n
		order = append(order, n.ID)
		if NodeKind(n.Data.Type) == KindTrigger {
			triggers = append(triggers, n.ID)
		}
	}
	switch len(triggers) {
	case 0:
		return nil, []model.AutomationValidationError{errAt("", "trigger", "Add a trigger")}
	case 1:
		g.TriggerID = triggers[0]
	default:
		for _, id := range triggers[1:] {
			errs = append(errs, errAt(id, "type", "An automation has exactly one trigger"))
		}
		return nil, errs
	}

	// Edges and handles.
	out := map[string][]model.WorkflowEdge{}
	in := map[string]int{}
	for _, e := range w.Edges {
		if byID[e.Source] == nil || byID[e.Target] == nil {
			errs = append(errs, errAt(e.Source, "edges", "A connection points to a step that does not exist"))
			continue
		}
		out[e.Source] = append(out[e.Source], e)
		in[e.Target]++
	}
	if in[g.TriggerID] > 0 {
		errs = append(errs, errAt(g.TriggerID, "edges", "Nothing can connect into the trigger"))
	}
	for _, id := range order {
		n := byID[id]
		edges := out[id]
		if NodeKind(n.Data.Type) == KindCondition {
			mode, _ := n.Data.Config["mode"].(string)
			var yes, no int
			for _, e := range edges {
				switch e.SourceHandle {
				case "yes":
					yes++
					g.Yes[id] = e.Target
				case "no":
					no++
					g.No[id] = e.Target
				default:
					errs = append(errs, errAt(id, "edges", "Connect a condition from its Yes or No handle"))
				}
			}
			switch {
			case yes > 1 || no > 1:
				errs = append(errs, errAt(id, "edges", "A condition has at most one Yes and one No connection"))
			case mode == "filter" && no > 0:
				errs = append(errs, errAt(id, "edges", "A filter continues only on Yes"))
			case yes+no == 0:
				errs = append(errs, errAt(id, "edges", "Connect the condition's Yes or No branch"))
			}
			continue
		}
		if len(edges) > 1 {
			errs = append(errs, errAt(id, "edges", "This step can have only one next step"))
		}
		for _, e := range edges {
			if e.SourceHandle != "" {
				errs = append(errs, errAt(id, "edges", "Only conditions have Yes/No connections"))
			}
		}
		if len(edges) > 0 {
			g.Next[id] = edges[0].Target
		}
	}
	if len(out[g.TriggerID]) != 1 {
		errs = append(errs, errAt(g.TriggerID, "edges", "Connect the trigger to exactly one first step"))
	}
	if len(errs) > 0 {
		return nil, errs
	}

	// Acyclic (Kahn) and reachable from the trigger.
	succ := func(id string) []string {
		var s []string
		for _, e := range out[id] {
			s = append(s, e.Target)
		}
		return s
	}
	indeg := map[string]int{}
	for k, v := range in {
		indeg[k] = v
	}
	var queue []string
	for _, id := range order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, t := range succ(id) {
			if indeg[t]--; indeg[t] == 0 {
				queue = append(queue, t)
			}
		}
	}
	if visited != len(order) {
		for _, id := range order {
			if indeg[id] > 0 {
				errs = append(errs, errAt(id, "edges", "Steps cannot loop back"))
			}
		}
		return nil, errs
	}
	reach := reachableFrom(g.TriggerID, "", succ)
	for _, id := range order {
		if !reach[id] {
			errs = append(errs, errAt(id, "edges", "This step cannot be reached from the trigger"))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}

	// Typed configs.
	refs := map[string]bool{}
	for _, id := range order {
		n := byID[id]
		cn, nodeErrs := compileNode(n)
		errs = append(errs, nodeErrs...)
		if cn != nil {
			g.Nodes[id] = cn
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	trig := g.Trigger()
	if trig.ListUUID != "" {
		refs[trig.ListUUID] = true
	}
	for _, id := range order {
		cn := g.Nodes[id]
		switch cn.Kind {
		case KindEmail:
			refs[cn.Email.TemplateUUID], refs[cn.Email.IdentityUUID] = true, true
		case KindWebhook:
			refs[cn.Webhook.WebhookUUID] = true
		case KindAction:
			if cn.Action.ListUUID != "" {
				refs[cn.Action.ListUUID] = true
				if cn.Action.Action == "add_to_list" && strings.EqualFold(cn.Action.ListUUID, trig.ListUUID) {
					errs = append(errs, errAt(id, "listUuid", "Adding contacts to the trigger's own list would enroll them again"))
				}
			}
		case KindCondition:
			if cn.Cond.EmailNodeID == "" {
				continue
			}
			target := g.Nodes[cn.Cond.EmailNodeID]
			switch {
			case target == nil || target.Kind != KindEmail:
				errs = append(errs, errAt(id, "emailNodeId", "Choose an email step"))
			case reachableFrom(g.TriggerID, cn.Cond.EmailNodeID, succ)[id]:
				errs = append(errs, errAt(id, "emailNodeId", "Every path to this condition must pass the chosen email step"))
			}
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	for r := range refs {
		g.Refs = append(g.Refs, r)
	}
	sort.Strings(g.Refs)
	g.Hash = WorkflowHash(w)
	return g, nil
}

// reachableFrom walks successors from start, never entering skip.
func reachableFrom(start, skip string, succ func(string) []string) map[string]bool {
	seen := map[string]bool{start: true}
	stack := []string{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, t := range succ(id) {
			if t != skip && !seen[t] {
				seen[t] = true
				stack = append(stack, t)
			}
		}
	}
	return seen
}

func notSupported(nodeID, field string, value any) model.AutomationValidationError {
	return errAt(nodeID, field, "%v is not supported", value)
}

func cfgString(c map[string]any, k string) string {
	s, _ := c[k].(string)
	return strings.TrimSpace(s)
}

// cfgScalar renders a string or number config value as text.
func cfgScalar(c map[string]any, k string) string {
	switch v := c[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func checkUUID(nodeID, field, value, missing string) (string, []model.AutomationValidationError) {
	if value == "" {
		return "", []model.AutomationValidationError{errAt(nodeID, field, "%s", missing)}
	}
	u, err := uuid.Parse(value)
	if err != nil {
		return "", []model.AutomationValidationError{errAt(nodeID, field, "%s", missing)}
	}
	return u.String(), nil
}

func compileNode(n *model.WorkflowNode) (*CompiledNode, []model.AutomationValidationError) {
	c, id := n.Data.Config, n.ID
	cn := &CompiledNode{ID: id, Kind: NodeKind(n.Data.Type)}
	var errs []model.AutomationValidationError
	add := func(e ...model.AutomationValidationError) { errs = append(errs, e...) }
	switch cn.Kind {
	case KindTrigger:
		t := &TriggerCfg{Event: NormalizeTriggerType(cfgString(c, "event"))}
		switch t.Event {
		case AutomationTriggerSubscribed, AutomationTriggerCreated:
			if raw, ok := c["sources"].([]any); ok {
				allowed := map[string]bool{}
				for _, s := range AutomationTriggerSources {
					allowed[s] = true
				}
				seen := map[string]bool{}
				for _, v := range raw {
					s, _ := v.(string)
					if !allowed[s] {
						add(notSupported(id, "sources", s))
					} else if !seen[s] {
						seen[s] = true
						t.Sources = append(t.Sources, s)
					}
				}
				if len(raw) == 0 {
					add(errAt(id, "sources", "Choose at least one source"))
				}
			} else {
				t.Sources = append([]string(nil), DefaultAutomationTriggerSources...)
			}
			sort.Strings(t.Sources)
			if l := cfgString(c, "listUuid"); l != "" {
				if t.Event != AutomationTriggerSubscribed {
					add(errAt(id, "listUuid", "Only the subscribed trigger can filter by list"))
				} else {
					var e []model.AutomationValidationError
					t.ListUUID, e = checkUUID(id, "listUuid", l, "Choose a list")
					add(e...)
				}
			}
		case AutomationTriggerManual:
			if cfgString(c, "listUuid") != "" {
				add(errAt(id, "listUuid", "Manual enrollment has no list filter"))
			}
		case "":
			add(errAt(id, "event", "Choose a trigger"))
		default:
			add(notSupported(id, "event", t.Event))
		}
		cn.Trigger = t
	case KindEmail:
		e := &EmailCfg{Subject: cfgString(c, "subject"), TrackOpens: true, TrackClicks: true}
		if v, ok := c["trackOpens"].(bool); ok {
			e.TrackOpens = v
		}
		if v, ok := c["trackClicks"].(bool); ok {
			e.TrackClicks = v
		}
		var ve []model.AutomationValidationError
		if cfgString(c, "templateUuid") == "" && cfgScalar(c, "templateId") != "" {
			add(errAt(id, "templateUuid", "Choose the template again; this step refers to a template that does not exist"))
		} else {
			e.TemplateUUID, ve = checkUUID(id, "templateUuid", cfgString(c, "templateUuid"), "Choose a template")
			add(ve...)
		}
		if cfgString(c, "identityUuid") == "" && cfgScalar(c, "identityId") != "" {
			add(errAt(id, "identityUuid", "Choose the sender again; this step refers to a sender that does not exist"))
		} else {
			e.IdentityUUID, ve = checkUUID(id, "identityUuid", cfgString(c, "identityUuid"), "Choose a sender")
			add(ve...)
		}
		if len(e.Subject) > maxSubjectBytes || strings.ContainsAny(e.Subject, "\r\n") {
			add(errAt(id, "subject", "Subject must be one line of at most %d bytes", maxSubjectBytes))
		}
		cn.Email = e
	case KindDelay:
		d := &DelayCfg{Unit: cfgString(c, "unit")}
		f, ok := c["duration"].(float64)
		if !ok || f != math.Trunc(f) || f < 1 || f > 1e6 {
			add(errAt(id, "duration", "Duration must be a whole number of at least 1"))
		} else {
			d.Duration = int(f)
		}
		if _, ok := delayUnits[d.Unit]; !ok {
			add(errAt(id, "unit", "Unit must be minutes, hours, days or weeks"))
		} else if d.Duration > 0 && (d.Total() < time.Minute || d.Total() > 365*24*time.Hour) {
			add(errAt(id, "duration", "A wait must be between 1 minute and 365 days"))
		}
		cn.Delay = d
	case KindCondition:
		k := &CondCfg{Field: cfgString(c, "field"), Mode: cfgString(c, "mode"), Operator: cfgString(c, "operator")}
		if k.Mode == "" {
			k.Mode = "if_else"
		} else if k.Mode != "if_else" && k.Mode != "filter" {
			add(notSupported(id, "mode", k.Mode))
		}
		switch k.Field {
		case "email_opened", "email_clicked":
			k.Operator = ""
			if k.EmailNodeID = cfgString(c, "emailNodeId"); k.EmailNodeID == "" {
				add(errAt(id, "emailNodeId", "Choose an email step"))
			}
		case "engagement_score":
			if !scoreOperators[k.Operator] {
				add(errAt(id, "operator", "Choose equals, not equals, greater than or less than"))
			}
			k.Value = strings.TrimSpace(cfgScalar(c, "value"))
			if _, err := strconv.ParseFloat(k.Value, 64); err != nil {
				add(errAt(id, "value", "Engagement score must be a number"))
			}
		case "custom_field":
			if k.Attribute = cfgString(c, "attribute"); !attributeKeyRe.MatchString(k.Attribute) {
				add(errAt(id, "attribute", "Attribute names use letters, digits, '.', '-' and '_' (1-64 characters)"))
			}
			if !fieldOperators[k.Operator] {
				add(errAt(id, "operator", "Choose an operator"))
			}
			if k.Value = cfgScalar(c, "value"); len(k.Value) > maxAutomationValue {
				add(errAt(id, "value", "Value is longer than %d bytes", maxAutomationValue))
			}
		case "":
			add(errAt(id, "field", "Choose what to check"))
		default:
			add(notSupported(id, "field", k.Field))
		}
		cn.Cond = k
	case KindAction:
		a := &ActionCfg{Action: cfgString(c, "action")}
		switch a.Action {
		case "add_to_list", "remove_from_list":
			var ve []model.AutomationValidationError
			a.ListUUID, ve = checkUUID(id, "listUuid", cfgString(c, "listUuid"), "Choose a list")
			add(ve...)
		case "update_field":
			a.Attribute, a.Value = cfgString(c, "attribute"), cfgScalar(c, "value")
			switch {
			case !attributeKeyRe.MatchString(a.Attribute):
				add(errAt(id, "attribute", "Attribute names use letters, digits, '.', '-' and '_' (1-64 characters)"))
			case reservedAttrKey[strings.ToLower(a.Attribute)]:
				add(errAt(id, "attribute", "Automations cannot change a contact's %s", strings.ToLower(a.Attribute)))
			}
			if len(a.Value) > maxAutomationValue {
				add(errAt(id, "value", "Value is longer than %d bytes", maxAutomationValue))
			}
		case "":
			add(errAt(id, "action", "Choose an action"))
		default:
			add(notSupported(id, "action", a.Action))
		}
		cn.Action = a
	case KindWebhook:
		for _, k := range []string{"url", "method"} {
			if cfgString(c, k) != "" {
				add(errAt(id, k, "Webhook %s is not supported; choose an endpoint from Settings → Webhooks", k))
			}
		}
		var ve []model.AutomationValidationError
		h := &WebhookCfg{}
		h.WebhookUUID, ve = checkUUID(id, "webhookUuid", cfgString(c, "webhookUuid"), "Choose a webhook endpoint")
		add(ve...)
		cn.Webhook = h
	}
	return cn, errs
}

// WorkflowHash identifies a normalized graph's behaviour: kinds, configs and
// connections. Positions, labels and edge styling do not change it.
func WorkflowHash(w *model.Workflow) string {
	type node struct {
		ID     string         `json:"id"`
		Kind   string         `json:"kind"`
		Config map[string]any `json:"config"`
	}
	type edge struct {
		Source string `json:"s"`
		Handle string `json:"h"`
		Target string `json:"t"`
	}
	canon := struct {
		Schema int    `json:"schema"`
		Nodes  []node `json:"nodes"`
		Edges  []edge `json:"edges"`
	}{Schema: model.WorkflowSchemaVersion}
	for _, n := range w.Nodes {
		canon.Nodes = append(canon.Nodes, node{n.ID, n.Data.Type, n.Data.Config})
	}
	for _, e := range w.Edges {
		canon.Edges = append(canon.Edges, edge{e.Source, e.SourceHandle, e.Target})
	}
	sort.Slice(canon.Nodes, func(i, j int) bool { return canon.Nodes[i].ID < canon.Nodes[j].ID })
	sort.Slice(canon.Edges, func(i, j int) bool {
		a, b := canon.Edges[i], canon.Edges[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Handle != b.Handle {
			return a.Handle < b.Handle
		}
		return a.Target < b.Target
	})
	b, _ := json.Marshal(canon) // map keys marshal sorted
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// EvalCondition decides a condition for a contact. opened/clicked come from
// the referenced email step's message. A missing attribute is false.
func EvalCondition(c *CondCfg, contact ContactFacts, opened, clicked bool) bool {
	switch c.Field {
	case "email_opened":
		return opened
	case "email_clicked":
		return clicked
	case "engagement_score":
		return compareValues(strconv.FormatFloat(contact.EngagementScore, 'f', -1, 64), c.Operator, c.Value)
	case "custom_field":
		v, ok := contact.Attributes[c.Attribute]
		if !ok || v == nil {
			return false
		}
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case float64:
			s = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			s = strconv.FormatBool(x)
		default:
			b, _ := json.Marshal(x)
			s = string(b)
		}
		return compareValues(s, c.Operator, c.Value)
	}
	return false
}

// compareValues compares numerically when both sides parse as numbers, else
// as case-insensitive strings.
func compareValues(actual, op, want string) bool {
	a, errA := strconv.ParseFloat(strings.TrimSpace(actual), 64)
	b, errB := strconv.ParseFloat(strings.TrimSpace(want), 64)
	if errA == nil && errB == nil {
		switch op {
		case "equals":
			return a == b
		case "not_equals":
			return a != b
		case "greater_than":
			return a > b
		case "less_than":
			return a < b
		case "contains":
			return strings.Contains(strings.ToLower(actual), strings.ToLower(want))
		}
		return false
	}
	x, y := strings.ToLower(actual), strings.ToLower(want)
	switch op {
	case "equals":
		return x == y
	case "not_equals":
		return x != y
	case "contains":
		return strings.Contains(x, y)
	case "greater_than":
		return x > y
	case "less_than":
		return x < y
	}
	return false
}

// PrepareAutomationDraft normalizes a graph being saved and enforces the
// structural limits; full validation waits for validate/activate.
func PrepareAutomationDraft(w *model.Workflow, triggerType string, cfg map[string]any) (*model.Workflow, string, map[string]any, error) {
	norm, trigger, triggerCfg, err := NormalizeWorkflow(w, triggerType, cfg)
	if err != nil {
		return nil, "", nil, &AutomationInvalidError{Errors: []model.AutomationValidationError{errAt("", "triggerType", "%s", err.Error())}}
	}
	if errs := ValidateStructure(norm); len(errs) > 0 {
		return nil, "", nil, &AutomationInvalidError{Errors: errs}
	}
	return norm, trigger, triggerCfg, nil
}

// ValidReentryPolicy reports whether p is a stored reentry policy.
func ValidReentryPolicy(p string) bool { return p == "never" || p == "after_exit" }
