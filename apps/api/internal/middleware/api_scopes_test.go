package middleware

import "testing"

func TestAPIKeyScopeMatrixFailsClosed(t *testing.T) {
	tests := []struct{ method, path, want string }{
		{"GET", "/api/v1/inbox/received/email", "email:read"}, {"GET", "/api/v1/inbox/received/email/attachments/file", "email:read"}, {"GET", "/api/v1/inbox/changes", "email:read"}, {"POST", "/api/v1/inbox/received/mark", "email:manage"}, {"POST", "/api/v1/compose/send", "email:send"}, {"POST", "/api/v1/emails/batch", "email:send"}, {"POST", "/api/v1/domains/domain/setup-sending", "domains:manage"}, {"GET", "/api/v1/domains/domain/sending-status", "domains:read"}, {"POST", "/api/v1/webhook-deliveries/delivery/replay", "webhooks:manage"}, {"PUT", "/api/v1/webhook-triggers/trigger", "webhooks:manage"},
		{"GET", "/api/v1/inbox/trusted-senders", "email:read"}, {"POST", "/api/v1/inbox/trusted-senders", "email:manage"}, {"DELETE", "/api/v1/inbox/trusted-senders/sender", "email:manage"}, {"POST", "/api/v1/oauth/link/confirm", ""},
		{"POST", "/api/v1/api-keys", ""}, {"DELETE", "/api/v1/api-keys/other", ""}, {"POST", "/api/v1/security/2fa/setup", ""}, {"GET", "/api/v1/settings", ""}, {"POST", "/api/v1/auth/change-password", ""}, {"POST", "/api/v1/unknown/new-admin", ""}, {"POST", "/api/v1/domains/domain/arbitrary-action", ""},
	}
	for _, tt := range tests {
		scope, ok := APIKeyScope(tt.method, tt.path)
		if scope != tt.want || ok != (tt.want != "") {
			t.Errorf("%s %s: got %q/%t want %q", tt.method, tt.path, scope, ok, tt.want)
		}
	}
}
