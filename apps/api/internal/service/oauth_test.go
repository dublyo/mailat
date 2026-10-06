package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestOAuthProfileParsers(t *testing.T) {
	g, err := parseGoogleUserInfo([]byte(`{"id":"1","email":"a@example.com","verified_email":true,"name":"A"}`))
	if err != nil || !g.EmailVerified {
		t.Fatal("google verified", err)
	}
	g, _ = parseGoogleUserInfo([]byte(`{"id":"1","email":"a@example.com","verified_email":false}`))
	if g.EmailVerified {
		t.Fatal("google unverified accepted")
	}
	// GitHub ignores the public profile email and uses the primary verified address.
	gh, err := parseGitHubUserInfo([]byte(`{"id":7,"login":"octo","email":"victim@example.com"}`), []byte(`[{"email":"other@example.com","primary":false,"verified":true},{"email":"me@example.com","primary":true,"verified":true}]`))
	if err != nil || gh.Email != "me@example.com" || !gh.EmailVerified || gh.ID != "7" || gh.Name != "octo" {
		t.Fatalf("github: %+v %v", gh, err)
	}
	gh, _ = parseGitHubUserInfo([]byte(`{"id":7,"email":"victim@example.com"}`), []byte(`[{"email":"me@example.com","primary":true,"verified":false}]`))
	if gh.Email != "" || gh.EmailVerified {
		t.Fatalf("github unverified primary used: %+v", gh)
	}
	ms, _ := parseMicrosoftUserInfo([]byte(`{"id":"m","mail":"a@example.com","displayName":"A"}`))
	if ms.EmailVerified || ms.Email != "a@example.com" {
		t.Fatal("microsoft must never be verified")
	}
}

func TestOAuthAuthURLRequestsNoOfflineAccess(t *testing.T) {
	s := NewOAuthService(nil, &config.Config{GoogleClientID: "id", GoogleClientSecret: "secret", APIUrl: "https://api.example.test"})
	u, err := s.GetAuthURL(ProviderGoogle, "state")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"access_type", "prompt=consent"} {
		if strings.Contains(u, banned) {
			t.Fatalf("auth URL requests %s: %s", banned, u)
		}
	}
}

func oauthFixture(t *testing.T, seedUsers bool) (*OAuthService, context.Context) {
	t.Helper()
	db := testutil.Database(t)
	if seedUsers {
		if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now()); INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'owner@example.test','x','Owner','owner',now()),(2,1,'member@example.test','x','Member','member',now())`); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{JWTSecret: "oauth-fixture-secret", GoogleClientID: "id", GoogleClientSecret: "secret", MicrosoftClientID: "id", MicrosoftClientSecret: "secret", APIUrl: "http://api.test"}
	return NewOAuthService(db, cfg), context.Background()
}

func TestOAuthLoginNeverLinksByEmail(t *testing.T) {
	s, ctx := oauthFixture(t, true)
	// A verified provider identity with an existing user's email is not linked.
	_, _, _, err := s.FindLoginUser(ctx, ProviderGoogle, &OAuthUserInfo{ID: "g-attacker", Email: "owner@example.test", EmailVerified: true})
	if !errors.Is(err, ErrOAuthNotLinked) {
		t.Fatalf("want not linked, got %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM oauth_connections`).Scan(&n)
	if n != 0 {
		t.Fatal("connection created by email match")
	}
	// An existing connection signs in and refreshes profile fields only.
	if _, err = s.db.Exec(`INSERT INTO oauth_connections(user_id,provider,provider_user_id,access_token,updated_at) VALUES(1,'google','g-owner','legacy-token',now())`); err != nil {
		t.Fatal(err)
	}
	user, org, isNew, err := s.FindLoginUser(ctx, ProviderGoogle, &OAuthUserInfo{ID: "g-owner", Email: "new@example.test", Name: "Owner"})
	if err != nil || user != 1 || org != 1 || isNew {
		t.Fatal("linked login", user, org, isNew, err)
	}
	var token *string
	s.db.QueryRow(`SELECT access_token FROM oauth_connections WHERE provider_user_id='g-owner'`).Scan(&token)
	if token != nil {
		t.Fatal("provider token retained")
	}
	// A passwordless (OAuth-only) account cannot use password login.
	s.db.Exec(`UPDATE users SET password_hash='' WHERE id=1`)
	if _, err = NewAuthService(s.db, s.cfg).Login(ctx, &model.LoginRequest{Email: "owner@example.test", Password: "anything"}); err == nil {
		t.Fatal("empty password hash accepted")
	}
}

func TestOAuthBootstrapRequiresVerifiedEmail(t *testing.T) {
	s, ctx := oauthFixture(t, false)
	if _, _, _, err := s.FindLoginUser(ctx, ProviderMicrosoft, &OAuthUserInfo{ID: "m-1", Email: "owner@example.test", Name: "Owner"}); !errors.Is(err, ErrOAuthEmailUnverified) {
		t.Fatalf("microsoft bootstrap: %v", err)
	}
	user, _, isNew, err := s.FindLoginUser(ctx, ProviderGoogle, &OAuthUserInfo{ID: "g-1", Email: "owner@example.test", Name: "Owner", EmailVerified: true})
	if err != nil || !isNew || user == 0 {
		t.Fatal("verified bootstrap", err)
	}
	var role string
	s.db.QueryRow(`SELECT role FROM users WHERE id=$1`, user).Scan(&role)
	if role != "owner" {
		t.Fatal("bootstrap role", role)
	}
	// Once an owner exists, a new verified identity is not linked automatically.
	if _, _, _, err = s.FindLoginUser(ctx, ProviderGoogle, &OAuthUserInfo{ID: "g-2", Email: "second@example.test", EmailVerified: true}); !errors.Is(err, ErrOAuthNotLinked) {
		t.Fatalf("second bootstrap: %v", err)
	}
}

func TestOAuthStateAndLinkTickets(t *testing.T) {
	s, ctx := oauthFixture(t, true)
	state, err := s.SaveState(ctx, "link", ProviderGoogle, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ConsumeState(ctx, state, ProviderMicrosoft); !errors.Is(err, ErrOAuthInvalidState) {
		t.Fatal("state accepted for another provider")
	}
	purpose, owner, err := s.ConsumeState(ctx, state, ProviderGoogle)
	if err != nil || purpose != "link" || owner != 1 {
		t.Fatal("consume", purpose, owner, err)
	}
	if _, _, err = s.ConsumeState(ctx, state, ProviderGoogle); !errors.Is(err, ErrOAuthInvalidState) {
		t.Fatal("state replay accepted")
	}
	expired, _ := s.SaveState(ctx, "login", ProviderGoogle, 0)
	s.db.Exec(`UPDATE oauth_states SET expires_at=now()-interval '1 second'`)
	if _, _, err = s.ConsumeState(ctx, expired, ProviderGoogle); !errors.Is(err, ErrOAuthInvalidState) {
		t.Fatal("expired state accepted")
	}

	info := &OAuthUserInfo{ID: "g-owner", Email: "owner@gmail.test", Name: "Owner"}
	foreign, _ := s.CreateLinkTicket(ctx, 1, ProviderGoogle, info)
	if _, err = s.ConfirmLink(ctx, 2, foreign); !errors.Is(err, ErrOAuthLinkMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err = s.ConfirmLink(ctx, 1, foreign); !errors.Is(err, ErrOAuthInvalidTicket) {
		t.Fatal("mismatched ticket stayed usable")
	}
	ticket, _ := s.CreateLinkTicket(ctx, 1, ProviderGoogle, info)
	if p, err := s.ConfirmLink(ctx, 1, ticket); err != nil || p != "google" {
		t.Fatal("confirm", p, err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM oauth_connections WHERE user_id=1 AND provider_user_id='g-owner' AND access_token IS NULL AND refresh_token IS NULL AND token_expiry IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatal("connection not stored without tokens")
	}
	again, _ := s.CreateLinkTicket(ctx, 1, ProviderGoogle, info)
	if _, err = s.ConfirmLink(ctx, 1, again); err != nil {
		t.Fatal("relink same user must be idempotent", err)
	}
	taken, _ := s.CreateLinkTicket(ctx, 2, ProviderGoogle, info)
	if _, err = s.ConfirmLink(ctx, 2, taken); !errors.Is(err, ErrOAuthAlreadyLinked) {
		t.Fatalf("already linked: %v", err)
	}
}

func TestOAuthGitHubProfileFromStub(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" && r.Header.Get("Authorization") != "Bearer stub-token" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"id":42,"login":"octo","email":"public@example.com"}`))
		case "/user/emails":
			w.Write([]byte(`[{"email":"Primary@Example.com","primary":true,"verified":true}]`))
		case "/token":
			w.Write([]byte(`{"access_token":"stub-token"}`))
		}
	}))
	defer stub.Close()
	s := NewOAuthService(nil, &config.Config{GitHubClientID: "id", GitHubClientSecret: "secret"})
	c := s.ProviderConfig(ProviderGitHub)
	c.TokenURL, c.UserInfoURL, c.EmailsURL = stub.URL+"/token", stub.URL+"/user", stub.URL+"/user/emails"
	token, err := s.ExchangeCode(context.Background(), ProviderGitHub, "code")
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.GetUserInfo(context.Background(), ProviderGitHub, token)
	if err != nil || info.Email != "primary@example.com" || !info.EmailVerified || info.ID != "42" {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err = s.GetUserInfo(context.Background(), ProviderGitHub, "bad"); !errors.Is(err, ErrOAuthProvider) {
		t.Fatal("provider failure not typed", err)
	}
}
