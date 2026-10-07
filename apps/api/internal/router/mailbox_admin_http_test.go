package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// The mailbox admin API through the real router: owner/admin sessions only,
// JSON and text/csv bodies, 201/204 answers. Only password mode is used, so
// no mail is sent.
func TestMailboxAdminHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "local-mailbox-admin-fixture-only", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture", DisableAppLimits: true}
	previousDB, previousCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = previousDB, previousCfg })
	password, _ := bcrypt.GenerateFromPassword([]byte("mailbox-admin-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Boxes','boxes',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES
			(1,1,'owner@boxes.test',$1,'Owner','owner',now()),(2,1,'member@boxes.test',$1,'Member','member',now())`, string(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domains(id,uuid,org_id,name,status,verification_token,ses_verified,updated_at) VALUES(1,'00000000-0000-0000-0000-0000000000d1',1,'boxes.test','active','token',true,now());
		INSERT INTO identities(id,user_id,domain_id,email,can_send,updated_at) VALUES(1,1,1,'owner@boxes.test',true,now());
		SELECT setval(pg_get_serial_sequence('users','id'),100); SELECT setval(pg_get_serial_sequence('identities','id'),100);`); err != nil {
		t.Fatal(err)
	}
	s := startRouter(t, "mailbox-admin", cfg)
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", s.GetListenedPort())
	call := func(method, path, token, contentType string, body []byte) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	expect := func(want int, method, path, token string, body any, out any) {
		t.Helper()
		data, _ := json.Marshal(body)
		status, b := call(method, path, token, "application/json", data)
		if status != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, status, want, b)
		}
		if out != nil {
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(b, &envelope); err != nil || json.Unmarshal(envelope.Data, out) != nil {
				t.Fatalf("%s %s: %s", method, path, b)
			}
		}
	}
	login := func(email, pw string) string {
		t.Helper()
		var out model.LoginResponse
		expect(200, "POST", "/auth/login", "", map[string]string{"email": email, "password": pw}, &out)
		return out.Token
	}
	owner, member := login("owner@boxes.test", "mailbox-admin-password"), login("member@boxes.test", "mailbox-admin-password")
	const domainPath = "/org/domains/00000000-0000-0000-0000-0000000000d1/mailboxes"

	// Members and admin-owned API keys never reach the mailbox admin API.
	key, err := service.NewAuthService(db, cfg).CreateAPIKey(context.Background(), 1, 1, &model.CreateApiKeyRequest{Name: "Owner key", Permissions: []string{"domains:read", "domains:manage", "identities:read", "identities:manage"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{member, key.Key} {
		expect(403, "GET", domainPath, token, nil, nil)
		expect(403, "POST", domainPath, token, map[string]any{}, nil)
		expect(403, "GET", "/org/mailboxes/00000000-0000-0000-0000-000000000000", token, nil, nil)
	}

	var created service.CreateMailboxResult
	expect(201, "POST", domainPath, owner, map[string]any{"localPart": "ibrahim", "name": "Ibrahim", "access": map[string]string{"mode": "password", "password": "initial-password"}}, &created)
	box := created.Mailbox
	if box == nil || box.Address != "ibrahim@boxes.test" || box.Status != "active" || len(created.Warnings) != 1 || created.Warnings[0] != "receiving_disabled" {
		t.Fatalf("create %+v", created)
	}
	expect(409, "POST", domainPath, owner, map[string]any{"localPart": "ibrahim", "name": "Again", "access": map[string]string{"mode": "password", "password": "initial-password"}}, nil)
	expect(400, "POST", domainPath, owner, map[string]any{"localPart": "a+b", "name": "Plus", "access": map[string]string{"mode": "password", "password": "initial-password"}}, nil)
	expect(400, "POST", domainPath, owner, "not an object", nil)

	var list service.DomainMailboxes
	expect(200, "GET", domainPath, owner, nil, &list)
	if list.Domain.Name != "boxes.test" || list.CatchAll != nil || len(list.Mailboxes) != 1 || list.Mailboxes[0].UserUUID != box.UserUUID {
		t.Fatalf("list %+v", list)
	}
	path := "/org/mailboxes/" + box.UserUUID
	var mb service.MailboxAccount
	expect(200, "PUT", path, owner, map[string]any{"wildcardSender": true, "mayReceive": false}, &mb)
	if !mb.WildcardSender || mb.MayReceive || !mb.MaySend {
		t.Fatalf("update %+v", mb)
	}
	var alias service.MailboxAlias
	expect(201, "POST", path+"/aliases", owner, map[string]string{"localPart": "sales"}, &alias)
	expect(409, "POST", path+"/aliases", owner, map[string]string{"localPart": "owner"}, nil)
	var detail service.MailboxDetail
	expect(200, "GET", path, owner, nil, &detail)
	if detail.Mailbox == nil || len(detail.Aliases) != 1 || detail.Aliases[0].Address != "sales@boxes.test" || !detail.Overview.WildcardSender || detail.Invite != nil {
		t.Fatalf("detail %+v", detail)
	}
	if status, b := call("DELETE", path+"/aliases/"+alias.UUID, owner, "application/json", nil); status != 204 || len(b) != 0 {
		t.Fatalf("delete alias: %d %q", status, b)
	}
	expect(404, "DELETE", path+"/aliases/"+alias.UUID, owner, nil, nil)

	// Access: set password ends the user's sessions; other actions answer by state.
	login("ibrahim@boxes.test", "initial-password")
	var pw service.MailboxPasswordResult
	expect(200, "POST", path+"/password", owner, map[string]string{"mode": "set", "password": "second-password"}, &pw)
	if pw.SessionsRevoked != 1 {
		t.Fatalf("password %+v", pw)
	}
	expect(400, "POST", path+"/password", owner, map[string]string{"mode": "magic"}, nil)
	expect(400, "POST", path+"/password", owner, map[string]string{"mode": "link"}, nil) // no recovery email
	expect(409, "POST", path+"/invite/resend", owner, nil, nil)
	expect(409, "POST", path+"/2fa/reset", owner, nil, nil)
	expect(409, "POST", path+"/reactivate", owner, nil, nil)
	expect(200, "POST", path+"/suspend", owner, nil, &mb)
	if mb.Status != "suspended" {
		t.Fatalf("suspend %+v", mb)
	}
	expect(200, "POST", path+"/reactivate", owner, nil, &mb)
	if mb.Status != "active" {
		t.Fatalf("reactivate %+v", mb)
	}

	// CSV import: a dry run, then a commit; bodies over 1 MiB are refused.
	csv := []byte("local_part,name,password\nanna,Anna A,anna-password\nibrahim,Ibrahim,ibrahim-password\n")
	var imported service.MailboxImportResult
	for _, step := range []struct {
		query, result string
	}{{"", "ok"}, {"?dryRun=true", "ok"}, {"?dryRun=false", "created"}} {
		status, b := call("POST", domainPath+"/import"+step.query, owner, "text/csv", csv)
		var envelope struct {
			Data service.MailboxImportResult `json:"data"`
		}
		if status != 200 || json.Unmarshal(b, &envelope) != nil {
			t.Fatalf("import %s: %d %s", step.query, status, b)
		}
		imported = envelope.Data
		if len(imported.Rows) != 2 || imported.Rows[0].Result != step.result || imported.Rows[1].Result != "error" || strings.Contains(string(b), "anna-password") {
			t.Fatalf("import %s: %s", step.query, b)
		}
	}
	if imported.DryRun {
		t.Fatal("dryRun=false reported a dry run")
	}
	login("anna@boxes.test", "anna-password")
	big := append([]byte("local_part,name,password\n"), bytes.Repeat([]byte("x"), 1<<20)...)
	if status, b := call("POST", domainPath+"/import", owner, "text/csv", big); status != 400 || !strings.Contains(string(b), "larger than 1 MiB") {
		t.Fatalf("oversized import: %d %s", status, b)
	}
	if status, b := call("POST", domainPath+"/import", owner, "text/csv", []byte("local_part,name,secret\n")); status != 400 || !strings.Contains(string(b), "Unknown column") {
		t.Fatalf("unknown column: %d %s", status, b)
	}

	// Remove: the login stops working and the mailbox leaves the live list.
	var removed service.RemoveMemberResult
	expect(200, "DELETE", path, owner, map[string]any{}, &removed)
	if !removed.Removed || removed.IdentitiesDisabled != 1 {
		t.Fatalf("remove %+v", removed)
	}
	expect(404, "GET", path, owner, nil, nil)
	expect(200, "GET", domainPath+"?removed=true", owner, nil, &list)
	if len(list.Mailboxes) != 2 || list.Mailboxes[1].Status != "removed" {
		t.Fatalf("removed list %+v", list.Mailboxes)
	}
	expect(404, "DELETE", "/org/mailboxes/"+box.UserUUID, owner, nil, nil)
}
