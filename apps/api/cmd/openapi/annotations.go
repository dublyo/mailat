package main

import (
	"go/ast"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/middleware"
)

func hasValidation(value, rule string) bool {
	for _, part := range strings.Split(strings.Split(value, "#")[0], "|") {
		if part == rule {
			return true
		}
	}
	return false
}

// Preserve DTO defaults and simple validation rules. Conditional/service rules
// are annotated separately; guessing them would misrepresent accepted requests.
func fieldConstraints(s object, tag reflect.StructTag) {
	v := tag.Get("v")
	if hasValidation(v, "email") {
		s["format"] = "email"
	}
	if hasValidation(v, "url") {
		s["format"] = "uri"
	}
	for _, rule := range strings.Split(strings.Split(v, "#")[0], "|") {
		pair := strings.SplitN(rule, ":", 2)
		if len(pair) == 2 {
			if n, err := strconv.Atoi(pair[1]); err == nil {
				switch pair[0] {
				case "min-length":
					s["minLength"] = n
				case "max-length":
					s["maxLength"] = n
				}
			}
		}
	}
	if value, ok := tag.Lookup("d"); ok {
		switch s["type"] {
		case "integer":
			if n, err := strconv.Atoi(value); err == nil {
				s["default"] = n
			}
		case "boolean":
			if b, err := strconv.ParseBool(value); err == nil {
				s["default"] = b
			}
		case "string":
			s["default"] = value
		}
	}
}

func resolveSchema(s object) object {
	if ref, ok := s["$ref"].(string); ok {
		if d, ok := types[strings.TrimPrefix(ref, "#/components/schemas/")]; ok {
			return schema(d.expr, d.pkg)
		}
	}
	if all, ok := s["allOf"].([]interface{}); ok && len(all) == 1 {
		if inner, ok := all[0].(object); ok {
			return resolveSchema(inner)
		}
	}
	return s
}

func selectedField(s object, goName string) object {
	if all, ok := s["allOf"].([]interface{}); ok && len(all) == 1 {
		if inner, ok := all[0].(object); ok {
			return selectedField(inner, goName)
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		d := types[strings.TrimPrefix(ref, "#/components/schemas/")]
		if st, ok := d.expr.(*ast.StructType); ok {
			for _, f := range st.Fields.List {
				for _, name := range f.Names {
					if name.Name == goName {
						return schema(f.Type, d.pkg)
					}
				}
			}
		}
	}
	return object{}
}

// r.Get also reads query values on GET handlers. Do not turn POST body fields
// into query parameters, and let the caller remove already-known path names.
func queryParameters(f *ast.FuncDecl, verb string) []object {
	params := map[string]object{}
	ast.Inspect(f.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		kind := "string"
		if inner, ok := sel.X.(*ast.CallExpr); ok {
			switch sel.Sel.Name {
			case "Int", "Int64", "Uint", "Uint64":
				kind = "integer"
			case "Bool":
				kind = "boolean"
			case "Float32", "Float64":
				kind = "number"
			}
			call = inner
			sel, ok = call.Fun.(*ast.SelectorExpr)
		}
		if !ok || typeName(sel.X) != "r" || (sel.Sel.Name != "GetQuery" && !(sel.Sel.Name == "Get" && verb == "GET")) || len(call.Args) == 0 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, err := strconv.Unquote(literal.Value)
		if err != nil || params[name] != nil {
			return true
		}
		s := object{"type": kind}
		if len(call.Args) > 1 {
			if literal, ok := call.Args[1].(*ast.BasicLit); ok {
				if kind == "integer" {
					if value, err := strconv.Atoi(literal.Value); err == nil {
						s["default"] = value
					}
				} else if value, err := strconv.Unquote(literal.Value); err == nil {
					s["default"] = value
				}
			} else if value, ok := call.Args[1].(*ast.Ident); ok && kind == "boolean" {
				s["default"] = value.Name == "true"
			}
		}
		params[name] = object{"name": name, "in": "query", "schema": s}
		return true
	})
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]object, 0, len(names))
	for _, name := range names {
		out = append(out, params[name])
	}
	return out
}

func successStatus(f *ast.FuncDecl) string {
	status := "200"
	if f != nil {
		ast.Inspect(f.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && typeName(sel.X) == "response" && sel.Sel.Name == "Created" {
					status = "201"
				}
			}
			return true
		})
	}
	return status
}

func annotateBackend(op object, controller, path string) {
	backend, note := "mailat", "Mailat application, account or shared resource API. Individual operations can require configured external services; this label is not a runtime-readiness guarantee."
	if controller == "InboxController" {
		backend, note = "jmap", "Legacy mailbox operation requiring configured Stalwart/JMAP accounts. This is not the SES received-mail API; use /inbox/received and /inbox/changes for SES automation."
	} else if controller == "ReceivedInboxController" || controller == "DomainController" || strings.HasPrefix(path, "/emails") || strings.HasPrefix(path, "/rules") || path == "/webhooks/ses/incoming" {
		backend, note = "ses", "SES email workflow and its stored mailbox/configuration data. Provider actions require configured AWS permissions and verified owned domains; receiving is opt-in."
	} else if strings.HasPrefix(path, "/campaigns") || strings.HasPrefix(path, "/campaign-settings") || strings.HasPrefix(path, "/tracking") {
		backend, note = "ses", "Campaigns send only through SES from an owned, can_send identity on an active, SES-verified domain with bounce/complaint feedback configured; requires EMAIL_PROVIDER=ses."
	} else if strings.HasPrefix(path, "/automations") {
		backend, note = "ses", "Published versions run in every API process via a database-leased executor. Email steps send through the SES marketing pipeline with suppression, unsubscribe and List-Unsubscribe. Editing changes the draft; activate publishes."
	} else if controller == "ComposeController" {
		backend, note = "ses", "Primary SES compose workflow. Reply/forward contexts use public SES mailbox UUIDs; draft and attachment operations use owned records. In legacy non-SES configurations, sending can fall back to JMAP."
	} else if controller == "IdentityController" {
		note = "Owned sender identities support SES. Legacy identities with a Stalwart account can also synchronize password/deletion changes to Stalwart; SES identities do not require a separate mailbox server."
	} else if strings.HasPrefix(path, "/auto-replies") {
		backend, note = "ses", "Executed on SES arrival for the rule owner's identities (empty identityIds means all of them); the oldest active rule wins. Replies go only to the original sender, through the durable send queue, with Auto-Submitted: auto-replied. No reply to lists, bulk or automated mail (RFC 3834), mailer-daemon/no-reply senders, org addresses, Bcc or catch-all deliveries, spam, or mail failing DMARC (or both SPF and DKIM). Each sender gets at most one reply per replyIntervalDays (or ever, with replyOnce); each rule is capped per day (AUTO_REPLY_DAILY_LIMIT)."
	} else if strings.HasPrefix(path, "/forwards") {
		backend, note = "ses", "Executed on SES arrival once the destination confirms the emailed link (POST /forwards/verify, 48 h, single use). Each copy is sent through SES from the identity as \"Original Name via Mailat\" with Reply-To set to the original sender; subject, body and attachments are unchanged. At most 5 forwards per identity, only to outside addresses. Spam, DMARC reports, loops (X-Mailat-Loop), messages over FORWARD_MAX_BYTES and mail beyond FORWARD_DAILY_LIMIT per forward are not forwarded. A complaint, permanent bounce or suppressed destination suspends the forward until it is verified again. keepCopy=false archives (never deletes) the local copy."
	} else if strings.HasPrefix(path, "/org/invites") || strings.HasPrefix(path, "/auth/invites") {
		backend, note = "ses", "Invite links are emailed through SES from an owned sending identity using the durable send queue, suppression list and monthly quota. The single-use token travels in the link fragment and only its SHA-256 hash is stored; accepting creates the account in the inviting organization and signs it in."
	} else if strings.HasPrefix(path, "/org/mailboxes") || strings.HasPrefix(path, "/org/domains/") {
		backend, note = "ses", "Mailbox users: a login for one address on an active, SES-verified domain that sees only its own mail, compose and settings. Mail to the address (and its local+tag forms and send-as aliases) is kept for the user from creation, even while the setup link is pending; removing the mailbox sends new mail to the catch-all. Setup and reset links (72 h, single use) and notices go through SES from your sending identity. Password, reset-link, recovery-email and two-factor changes are an audited exception to admins never controlling another user's access: they apply to mailbox users only and end every session."
	} else if strings.HasPrefix(path, "/org/") {
		note = "Organization administration for owners and admins. Admins manage members and identities but never read another member's mail."
	} else if strings.HasPrefix(path, "/shared-mailboxes") {
		backend, note = "ses", "Executed on SES arrival: mail to the shared address is copied to every active member with canRead (at most 50 members), and members with canSend can compose as it. Admins see every mailbox but read no mail unless they are members; other users see only mailboxes they belong to. Owners and admins create and delete; admins and canManage members manage membership, and the last reader cannot be removed. Removing a member deletes that member's copies. Rows created before shared identities existed are listed with active=false and must be recreated."
	}
	op["x-mailat-backend"], op["x-mailat-backend-note"] = backend, note
}

// These narrow annotations cover service-enforced behavior and non-JSON
// transports that cannot be inferred from DTOs alone. Keep regression tests
// adjacent to them rather than maintaining a separate endpoint catalog.
func customizeContract(op object, verb, path string) {
	if path == "/sse/connect" {
		op["parameters"] = []object{{"name": "token", "in": "query", "required": false, "schema": object{"type": "string"}, "description": "Browser-only short-lived stream ticket from POST /auth/stream-token. Never place a session JWT or API key here. Headless integrations use Authorization: Bearer with email:read instead."}, {"name": "cursor", "in": "query", "required": false, "schema": object{"type": "string"}, "description": "Mailbox change cursor to resume from (from GET /inbox/changes or the last event id), or now (the default) to start at the current position."}}
	}
	if params, ok := op["parameters"].([]object); ok {
		for _, parameter := range params {
			s := parameter["schema"].(object)
			if path == "/webhook-deliveries" {
				switch parameter["name"] {
				case "page":
					s["default"], s["minimum"] = 1, 1
				case "pageSize":
					s["default"], s["minimum"], s["maximum"] = 50, 1, 100
					s["description"] = "Values outside 1–100 use 50."
				case "status":
					s["description"] = "Optional exact delivery status filter; unmatched values return an empty collection."
				}
			}
			if path == "/inbox/changes" && parameter["name"] == "limit" {
				s["type"], s["minimum"], s["maximum"] = "integer", 1, 500
			}
		}
	}
	responses := op["responses"].(object)
	if op["x-api-key-scope"] != nil {
		for status, value := range responses {
			if strings.HasPrefix(status, "2") {
				value.(object)["headers"] = object{"X-RateLimit-Remaining": object{"description": "Remaining requests in this API key's current minute window (present for API-key authentication).", "schema": object{"type": "integer", "minimum": 0}}}
			}
		}
	}
	dataSchema := func(s object) {
		responses["200"].(object)["content"] = object{"application/json": object{"schema": envelope(s)}}
	}

	if strings.HasPrefix(path, "/signup-forms") || strings.HasPrefix(path, "/public/forms") {
		op["tags"] = []string{"Signup forms"}
		op["description"] = "Hosted and embeddable opt-in forms. One static list per form; its single/double confirmation policy is inherited. Management is scoped to the authenticated creator. Public requests never need an API key."
		ref := func(name string) object { return object{"$ref": "#/components/schemas/model." + name} }
		req := ""
		if strings.HasPrefix(path, "/signup-forms") {
			switch {
			case verb == "POST" || verb == "PUT":
				req = "SaveSignupFormRequest"
				dataSchema(ref("SignupForm"))
			case strings.HasSuffix(path, "/signups"):
				dataSchema(ref("SignupEntries"))
			case path == "/signup-forms":
				dataSchema(object{"type": "array", "items": ref("SignupForm")})
			case verb == "GET":
				dataSchema(ref("SignupForm"))
			}
		} else {
			switch {
			case verb == "GET":
				dataSchema(ref("PublicSignupForm"))
			case path == "/public/forms/confirm":
				req = "ConfirmSignupRequest"
				dataSchema(ref("SignupResult"))
			default:
				req = "SubmitSignupRequest"
				dataSchema(ref("SignupResult"))
			}
			op["description"] = "Public opt-in endpoint; never send Authorization. Fetch a fresh form challenge before submitting. Consent is required. Successful duplicate or suppressed signups return the same generic response. Confirmation is POST-only with a single-use 24-hour token from the email URL fragment. GET cannot confirm."
			for _, code := range []string{"409", "410", "429", "503"} {
				responses[code] = object{"description": "Expired/changed form, invalid/used token, rate limit, or temporarily unavailable sender", "content": object{"application/json": object{"schema": envelope(nil)}}}
			}
		}
		if req != "" {
			op["requestBody"] = object{"required": true, "content": object{"application/json": object{"schema": ref(req)}}}
		}
	}
	if path == "/org/domains/:domainUuid/mailboxes/import" {
		op["requestBody"] = object{"required": true, "content": object{"text/csv": object{"schema": object{"type": "string", "maxLength": 1 << 20},
			"example": "local_part,name,invite_email,password,may_send,may_receive\nibrahim,Ibrahim,ibrahim.personal@example.net,,true,true\nsales-desk,Sales desk,,an-initial-password,true,false\n"}}}
	}
	if path == "/org/mailboxes/:userUuid/password" {
		ref := func(name string) object { return object{"$ref": "#/components/schemas/service." + name} }
		dataSchema(object{"oneOf": []object{ref("MailboxPasswordResult"), ref("MailboxLinkResult")}, "description": "mode set answers sessionsRevoked; mode link answers the mailed invite (reset link)."})
	}
	if path == "/org/mailboxes/:userUuid/aliases/:aliasUuid" && verb == "DELETE" {
		delete(responses, "200")
		responses["204"] = object{"description": "Alias removed"}
	}
	if path == "/webhook-triggers/:id" && verb == "PUT" {
		dataSchema(object{"$ref": "#/components/schemas/service.WebhookTrigger"})
	}
	if strings.HasPrefix(path, "/inbox/filters/") && strings.HasSuffix(path, "/test") {
		actions := object{"labels": object{"type": "array", "items": object{"type": "string"}}, "folder": object{"type": "string"}}
		for _, name := range []string{"star", "markRead", "archive", "trash"} {
			actions[name] = object{"type": "boolean"}
		}
		dataSchema(object{"type": "object", "properties": object{"matches": object{"type": "boolean"}, "active": object{"type": "boolean"}, "actions": object{"type": "object", "properties": actions}}})
	}
	switch path {
	case "/branding/css":
		responses["200"] = object{"description": "Brand CSS variables", "content": object{"text/css": object{"schema": object{"type": "string"}}}}
	case "/tracking/open/:token":
		responses["200"] = object{"description": "Transparent tracking pixel", "content": object{"image/gif": object{"schema": object{"type": "string", "format": "binary"}}}}
	case "/tracking/click/:token", "/oauth/:provider", "/oauth/:provider/callback":
		if verb == "GET" {
			delete(responses, "200")
			description := "Redirect to the configured destination"
			if path != "/tracking/click/:token" {
				description = "Browser redirect to the provider, to the SPA (session fragment or Settings link ticket), or to /login?oauthError=<invalid_state|provider_error|not_linked|email_unverified|registration_closed|rate_limited>"
			}
			responses["302"] = object{"description": description, "headers": object{"Location": object{"schema": object{"type": "string", "format": "uri-reference"}}}}
		}
	case "/health":
		dataSchema(object{"type": "object", "properties": object{"status": object{"type": "string", "enum": []string{"healthy", "unhealthy"}}, "version": object{"type": "string"}, "timestamp": object{"type": "string", "format": "date-time"}, "checks": object{"type": "object", "additionalProperties": object{"type": "string", "enum": []string{"ok", "unavailable"}}}}})
		responses["503"] = object{"description": "A database or Redis check failed: code 503, data.status is unhealthy and data.version is still present. Failure details are only logged server-side.", "content": responses["200"].(object)["content"]}
	case "/ready":
		dataSchema(object{"type": "object", "properties": object{"ready": object{"type": "boolean"}, "timestamp": object{"type": "string", "format": "date-time"}}})
		responses["503"] = object{"description": "Postgres or Redis did not answer within 2 seconds; data.ready is false.", "content": responses["200"].(object)["content"]}
	}
}

func semanticConstraints(name string, s object) {
	p, ok := s["properties"].(object)
	if !ok {
		return
	}
	set := func(field string, values object) {
		if existing, ok := p[field].(object); ok {
			for k, v := range values {
				existing[k] = v
			}
		}
	}
	events := append([]string{}, eventoutbox.Types...)
	for _, kind := range eventoutbox.Types {
		events = append(events, strings.ReplaceAll(kind, ".", "_"))
	}
	events = append(events, "bounce_received", "complaint_received")
	switch name {
	case "eventoutbox.Delivery":
		// Detail loads the exact immutable envelope; list responses omit payload.
		p["payload"] = object{"allOf": []object{{"$ref": "#/components/schemas/eventoutbox.Envelope"}}, "description": "Immutable event body returned by delivery detail; omitted from the paginated list. Replay preserves this body and event ID."}
	case "eventoutbox.Envelope":
		set("version", object{"enum": []string{"1"}})
		set("id", object{"format": "uuid"})
		set("type", object{"enum": append(append([]string{}, eventoutbox.Types...), "webhook.test")})
		set("data", object{"description": "Event-specific fields. Mail events include messageUuid; additional fields depend on event type. Verify the signature over raw bytes before parsing."})
	case "model.CreateIdentityRequest":
		s["required"] = []string{"domainId", "email"}
		set("domainId", object{"format": "uuid", "description": "UUID of an active domain owned by the caller's organization; SES verification is required in SES mode."})
		set("email", object{"format": "email", "description": "Complete address on that domain; arbitrary external domains are rejected."})
		set("password", object{"writeOnly": true, "description": "Required with at least 8 characters for legacy non-SES identities; SES addresses do not require one."})
	case "model.InboxListRequest":
		set("page", object{"default": 1, "description": "One-based page. Values below 1 use 1."})
		set("pageSize", object{"default": 50, "description": "Values below 1 use 50; values above 100 are capped to 100."})
		set("sortBy", object{"description": "subject, from_email or size_bytes; other values use received_at."})
		set("sortOrder", object{"description": "asc selects ascending order; other values use descending order."})
	case "model.SendEmailRequest", "model.ComposeEmailRequest":
		// At least one of the three recipient arrays is required, not necessarily To.
		choices := []object{}
		for _, field := range []string{"to", "cc", "bcc"} {
			choices = append(choices, object{"required": []string{field}, "properties": object{field: object{"type": "array", "minItems": 1}}})
		}
		s["anyOf"] = choices
		s["description"] = "At least one recipient across to/cc/bcc; SES accepts at most 50 combined. The rendered subject is limited to 1000 bytes and combined text/HTML body to 2 MiB. Limits are bytes, not Unicode character counts."
		set("attachments", object{"maxItems": 50})
		set("idempotencyKey", object{"minLength": 8, "maxLength": 128, "description": "Alternative to the single-send Idempotency-Key header; both must agree when supplied. At least one form is mandatory."})
		if name == "model.SendEmailRequest" {
			s["description"] = s["description"].(string) + " Supply text, html, templateId or attachments. Single-send requests also require a header or body idempotency key."
		}
	case "model.BatchSendRequest":
		set("emails", object{"minItems": 1, "maxItems": 100})
	case "model.Campaign":
		set("status", object{"enum": []string{"draft", "scheduled", "sending", "paused", "sent", "cancelled"}})
		set("statusReason", object{"nullable": true, "description": "Why the campaign is paused, throttled, reset or finished early: user_paused, sender_unavailable, provider_paused, provider_rejected, monthly_quota_exceeded, bounce_rate_high, complaint_rate_high, invalid_segment, no_postal_address, unsubscribe_url_invalid, list_unavailable, ses_daily_quota, ses_throttled, no_eligible_recipients or legacy_requires_review."})
		set("openCount", object{"description": "Unique recipients who opened; rates use sentCount as the denominator."})
		set("clickCount", object{"description": "Unique recipients who clicked; a click also counts as an open."})
	case "model.CreateCampaignRequest", "model.UpdateCampaignRequest":
		set("trackOpens", object{"default": true})
		set("trackClicks", object{"default": true})
		set("fromEmail", object{"format": "email", "description": "Exact address of a can_send identity the campaign creator owns on an active, SES-verified domain."})
	case "model.ScheduleCampaignRequest":
		set("scheduledAt", object{"format": "date-time", "description": "RFC 3339 time between one minute and 365 days from now; its offset sets the time zone."})
		set("timezone", object{"deprecated": true, "description": "Ignored; use the offset in scheduledAt."})
	case "model.CampaignTestRequest":
		s["required"] = []string{"emails"}
		set("emails", object{"minItems": 1, "maxItems": 5, "items": object{"type": "string", "format": "email"}, "description": "Each address gets its own [Test] message without tracking. Suppressed addresses are rejected."})
	case "model.CampaignSettings":
		set("postalAddress", object{"maxLength": 500, "description": "Physical mailing address added to every campaign footer; required before sending. Newlines are kept."})
	case "model.CampaignRecipient":
		set("status", object{"enum": []string{"pending", "claimed", "sending", "sent", "failed", "unknown", "skipped", "cancelled"}, "description": "unknown means the outcome is uncertain; it is never retried."})
		set("skipReason", object{"nullable": true, "enum": []any{"inactive", "not_member", "suppressed", "contact_deleted", "email_changed", nil}})
		set("deliveryStatus", object{"nullable": true, "enum": []any{"delivered", "bounced", "complained", nil}, "description": "From SES feedback; only moves up delivered < bounced < complained."})
	case "model.CreateApiKeyRequest":
		s["required"] = []string{"name", "permissions"}
		permissions := make([]string, 0, len(middleware.APIKeyPermissions))
		for permission := range middleware.APIKeyPermissions {
			permissions = append(permissions, permission)
		}
		sort.Strings(permissions)
		set("permissions", object{"minItems": 1, "uniqueItems": true, "items": object{"type": "string", "enum": permissions}})
		set("rateLimit", object{"default": 100, "minimum": 0, "maximum": 10000, "description": "Requests per minute. Omit or use 0 for 100; otherwise 1–10000."})
		set("expiresAt", object{"description": "Optional future RFC3339 expiry. Null means no expiry."})
	case "service.CreateWebhookTriggerInput":
		s["required"] = []string{"name", "triggerType", "webhookUrl"}
		set("triggerType", object{"enum": events, "description": "Use a canonical dotted event name. Legacy underscored aliases are accepted; webhook.test is emitted only by the test endpoint."})
		set("webhookUrl", object{"format": "uri", "description": "Public HTTPS only, at most 2048 bytes; no credentials/fragments/private or reserved destinations. Redirects are rejected."})
	case "model.CreateWebhookRequest", "model.UpdateWebhookRequest":
		set("events", object{"items": object{"type": "string", "enum": events}, "description": "Use canonical dotted event names. Legacy aliases remain accepted; webhook.test is emitted only by the test endpoint."})
		set("url", object{"format": "uri", "description": "Public HTTPS only, at most 2048 bytes; redirects and private/reserved destinations are rejected."})
	case "handler.ValidateCredentialsRequest", "handler.ProvisionRequest":
		s["required"] = []string{"region", "accessKeyId", "secretAccessKey"}
		set("secretAccessKey", object{"writeOnly": true})
	case "provider.DMARCInspection":
		set("status", object{"description": "Published DNS inspection state. Check canCreate/verified and reason; a suggested value is not proof it is configured."})
	}
}
