package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Organization management is gated in the real router: members get 403 on
// domain, identity, receiving, API-key, branding and /org writes, while owner
// and admin sessions and admin-owned API keys pass. Roles are read live.
func TestOrganizationRoleMatrixHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "local-role-matrix-fixture-only", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture", DisableAppLimits: true}
	previousDB, previousCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = previousDB, previousCfg })
	password, _ := bcrypt.GenerateFromPassword([]byte("role-matrix-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Roles','roles',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES
			(1,1,'owner@roles.test',$1,'Owner','owner',now()),(2,1,'admin@roles.test',$1,'Admin','admin',now()),(3,1,'member@roles.test',$1,'Member','member',now());`, string(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domains(id,uuid,org_id,name,status,verification_token,ses_verified,updated_at) VALUES(1,'00000000-0000-0000-0000-0000000000d1',1,'roles.test','active','token',true,now());
		INSERT INTO identities(id,uuid,user_id,domain_id,email,can_send,updated_at) VALUES
			(1,'00000000-0000-0000-0000-0000000000a1',1,1,'owner@roles.test',true,now()),
			(2,'00000000-0000-0000-0000-0000000000a2',2,1,'admin@roles.test',true,now()),
			(3,'00000000-0000-0000-0000-0000000000a3',3,1,'member@roles.test',true,now()),
			(4,'00000000-0000-0000-0000-0000000000a4',3,1,'member2@roles.test',true,now());
		SELECT setval(pg_get_serial_sequence('identities','id'),100);`); err != nil {
		t.Fatal(err)
	}

	s := ghttp.GetServer(fmt.Sprintf("roles-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	SetupWithContext(ctx, s, cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", s.GetListenedPort())
	call := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	expect := func(want int, method, path, token string, body any) {
		t.Helper()
		if got, b := call(method, path, token, body); got != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, got, want, b)
		}
	}
	login := func(email string) string {
		t.Helper()
		status, b := call("POST", "/auth/login", "", map[string]string{"email": email, "password": "role-matrix-password"})
		var out struct {
			Data model.LoginResponse `json:"data"`
		}
		if status != 200 || json.Unmarshal(b, &out) != nil || out.Data.Token == "" {
			t.Fatalf("login %s: %d %s", email, status, b)
		}
		return out.Data.Token
	}
	owner, admin, member := login("owner@roles.test"), login("admin@roles.test"), login("member@roles.test")
	auth := service.NewAuthService(db, cfg)
	scopes := []string{"domains:read", "domains:manage", "identities:read", "identities:manage", "email:send"}
	adminKey, err := auth.CreateAPIKey(context.Background(), 1, 2, &model.CreateApiKeyRequest{Name: "Admin key", Permissions: scopes, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// Only admins create keys, so a member-owned key is one kept after a demotion.
	if _, err = db.Exec(`UPDATE users SET role='admin' WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	memberKey, err := auth.CreateAPIKey(context.Background(), 1, 3, &model.CreateApiKeyRequest{Name: "Member key", Permissions: scopes, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE users SET role='member' WHERE id=3`); err != nil {
		t.Fatal(err)
	}

	const domain, memberIdentity = "/domains/00000000-0000-0000-0000-0000000000d1", "/identities/00000000-0000-0000-0000-0000000000a3"
	memberWrites := [][2]string{
		{"POST", "/domains"}, {"POST", domain + "/verify"}, {"POST", domain + "/ses-verify"}, {"POST", domain + "/setup-sending"},
		{"POST", domain + "/dns/cloudflare"}, {"POST", "/domains/cloudflare/zones"}, {"DELETE", domain}, {"POST", "/inbox/setup"},
		{"POST", "/identities"}, {"DELETE", memberIdentity}, {"POST", memberIdentity + "/catch-all"},
		{"GET", "/api-keys"}, {"POST", "/api-keys"}, {"DELETE", "/api-keys/" + memberKey.UUID},
		{"PUT", "/branding"}, {"POST", "/branding/verify-domain"}, {"POST", "/shared-mailboxes"}, {"DELETE", "/shared-mailboxes/1"},
		{"GET", "/org/members"}, {"PUT", "/org/members/00000000-0000-0000-0000-000000000000"}, {"DELETE", "/org/members/00000000-0000-0000-0000-000000000000"},
		{"GET", "/org/invites"}, {"POST", "/org/invites"}, {"POST", "/org/invites/00000000-0000-0000-0000-000000000000/resend"}, {"DELETE", "/org/invites/00000000-0000-0000-0000-000000000000"},
		{"GET", "/org/identities"}, {"PUT", "/org/identities/00000000-0000-0000-0000-0000000000a1/owner"},
		// Health data is org-wide, so every /health/* route is admin-only.
		{"POST", "/health/blacklist-check"}, {"GET", "/health/reputation"}, {"GET", "/health/ses-limits"}, {"GET", "/health/summary"},
		{"GET", "/health/warmup/schedules"}, {"POST", "/health/warmup"}, {"GET", "/health/warmup/1"}, {"GET", "/health/quota"},
		{"GET", "/health/logs"}, {"GET", "/health/alerts"}, {"POST", "/health/alerts/1/acknowledge"},
	}
	for _, route := range memberWrites {
		expect(403, route[0], route[1], member, map[string]any{})
	}
	// Members keep their own reads and non-admin identity edits.
	expect(200, "GET", "/domains", member, nil)
	// Receiving status is a read members may make; a missing domain proves the
	// role gate passed without a live DNS lookup.
	expect(404, "GET", "/domains/00000000-0000-0000-0000-0000000000ff/receiving", member, nil)
	expect(200, "GET", "/identities", member, nil)
	expect(200, "PUT", memberIdentity, member, map[string]any{"displayName": "Member Two"})
	expect(403, "PUT", memberIdentity, member, map[string]any{"isCatchAll": true})
	expect(403, "PUT", memberIdentity, member, map[string]any{"canReceive": false})
	expect(404, "PUT", "/identities/00000000-0000-0000-0000-0000000000a1", member, map[string]any{"displayName": "Not mine"})

	// POST /emails follows the send-as rule for members, by session or key,
	// and nobody may send as another user's send-as alias. Only rejected sends
	// are exercised here, so nothing reaches the provider.
	if _, err = db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) VALUES(3,'sales@roles.test')`); err != nil {
		t.Fatal(err)
	}
	sendAs := func(token, from, key, want string) {
		t.Helper()
		status, b := call("POST", "/emails", token, map[string]any{"from": from, "to": []string{"r@external.test"}, "subject": "s", "text": "t", "idempotencyKey": key})
		if status != 400 || !bytes.Contains(b, []byte(want)) {
			t.Fatalf("send as %s: %d %s", from, status, b)
		}
	}
	sendAs(member, "ceo@roles.test", "member-free-address", "one of your addresses")
	sendAs(memberKey.Key, "ceo@roles.test", "member-key-free-address", "one of your addresses")
	sendAs(member, "owner@roles.test", "member-foreign-address", "belongs to another user")
	sendAs(owner, "sales@roles.test", "owner-foreign-alias", "belongs to another user")

	// Admin sessions pass the gate; validation and ownership still apply.
	expect(200, "GET", "/org/members", admin, nil)
	expect(200, "GET", "/org/invites", admin, nil)
	expect(200, "GET", "/org/identities", admin, nil)
	expect(200, "GET", "/api-keys", admin, nil)
	expect(200, "GET", "/health/logs", admin, nil)
	expect(200, "GET", "/health/alerts", admin, nil)
	expect(400, "POST", "/domains", admin, map[string]any{})
	expect(400, "POST", "/identities", admin, map[string]any{})
	expect(200, "POST", "/identities/00000000-0000-0000-0000-0000000000a2/catch-all", admin, map[string]any{"isCatchAll": true})
	expect(200, "DELETE", "/identities/00000000-0000-0000-0000-0000000000a4", admin, nil)
	expect(403, "PUT", "/org/members/00000000-0000-0000-0000-000000000000", admin, map[string]any{"role": "admin"})
	expect(200, "GET", "/org/members", owner, nil)

	// API keys: an admin-owned key passes, a member-owned key does not, and
	// neither can manage API keys or reach /org.
	expect(200, "POST", "/identities", adminKey.Key, map[string]any{"domainId": "00000000-0000-0000-0000-0000000000d1", "email": "keyed@roles.test", "displayName": "Keyed"})
	expect(400, "POST", "/domains", adminKey.Key, map[string]any{})
	expect(403, "POST", "/identities", memberKey.Key, map[string]any{"domainId": "00000000-0000-0000-0000-0000000000d1", "email": "nope@roles.test", "displayName": "Nope"})
	expect(403, "POST", "/domains", memberKey.Key, map[string]any{})
	expect(200, "GET", "/domains", memberKey.Key, nil)
	expect(404, "GET", "/domains/00000000-0000-0000-0000-0000000000ff/receiving", memberKey.Key, nil)
	expect(403, "GET", "/api-keys", adminKey.Key, nil)
	expect(403, "GET", "/org/members", adminKey.Key, nil)

	// Demotion applies on the next request, for the session and the key.
	var adminUUID string
	if err := db.QueryRow(`SELECT uuid::text FROM users WHERE id=2`).Scan(&adminUUID); err != nil {
		t.Fatal(err)
	}
	expect(200, "PUT", "/org/members/"+adminUUID, owner, map[string]any{"role": "member"})
	expect(403, "GET", "/org/members", admin, nil)
	expect(403, "POST", "/identities", adminKey.Key, map[string]any{"domainId": "00000000-0000-0000-0000-0000000000d1", "email": "late@roles.test", "displayName": "Late"})

	// The invite endpoints are public and give one generic answer.
	expect(404, "POST", "/auth/invites/lookup", "", map[string]any{"token": "unknown"})
	expect(400, "POST", "/auth/invites/accept", "", map[string]any{"token": "unknown", "name": "Someone", "password": "long-enough"})
	expect(400, "POST", "/auth/invites/accept", "", "not an object")
	for i := 1; i < service.RuleInviteLookupIP.Limit; i++ {
		call("POST", "/auth/invites/lookup", "", map[string]any{"token": "unknown"})
	}
	expect(429, "POST", "/auth/invites/lookup", "", map[string]any{"token": "unknown"})
}
