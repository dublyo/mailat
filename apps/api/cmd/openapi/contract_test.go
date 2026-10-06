package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGeneratedContractAndPublishedLinks(t *testing.T) {
	root := filepath.Join("..", "..")
	check := exec.Command("go", "run", "./cmd/openapi", "--check")
	check.Dir = root
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated API drift: %v\n%s", err, output)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal/apidocs/openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]interface{}
	if err = json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]interface{})
	schemas := spec["components"].(map[string]interface{})["schemas"].(map[string]interface{})
	ids := map[string]bool{}
	param := regexp.MustCompile(`\{(\w+)\}`)
	for path, methods := range paths {
		for method, entry := range methods.(map[string]interface{}) {
			op := entry.(map[string]interface{})
			id := op["operationId"].(string)
			if ids[id] {
				t.Fatalf("duplicate operation id %s", id)
			}
			ids[id] = true
			params := map[string]bool{}
			if values, ok := op["parameters"].([]interface{}); ok {
				for _, value := range values {
					p := value.(map[string]interface{})
					if p["in"] == "path" && p["required"] == true {
						params[p["name"].(string)] = true
					}
				}
			}
			for _, p := range param.FindAllStringSubmatch(path, -1) {
				if !params[p[1]] {
					t.Errorf("%s %s missing path parameter %s", method, path, p[1])
				}
			}
		}
	}
	var walk func(interface{})
	walk = func(v interface{}) {
		switch x := v.(type) {
		case map[string]interface{}:
			if ref, ok := x["$ref"].(string); ok {
				if _, exists := schemas[strings.TrimPrefix(ref, "#/components/schemas/")]; !exists {
					t.Errorf("unresolved ref %s", ref)
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []interface{}:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(spec)
	ui, err := os.ReadFile(filepath.Join(root, "../web/src/views/API.vue"))
	if err != nil {
		t.Fatal(err)
	}
	colon := regexp.MustCompile(`:(\w+)`)
	for _, m := range regexp.MustCompile(`method: '(\w+)', path: '([^']+)'`).FindAllStringSubmatch(string(ui), -1) {
		path := colon.ReplaceAllString(m[2], "{$1}")
		verbs, ok := paths[path].(map[string]interface{})
		if !ok || verbs[strings.ToLower(m[1])] == nil {
			t.Errorf("broken API docs link: %s %s", m[1], m[2])
		}
	}
	for _, path := range []string{"/api/v1/inbox/changes", "/api/v1/inbox/labels", "/api/v1/inbox/filters", "/api/v1/webhook-deliveries", "/api/v1/domains/{uuid}/sending-status"} {
		if paths[path] == nil {
			t.Errorf("core automation route absent: %s", path)
		}
	}
}

func TestDMARCReportsFolderContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "internal/apidocs/openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]interface{}
	if err = json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]interface{})["schemas"].(map[string]interface{})
	props := func(name string) map[string]interface{} {
		t.Helper()
		v, ok := schemas[name].(map[string]interface{})
		if !ok {
			t.Fatalf("missing schema %s", name)
		}
		return v["properties"].(map[string]interface{})
	}
	contains := func(values interface{}, want string) bool {
		for _, v := range values.([]interface{}) {
			if v == want {
				return true
			}
		}
		return false
	}
	counts := props("model.InboxCountsResponse")
	for _, name := range []string{"inboxUnread", "dmarcReports", "dmarcReportsUnread", "unread"} {
		if counts[name].(map[string]interface{})["type"] != "integer" {
			t.Errorf("count type missing: %s", name)
		}
	}
	move := props("model.MoveEmailsRequest")["folder"].(map[string]interface{})
	if !contains(move["enum"], "dmarc-reports") || contains(move["enum"], "all") {
		t.Fatal("move destination enum does not match service restrictions")
	}
	preference := props("service.UserSettings")["autoOrganizeDmarcReports"].(map[string]interface{})
	if preference["type"] != "boolean" || preference["default"] != true {
		t.Fatal("preference default missing")
	}
	update := schemas["service.UpdateSettingsRequest"].(map[string]interface{})
	if required, ok := update["required"]; ok && contains(required, "autoOrganizeDmarcReports") {
		t.Fatal("partial preference update must be optional")
	}
	paths := spec["paths"].(map[string]interface{})
	for _, verb := range []string{"get", "put"} {
		op := paths["/api/v1/settings"].(map[string]interface{})[verb].(map[string]interface{})
		if op["x-human-session-required"] != true || op["x-api-key-scope"] != nil {
			t.Fatal("settings authorization contract changed")
		}
	}
	list := paths["/api/v1/inbox/received"].(map[string]interface{})["get"].(map[string]interface{})
	found := false
	for _, value := range list["parameters"].([]interface{}) {
		p := value.(map[string]interface{})
		if p["name"] == "folder" {
			found = contains(p["schema"].(map[string]interface{})["enum"], "dmarc-reports")
		}
	}
	if !found {
		t.Fatal("list query omitted DMARC reports")
	}
}

func TestCampaignContract(t *testing.T) {
	paths, schemas := publishedContract(t)
	op := func(path, method string) map[string]interface{} {
		t.Helper()
		verbs, ok := paths[path].(map[string]interface{})
		if !ok || verbs[method] == nil {
			t.Fatalf("missing %s %s", method, path)
		}
		return verbs[method].(map[string]interface{})
	}
	dataRef := func(o map[string]interface{}) string {
		schema := o["responses"].(map[string]interface{})["200"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
		data := schema["properties"].(map[string]interface{})["data"].(map[string]interface{})
		if all, ok := data["allOf"].([]interface{}); ok {
			return all[0].(map[string]interface{})["$ref"].(string)
		}
		return ""
	}
	for _, tc := range []struct{ method, path, scope, ref string }{
		{"get", "/api/v1/campaigns/{uuid}/progress", "campaigns:read", "model.CampaignProgressResponse"},
		{"get", "/api/v1/campaigns/{uuid}/audience", "campaigns:read", "model.CampaignAudienceResponse"},
		{"get", "/api/v1/campaigns/{uuid}/recipients", "campaigns:read", "model.CampaignRecipientListResponse"},
		{"get", "/api/v1/campaigns/{uuid}/stats", "campaigns:read", "model.CampaignStatsResponse"},
		{"post", "/api/v1/campaigns/{uuid}/preview", "campaigns:read", "model.CampaignPreviewResponse"},
		{"post", "/api/v1/campaigns/{uuid}/test", "campaigns:manage", "model.CampaignTestResponse"},
		{"post", "/api/v1/campaigns/{uuid}/cancel", "campaigns:manage", "model.Campaign"},
		{"get", "/api/v1/campaign-settings", "campaigns:read", "model.CampaignSettings"},
		{"put", "/api/v1/campaign-settings", "campaigns:manage", "model.CampaignSettings"},
	} {
		o := op(tc.path, tc.method)
		if o["x-api-key-scope"] != tc.scope || o["x-mailat-backend"] != "ses" {
			t.Errorf("%s %s: scope %v backend %v", tc.method, tc.path, o["x-api-key-scope"], o["x-mailat-backend"])
		}
		if got := dataRef(o); got != "#/components/schemas/"+tc.ref {
			t.Errorf("%s %s returns %q", tc.method, tc.path, got)
		}
	}
	key := false
	for _, value := range op("/api/v1/campaigns/{uuid}/test", "post")["parameters"].([]interface{}) {
		p := value.(map[string]interface{})
		if p["name"] == "Idempotency-Key" {
			key = p["in"] == "header" && p["required"] == true
		}
	}
	if !key {
		t.Fatal("test send must require an Idempotency-Key header")
	}
	emails := schemas["model.CampaignTestRequest"].(map[string]interface{})["properties"].(map[string]interface{})["emails"].(map[string]interface{})
	if emails["minItems"] != float64(1) || emails["maxItems"] != float64(5) {
		t.Fatal("test send recipient bounds missing")
	}
	campaign := schemas["model.Campaign"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, field := range []string{"trackOpens", "trackClicks", "statusReason", "preparedAt", "throttledUntil", "failedCount", "skippedCount", "unknownCount", "listType", "createdByUserId"} {
		if campaign[field] == nil {
			t.Errorf("campaign field missing: %s", field)
		}
	}
	for _, legacy := range []string{"stats", "listIds", "htmlBody", "fromIdentityId"} {
		if campaign[legacy] != nil {
			t.Errorf("legacy campaign field still published: %s", legacy)
		}
	}
	if !strings.Contains(op("/api/v1/campaigns", "post")["x-mailat-backend-note"].(string), "feedback") {
		t.Fatal("campaign backend note must state the SES sender requirements")
	}
}

func TestAutomationContract(t *testing.T) {
	paths, _ := publishedContract(t)
	for _, tc := range []struct{ method, path, scope, ref string }{
		{"get", "/api/v1/automations/{uuid}/enrollments", "automations:read", "model.AutomationEnrollmentListResult"},
		{"get", "/api/v1/automations/{uuid}/enrollments/{enrollmentUuid}", "automations:read", "model.AutomationEnrollmentView"},
		{"post", "/api/v1/automations/{uuid}/enroll", "automations:enroll", "model.EnrollResult"},
		{"post", "/api/v1/automations/{uuid}/enrollments/{enrollmentUuid}/cancel", "automations:enroll", "model.AutomationEnrollmentView"},
		{"post", "/api/v1/automations/{uuid}/enrollments/{enrollmentUuid}/retry", "automations:enroll", "model.AutomationEnrollmentView"},
		{"post", "/api/v1/automations/{uuid}/activate", "", ""},
		{"post", "/api/v1/automations/{uuid}/archive", "", ""},
	} {
		o := paths[tc.path].(map[string]interface{})[tc.method].(map[string]interface{})
		if tc.scope == "" {
			if o["x-api-key-scope"] != nil || o["x-human-session-required"] != true {
				t.Errorf("%s %s must be session-only", tc.method, tc.path)
			}
			continue
		}
		if o["x-api-key-scope"] != tc.scope || o["x-mailat-backend"] != "ses" {
			t.Errorf("%s %s: scope %v backend %v", tc.method, tc.path, o["x-api-key-scope"], o["x-mailat-backend"])
		}
		schema := o["responses"].(map[string]interface{})["200"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
		data := schema["properties"].(map[string]interface{})["data"].(map[string]interface{})
		if all, ok := data["allOf"].([]interface{}); !ok || all[0].(map[string]interface{})["$ref"] != "#/components/schemas/"+tc.ref {
			t.Errorf("%s %s returns %v", tc.method, tc.path, data)
		}
	}
}
