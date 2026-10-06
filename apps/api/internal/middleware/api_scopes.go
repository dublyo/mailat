package middleware

import "strings"

// Explicit method/path metadata fails closed for new endpoints. A newly added
// route never inherits write authority from a prefix or a read scope.
var apiRouteScopes = map[string]string{}

var APIKeyPermissions = map[string]bool{
	"email:send": true, "email:read": true, "email:manage": true,
	"domains:read": true, "domains:manage": true, "identities:read": true, "identities:manage": true,
	"templates:read": true, "templates:manage": true, "webhooks:manage": true, "contacts:manage": true,
	"campaigns:read": true, "campaigns:manage": true, "automations:read": true, "automations:enroll": true,
}

func init() {
	add := func(scope, method, paths string) {
		for _, path := range strings.Fields(paths) {
			apiRouteScopes[method+" /api/v1"+path] = scope
		}
	}
	add("email:read", "GET", "/inbox /inbox/mailboxes /inbox/emails/:id /inbox/threads/:id /inbox/search /inbox/received /inbox/received/counts /inbox/received/:uuid /inbox/received/:uuid/attachments/:attachmentUuid /inbox/changes /emails/:id /compose/reply/:id /compose/forward/:id /sse/connect /inbox/labels /labels /inbox/filters /inbox/filters/:uuid /inbox/trusted-senders /rules /rules/:id")
	add("email:send", "POST", "/emails /emails/batch /compose/send /compose/drafts /compose/attachments")
	add("email:send", "PUT", "/compose/drafts/:id")
	add("email:send", "DELETE", "/compose/drafts/:id")
	add("email:manage", "POST", "/inbox/received/mark /inbox/received/star /inbox/received/move /inbox/received/trash /inbox/received/labels /inbox/mark-read /inbox/toggle-flag /inbox/move /inbox/delete /inbox/labels /labels /inbox/filters /inbox/filters/:uuid/test /inbox/trusted-senders /rules /rules/reorder /rules/:id/test")
	add("email:manage", "PUT", "/inbox/labels/:uuid /labels/:uuid /inbox/filters/:uuid /rules/:id")
	add("email:manage", "DELETE", "/emails/:id /inbox/labels/:uuid /labels/:uuid /inbox/filters/:uuid /inbox/trusted-senders/:uuid /rules/:id")
	add("domains:read", "GET", "/domains /domains/:uuid /domains/:uuid/dmarc /domains/:uuid/ses-status /domains/:uuid/sending-status")
	add("domains:manage", "POST", "/domains /domains/:uuid/verify /domains/:uuid/ses-verify /domains/:uuid/dns/cloudflare /domains/cloudflare/zones /domains/:uuid/setup-sending /inbox/setup")
	add("domains:manage", "DELETE", "/domains/:uuid")
	add("identities:read", "GET", "/identities /identities/:uuid")
	add("identities:manage", "POST", "/identities /identities/:uuid/catch-all")
	add("identities:manage", "PUT", "/identities/:uuid")
	add("identities:manage", "DELETE", "/identities/:uuid")
	add("templates:read", "GET", "/templates /templates/:uuid")
	add("templates:read", "POST", "/templates/:uuid/preview")
	add("templates:manage", "POST", "/templates")
	add("templates:manage", "PUT", "/templates/:uuid")
	add("templates:manage", "DELETE", "/templates/:uuid")
	add("webhooks:manage", "GET", "/webhooks /webhooks/:uuid /webhooks/:uuid/calls /webhooks/:uuid/attempts /webhook-deliveries /webhook-deliveries/:uuid /webhook-triggers/types /webhook-triggers /webhook-triggers/:id")
	add("webhooks:manage", "POST", "/webhooks /webhooks/:uuid/test /webhooks/:uuid/rotate-secret /webhooks/:uuid/replay /webhooks/:uuid/calls/:id/replay /webhook-deliveries/:uuid/replay /webhook-triggers /webhook-triggers/:id/test /webhook-triggers/:id/rotate-secret /webhook-triggers/:id/replay")
	add("webhooks:manage", "PUT", "/webhooks/:uuid /webhook-triggers/:id")
	add("webhooks:manage", "DELETE", "/webhooks/:uuid /webhook-triggers/:id")
	add("contacts:manage", "GET", "/signup-forms /signup-forms/:uuid /signup-forms/:uuid/signups /contacts /contacts/:uuid /contacts/:uuid/export /contacts/:uuid/consent-audit /lists /lists/:uuid /lists/:uuid/contacts")
	add("contacts:manage", "POST", "/signup-forms /contacts /contacts/import /contacts/export /contacts/unsubscribe /lists /lists/:uuid/contacts /lists/:uuid/contacts/import /lists/:uuid/contacts/manual")
	add("contacts:manage", "PUT", "/signup-forms/:uuid /contacts/:uuid /lists/:uuid")
	add("campaigns:read", "GET", "/campaigns /campaigns/:uuid /campaigns/:uuid/stats /campaigns/:uuid/progress /campaigns/:uuid/audience /campaigns/:uuid/recipients /campaign-settings")
	add("campaigns:read", "POST", "/campaigns/:uuid/preview")
	add("campaigns:manage", "POST", "/campaigns /campaigns/:uuid/schedule /campaigns/:uuid/send /campaigns/:uuid/pause /campaigns/:uuid/resume /campaigns/:uuid/cancel /campaigns/:uuid/test")
	add("campaigns:manage", "PUT", "/campaigns/:uuid /campaign-settings")
	add("campaigns:manage", "DELETE", "/campaigns/:uuid")
	// Automation management (create, update, delete, validate, activate, pause,
	// archive) stays session-only. automations:read exposes contact emails.
	add("automations:read", "GET", "/automations /automations/:uuid /automations/:uuid/stats /automations/:uuid/enrollments /automations/:uuid/enrollments/:enrollmentUuid")
	add("automations:enroll", "POST", "/automations/:uuid/enroll /automations/:uuid/enrollments/:enrollmentUuid/cancel /automations/:uuid/enrollments/:enrollmentUuid/retry")
	add("contacts:manage", "DELETE", "/signup-forms/:uuid /contacts/:uuid /contacts/:uuid/gdpr /lists/:uuid /lists/:uuid/contacts")
}

func APIKeyScope(method, path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	if scope, ok := apiRouteScopes[method+" "+path]; ok {
		return scope, true
	}
	parts := strings.Split(path, "/")
	for route, scope := range apiRouteScopes {
		fields := strings.SplitN(route, " ", 2)
		if fields[0] != method {
			continue
		}
		pattern := strings.Split(fields[1], "/")
		if len(parts) != len(pattern) {
			continue
		}
		matched := true
		for i := range parts {
			if parts[i] == "" && pattern[i] != "" {
				matched = false
				break
			}
			if strings.HasPrefix(pattern[i], ":") {
				continue
			}
			if parts[i] != pattern[i] {
				matched = false
				break
			}
		}
		if matched {
			return scope, true
		}
	}
	return "", false
}
