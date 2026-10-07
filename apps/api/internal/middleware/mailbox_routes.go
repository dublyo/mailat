package middleware

import (
	"sort"
	"strings"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
)

// mailboxRoutes is the complete set of routes a mailbox user may call, keyed
// "METHOD /api/v1/<registered pattern>". Everything else is denied, so a new
// route stays closed to mailbox users until it is listed here.
var mailboxRoutes = map[string]bool{}

func init() {
	add := func(method, paths string) {
		for _, path := range strings.Fields(paths) {
			mailboxRoutes[method+" /api/v1"+path] = true
		}
	}
	// Own account, sessions and 2FA.
	add("GET", "/auth/me /auth/sessions /settings /sse/connect /security/2fa/status /security/my-activity /security/sessions /security/webauthn/credentials")
	add("POST", "/auth/logout /auth/stream-token /auth/sessions/revoke-all /auth/change-password /auth/2fa/enable /auth/2fa/verify /auth/2fa/disable")
	add("POST", "/security/2fa/setup /security/2fa/verify /security/2fa/disable /security/2fa/backup-codes /security/sessions/revoke-all")
	add("POST", "/security/webauthn/register/begin /security/webauthn/register/finish /security/webauthn/authenticate/begin /security/webauthn/authenticate/finish")
	add("DELETE", "/auth/sessions/:uuid /security/sessions/:uuid /security/webauthn/credentials/:uuid")
	add("PUT", "/settings")
	// Own mailbox: received mail, labels, filters, trusted senders.
	add("GET", "/inbox/received /inbox/received/counts /inbox/received/:uuid /inbox/received/:uuid/attachments/:attachmentUuid /inbox/changes")
	add("GET", "/inbox/labels /labels /inbox/filters /inbox/filters/:uuid /inbox/trusted-senders")
	add("POST", "/inbox/received/mark /inbox/received/star /inbox/received/move /inbox/received/trash /inbox/received/labels")
	add("POST", "/inbox/labels /labels /inbox/filters /inbox/filters/:uuid/test /inbox/trusted-senders")
	add("PUT", "/inbox/labels/:uuid /labels/:uuid /inbox/filters/:uuid")
	add("DELETE", "/inbox/labels/:uuid /labels/:uuid /inbox/filters/:uuid /inbox/trusted-senders/:uuid")
	// Own identities (the service limits edits), compose, auto-replies and forwards.
	add("GET", "/identities /identities/:uuid /compose/reply/:id /compose/forward/:id /auto-replies /auto-replies/:id /forwards")
	add("PUT", "/identities/:uuid /compose/drafts/:id /auto-replies/:id /forwards/:uuid")
	add("POST", "/compose/send /compose/drafts /compose/attachments /auto-replies /forwards /forwards/:uuid/resend-verification")
	add("DELETE", "/compose/drafts/:id /auto-replies/:id /forwards/:uuid")
	// Shared mailboxes they belong to, and push notifications.
	add("GET", "/shared-mailboxes /shared-mailboxes/:id /push/vapid-key /push/subscriptions")
	add("POST", "/push/subscribe /push/unsubscribe")
	add("PUT", "/push/subscriptions/:uuid/preferences")
}

// MailboxRouteKeys lists the mailbox allowlist, sorted, for tests and docs.
func MailboxRouteKeys() []string {
	out := make([]string, 0, len(mailboxRoutes))
	for k := range mailboxRoutes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsMailbox reports whether the caller is a mailbox user, or an API key owned
// by one (such keys are rejected at authentication).
func IsMailbox(c *model.JWTClaims) bool {
	if c == nil {
		return false
	}
	role := c.Role
	if role == "api" {
		role = c.KeyOwnerRole
	}
	return role == "mailbox"
}

// MailboxGate runs after Auth. It keys on the route GoFrame matched, not the
// raw path, so trailing slashes or encoding cannot change the answer.
func MailboxGate(r *ghttp.Request) {
	c := GetClaims(r)
	if c == nil {
		response.Unauthorized(r, "Authentication required")
		return
	}
	if !mailboxAllowed(c, r.Router) {
		response.Forbidden(r, "Not available for mailbox accounts")
		return
	}
	r.Middleware.Next()
}

// mailboxAllowed is the gate's decision; an unknown matched route denies.
func mailboxAllowed(c *model.JWTClaims, router *ghttp.Router) bool {
	if !IsMailbox(c) {
		return true
	}
	return router != nil && mailboxRoutes[router.Method+" "+router.Uri]
}
