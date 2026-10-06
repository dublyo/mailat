package middleware

import (
	"strings"
	"testing"
)

func TestAPIKeyScopeMatrixFailsClosed(t *testing.T) {
	tests := []struct{ method, path, want string }{
		{"GET", "/api/v1/inbox/received/email", "email:read"}, {"GET", "/api/v1/inbox/received/email/attachments/file", "email:read"}, {"GET", "/api/v1/inbox/changes", "email:read"}, {"POST", "/api/v1/inbox/received/mark", "email:manage"}, {"POST", "/api/v1/compose/send", "email:send"}, {"POST", "/api/v1/emails/batch", "email:send"}, {"POST", "/api/v1/domains/domain/setup-sending", "domains:manage"}, {"GET", "/api/v1/domains/domain/sending-status", "domains:read"}, {"POST", "/api/v1/webhook-deliveries/delivery/replay", "webhooks:manage"}, {"PUT", "/api/v1/webhook-triggers/trigger", "webhooks:manage"},
		{"GET", "/api/v1/inbox/trusted-senders", "email:read"}, {"POST", "/api/v1/inbox/trusted-senders", "email:manage"}, {"DELETE", "/api/v1/inbox/trusted-senders/sender", "email:manage"}, {"POST", "/api/v1/oauth/link/confirm", ""},
		{"POST", "/api/v1/api-keys", ""}, {"DELETE", "/api/v1/api-keys/other", ""}, {"POST", "/api/v1/security/2fa/setup", ""}, {"GET", "/api/v1/settings", ""}, {"POST", "/api/v1/auth/change-password", ""}, {"POST", "/api/v1/unknown/new-admin", ""}, {"POST", "/api/v1/domains/domain/arbitrary-action", ""},
		{"GET", "/api/v1/campaigns", "campaigns:read"}, {"GET", "/api/v1/campaigns/c", "campaigns:read"}, {"GET", "/api/v1/campaigns/c/stats", "campaigns:read"}, {"GET", "/api/v1/campaigns/c/progress", "campaigns:read"}, {"GET", "/api/v1/campaigns/c/audience", "campaigns:read"}, {"GET", "/api/v1/campaigns/c/recipients", "campaigns:read"}, {"GET", "/api/v1/campaign-settings", "campaigns:read"}, {"POST", "/api/v1/campaigns/c/preview", "campaigns:read"},
		{"POST", "/api/v1/campaigns", "campaigns:manage"}, {"POST", "/api/v1/campaigns/c/schedule", "campaigns:manage"}, {"POST", "/api/v1/campaigns/c/send", "campaigns:manage"}, {"POST", "/api/v1/campaigns/c/pause", "campaigns:manage"}, {"POST", "/api/v1/campaigns/c/resume", "campaigns:manage"}, {"POST", "/api/v1/campaigns/c/cancel", "campaigns:manage"}, {"POST", "/api/v1/campaigns/c/test", "campaigns:manage"}, {"PUT", "/api/v1/campaigns/c", "campaigns:manage"}, {"PUT", "/api/v1/campaign-settings", "campaigns:manage"}, {"DELETE", "/api/v1/campaigns/c", "campaigns:manage"},
		{"GET", "/api/v1/automations", "automations:read"}, {"GET", "/api/v1/automations/a", "automations:read"}, {"GET", "/api/v1/automations/a/stats", "automations:read"}, {"GET", "/api/v1/automations/a/enrollments", "automations:read"}, {"GET", "/api/v1/automations/a/enrollments/e", "automations:read"},
		{"POST", "/api/v1/automations/a/enroll", "automations:enroll"}, {"POST", "/api/v1/automations/a/enrollments/e/cancel", "automations:enroll"}, {"POST", "/api/v1/automations/a/enrollments/e/retry", "automations:enroll"},
		{"POST", "/api/v1/automations", ""}, {"PUT", "/api/v1/automations/a", ""}, {"DELETE", "/api/v1/automations/a", ""}, {"POST", "/api/v1/automations/a/validate", ""}, {"POST", "/api/v1/automations/a/activate", ""}, {"POST", "/api/v1/automations/a/pause", ""}, {"POST", "/api/v1/automations/a/archive", ""}, {"GET", "/api/v1/automations/a/enroll", ""},
		{"POST", "/api/v1/campaigns/c/unknown", ""}, {"PUT", "/api/v1/campaigns/c/send", ""}, {"DELETE", "/api/v1/campaign-settings", ""}, {"GET", "/api/v1/campaigns/c/test", ""},
	}
	for _, tt := range tests {
		scope, ok := APIKeyScope(tt.method, tt.path)
		if scope != tt.want || ok != (tt.want != "") {
			t.Errorf("%s %s: got %q/%t want %q", tt.method, tt.path, scope, ok, tt.want)
		}
	}
}

// Two patterns that can match the same path must not grant different scopes,
// since map iteration order would then decide the scope.
func TestAPIKeyScopePatternsDoNotOverlap(t *testing.T) {
	for a, scopeA := range apiRouteScopes {
		for b, scopeB := range apiRouteScopes {
			if a >= b || scopeA == scopeB {
				continue
			}
			ma, pa, _ := strings.Cut(a, " ")
			mb, pb, _ := strings.Cut(b, " ")
			sa, sb := strings.Split(pa, "/"), strings.Split(pb, "/")
			if ma != mb || len(sa) != len(sb) {
				continue
			}
			overlap := true
			for i := range sa {
				if sa[i] != sb[i] && !strings.HasPrefix(sa[i], ":") && !strings.HasPrefix(sb[i], ":") {
					overlap = false
					break
				}
			}
			if overlap {
				t.Errorf("%q (%s) overlaps %q (%s)", a, scopeA, b, scopeB)
			}
		}
	}
}
