package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/gogf/gf/v2/net/ghttp"
	"golang.org/x/crypto/bcrypt"
)

// Real router/auth/database acceptance. SES is captured on loopback; each run
// migrates and destroys its own database schema without real outbound email.
func TestSignupFormsHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "signup-http-fixture", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture", DisableAppLimits: true, WebUrl: "http://localhost:3100"}
	// Force the synchronous fallback to a loopback SES capture; never enqueue
	// fixture mail into an existing Redis or contact a real provider.
	cfg.RedisURL = "redis://127.0.0.1:1"
	messages := make(chan string, 10)
	ses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Content struct{ Raw struct{ Data string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(envelope.Content.Raw.Data)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		// Decode MIME text instead of searching its transfer-encoded wire body.
		msg, e := mail.ReadMessage(bytes.NewReader(raw))
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		_, params, e := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		parts := multipart.NewReader(msg.Body, params["boundary"])
		var decoded strings.Builder
		for {
			part, e := parts.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Error(e)
				break
			}
			var reader io.Reader = part
			media, nested, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if strings.HasPrefix(media, "multipart/") {
				child := multipart.NewReader(part, nested["boundary"])
				for {
					leaf, e := child.NextPart()
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Error(e)
						break
					}
					var r io.Reader = leaf
					if leaf.Header.Get("Content-Transfer-Encoding") == "base64" {
						r = base64.NewDecoder(base64.StdEncoding, leaf)
					}
					b, _ := io.ReadAll(r)
					decoded.Write(b)
				}
				continue
			}
			switch part.Header.Get("Content-Transfer-Encoding") {
			case "base64":
				reader = base64.NewDecoder(base64.StdEncoding, part)
			case "quoted-printable":
				reader = quotedprintable.NewReader(part)
			}
			content, _ := io.ReadAll(reader)
			decoded.Write(content)
		}
		messages <- decoded.String()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"MessageId":"local-confirmation"}`)
	}))
	t.Cleanup(ses.Close)
	t.Cleanup(func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			var pending int
			if e := db.QueryRow(`SELECT count(*) FROM emails WHERE status IN ('queued','sending')`).Scan(&pending); e != nil || pending == 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	t.Setenv("AWS_ENDPOINT_URL_SESV2", ses.URL)
	previousDB, previousCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = previousDB, previousCfg })
	password, _ := bcrypt.GenerateFromPassword([]byte("mailat-local-test"), bcrypt.MinCost)
	_, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Signup QA','signup-qa',now()),(2,'Other','other',now());`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'qa@example.test',$1,'Signup QA','owner',now()),(2,2,'other@example.test',$1,'Other','owner',now());`, string(password))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO domains(id,org_id,name,status,verification_token,ses_verified,updated_at) VALUES(1,1,'fixture.test','active','fixture',true,now()); INSERT INTO identities(id,user_id,domain_id,email,display_name,updated_at) VALUES(1,1,1,'qa@fixture.test','Signup QA',now());`)
	if err != nil {
		t.Fatal(err)
	}
	s := ghttp.GetServer(fmt.Sprintf("signup-http-%d", time.Now().UnixNano()))
	address := os.Getenv("MAILAT_FORMS_UI_ADDRESS")
	if address == "" {
		address = "127.0.0.1:0"
	}
	s.SetAddr(address)
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	SetupWithContext(ctx, s, cfg)
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())
	call := func(method, path, key string, body any, status int) json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s %s got %d want %d: %s", method, path, res.StatusCode, status, b)
		}
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		json.Unmarshal(b, &env)
		return env.Data
	}
	auth := service.NewAuthService(db, cfg)
	key, err := auth.CreateAPIKey(ctx, 1, 1, &model.CreateApiKeyRequest{Name: "Forms QA", Permissions: []string{"contacts:manage"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.CreateAPIKey(ctx, 2, 2, &model.CreateApiKeyRequest{Name: "Other QA", Permissions: []string{"contacts:manage"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	read, err := auth.CreateAPIKey(ctx, 1, 1, &model.CreateApiKeyRequest{Name: "Read only", Permissions: []string{"email:read"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/signup-forms", "", nil, 401)
	call("GET", "/api/v1/signup-forms", read.Key, nil, 403)
	var list model.List
	json.Unmarshal(call("POST", "/api/v1/lists", key.Key, &model.CreateListRequest{Name: "The Mailat letter"}, 200), &list)
	if list.ConfirmationMode != "single" {
		t.Fatal("list default")
	}
	draft := model.SaveSignupFormRequest{ListID: list.UUID, Name: "Website invitation", Title: "Good things in your inbox", Description: "Fresh ideas, product news, and useful email tips. A small letter, twice a month.", ConsentText: "Yes, send me Mailat’s twice-monthly email updates. I can unsubscribe at any time.", ButtonText: "Count me in", CollectName: true}
	var form model.SignupForm
	json.Unmarshal(call("POST", "/api/v1/signup-forms", key.Key, draft, 200), &form)
	path := "/api/v1/public/forms/" + form.UUID
	call("GET", path, "", nil, 404)
	draft.Published = true
	call("PUT", "/api/v1/signup-forms/"+form.UUID, key.Key, draft, 200)
	for _, suffix := range []string{"", "/signups"} {
		call("GET", "/api/v1/signup-forms/"+form.UUID+suffix, other.Key, nil, 404)
	}
	call("PUT", "/api/v1/signup-forms/"+form.UUID, other.Key, draft, 400)
	call("DELETE", "/api/v1/signup-forms/"+form.UUID, other.Key, nil, 404)
	var public model.PublicSignupForm
	b := call("GET", path, "", nil, 200)
	json.Unmarshal(b, &public)
	for _, secret := range []string{"identityId", "orgId", "createdBy", "fromEmail"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("public metadata leak", secret)
		}
	}
	call("POST", path+"/submit", "", map[string]any{"email": "http@reader.test", "consent": true, "challenge": public.Challenge, "unexpected": true}, 400)
	call("POST", path+"/submit", "", map[string]any{"email": strings.Repeat("x", 17000)}, 400)
	request := model.SubmitSignupRequest{Email: "http@reader.test", FirstName: "Reader", Consent: true, Challenge: public.Challenge}
	call("POST", path+"/submit", "", request, 200)
	call("POST", path+"/submit", "", request, 200)
	var entries model.SignupEntries
	json.Unmarshal(call("GET", "/api/v1/signup-forms/"+form.UUID+"/signups", key.Key, nil, 200), &entries)
	if entries.Total != 1 || len(entries.Items) != 1 || entries.Items[0].Status != "subscribed" {
		t.Fatalf("bad history %+v", entries)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM list_contacts`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate membership", count)
	}
	call("GET", "/api/v1/public/forms/confirm", "", nil, 404)
	call("POST", "/api/v1/public/forms/confirm", "", model.ConfirmSignupRequest{Token: "invalid"}, 410)
	draft.Published = false
	call("PUT", "/api/v1/signup-forms/"+form.UUID, key.Key, draft, 200)
	call("POST", path+"/submit", "", request, 404)
	draft.Published = true
	call("PUT", "/api/v1/signup-forms/"+form.UUID, key.Key, draft, 200)
	call("POST", path+"/submit", "", request, 409)
	// A second form can target the same list; deleting it must preserve contacts.
	var second model.SignupForm
	json.Unmarshal(call("POST", "/api/v1/signup-forms", key.Key, draft, 200), &second)
	call("DELETE", "/api/v1/signup-forms/"+second.UUID, key.Key, nil, 200)
	db.QueryRow(`SELECT count(*) FROM list_contacts`).Scan(&count)
	if count != 1 {
		t.Fatal("deletion removed contacts")
	}
	// Exercise the production transactional sender through the captured SES transport.
	var doubleList model.List
	json.Unmarshal(call("POST", "/api/v1/lists", key.Key, &model.CreateListRequest{Name: "Confirmed readers", ConfirmationMode: "double"}, 200), &doubleList)
	var identityUUID string
	db.QueryRow(`SELECT uuid FROM identities WHERE id=1`).Scan(&identityUUID)
	draft.ListID = doubleList.UUID
	draft.IdentityID = identityUUID
	draft.Name = "Confirmed invitation"
	var doubleForm model.SignupForm
	json.Unmarshal(call("POST", "/api/v1/signup-forms", key.Key, draft, 200), &doubleForm)
	doublePath := "/api/v1/public/forms/" + doubleForm.UUID
	json.Unmarshal(call("GET", doublePath, "", nil, 200), &public)
	call("POST", doublePath+"/submit", "", model.SubmitSignupRequest{Email: "confirm@reader.test", Consent: true, Challenge: public.Challenge}, 200)
	db.QueryRow(`SELECT count(*) FROM contacts WHERE email='confirm@reader.test'`).Scan(&count)
	if count != 0 {
		t.Fatal("pending contact activated")
	}
	var captured string
	select {
	case captured = <-messages:
	case <-time.After(15 * time.Second):
		t.Fatal("confirmation did not reach local SES capture")
	}
	match := regexp.MustCompile(`/subscribe/confirm#([A-Za-z0-9_-]{43})`).FindStringSubmatch(captured)
	if len(match) != 2 {
		t.Fatalf("confirmation missing from captured MIME")
	}
	call("POST", "/api/v1/public/forms/confirm", "", model.ConfirmSignupRequest{Token: match[1]}, 200)
	call("POST", "/api/v1/public/forms/confirm", "", model.ConfirmSignupRequest{Token: match[1]}, 410)
	db.QueryRow(`SELECT count(*) FROM contacts WHERE email='confirm@reader.test' AND status='active'`).Scan(&count)
	if count != 1 {
		t.Fatal("confirmation missing")
	}
	t.Log("Hosted fixture: " + cfg.WebUrl + "/subscribe/" + form.UUID)
	if stop := os.Getenv("MAILAT_FORMS_UI_STOP_FILE"); stop != "" {
		t.Log("Local signup UI fixture ready at " + base)
		for deadline := time.Now().Add(30 * time.Minute); time.Now().Before(deadline); {
			if _, err := os.Stat(stop); err == nil {
				break
			}
			select {
			case raw := <-messages:
				if link := regexp.MustCompile(`/subscribe/confirm#[A-Za-z0-9_-]{43}`).FindString(raw); link != "" {
					t.Log("Captured local confirmation: " + cfg.WebUrl + link)
				}
			case <-time.After(time.Second):
			}
		}
	}
}
