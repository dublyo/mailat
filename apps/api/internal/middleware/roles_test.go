package middleware

import (
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
)

func TestIsOrgAdminMatrix(t *testing.T) {
	tests := []struct {
		role, keyOwner string
		want           bool
	}{
		{"owner", "", true}, {"admin", "", true}, {"member", "", false}, {"", "", false},
		// A session never borrows a key owner's role.
		{"member", "owner", false},
		{"api", "owner", true}, {"api", "admin", true}, {"api", "member", false}, {"api", "", false}, {"api", "api", false},
	}
	for _, tt := range tests {
		if got := IsOrgAdmin(&model.JWTClaims{Role: tt.role, KeyOwnerRole: tt.keyOwner}); got != tt.want {
			t.Errorf("role=%q owner=%q: got %v", tt.role, tt.keyOwner, got)
		}
	}
	if IsOrgAdmin(nil) {
		t.Fatal("nil claims are not an admin")
	}
}

// Organization membership routes are human-only: no API key scope reaches them.
func TestOrgRoutesHaveNoAPIKeyScope(t *testing.T) {
	for _, route := range [][2]string{
		{"GET", "/api/v1/org/members"}, {"PUT", "/api/v1/org/members/u"}, {"DELETE", "/api/v1/org/members/u"},
		{"GET", "/api/v1/org/invites"}, {"POST", "/api/v1/org/invites"}, {"POST", "/api/v1/org/invites/i/resend"}, {"DELETE", "/api/v1/org/invites/i"},
		{"GET", "/api/v1/org/identities"}, {"PUT", "/api/v1/org/identities/i/owner"},
		{"GET", "/api/v1/org/domains/d/mailboxes"}, {"POST", "/api/v1/org/domains/d/mailboxes"}, {"POST", "/api/v1/org/domains/d/mailboxes/import"},
		{"GET", "/api/v1/org/mailboxes/u"}, {"PUT", "/api/v1/org/mailboxes/u"}, {"DELETE", "/api/v1/org/mailboxes/u"},
		{"POST", "/api/v1/org/mailboxes/u/aliases"}, {"DELETE", "/api/v1/org/mailboxes/u/aliases/a"}, {"POST", "/api/v1/org/mailboxes/u/password"},
		{"POST", "/api/v1/org/mailboxes/u/2fa/reset"}, {"POST", "/api/v1/org/mailboxes/u/invite/resend"},
		{"POST", "/api/v1/org/mailboxes/u/suspend"}, {"POST", "/api/v1/org/mailboxes/u/reactivate"},
		{"POST", "/api/v1/auth/invites/lookup"}, {"POST", "/api/v1/auth/invites/accept"},
		{"GET", "/api/v1/api-keys"}, {"POST", "/api/v1/api-keys"}, {"DELETE", "/api/v1/api-keys/k"},
		{"PUT", "/api/v1/branding"}, {"POST", "/api/v1/branding/verify-domain"},
	} {
		if scope, ok := APIKeyScope(route[0], route[1]); ok {
			t.Errorf("%s %s has scope %s", route[0], route[1], scope)
		}
	}
}
