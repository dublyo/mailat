package middleware

import (
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/gogf/gf/v2/net/ghttp"
)

func TestMailboxGateMatrix(t *testing.T) {
	box := &model.JWTClaims{Role: "mailbox"}
	boxKey := &model.JWTClaims{Role: "api", KeyOwnerRole: "mailbox"}
	member := &model.JWTClaims{Role: "member"}
	route := func(method, uri string) *ghttp.Router { return &ghttp.Router{Method: method, Uri: uri} }
	tests := []struct {
		claims *model.JWTClaims
		router *ghttp.Router
		want   bool
	}{
		{box, route("GET", "/api/v1/inbox/received"), true},
		{box, route("GET", "/api/v1/inbox/received/:uuid"), true},
		{box, route("POST", "/api/v1/compose/send"), true},
		{box, route("PUT", "/api/v1/identities/:uuid"), true},
		{box, route("GET", "/api/v1/shared-mailboxes/:id"), true},
		{box, route("POST", "/api/v1/auth/change-password"), true},
		// Same path, another method; staff and admin routes; unknown routes.
		{box, route("POST", "/api/v1/identities"), false},
		{box, route("DELETE", "/api/v1/identities/:uuid"), false},
		{box, route("GET", "/api/v1/domains"), false},
		{box, route("POST", "/api/v1/emails"), false},
		{box, route("GET", "/api/v1/health/logs"), false},
		{box, route("GET", "/api/v1/security/audit-logs"), false},
		{box, route("GET", "/api/v1/oauth/connections"), false},
		{box, route("GET", "/api/v1/shared-mailboxes/:id/members"), false},
		{box, route("GET", "/api/v1/inbox/received/x"), false},
		{box, route("GET", "/api/v1/inbox/received/"), false},
		{box, route("get", "/api/v1/inbox/received"), false},
		{box, route("GET", "/api/v1/unknown"), false},
		{box, nil, false},
		{boxKey, route("GET", "/api/v1/inbox/received"), true},
		{boxKey, route("GET", "/api/v1/domains"), false},
		// Everyone else passes; other middleware decides.
		{member, route("GET", "/api/v1/domains"), true},
		{member, nil, true},
		{&model.JWTClaims{Role: "admin"}, route("POST", "/api/v1/domains"), true},
		{&model.JWTClaims{Role: "api", KeyOwnerRole: "owner"}, route("POST", "/api/v1/domains"), true},
		// A session never borrows a key owner's role.
		{&model.JWTClaims{Role: "member", KeyOwnerRole: "mailbox"}, route("GET", "/api/v1/domains"), true},
	}
	for _, tt := range tests {
		if got := mailboxAllowed(tt.claims, tt.router); got != tt.want {
			t.Errorf("%+v %+v: got %v", tt.claims, tt.router, got)
		}
	}
}

func TestMailboxRoleHelpers(t *testing.T) {
	if !IsMailbox(&model.JWTClaims{Role: "mailbox"}) || !IsMailbox(&model.JWTClaims{Role: "api", KeyOwnerRole: "mailbox"}) {
		t.Fatal("mailbox session or key not recognized")
	}
	if IsMailbox(nil) || IsMailbox(&model.JWTClaims{Role: "member"}) {
		t.Fatal("non-mailbox recognized as mailbox")
	}
	if IsOrgAdmin(&model.JWTClaims{Role: "mailbox"}) || IsOrgAdmin(&model.JWTClaims{Role: "api", KeyOwnerRole: "mailbox"}) {
		t.Fatal("mailbox is never an org admin")
	}
	for _, k := range MailboxRouteKeys() {
		if !strings.Contains(k, " /api/v1/") {
			t.Fatalf("malformed allowlist key %q", k)
		}
	}
}
