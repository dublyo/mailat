// Command openapi derives the route inventory and JSON DTO schemas from the
// application. Run from apps/api; --check detects stale generated contracts.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dublyo/mailat/api/internal/middleware"
)

type object = map[string]interface{}
type declaration struct {
	pkg  string
	expr ast.Expr
}

var types = map[string]declaration{}
var methods = map[string]*ast.FuncDecl{}
var methodPackages = map[string]string{}
var controllerFields = map[string]map[string]string{}

func key(pkg, name string) string { return pkg + "." + name }
func typeName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return typeName(x.X)
	case *ast.SelectorExpr:
		return typeName(x.X) + "." + x.Sel.Name
	}
	return ""
}
func schema(expr ast.Expr, pkg string) object {
	switch x := expr.(type) {
	case *ast.StarExpr:
		s := schema(x.X, pkg)
		return object{"allOf": []interface{}{s}, "nullable": true}
	case *ast.Ident:
		switch x.Name {
		case "string":
			return object{"type": "string"}
		case "bool":
			return object{"type": "boolean"}
		case "int", "int32", "int64", "uint", "uint64":
			return object{"type": "integer", "format": "int64"}
		case "float32", "float64":
			return object{"type": "number"}
		case "interface{}", "any":
			return object{}
		}
		if _, ok := types[key(pkg, x.Name)]; ok {
			return object{"$ref": "#/components/schemas/" + key(pkg, x.Name)}
		}
	case *ast.SelectorExpr:
		name := typeName(x)
		if name == "time.Time" {
			return object{"type": "string", "format": "date-time"}
		}
		if name == "json.RawMessage" {
			return object{}
		}
		if _, ok := types[name]; ok {
			return object{"$ref": "#/components/schemas/" + name}
		}
	case *ast.ArrayType:
		if typeName(x.Elt) == "byte" {
			return object{"type": "string", "format": "byte"}
		}
		return object{"type": "array", "items": schema(x.Elt, pkg)}
	case *ast.MapType:
		return object{"type": "object", "additionalProperties": schema(x.Value, pkg)}
	case *ast.StructType:
		props := object{}
		required := []string{}
		for _, f := range x.Fields.List {
			if len(f.Names) == 0 || !ast.IsExported(f.Names[0].Name) {
				continue
			}
			name := f.Names[0].Name
			tag := ""
			if f.Tag != nil {
				tag, _ = strconv.Unquote(f.Tag.Value)
			}
			jt := reflect.StructTag(tag).Get("json")
			if jt == "-" || jt == "" {
				continue
			}
			if jt != "" {
				name = strings.Split(jt, ",")[0]
			}
			v := reflect.StructTag(tag).Get("v")
			p := schema(f.Type, pkg)
			if strings.Contains(v, "required") {
				required = append(required, name)
			}
			if strings.Contains(v, "email") {
				p["format"] = "email"
			}
			if f.Comment != nil {
				p["description"] = strings.TrimSpace(f.Comment.Text())
			}
			props[name] = p
		}
		out := object{"type": "object", "properties": props}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	return object{}
}
func load() {
	for _, pkg := range []string{"model", "service", "controller", "eventoutbox", "handler"} {
		files, err := filepath.Glob("internal/" + pkg + "/*.go")
		must(err)
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			must(err)
			for _, d := range f.Decls {
				switch x := d.(type) {
				case *ast.GenDecl:
					for _, spec := range x.Specs {
						t, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						types[key(pkg, t.Name.Name)] = declaration{pkg, t.Type}
						if st, ok := t.Type.(*ast.StructType); ok {
							fields := map[string]string{}
							for _, field := range st.Fields.List {
								if len(field.Names) > 0 {
									fields[field.Names[0].Name] = typeName(field.Type)
								}
							}
							controllerFields[t.Name.Name] = fields
						}
					}
				case *ast.FuncDecl:
					if x.Recv != nil && len(x.Recv.List) > 0 {
						k := typeName(x.Recv.List[0].Type) + "." + x.Name.Name
						methods[k] = x
						methodPackages[k] = pkg
					}
				}
			}
		}
	}
}
func requestSchema(f *ast.FuncDecl, pkg string) object {
	var out object
	if f == nil {
		return nil
	}
	ast.Inspect(f.Body, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if ok && v.Type != nil {
			for _, name := range v.Names {
				if name.Name == "req" || name.Name == "input" {
					out = schema(v.Type, pkg)
				}
			}
		}
		return true
	})
	return out
}

// methodResult follows fluent service calls (ForUser(...).List(...)) as well
// as direct calls, so owned webhook resources keep their response contracts.
func methodResult(controller string, call *ast.CallExpr) (*ast.FuncDecl, string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, ""
	}
	var serviceType func(ast.Expr) string
	serviceType = func(expr ast.Expr) string {
		switch x := expr.(type) {
		case *ast.SelectorExpr:
			return strings.TrimPrefix(controllerFields[controller][x.Sel.Name], "service.")
		case *ast.CallExpr:
			m, pkg := methodResult(controller, x)
			if m != nil && m.Type.Results != nil && len(m.Type.Results.List) > 0 {
				return strings.TrimPrefix(typeName(m.Type.Results.List[0].Type), pkg+".")
			}
		}
		return ""
	}
	name := serviceType(sel.X) + "." + sel.Sel.Name
	return methods[name], methodPackages[name]
}
func resultSchema(controller string, f *ast.FuncDecl) object {
	if f == nil {
		return object{}
	}
	locals := map[string]object{}
	var infer func(ast.Expr) object
	infer = func(expr ast.Expr) object {
		switch x := expr.(type) {
		case *ast.Ident:
			if s, ok := locals[x.Name]; ok {
				return s
			}
			if x.Name == "true" || x.Name == "false" {
				return object{"type": "boolean"}
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				return object{"type": "string"}
			}
			return object{"type": "number"}
		case *ast.CompositeLit:
			if _, ok := x.Type.(*ast.MapType); ok {
				props := object{}
				for _, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if lit, ok := kv.Key.(*ast.BasicLit); ok {
							name, _ := strconv.Unquote(lit.Value)
							props[name] = infer(kv.Value)
						}
					}
				}
				return object{"type": "object", "properties": props}
			}
			return schema(x.Type, "controller")
		case *ast.CallExpr:
			m, pkg := methodResult(controller, x)
			if m != nil && m.Type.Results != nil && len(m.Type.Results.List) > 0 {
				return schema(m.Type.Results.List[0].Type, pkg)
			}
		}
		return object{}
	}
	out := object{}
	ast.Inspect(f.Body, func(n ast.Node) bool {
		if a, ok := n.(*ast.AssignStmt); ok && len(a.Rhs) == 1 {
			if call, ok := a.Rhs[0].(*ast.CallExpr); ok {
				m, pkg := methodResult(controller, call)
				if m != nil && m.Type.Results != nil {
					for i, lhs := range a.Lhs {
						if i >= len(m.Type.Results.List) {
							break
						}
						if id, ok := lhs.(*ast.Ident); ok && id.Name != "err" {
							locals[id.Name] = schema(m.Type.Results.List[i].Type, pkg)
						}
					}
				}
			} else if len(a.Lhs) == 1 {
				if id, ok := a.Lhs[0].(*ast.Ident); ok {
					locals[id.Name] = infer(a.Rhs[0])
				}
			}
		}
		if call, ok := n.(*ast.CallExpr); ok && len(call.Args) >= 2 {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && typeName(sel.X) == "response" && (sel.Sel.Name == "Success" || sel.Sel.Name == "SuccessWithMessage" || sel.Sel.Name == "Created") {
				index := 1
				if sel.Sel.Name == "SuccessWithMessage" {
					index = 2
				}
				if index < len(call.Args) {
					out = infer(call.Args[index])
				}
			}
		}
		return true
	})
	return out
}
func envelope(data object) object {
	return object{"type": "object", "required": []string{"code", "message"}, "properties": object{"code": object{"type": "integer", "example": 0}, "message": object{"type": "string"}, "data": data}}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	load()
	raw, err := os.ReadFile("internal/router/router.go")
	must(err)
	source := string(raw)
	bindings := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+)\s*:=\s*(?:controller|handler).New(\w+)\(`).FindAllStringSubmatch(source, -1) {
		bindings[m[1]] = m[2]
	}
	paths := object{}
	public := map[string]bool{"/health": true, "/ready": true, "/auth/register-status": true, "/auth/register": true, "/auth/login": true, "/auth/2fa/challenge": true, "/webhooks/ses/incoming": true, "/oauth/providers": true, "/oauth/:provider": true, "/oauth/:provider/callback": true, "/forwards/:id/verify": true}
	lineRx := regexp.MustCompile(`(\w+)\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)",\s*(\w+)\.(\w+)\)`)
	paramRx := regexp.MustCompile(`:(\w+)`)
	for _, m := range lineRx.FindAllStringSubmatch(source, -1) {
		group, verb, path, ctrl, method := m[1], m[2], m[3], m[4], m[5]
		if group == "authGroup" {
			path = "/auth" + path
		}
		if ctrl == "r" || bindings[ctrl] == "" {
			continue
		}
		full := "/api/v1" + path
		templated := paramRx.ReplaceAllString(full, "{$1}")
		controller := bindings[ctrl]
		f := methods[controller+"."+method]
		description := method
		if f != nil && f.Doc != nil {
			description = strings.TrimSpace(f.Doc.Text())
		}
		op := object{"operationId": strings.ToLower(verb) + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(full, "/", "_"), ":", ""), "-", "_"), "summary": method, "description": description, "tags": []string{strings.Split(strings.TrimPrefix(path, "/"), "/")[0]}, "security": []object{{"bearerAuth": []string{}}}}
		if public[path] || strings.HasPrefix(path, "/tracking/") || strings.HasPrefix(path, "/unsubscribe/") || strings.HasPrefix(path, "/preferences/") || strings.HasPrefix(path, "/confirm/") {
			op["security"] = []object{}
		}
		if scope, ok := middleware.APIKeyScope(verb, full); ok {
			op["x-api-key-scope"] = scope
		} else {
			op["x-human-session-required"] = len(op["security"].([]object)) > 0
		}
		params := []object{}
		for _, p := range paramRx.FindAllStringSubmatch(path, -1) {
			params = append(params, object{"name": p[1], "in": "path", "required": true, "schema": object{"type": "string"}})
		}
		req := requestSchema(f, methodPackages[controller+"."+method])
		if verb == "GET" && req != nil {
			rs := req
			if ref, ok := req["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				d := types[name]
				rs = schema(d.expr, d.pkg)
				customizeSchema(name, rs)
			}
			if props, ok := rs["properties"].(object); ok {
				names := make([]string, 0, len(props))
				for name := range props {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					params = append(params, object{"name": name, "in": "query", "schema": props[name]})
				}
			}
		}
		if verb == "POST" || verb == "PUT" || verb == "PATCH" {
			if req != nil {
				op["requestBody"] = object{"required": true, "content": object{"application/json": object{"schema": req}}}
			}
		}
		if f != nil {
			seen := map[string]bool{}
			for _, p := range params {
				seen[fmt.Sprint(p["name"])] = true
			}
			ast.Inspect(f.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || typeName(sel.X) != "r" || sel.Sel.Name != "GetQuery" {
					return true
				}
				v, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return true
				}
				name, _ := strconv.Unquote(v.Value)
				if !seen[name] {
					params = append(params, object{"name": name, "in": "query", "schema": object{"type": "string"}})
					seen[name] = true
				}
				return true
			})
		}
		if full == "/api/v1/emails" && verb == "POST" || full == "/api/v1/emails/batch" || full == "/api/v1/compose/send" {
			params = append(params, object{"name": "Idempotency-Key", "in": "header", "required": full != "/api/v1/emails", "schema": object{"type": "string", "minLength": 8, "maxLength": 128}, "description": "Persist one key per logical submission. Reuse it with the identical request on retries; changed content returns 409. Single-email sends accept either this header or JSON idempotencyKey; both must agree when supplied together. Batch and compose require this header."})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		responses := object{"200": object{"description": "Success. Collections return an empty array when no resources match.", "content": object{"application/json": object{"schema": envelope(resultSchema(controller, f))}}}}
		for _, status := range []string{"400", "401", "403", "404", "409", "410", "429", "500"} {
			responses[status] = object{"description": map[string]string{"400": "Invalid input", "401": "Missing, expired or revoked authentication", "403": "Insufficient permission", "404": "Resource not found", "409": "Conflict or changed idempotency payload", "410": "Recovery cursor expired; resynchronize", "429": "Rate limit exceeded; respect Retry-After", "500": "Internal failure; retain the idempotency key"}[status], "content": object{"application/json": object{"schema": envelope(object{})}}}
		}
		for _, status := range []string{"400", "401", "403", "404", "409", "410", "429", "500"} {
			value, _ := strconv.Atoi(status)
			responses[status].(object)["content"].(object)["application/json"].(object)["schema"] = object{"type": "object", "required": []string{"code", "message"}, "properties": object{"code": object{"type": "integer", "example": value}, "message": object{"type": "string"}}}
		}
		responses["429"].(object)["headers"] = object{"Retry-After": object{"description": "Seconds until the next fixed-minute request window.", "schema": object{"type": "integer", "minimum": 1}}}
		op["responses"] = responses
		customize(op, verb, path)
		if paths[templated] == nil {
			paths[templated] = object{}
		}
		paths[templated].(object)[strings.ToLower(verb)] = op
	}
	schemas := object{}
	for name, d := range types {
		if _, ok := d.expr.(*ast.StructType); !ok {
			continue
		}
		s := schema(d.expr, d.pkg)
		customizeSchema(name, s)
		if props, ok := s["properties"].(object); ok && props != nil {
			schemas[name] = s
		}
	}
	reachable := object{}
	var collect func(interface{})
	collect = func(v interface{}) {
		switch x := v.(type) {
		case map[string]interface{}:
			if ref, ok := x["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if _, seen := reachable[name]; !seen {
					if d, exists := schemas[name]; exists {
						reachable[name] = d
						collect(d)
					} else {
						panic("missing schema " + name)
					}
				}
			}
			for _, v := range x {
				collect(v)
			}
		case []interface{}:
			for _, v := range x {
				collect(v)
			}
		case []object:
			for _, v := range x {
				collect(v)
			}
		}
	}
	collect(paths)
	schemas = reachable
	spec := object{"openapi": "3.0.3", "info": object{"title": "Mailat API", "version": "2026-10-04", "description": "Self-hosted SES email automation. API keys use Authorization: Bearer ue_… and explicit per-operation scopes. Human-only administration is marked. Inbox detail is non-mutating. Webhooks use version 1 events and HMAC-SHA256 over timestamp + '.' + raw body. Cursor retention is 90 days."}, "servers": []object{{"url": "/"}}, "paths": paths, "components": object{"securitySchemes": object{"bearerAuth": object{"type": "http", "scheme": "bearer", "description": "Scoped Mailat API key or active human session JWT."}}, "schemas": schemas}}
	data, err := json.MarshalIndent(spec, "", "  ")
	must(err)
	data = append(data, '\n')
	path := "internal/apidocs/openapi.json"
	if len(os.Args) > 1 && os.Args[1] == "--check" {
		existing, e := os.ReadFile(path)
		must(e)
		if string(existing) != string(data) {
			fmt.Fprintln(os.Stderr, "OpenAPI is stale: run go run ./cmd/openapi")
			os.Exit(1)
		}
		fmt.Println("OpenAPI matches routes and DTOs")
		return
	}
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, data, 0644))
	fmt.Printf("Generated %d API paths\n", len(paths))
}

func customize(op object, verb, path string) {
	examples := map[string]interface{}{
		"POST /emails":                   object{"from": "hello@example.com", "to": []string{"recipient@example.net"}, "subject": "Welcome", "text": "Hello from Mailat", "attachments": []object{{"name": "hello.txt", "content": "SGVsbG8=", "type": "text/plain"}}},
		"POST /emails/batch":             object{"emails": []object{{"from": "hello@example.com", "to": []string{"recipient@example.net"}, "subject": "Welcome", "text": "Hello", "idempotencyKey": "workflow-job-42-item-1"}}},
		"POST /compose/send":             object{"identityId": 1, "to": []object{{"email": "recipient@example.net"}}, "subject": "Re: Hello", "textBody": "Thank you", "inReplyTo": "<original@example.net>", "references": []string{"<original@example.net>"}},
		"POST /compose/drafts":           object{"identityId": 1, "to": []object{{"email": "recipient@example.net"}}, "subject": "Draft", "textBody": "In progress"},
		"PUT /compose/drafts/:id":        object{"identityId": 1, "version": 1, "subject": "Updated draft", "textBody": "In progress"},
		"POST /inbox/received/mark":      object{"emailUuids": []string{"00000000-0000-4000-8000-000000000001"}, "isRead": true},
		"POST /inbox/received/star":      object{"emailUuids": []string{"00000000-0000-4000-8000-000000000001"}, "isStarred": true},
		"POST /inbox/received/move":      object{"emailUuids": []string{"00000000-0000-4000-8000-000000000001"}, "folder": "dmarc-reports"},
		"PUT /settings":                  object{"autoOrganizeDmarcReports": false},
		"POST /inbox/received/trash":     object{"emailUuids": []string{"00000000-0000-4000-8000-000000000001"}, "permanent": false},
		"POST /inbox/received/labels":    object{"emailUuids": []string{"00000000-0000-4000-8000-000000000001"}, "addLabels": []string{"Invoices"}, "removeLabels": []string{}},
		"POST /inbox/setup":              object{"domainId": 1},
		"POST /labels":                   object{"name": "Invoices", "color": "#2563eb"},
		"PUT /labels/:uuid":              object{"name": "Paid", "color": "#16a34a"},
		"POST /inbox/labels":             object{"name": "Invoices", "color": "#2563eb"},
		"PUT /inbox/labels/:uuid":        object{"name": "Paid", "color": "#16a34a"},
		"POST /webhooks":                 object{"name": "Mail automation", "url": "https://automation.example.com/webhook/mailat", "events": []string{"email.received", "email.delivered", "email.bounced"}},
		"PUT /webhooks/:uuid":            object{"active": true, "events": []string{"email.received", "email.delivered"}},
		"POST /domains":                  object{"name": "example.com"},
		"POST /identities":               object{"domainId": "00000000-0000-4000-8000-000000000001", "email": "hello@example.com", "displayName": "Example"},
		"POST /inbox/filters/:uuid/test": object{"from": "sender@example.net", "to": []string{"hello@example.com"}, "subject": "Invoice 42", "body": "Invoice details", "hasAttachments": false},
	}
	if body, ok := op["requestBody"].(object); ok {
		if content, ok := body["content"].(object); ok {
			if media, ok := content["application/json"].(object); ok {
				if example, exists := examples[verb+" "+path]; exists {
					media["example"] = example
				}
			}
		}
	}
	if path == "/compose/reply/:id" || path == "/compose/forward/:id" {
		op["description"] = "Get a context using the public SES mailbox message UUID as id. Use returned identityId, to, subject, inReplyTo, references and attachments to compose the submission; forward context includes authenticated attachment blob references. replyAll=true is supported on reply."
	}
	if path == "/domains/:uuid/setup-sending" || path == "/domains/:uuid/sending-status" {
		op["description"] = "Separate attachment storage and outbound SES feedback readiness. Setup does not enable receiving or change MX. Inspect storageReady, feedbackConfigured, subscriptionStatus, feedbackReady and reason even for HTTP 200: an existing foreign SES topic is preserved and reported as a conflict. SNS confirmation can remain pending."
	}
	if path == "/emails" && verb == "POST" || path == "/emails/batch" {
		op["description"] = fmt.Sprint(op["description"]) + " Requires a verified owned sender and stable Idempotency-Key. Base64 attachments preserve name, type (MIME), disposition and cid. Same key+content retries return the original result; changed payload is 409. A batch holds one content-bound key for the whole ordered request plus per-item keys. Inspect each item's success/error; retry the identical batch for incomplete items. queued/pending is not provider acceptance. sent is SES acceptance; delivered is destination-server acceptance. unknown needs investigation and must not be blindly retried with a new key."
	}
	if strings.HasPrefix(path, "/webhook") && path != "/webhooks/ses/incoming" {
		op["description"] = fmt.Sprint(op["description"]) + " User-owned subscriptions. Secret returned only on create/rotate. Events: version,id,type,createdAt,data.messageUuid (+data.identityUuid where available). Verify X-Webhook-Signature t=<unix>,v1=<HMAC-SHA256(timestamp+'.'+rawBody)> with 5-minute timestamp tolerance; deduplicate id. 8 delivery attempts, then dead letter; replay keeps event id. Tests return real HTTP outcome. Key revocation does not delete subscriptions; disabled owners pause delivery. Public HTTPS only; redirects are blocked."
	}

	if path == "/inbox/changes" {
		op["description"] = "Read changes after a commit-ordered cursor. Start with cursor=now before an initial inbox snapshot, then replay changes after that cursor. Deduplicate by cursor and messageUuid. Deleted messages remain as tombstones. A cursor older than 90 days returns 410; resynchronize. limit is 1–500."
	}
	if path == "/inbox/received/:uuid" && verb == "GET" {
		op["description"] = "Read an owned message and authenticated attachment URLs without changing unread state. Use POST /inbox/received/mark explicitly."
	}
	if path == "/inbox/received" && verb == "GET" {
		op["description"] = fmt.Sprint(op["description"]) + " The built-in dmarc-reports folder holds authenticated aggregate reports; all includes those messages. Search, pagination and identity/domain filters work normally. Reading does not change unread state."
	}
	if path == "/inbox/received/counts" {
		op["description"] = "Counts for the caller's owned mailbox. inboxUnread is the main Inbox unread badge; dmarcReports and dmarcReportsUnread count the dedicated folder. unread retains its global non-trash meaning and includes unread reports."
		op["parameters"] = []object{{"name": "identityId", "in": "query", "required": false, "schema": object{"type": "integer", "minimum": 0}, "description": "Omit or use zero for the unified mailbox; otherwise count an owned identity."}}
	}
	if path == "/inbox/received/move" {
		op["description"] = "Move owned messages to inbox, archive, spam, trash or dmarc-reports. The report folder is not archived, spam or trash. A manual move back to Inbox is not undone by recurring classification."
	}
	if path == "/settings" {
		op["description"] = fmt.Sprint(op["description"]) + " Human session required; scoped API keys cannot access account settings. autoOrganizeDmarcReports defaults to true, including users without a settings row. Omit the update field to preserve it; false opts out of sorting future arrivals. The preference does not move existing messages, remove the folder or change DNS/receiving setup."
	}
	if strings.HasPrefix(path, "/inbox/filters") && (verb == "POST" || verb == "PUT") && !strings.HasSuffix(path, "/test") {
		s := schema(types["model.InboxFilter"].expr, "model")
		customizeSchema("model.InboxFilter", s)
		p := s["properties"].(object)
		for _, n := range []string{"id", "uuid", "orgId", "userId", "matchCount", "lastMatchedAt", "createdAt", "updatedAt"} {
			delete(p, n)
		}
		op["requestBody"] = object{"required": true, "content": object{"application/json": object{"schema": s, "example": object{"name": "Invoice mail", "conditions": []object{{"field": "subject", "operator": "contains", "value": "invoice"}}, "conditionLogic": "all", "actionFolder": "archive", "active": true}}}}
	}
	if strings.HasPrefix(path, "/inbox/filters") {
		op["description"] = fmt.Sprint(op["description"]) + " Explicit user filter destinations override the automatic DMARC report folder. actionFolder accepts dmarc-reports; label-only and mark-read filters retain the automatically selected folder. Spam and virus decisions are preserved."
	}
	if path == "/compose/attachments" {
		op["requestBody"] = object{"required": true, "content": object{"multipart/form-data": object{"schema": object{"type": "object", "required": []string{"identityId", "file"}, "properties": object{"identityId": object{"type": "integer"}, "file": object{"type": "string", "format": "binary"}}}}}}
	}
	if strings.HasSuffix(path, "/attachments/:attachmentUuid") {
		op["responses"].(object)["200"] = object{"description": "Private attachment bytes", "content": object{"application/octet-stream": object{"schema": object{"type": "string", "format": "binary"}}}}
	}
	if path == "/sse/connect" {
		op["description"] = "Browser clients obtain a 60-second stream ticket from POST /auth/stream-token. Headless clients use Bearer API keys with email:read. Stream tickets cannot call other endpoints."
		op["responses"].(object)["200"] = object{"description": "Event stream", "content": object{"text/event-stream": object{"schema": object{"type": "string"}}}}
	}
	if path == "/rules" || strings.HasPrefix(path, "/rules/") {
		op["deprecated"] = true
		op["description"] = fmt.Sprint(op["description"]) + " Compatibility API synchronized with SES inbox filters. Prefer /inbox/filters. Unsupported legacy actions are rejected; migrated incompatible rules are disabled with migrationWarning."
	}
}

// These enum/semantic constraints are enforced in service routing rather than
// DTO validation tags, so keep them alongside the generated structural schema.
func customizeSchema(name string, s object) {
	p, ok := s["properties"].(object)
	if !ok {
		return
	}
	set := func(field string, values []string, description string) {
		if v, ok := p[field].(object); ok {
			v["enum"] = values
			v["description"] = description
		}
	}
	destinations := []string{"inbox", "archive", "spam", "trash", "dmarc-reports"}
	switch name {
	case "model.InboxListRequest":
		set("folder", []string{"inbox", "dmarc-reports", "sent", "drafts", "outbox", "spam", "trash", "starred", "archive", "all"}, "Built-in folder or virtual view. all includes DMARC reports.")
	case "model.MoveEmailsRequest":
		set("folder", destinations, "A manual move is retained; automatic classification is not repeated.")
	case "model.InboxFilter":
		set("actionFolder", append([]string{""}, destinations...), "An explicit destination overrides automatic DMARC organization. Empty leaves the selected folder unchanged.")
	case "model.InboxCountsResponse":
		for _, field := range []string{"inboxUnread", "dmarcReports", "dmarcReportsUnread", "unread"} {
			if v, ok := p[field].(object); ok {
				v["minimum"] = 0
			}
		}
		p["inboxUnread"].(object)["description"] = "Unread messages in the main Inbox only."
		p["dmarcReports"].(object)["description"] = "Messages in the dedicated DMARC Reports folder."
		p["dmarcReportsUnread"].(object)["description"] = "Unread messages in the dedicated DMARC Reports folder."
		p["unread"].(object)["description"] = "Global unread total excluding Trash; includes Spam and unread DMARC reports."
	case "service.UserSettings":
		if v, ok := p["autoOrganizeDmarcReports"].(object); ok {
			v["default"] = true
			v["description"] = "Automatically file qualifying authenticated aggregate reports for this user. Does not configure DNS or receiving."
		}
	case "service.UpdateSettingsRequest":
		if v, ok := p["autoOrganizeDmarcReports"].(object); ok {
			v["description"] = "Optional. Omit to preserve the current setting; false opts out for future arrivals without moving existing mail."
		}
	}
}
