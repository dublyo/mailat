package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/middleware"
)

func publishedContract(t *testing.T) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	b, err := os.ReadFile("../../internal/apidocs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]interface{}
	if err = json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	return spec["paths"].(map[string]interface{}), spec["components"].(map[string]interface{})["schemas"].(map[string]interface{})
}

func TestCompleteRouterInventoryAndAccessMetadata(t *testing.T) {
	paths, _ := publishedContract(t)
	raw, err := os.ReadFile("../../internal/router/router.go")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	colon := regexp.MustCompile(`:(\w+)`)
	for _, m := range regexp.MustCompile(`(\w+)\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)",\s*(\w+)\.(\w+)\)`).FindAllStringSubmatch(string(raw), -1) {
		// The public docs lambdas/serveOpenAPI are not controller API operations.
		if m[4] == "r" {
			continue
		}
		path := "/api/v1" + m[3]
		if m[1] == "authGroup" {
			path = "/api/v1/auth" + m[3]
		}
		want[strings.ToLower(m[2])+" "+colon.ReplaceAllString(path, "{$1}")] = true
	}
	for path, value := range paths {
		for method, value := range value.(map[string]interface{}) {
			entry := method + " " + path
			if !want[entry] {
				t.Errorf("unregistered operation %s", entry)
			}
			delete(want, entry)
			op := value.(map[string]interface{})
			scope, allowed := middleware.APIKeyScope(strings.ToUpper(method), path)
			if allowed && op["x-api-key-scope"] != scope {
				t.Errorf("scope mismatch %s", entry)
			}
			if !allowed && op["x-api-key-scope"] != nil {
				t.Errorf("unexpected key authority %s", entry)
			}
			if !allowed && op["x-human-session-required"] != (len(op["security"].([]interface{})) > 0) {
				t.Errorf("session/public classification mismatch %s", entry)
			}
			if op["x-mailat-backend"] != "mailat" && op["x-mailat-backend"] != "ses" && op["x-mailat-backend"] != "jmap" {
				t.Errorf("backend missing %s", entry)
			}
			if op["x-mailat-backend-note"] == "" {
				t.Errorf("backend explanation missing %s", entry)
			}
			for status, value := range op["responses"].(map[string]interface{}) {
				if !strings.HasPrefix(status, "2") {
					continue
				}
				content, _ := value.(map[string]interface{})["content"].(map[string]interface{})
				media, _ := content["application/json"].(map[string]interface{})
				schema, _ := media["schema"].(map[string]interface{})
				properties, _ := schema["properties"].(map[string]interface{})
				if data, exists := properties["data"].(map[string]interface{}); exists && len(data) == 0 {
					t.Errorf("opaque success envelope %s", entry)
				}
			}
		}
	}
	for missing := range want {
		t.Errorf("route absent from contract: %s", missing)
	}
}

func TestPublishedResponseAndQueryCorrections(t *testing.T) {
	paths, schemas := publishedContract(t)
	op := func(path, method string) map[string]interface{} {
		return paths[path].(map[string]interface{})[method].(map[string]interface{})
	}
	data := func(path, method, status string) map[string]interface{} {
		return op(path, method)["responses"].(map[string]interface{})[status].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})["properties"].(map[string]interface{})["data"].(map[string]interface{})
	}
	queries := map[string]interface{}{}
	for _, value := range op("/api/v1/webhook-deliveries", "get")["parameters"].([]interface{}) {
		p := value.(map[string]interface{})
		queries[p["name"].(string)] = p["schema"]
	}
	for _, name := range []string{"status", "page", "pageSize"} {
		if queries[name] == nil {
			t.Fatalf("missing delivery query %s", name)
		}
	}
	if queries["pageSize"].(map[string]interface{})["type"] != "integer" || queries["pageSize"].(map[string]interface{})["default"] != float64(50) {
		t.Fatal("delivery page size lost its actual type/default")
	}
	for _, path := range []string{"/api/v1/webhook-triggers", "/api/v1/shared-mailboxes", "/api/v1/shared-mailboxes/{id}/members", "/api/v1/forwards", "/api/v1/push/subscribe"} {
		r := op(path, "post")["responses"].(map[string]interface{})
		if r["201"] == nil || r["200"] != nil {
			t.Errorf("Created handler must advertise 201: %s", path)
		}
	}
	if op("/api/v1/oauth/{provider}", "delete")["responses"].(map[string]interface{})["200"] == nil {
		t.Fatal("unlink is JSON, not an OAuth redirect")
	}
	domains := data("/api/v1/domains", "get", "200")
	if domains["type"] != "array" {
		t.Fatal("domain collection is not an array")
	}
	fields := domains["items"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, name := range []string{"uuid", "name", "receivingEnabled", "sesVerified", "dnsRecords"} {
		if len(fields[name].(map[string]interface{})) == 0 {
			t.Errorf("domain field missing: %s", name)
		}
	}
	if data("/api/v1/domains/{uuid}/dmarc", "get", "200")["$ref"] != "#/components/schemas/provider.DMARCInspection" {
		t.Fatal("DMARC inspection is opaque")
	}
	if data("/api/v1/inbox/filters/{uuid}/test", "post", "200")["properties"].(map[string]interface{})["matches"].(map[string]interface{})["type"] != "boolean" {
		t.Fatal("filter preview missing its actual response")
	}
	for _, path := range []string{"/api/v1/inbox", "/api/v1/inbox/emails/{id}", "/api/v1/inbox/threads/{id}"} {
		if op(path, "get")["x-mailat-backend"] != "jmap" {
			t.Errorf("legacy backend missing %s", path)
		}
	}
	if op("/api/v1/inbox/received", "get")["x-mailat-backend"] != "ses" {
		t.Fatal("SES mailbox misclassified")
	}
	for path := range paths {
		if strings.Contains(path, "sieve") {
			t.Fatalf("Sieve is removed but %s is documented", path)
		}
	}
	if op("/api/v1/forwards", "post")["x-mailat-backend"] != "ses" || len(op("/api/v1/forwards/verify", "post")["security"].([]interface{})) != 0 {
		t.Fatal("forwarding must be documented as SES-executed with a public verify route")
	}
	if paths["/api/v1/forwards/{id}/verify"] != nil || paths["/api/v1/forwards/{uuid}/resend-verification"] == nil {
		t.Fatal("forward routes are stale")
	}
	reply := op("/api/v1/compose/reply/{id}", "get")["parameters"].([]interface{})
	foundReplyAll := false
	for _, value := range reply {
		p := value.(map[string]interface{})
		if p["name"] == "id" && p["in"] != "path" {
			t.Fatal("path id became a query field")
		}
		if p["name"] == "replyAll" {
			foundReplyAll = p["schema"].(map[string]interface{})["type"] == "boolean"
		}
	}
	if !foundReplyAll {
		t.Fatal("replyAll query missing")
	}
	props := schemas["model.CreateIdentityRequest"].(map[string]interface{})
	if len(props["required"].([]interface{})) != 2 {
		t.Fatal("identity service requirements missing")
	}
	if schemas["model.BatchSendRequest"].(map[string]interface{})["properties"].(map[string]interface{})["emails"].(map[string]interface{})["maxItems"] != float64(100) {
		t.Fatal("batch limit missing")
	}
	if op("/api/v1/unsubscribe/{token}", "delete")["requestBody"] == nil {
		t.Fatal("DELETE confirmation's reason body was lost")
	}
	delivery := schemas["eventoutbox.Delivery"].(map[string]interface{})["properties"].(map[string]interface{})
	payload := delivery["payload"].(map[string]interface{})["allOf"].([]interface{})[0].(map[string]interface{})
	if payload["$ref"] != "#/components/schemas/eventoutbox.Envelope" {
		t.Fatal("delivery detail must expose the actual signed event envelope")
	}
	envelope := schemas["eventoutbox.Envelope"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, field := range []string{"version", "id", "type", "createdAt", "data"} {
		if envelope[field] == nil {
			t.Errorf("signed event field missing: %s", field)
		}
	}
	if !strings.Contains(op("/api/v1/compose/reply/{id}", "get")["description"].(string), "from.email") {
		t.Fatal("reply context must explain the send request's sender mapping")
	}
}

func TestQueryDiscoveryDoesNotInventBodyOrPathFields(t *testing.T) {
	source := `package sample
func handler() {
 id := r.Get("uuid").String()
 page := r.Get("page").Int()
 active := r.GetQuery("active", false).Bool()
 size := r.GetQuery("pageSize", 20).Int()
 token := r.Header.Get("Authorization")
}`
	f, err := parser.ParseFile(token.NewFileSet(), "query.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := f.Decls[0].(*ast.FuncDecl)
	get := map[string]object{}
	for _, p := range queryParameters(handler, "GET") {
		get[p["name"].(string)] = p["schema"].(object)
	}
	if get["page"]["type"] != "integer" || get["active"]["default"] != false || get["pageSize"]["default"] != 20 {
		t.Fatal("query conversion/default inference changed")
	}
	if get["Authorization"] != nil {
		t.Fatal("header became a query parameter")
	}
	for _, p := range queryParameters(handler, "POST") {
		if p["name"] == "uuid" || p["name"] == "page" {
			t.Fatal("r.Get POST body became a query parameter")
		}
	}
}
