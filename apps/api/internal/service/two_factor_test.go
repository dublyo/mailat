package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestTOTPURIEscapesLabel(t *testing.T) {
	uri := totpURI("a b/c@example.test", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(uri, "otpauth://totp/Mailat:a%20b%2Fc@example.test?") {
		t.Fatalf("label not escaped as one segment: %s", uri)
	}
	for _, part := range []string{"secret=JBSWY3DPEHPK3PXP", "issuer=Mailat", "digits=6", "period=30"} {
		if !strings.Contains(uri, part) {
			t.Fatalf("missing %s in %s", part, uri)
		}
	}
}

func TestTwoFactorEnableRevokesOtherSessionsAndDisableRejectsReplay(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,name,password_hash,role,updated_at) VALUES(1,1,'user@example.test','User',$1,'owner',now())`, string(hash)); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{JWTSecret: "two-factor-fixture-secret"}
	auth := NewAuthService(db, cfg)
	login := func() string {
		t.Helper()
		r, err := auth.Login(ctx, &model.LoginRequest{Email: "user@example.test", Password: "fixture-password"})
		if err != nil || r.Token == "" {
			t.Fatal("login", err)
		}
		return r.Token
	}
	sessionA, sessionB := login(), login()
	svc := NewTwoFactorService(db, cfg)
	setup, err := svc.GenerateSetup(ctx, 1, "user@example.test")
	if err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(setup.QRCodeDataURL, "data:image/png;base64,"))
	if err != nil || !strings.HasPrefix(setup.QRCodeDataURL, "data:image/png;base64,") || !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("QR code is not a PNG data URL", err)
	}
	if setup.QRCodeURL != totpURI("user@example.test", setup.Secret) || setup.ManualCode == "" {
		t.Fatal("setup fields")
	}

	step := time.Now().Unix() / 30
	enableCode := generateTOTP(setup.Secret, step)
	codes, err := svc.VerifyAndEnable(ctx, 1, enableCode, hashToken(sessionA))
	if err != nil || len(codes) != 10 {
		t.Fatal("enable", err)
	}
	activeSession := func(token string) bool {
		var active bool
		if err := db.QueryRow(`SELECT active FROM user_sessions WHERE token_hash=$1`, hashToken(token)).Scan(&active); err != nil {
			t.Fatal(err)
		}
		return active
	}
	if !activeSession(sessionA) || activeSession(sessionB) {
		t.Fatal("enable must keep the calling session and revoke the others")
	}

	// The enabling code is spent: neither disable nor regenerate accepts it.
	if err = svc.Disable(ctx, 1, "fixture-password", enableCode); err == nil {
		t.Fatal("disable accepted the enabling code")
	}
	next := generateTOTP(setup.Secret, step+1)
	if _, err = svc.RegenerateBackupCodes(ctx, 1, "wrong-password", next); err == nil {
		t.Fatal("wrong password accepted")
	}
	fresh, err := svc.RegenerateBackupCodes(ctx, 1, "fixture-password", next)
	if err != nil {
		t.Fatal("regenerate", err)
	}
	if _, err = svc.RegenerateBackupCodes(ctx, 1, "fixture-password", next); err == nil {
		t.Fatal("regenerate replayed a TOTP step")
	}
	if err = svc.Disable(ctx, 1, "fixture-password", next); err == nil {
		t.Fatal("disable replayed a TOTP step")
	}
	if err = svc.Disable(ctx, 1, "fixture-password", codes[0]); err == nil {
		t.Fatal("old backup code survived regeneration")
	}
	if err = svc.Disable(ctx, 1, "fixture-password", fresh[0]); err != nil {
		t.Fatal("disable with backup code", err)
	}
	enabled, _, _ := svc.GetStatus(ctx, 1)
	if enabled {
		t.Fatal("still enabled")
	}
}
