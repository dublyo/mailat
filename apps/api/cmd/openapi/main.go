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
			if hasValidation(v, "required") {
				required = append(required, name)
			}
			fieldConstraints(p, reflect.StructTag(tag))
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
	for _, pkg := range []string{"model", "service", "controller", "eventoutbox", "handler", "provider"} {
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
					} else {
						methods[key(pkg, x.Name.Name)] = x
						methodPackages[key(pkg, x.Name.Name)] = pkg
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
	if name := typeName(sel.X) + "." + sel.Sel.Name; methods[name] != nil {
		return methods[name], methodPackages[name]
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
			if x.Name == "nil" {
				return nil
			}
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
			if x.Kind == token.FLOAT {
				return object{"type": "number"}
			}
			return object{"type": "integer"}
		case *ast.UnaryExpr:
			return infer(x.X)
		case *ast.SelectorExpr:
			return selectedField(infer(x.X), x.Sel.Name)
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
			if id, ok := x.Fun.(*ast.Ident); ok {
				if id.Name == "make" && len(x.Args) > 0 {
					return schema(x.Args[0], "controller")
				}
				if id.Name == "append" && len(x.Args) > 1 {
					return object{"type": "array", "items": infer(x.Args[1])}
				}
			}
			m, pkg := methodResult(controller, x)
			if m != nil && m.Type.Results != nil && len(m.Type.Results.List) > 0 {
				return schema(m.Type.Results.List[0].Type, pkg)
			}
		}
		return object{}
	}
	out := object{}
	ast.Inspect(f.Body, func(n ast.Node) bool {
		if v, ok := n.(*ast.ValueSpec); ok && v.Type != nil {
			for _, name := range v.Names {
				locals[name.Name] = schema(v.Type, "controller")
			}
		}
		if loop, ok := n.(*ast.RangeStmt); ok {
			if value, ok := loop.Value.(*ast.Ident); ok {
				if items, ok := resolveSchema(infer(loop.X))["items"].(object); ok {
					locals[value.Name] = items
				}
			}
		}
		if a, ok := n.(*ast.AssignStmt); ok && len(a.Rhs) == 1 {
			if len(a.Lhs) == 1 {
				if id, ok := a.Lhs[0].(*ast.Ident); ok {
					if s := infer(a.Rhs[0]); len(s) > 0 {
						locals[id.Name] = s
					}
				}
			}
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
	props := object{"code": object{"type": "integer", "example": 0}, "message": object{"type": "string"}}
	if data != nil {
		props["data"] = data
	}
	return object{"type": "object", "required": []string{"code", "message"}, "properties": props}
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
	public := map[string]bool{"/health": true, "/ready": true, "/auth/register-status": true, "/auth/register": true, "/auth/login": true, "/auth/2fa/challenge": true, "/webhooks/ses/incoming": true, "/oauth/providers": true, "/oauth/:provider": true, "/oauth/:provider/callback": true, "/forwards/verify": true, "/auth/invites/lookup": true, "/auth/invites/accept": true}
	lineRx := regexp.MustCompile(`(\w+)\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)",\s*(\w+)\.(\w+)\)`)
	paramRx := regexp.MustCompile(`:(\w+)`)
	mailboxAllowed := map[string]bool{}
	for _, k := range middleware.MailboxRouteKeys() {
		mailboxAllowed[k] = true
	}
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
		if public[path] || strings.HasPrefix(path, "/public/forms") || strings.HasPrefix(path, "/tracking/") || strings.HasPrefix(path, "/unsubscribe/") || strings.HasPrefix(path, "/preferences/") {
			op["security"] = []object{}
		}
		if scope, ok := middleware.APIKeyScope(verb, full); ok {
			op["x-api-key-scope"] = scope
		} else {
			op["x-human-session-required"] = len(op["security"].([]object)) > 0
		}
		annotateBackend(op, controller, path)
		// Mailbox users reach only their own mail, compose and account routes.
		if len(op["security"].([]object)) > 0 {
			op["x-mailat-mailbox-allowed"] = mailboxAllowed[verb+" "+full]
		}
		// Routes registered inside the router's admin groups need an organization role.
		if group == "adminGroup" || group == "humanAdminGroup" {
			op["x-mailat-role-required"] = []string{"owner", "admin"}
			if verb == "PUT" && path == "/org/members/:uuid" {
				op["x-mailat-role-required"] = []string{"owner"}
			}
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
		if verb == "POST" || verb == "PUT" || verb == "PATCH" || verb == "DELETE" {
			if req != nil {
				op["requestBody"] = object{"required": true, "content": object{"application/json": object{"schema": req}}}
			}
		}
		if f != nil {
			seen := map[string]bool{}
			for _, p := range params {
				seen[fmt.Sprint(p["name"])] = true
			}
			for _, parameter := range queryParameters(f, verb) {
				name := parameter["name"].(string)
				if !seen[name] {
					params = append(params, parameter)
					seen[name] = true
				}
			}
		}
		if full == "/api/v1/emails" && verb == "POST" || full == "/api/v1/emails/batch" || full == "/api/v1/compose/send" {
			params = append(params, object{"name": "Idempotency-Key", "in": "header", "required": full != "/api/v1/emails", "schema": object{"type": "string", "minLength": 8, "maxLength": 128}, "description": "Persist one key per logical submission. Reuse it with the identical request on retries; changed content returns 409. Single-email sends accept either this header or JSON idempotencyKey; both must agree when supplied together. Batch and compose require this header."})
		}
		if full == "/api/v1/campaigns/:uuid/test" {
			params = append(params, object{"name": "Idempotency-Key", "in": "header", "required": true, "schema": object{"type": "string", "minLength": 8, "maxLength": 128}, "description": "One key per logical test send. A retry with the same key and addresses returns the stored outcome without sending again; different addresses return 409."})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		responses := object{successStatus(f): object{"description": "Success", "content": object{"application/json": object{"schema": envelope(resultSchema(controller, f))}}}}
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
		customizeContract(op, verb, path)
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
	spec := object{"openapi": "3.0.3", "info": object{"title": "Mailat API", "version": contractVersion, "description": "Self-hosted SES email automation. API keys use Authorization: Bearer ue_… and explicit per-operation scopes. Human-only administration is marked. Roles are owner, admin, member and mailbox; x-mailat-role-required marks owner/admin routes, and x-mailat-mailbox-allowed marks the routes a mailbox user (a login that only uses its own mailbox) may call. API keys owned by a mailbox user are rejected. Inbox detail is non-mutating. Webhooks use version 1 events and HMAC-SHA256 over timestamp + '.' + raw body. Cursor retention is 90 days."}, "servers": []object{{"url": "/"}}, "paths": paths, "components": object{"securitySchemes": object{"bearerAuth": object{"type": "http", "scheme": "bearer", "description": "Scoped Mailat API key or active human session JWT."}}, "schemas": schemas}}
	data, err := json.MarshalIndent(spec, "", "  ")
	must(err)
	data = append(data, '\n')
	path := "internal/apidocs/openapi.json"
	digest, err := contractDigest(data)
	must(err)
	recorded, err := readVersionRecord(versionPath)
	must(err)
	if err := versionGate(recorded, contractVersion, digest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	version := versionFile(digest)
	if len(os.Args) > 1 && os.Args[1] == "--check" {
		existing, e := os.ReadFile(path)
		must(e)
		existingVersion, _ := os.ReadFile(versionPath)
		if string(existing) != string(data) || string(existingVersion) != string(version) {
			fmt.Fprintln(os.Stderr, "OpenAPI is stale: run go run ./cmd/openapi")
			os.Exit(1)
		}
		fmt.Println("OpenAPI matches routes and DTOs")
		return
	}
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, data, 0644))
	must(os.WriteFile(versionPath, version, 0644))
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
		"POST /inbox/trusted-senders":    object{"sender": "@example.net"},
		"POST /oauth/link/confirm":       object{"ticket": "4f9c2a7e0b1d3c5e6f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6"},
		"POST /security/2fa/disable":     object{"password": "current-password", "code": "123456"},
		"POST /auth/2fa/disable":         object{"password": "current-password", "code": "123456"},
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
	// Database-backed per-IP/account/user limits (fixed windows of 15 minutes
	// or 1 hour, not the per-minute API key window).
	authLimited := map[string]bool{"POST /auth/login": true, "POST /auth/register": true, "POST /auth/2fa/challenge": true, "POST /auth/2fa/enable": true, "POST /auth/2fa/verify": true, "POST /auth/2fa/disable": true, "POST /security/2fa/setup": true, "POST /security/2fa/verify": true, "POST /security/2fa/disable": true, "POST /security/2fa/backup-codes": true, "POST /auth/change-password": true, "POST /oauth/link/confirm": true, "POST /unsubscribe/:token": true, "DELETE /unsubscribe/:token": true, "PUT /preferences/:token": true, "POST /forwards/verify": true, "POST /auth/invites/lookup": true, "POST /auth/invites/accept": true, "POST /forwards": true, "POST /forwards/:uuid/resend-verification": true}
	if responses, ok := op["responses"].(object); ok && authLimited[verb+" "+path] {
		if limited, ok := responses["429"].(object); ok {
			limited["description"] = "Too many attempts from this client, account or user; respect Retry-After"
			limited["headers"] = object{"Retry-After": object{"description": "Seconds until the current rate-limit window ends.", "schema": object{"type": "integer", "minimum": 1}}}
			limited["content"].(object)["application/json"].(object)["example"] = object{"code": 429, "message": "Too many attempts. Try again in 12 minutes."}
		}
	}
	if path == "/compose/reply/:id" || path == "/compose/forward/:id" {
		op["description"] = "Get a context using the public SES mailbox message UUID as id. Map returned from.email to compose/send's fromEmail, and retain identityId, recipients, subject, bodies, inReplyTo, references and authorized attachments; do not post the context unchanged. Forward context includes authenticated attachment blob references. replyAll=true is supported on reply."
	}
	if path == "/domains/:uuid/setup-sending" || path == "/domains/:uuid/sending-status" {
		op["description"] = "Separate attachment storage and outbound SES feedback readiness. Setup does not enable receiving or change MX. Inspect storageReady, feedbackConfigured, subscriptionStatus, feedbackReady and reason even for HTTP 200: an existing foreign SES topic is preserved and reported as a conflict. SNS confirmation can remain pending."
	}
	if path == "/domains/:uuid/readiness" {
		op["description"] = "Read-only API sending checklist for the caller (or the API key's owner) on an organization domain. items, in order: verified (active and SES verified), dmarc (state published, inherited, missing, conflict or unknown; value is the policy or the suggested record), sending_resources (attachment storage and SES feedback; state awaiting_confirmation while the SNS subscription awaits confirmation, which needs no action), sending_identity (the caller has a personal can_send identity on the domain; value is that address, or the suggested noreply address) and receiving (optional; enabled plus the live root MX status, never blocks ready; value is the root MX only while receiving is enabled). status is ok, missing, pending, attention, unknown or off. fix names the action that resolves an item: verify, dmarc, setup_sending, create_identity or receiving; only owners and admins can act on verify, setup_sending and create_identity. ready is true when every required item is ok; a failed DMARC lookup reports unknown and leaves ready false until a re-check succeeds. suggestedIdentity is noreply@<domain> while that address is not an identity, send-as alias, open invite or another person's login. When a domain first becomes active and SES verified (POST /domains/:uuid/verify, or GET /domains/:uuid/ses-status by an owner or admin session), Mailat creates noreply@<domain> for the person who added it (if free and within the identity cap) and sets up sending resources once, in the background; it never publishes DNS. While that runs, automaticSetup is true and the affected items have state automatic_setup and no fix: read again shortly. DMARC and MX lookups are cached briefly; refresh=true re-checks now."
	}
	if path == "/domains/:uuid/ses-status" {
		op["description"] = "Checks the domain's SES verification status and stores it. When an owner or admin session finds an active domain verified for the first time, this also starts the one-time setup described under GET /domains/:uuid/readiness (noreply identity for the person who added it, then sending resources). Members and API keys only read and store the status."
	}
	if path == "/domains/:uuid/receiving" {
		op["description"] = "Read-only receiving status for an organization domain. enabled is the owner's opt-in; Mailat never publishes or changes a root MX on its own. mxRecord is the exact root MX to publish (name @). mxStatus: not_enabled (receiving off), missing (no root MX), published (the SES inbound host is the preferred MX), conflict (the root MX points elsewhere) or unknown (the public lookup failed). Lookups are cached briefly; refresh=true re-checks now. Sending works without receiving; mailboxes need it."
	}
	if path == "/domains/:uuid/dns/cloudflare" {
		op["description"] = fmt.Sprint(op["description"]) + " scope=receiving-mx adds only the root receiving MX. The root MX is written only while receiving is enabled and the zone has no other root MX; otherwise it is reported as skipped or conflict and nothing changes."
	}
	if path == "/emails" && verb == "POST" || path == "/emails/batch" {
		op["description"] = fmt.Sprint(op["description"]) + " Requires a verified owned sender and stable Idempotency-Key. Base64 attachments preserve name, type (MIME), disposition and cid. Same key+content retries return the original result; changed payload is 409. A batch holds one content-bound key for the whole ordered request plus per-item keys. Inspect each item's success/error; retry the identical batch for incomplete items. queued/pending is not provider acceptance. sent is SES acceptance; delivered is destination-server acceptance. unknown needs investigation and must not be blindly retried with a new key. Sender rule: owners, admins and their keys may send as any address on a verified domain of theirs; members and member-owned keys only as one of their identity addresses, a local+tag form of it, a send-as alias granted to it, or any address on its domain when that identity's wildcard sender switch is on. Nobody may send as another user's identity address or send-as alias (400). The Sent copy is stored under the matching identity."
	}
	if path == "/identities/:uuid" && verb == "PUT" {
		op["description"] = "Update one of the caller's personal identities. signatureHtml and signatureText (at most 20 KB each) are stored as given; clients sanitise the HTML before inserting it into compose. isCatchAll and canReceive need an owner or admin. Identity reads return the signature, wildcardSender and sendAliases for the caller's own personal identities."
	}
	if strings.HasPrefix(path, "/webhook") && path != "/webhooks/ses/incoming" {
		op["description"] = fmt.Sprint(op["description"]) + " User-owned subscriptions. Secret returned only on create/rotate. Events: version,id,type,createdAt,data.messageUuid (+data.identityUuid where available). Verify X-Webhook-Signature t=<unix>,v1=<HMAC-SHA256(timestamp+'.'+rawBody)> with 5-minute timestamp tolerance; deduplicate id. 8 delivery attempts, then dead letter; replay keeps event id. Tests return real HTTP outcome. Key revocation does not delete subscriptions; disabled owners pause delivery. Public HTTPS only; redirects are blocked."
	}

	if path == "/inbox/changes" {
		op["description"] = "Read changes after a commit-ordered cursor. Start with cursor=now before an initial inbox snapshot, then replay changes after that cursor. Deduplicate by cursor and messageUuid. Deleted messages remain as tombstones. A cursor older than 90 days returns 410; resynchronize. limit is 1–500."
	}
	if path == "/inbox/received/:uuid" && verb == "GET" {
		op["description"] = "Read an owned message and authenticated attachment URLs without changing unread state. Use POST /inbox/received/mark explicitly. remoteImages is allowed for sent mail, for users whose remoteImages setting is always, and for trusted senders whose DMARC passed outside Spam; otherwise it is blocked and clients should not load remote content until the user asks."
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
		media := object{"schema": s, "example": object{"name": "Invoice mail", "conditions": []object{{"field": "subject", "operator": "contains", "value": "invoice"}}, "conditionLogic": "all", "actionFolder": "archive", "active": true}}
		if verb == "POST" {
			delete(media, "example")
			media["examples"] = object{
				"filter":        object{"summary": "Filter", "value": object{"kind": "filter", "name": "Invoice mail", "conditions": []object{{"field": "subject", "operator": "contains", "value": "invoice"}}, "conditionLogic": "all", "actionFolder": "archive", "active": true}},
				"blockedSender": object{"summary": "Blocked sender", "value": object{"kind": "blocked_sender", "name": "Blocked: @spam.example", "priority": 0, "active": true, "conditions": []object{{"field": "from", "operator": "endsWith", "value": "@spam.example"}}, "conditionLogic": "all", "actionFolder": "spam", "actionMarkRead": true}},
			}
		}
		op["requestBody"] = object{"required": true, "content": object{"application/json": media}}
	}
	if path == "/inbox/filters" && verb == "GET" {
		op["parameters"] = []object{{"name": "kind", "in": "query", "required": false, "schema": object{"type": "string", "enum": []string{"filter", "blocked_sender"}}, "description": "Omit for every rule in evaluation order (blocked senders last)."}}
	}
	if path == "/inbox/filters" && verb == "POST" {
		op["description"] = fmt.Sprint(op["description"]) + " kind=blocked_sender takes exactly one from condition (equals an address, or endsWith @domain; subdomains are not covered) and actionFolder spam or trash, with only actionMarkRead as an extra action; the value is lowercased and priority forced to 0. Blocked-sender rules always run after every other filter, so no filter can route a blocked sender back to the inbox. Blocking an already blocked sender returns the existing rule with 200."
	}
	if strings.HasPrefix(path, "/inbox/filters/") && verb == "PUT" {
		op["description"] = fmt.Sprint(op["description"]) + " kind cannot be changed (400)."
	}
	if strings.HasPrefix(path, "/inbox/trusted-senders") {
		op["description"] = "Per-user trusted senders (an address or @domain, max 500). Remote images in their mail load automatically only when DMARC passed and the message is not in Spam. POST is idempotent: 201 when created, 200 with the existing entry otherwise; 409 limit_reached at the cap."
		if verb == "POST" {
			op["requestBody"] = object{"required": true, "content": object{"application/json": object{"schema": object{"type": "object", "required": []string{"sender"}, "properties": object{"sender": object{"type": "string", "minLength": 3, "maxLength": 320, "description": "name@example.com or @example.com"}}}, "example": object{"sender": "@example.net"}}}}
			if responses, ok := op["responses"].(object); ok {
				if created, ok := responses["201"].(object); ok {
					responses["200"] = object{"description": "Sender was already trusted; the existing entry is returned", "content": created["content"]}
				}
				responses["409"] = object{"description": "limit_reached: remove a trusted sender before adding another", "content": object{"application/json": object{"schema": envelope(nil)}}}
			}
		}
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
		op["description"] = "Browser clients obtain a 60-second, single-use stream ticket from POST /auth/stream-token for every (re)connect; a reused ticket returns 401. The open stream lives as long as the session (or API key) and closes when it is revoked. Headless clients use Bearer API keys with email:read. Stream tickets cannot call other endpoints. Events (JSON {type,data}): connected{clientId,cursor}; new_email{cursor,uuid,identityId,summary}, email_update{cursor,uuid,summary} and email_deleted{cursor,uuids}, each with SSE id = cursor; counts_update{counts}; resync{cursor,reason} (expired or ahead cursor, or more than 500 pending changes: reload and adopt the cursor); heartbeat. 503 when the server's connection limit is reached; each user keeps at most 10 streams (the oldest is closed)."
		op["responses"].(object)["200"] = object{"description": "Event stream", "content": object{"text/event-stream": object{"schema": object{"type": "string"}}}}
	}
	if path == "/push/vapid-key" {
		op["description"] = "enabled=false (and an empty publicKey) means the server has no VAPID keys and cannot send push notifications."
	}
	if path == "/push/subscribe" {
		op["description"] = "Registers this browser for new-mail notifications. The endpoint must be an https URL (at most 1 KiB) on a configured push service host; p256dhKey (65 bytes) and authKey (16 bytes) are base64url. 400 for invalid keys, a disallowed host or when push is not configured; 409 when the endpoint belongs to another account. Re-subscribing an owned endpoint reactivates it."
		op["responses"].(object)["409"] = object{"description": "The push endpoint belongs to another account", "content": object{"application/json": object{"schema": envelope(nil)}}}
	}
	if path == "/rules" || strings.HasPrefix(path, "/rules/") {
		op["deprecated"] = true
		op["description"] = fmt.Sprint(op["description"]) + " Compatibility API synchronized with SES inbox filters. Prefer /inbox/filters. Unsupported legacy actions are rejected; migrated incompatible rules are disabled with migrationWarning."
	}
}

// These enum/semantic constraints are enforced in service routing rather than
// DTO validation tags, so keep them alongside the generated structural schema.
func customizeSchema(name string, s object) {
	semanticConstraints(name, s)
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
		set("kind", []string{"filter", "blocked_sender"}, "filter (default) or blocked_sender. Blocked senders run after every other filter. Immutable after creation.")
	case "model.ReceivedEmail":
		set("remoteImages", []string{"blocked", "allowed"}, "Viewer-specific remote-content decision; present on single-message reads.")
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
	case "service.UserSettings", "service.UpdateSettingsRequest":
		set("remoteImages", []string{"ask", "always"}, "ask (default) blocks remote images until the user shows them; always loads them for every message.")
	}
	switch name {
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
