package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// routerPin is the login PIN fixture users choose at their first sign-in.
const routerPin = "2580"

func postJSON(t *testing.T, url, token string, body any) (int, model.LoginResponse, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var raw bytes.Buffer
	_, _ = raw.ReadFrom(res.Body)
	var out struct {
		Data model.LoginResponse `json:"data"`
	}
	_ = json.Unmarshal(raw.Bytes(), &out)
	return res.StatusCode, out.Data, raw.Bytes()
}

// routerLogin signs in through the real router (apiBase ends in /api/v1):
// the password, then the login PIN step (setting routerPin the first time).
func routerLogin(t *testing.T, apiBase, email, password string) string {
	t.Helper()
	status, step, body := postJSON(t, apiBase+"/auth/login", "", map[string]string{"email": email, "password": password})
	if status != 200 || step.Token != "" || step.ChallengeToken == "" {
		t.Fatalf("login %s: %d %s", email, status, body)
	}
	path := "/auth/pin/verify"
	if step.RequiresPinSetup {
		path = "/auth/pin/setup"
	}
	status, done, body := postJSON(t, apiBase+path, "", map[string]any{"challengeToken": step.ChallengeToken, "pin": routerPin})
	if status != 200 || done.Token == "" {
		t.Fatalf("PIN step %s: %d %s", email, status, body)
	}
	return done.Token
}

// The admin PIN reset route: admins reset members and mailbox users, only
// the owner resets an admin, nobody resets the owner; the reset user signs in
// with a new PIN. Members and mailbox users cannot call it.
func TestLoginPinAdminResetHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "pin-reset-fixture-secret", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture", DisableAppLimits: true}
	oldDB, oldCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = oldDB, oldCfg })
	hash, _ := bcrypt.GenerateFromPassword([]byte("pin-reset-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Pins','pins',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,uuid,org_id,email,password_hash,name,role,updated_at) VALUES
		(1,'00000000-0000-0000-0000-0000000000a1',1,'owner@pins.test',$1,'Owner','owner',now()),
		(2,'00000000-0000-0000-0000-0000000000a2',1,'admin@pins.test',$1,'Admin','admin',now()),
		(3,'00000000-0000-0000-0000-0000000000a3',1,'member@pins.test',$1,'Member','member',now()),
		(4,'00000000-0000-0000-0000-0000000000a4',1,'admin2@pins.test',$1,'Admin Two','admin',now())`, string(hash)); err != nil {
		t.Fatal(err)
	}
	s := startRouter(t, "pin-reset", cfg)
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", s.GetListenedPort())
	owner := routerLogin(t, base, "owner@pins.test", "pin-reset-password")
	admin := routerLogin(t, base, "admin@pins.test", "pin-reset-password")
	member := routerLogin(t, base, "member@pins.test", "pin-reset-password")
	routerLogin(t, base, "admin2@pins.test", "pin-reset-password")
	reset := func(token, uuid string) int {
		status, _, _ := postJSON(t, base+"/org/members/"+uuid+"/pin/reset", token, map[string]any{})
		return status
	}
	if got := reset(member, "00000000-0000-0000-0000-0000000000a3"); got != 403 {
		t.Fatalf("member reset: %d", got)
	}
	if got := reset(admin, "00000000-0000-0000-0000-0000000000a1"); got != 403 {
		t.Fatalf("admin reset owner: %d", got)
	}
	if got := reset(admin, "00000000-0000-0000-0000-0000000000a4"); got != 403 {
		t.Fatalf("admin reset admin: %d", got)
	}
	if got := reset(admin, "00000000-0000-0000-0000-0000000000a3"); got != 200 {
		t.Fatalf("admin reset member: %d", got)
	}
	// The member was signed out and sets a new PIN at the next sign-in.
	req, _ := http.NewRequest("GET", base+"/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+member)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Fatalf("member session survived: %v %v", res, err)
	}
	status, step, body := postJSON(t, base+"/auth/login", "", map[string]string{"email": "member@pins.test", "password": "pin-reset-password"})
	if status != 200 || !step.RequiresPinSetup {
		t.Fatalf("after reset: %d %s", status, body)
	}
	if got := reset(owner, "00000000-0000-0000-0000-0000000000a4"); got != 200 {
		t.Fatalf("owner reset admin: %d", got)
	}
	if got := reset(owner, "00000000-0000-0000-0000-0000000000a1"); got != 409 {
		t.Fatalf("owner reset self: %d", got)
	}
}
