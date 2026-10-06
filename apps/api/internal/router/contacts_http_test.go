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

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/gogf/gf/v2/net/ghttp"
)

// Contacts compliance through the real router: typed error codes, partial
// imports, literal search and the removed legacy GET /confirm/:token.
func TestContactsComplianceHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "contacts-http-fixture-contacts-http", JWTExpiresIn: "24h", EmailProvider: "ses", DisableAppLimits: true, WebUrl: "http://localhost:3100", RedisURL: "redis://127.0.0.1:1"}
	previousDB, previousCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = previousDB, previousCfg })
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now()),(2,'Two','two',now());
		INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'qa@one.test','x','QA','owner',now());
		INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'News',now()),(2,2,'Foreign',now());`); err != nil {
		t.Fatal(err)
	}
	s := ghttp.GetServer(fmt.Sprintf("contacts-http-%d", time.Now().UnixNano()))
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
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())
	key, err := service.NewAuthService(db, cfg).CreateAPIKey(ctx, 1, 1, &model.CreateApiKeyRequest{Name: "Contacts QA", Permissions: []string{"contacts:manage"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	type envelope struct {
		Data json.RawMessage `json:"data"`
	}
	call := func(method, path string, body any, status int) json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key.Key)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s %s got %d want %d: %s", method, path, res.StatusCode, status, b)
		}
		var env envelope
		json.Unmarshal(b, &env)
		return env.Data
	}
	wantCode := func(data json.RawMessage, code string) {
		t.Helper()
		var e struct{ Error string }
		if json.Unmarshal(data, &e); e.Error != code {
			t.Fatalf("error code %q want %q", e.Error, code)
		}
	}

	call("GET", "/api/v1/confirm/anything", nil, 404)
	wantCode(call("POST", "/api/v1/contacts", map[string]any{"email": "a@x.test", "listIds": []int{2}}, 400), "unknown_list")
	var created model.Contact
	json.Unmarshal(call("POST", "/api/v1/contacts", map[string]any{"email": "Deal@X.test", "firstName": "50% off", "listIds": []int{1}}, 200), &created)
	if created.Email != "deal@x.test" {
		t.Fatalf("email not normalized: %q", created.Email)
	}
	wantCode(call("PUT", "/api/v1/contacts/"+created.UUID, map[string]any{"status": "bounced"}, 400), "system_status")
	call("PUT", "/api/v1/contacts/"+created.UUID, map[string]any{"status": "unsubscribed"}, 200)
	wantCode(call("PUT", "/api/v1/contacts/"+created.UUID, map[string]any{"status": "active"}, 409), "reactivation_blocked")

	var imported model.ImportContactsResponse
	json.Unmarshal(call("POST", "/api/v1/contacts/import", map[string]any{"listIds": []int{1}, "contacts": []map[string]string{{"email": "fifty@x.test", "firstName": "500 club"}, {"email": "broken"}, {"email": "deal@x.test"}}}, 200), &imported)
	if imported.Imported != 1 || imported.Suppressed != 1 || len(imported.Errors) != 1 {
		t.Fatalf("partial import %+v", imported)
	}

	var listed model.ContactListResponse
	json.Unmarshal(call("GET", "/api/v1/contacts?query=50%25&page=1&pageSize=50", nil, 200), &listed)
	if listed.Total != 1 || listed.Contacts[0].Email != "deal@x.test" {
		t.Fatalf("literal search %+v", listed)
	}
	json.Unmarshal(call("GET", "/api/v1/contacts?sortBy=firstName&sortOrder=asc", nil, 200), &listed)
	if len(listed.Contacts) != 2 || listed.Contacts[0].FirstName != "50% off" {
		t.Fatalf("sorted %+v", listed.Contacts)
	}
}
