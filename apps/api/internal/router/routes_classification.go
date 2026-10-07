package router

import "strings"

// routeClasses classifies every /api/v1 route, keyed "METHOD /api/v1/<pattern>".
// TestEveryRouteIsClassified fails on a route without a class, so each new
// route needs a deliberate decision:
//   - public: no authentication
//   - mailbox: any signed-in user, mailbox users included (= middleware mailbox allowlist)
//   - staff: owners, admins and members; mailbox users get 403
//   - admin: owner or admin (RequireOrgAdmin); admin-owned API keys where scoped
//   - humanAdmin: owner or admin session only
var routeClasses = map[string]string{}

func init() {
	add := func(class, method, paths string) {
		for _, path := range strings.Fields(paths) {
			routeClasses[method+" /api/v1"+path] = class
		}
	}
	add("public", "GET", "/auth/register-status /health /oauth/:provider /oauth/:provider/callback /oauth/providers /openapi.json /preferences/:token /public/forms/:uuid /ready /tracking/click/:token /tracking/open/:token /unsubscribe/:token")
	add("public", "POST", "/auth/2fa/challenge /auth/invites/accept /auth/invites/lookup /auth/login /auth/register /forwards/verify /public/forms/:uuid/submit /public/forms/confirm /unsubscribe/:token /webhooks/ses/incoming")
	add("public", "PUT", "/preferences/:token")
	add("public", "DELETE", "/unsubscribe/:token")
	add("mailbox", "GET", "/auth/me /auth/sessions /auto-replies /auto-replies/:id /compose/forward/:id /compose/reply/:id /forwards /identities /identities/:uuid /inbox/changes /inbox/filters /inbox/filters/:uuid /inbox/labels /inbox/received /inbox/received/:uuid /inbox/received/:uuid/attachments/:attachmentUuid /inbox/received/counts /inbox/trusted-senders /labels /push/subscriptions /push/vapid-key /security/2fa/status /security/my-activity /security/sessions /security/webauthn/credentials /settings /shared-mailboxes /shared-mailboxes/:id /sse/connect")
	add("mailbox", "POST", "/auth/2fa/disable /auth/2fa/enable /auth/2fa/verify /auth/change-password /auth/logout /auth/sessions/revoke-all /auth/stream-token /auto-replies /compose/attachments /compose/drafts /compose/send /forwards /forwards/:uuid/resend-verification /inbox/filters /inbox/filters/:uuid/test /inbox/labels /inbox/received/labels /inbox/received/mark /inbox/received/move /inbox/received/star /inbox/received/trash /inbox/trusted-senders /labels /push/subscribe /push/unsubscribe /security/2fa/backup-codes /security/2fa/disable /security/2fa/setup /security/2fa/verify /security/sessions/revoke-all /security/webauthn/authenticate/begin /security/webauthn/authenticate/finish /security/webauthn/register/begin /security/webauthn/register/finish")
	add("mailbox", "PUT", "/auto-replies/:id /compose/drafts/:id /forwards/:uuid /identities/:uuid /inbox/filters/:uuid /inbox/labels/:uuid /labels/:uuid /push/subscriptions/:uuid/preferences /settings")
	add("mailbox", "DELETE", "/auth/sessions/:uuid /auto-replies/:id /compose/drafts/:id /forwards/:uuid /inbox/filters/:uuid /inbox/labels/:uuid /inbox/trusted-senders/:uuid /labels/:uuid /security/sessions/:uuid /security/webauthn/credentials/:uuid")
	add("staff", "GET", "/automations /automations/:uuid /automations/:uuid/enrollments /automations/:uuid/enrollments/:enrollmentUuid /automations/:uuid/stats /branding /branding/css /campaign-settings /campaigns /campaigns/:uuid /campaigns/:uuid/audience /campaigns/:uuid/progress /campaigns/:uuid/recipients /campaigns/:uuid/stats /contacts /contacts/:uuid /contacts/:uuid/consent-audit /contacts/:uuid/export /domains /domains/:uuid /domains/:uuid/dmarc /domains/:uuid/sending-status /domains/:uuid/ses-status /emails/:id /inbox /inbox/emails/:id /inbox/mailboxes /inbox/search /inbox/threads/:id /lists /lists/:uuid /lists/:uuid/contacts /oauth/connections /rules /rules/:id /security/audit-logs /security/events /shared-mailboxes/:id/members /signup-forms /signup-forms/:uuid /signup-forms/:uuid/signups /templates /templates/:uuid /webhook-deliveries /webhook-deliveries/:uuid /webhook-triggers /webhook-triggers/:id /webhook-triggers/types /webhooks /webhooks/:uuid /webhooks/:uuid/calls")
	add("staff", "POST", "/automations /automations/:uuid/activate /automations/:uuid/archive /automations/:uuid/enroll /automations/:uuid/enrollments/:enrollmentUuid/cancel /automations/:uuid/enrollments/:enrollmentUuid/retry /automations/:uuid/pause /automations/:uuid/validate /campaigns /campaigns/:uuid/cancel /campaigns/:uuid/pause /campaigns/:uuid/preview /campaigns/:uuid/resume /campaigns/:uuid/schedule /campaigns/:uuid/send /campaigns/:uuid/test /contacts /contacts/export /contacts/import /contacts/unsubscribe /emails /emails/batch /inbox/delete /inbox/mark-read /inbox/move /inbox/toggle-flag /lists /lists/:uuid/contacts /lists/:uuid/contacts/import /lists/:uuid/contacts/manual /oauth/:provider/connect /oauth/link/confirm /rules /rules/:id/test /rules/reorder /shared-mailboxes/:id/members /signup-forms /templates /templates/:uuid/preview /webhook-deliveries/:uuid/replay /webhook-triggers /webhook-triggers/:id/rotate-secret /webhook-triggers/:id/test /webhooks /webhooks/:uuid/rotate-secret /webhooks/:uuid/test")
	add("staff", "PUT", "/automations/:uuid /campaign-settings /campaigns/:uuid /contacts/:uuid /identities/:uuid/password /lists/:uuid /rules/:id /shared-mailboxes/:id/members/:userId /signup-forms/:uuid /templates/:uuid /webhook-triggers/:id /webhooks/:uuid")
	add("staff", "DELETE", "/automations/:uuid /campaigns/:uuid /contacts/:uuid /contacts/:uuid/gdpr /emails/:id /lists/:uuid /lists/:uuid/contacts /oauth/:provider /rules/:id /shared-mailboxes/:id/members/:userId /signup-forms/:uuid /templates/:uuid /webhook-triggers/:id /webhooks/:uuid")
	add("admin", "GET", "/health/alerts /health/logs /health/quota /health/reputation /health/ses-limits /health/summary /health/warmup/:ip /health/warmup/schedules /org/identities /org/invites /org/members")
	add("admin", "POST", "/branding/verify-domain /domains /domains/:uuid/dns/cloudflare /domains/:uuid/ses-verify /domains/:uuid/setup-sending /domains/:uuid/verify /domains/cloudflare/zones /health/alerts/:id/acknowledge /health/blacklist-check /health/warmup /identities /identities/:uuid/catch-all /inbox/setup /org/invites /org/invites/:uuid/resend /shared-mailboxes")
	add("admin", "PUT", "/branding /org/identities/:uuid/owner /org/members/:uuid")
	add("admin", "DELETE", "/domains/:uuid /identities/:uuid /org/invites/:uuid /org/members/:uuid /shared-mailboxes/:id")
	add("humanAdmin", "GET", "/api-keys")
	add("humanAdmin", "POST", "/api-keys")
	add("humanAdmin", "DELETE", "/api-keys/:uuid")
}
