package service

import (
	"context"
	"fmt"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"golang.org/x/crypto/bcrypt"
	"sync"
	"testing"
	"time"
)

func TestTOTPLoginAndReplayIntegration(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',now());`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,name,password_hash,role,totp_enabled,totp_secret,updated_at) VALUES(1,1,'user@example.test','User',$1,'owner',true,'JBSWY3DPEHPK3PXP',now())`, string(hash)); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(db, &config.Config{JWTSecret: "fixture-only-secret"})
	req := &model.LoginRequest{Email: "user@example.test", Password: "fixture-password"}
	first, err := auth.Login(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.Login(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	code := generateTOTP("JBSWY3DPEHPK3PXP", time.Now().Unix()/30)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, challenge := range []string{first.ChallengeToken, second.ChallengeToken} {
		wg.Add(1)
		go func(challenge string) {
			defer wg.Done()
			result, err := auth.CompleteChallenge(ctx, challenge, code)
			if err == nil && (result == nil || result.Token == "") {
				err = fmt.Errorf("missing token")
			}
			results <- err
		}(challenge)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("TOTP replay accepted: %d sessions", success)
	}
	// A challenge made before a password change must not issue a later session.
	pending, err := auth.Login(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err = NewSessionService(db, auth.cfg).ChangePassword(ctx, 1, "fixture-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CompleteChallenge(ctx, pending.ChallengeToken, code); err == nil {
		t.Fatal("old credential challenge survived password change")
	}
}
func TestTOTPRejectsMalformedInputs(t *testing.T) {
	svc := &TwoFactorService{}
	for _, pair := range [][2]string{{"", ""}, {"bad base32", ""}, {"JBSWY3DPEHPK3PXP", ""}, {"JBSWY3DPEHPK3PXP", "123"}, {"JBSWY3DPEHPK3PXP", "abcdef"}} {
		if svc.verifyTOTP(pair[0], pair[1]) {
			t.Fatal("malformed TOTP accepted")
		}
	}
	// RFC 6238 SHA-1 test vector at time59, reduced to this application's six digits.
	if got := generateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 1); got != "287082" {
		t.Fatal(got)
	}
}

func TestTOTPSetupCanOnlyBeEnabledOnce(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',now()); INSERT INTO users(id,org_id,email,name,password_hash,role,totp_enabled,totp_secret,updated_at) VALUES(1,1,'user@example.test','User','unused','owner',false,'JBSWY3DPEHPK3PXP',now())`); err != nil {
		t.Fatal(err)
	}
	svc := NewTwoFactorService(db, &config.Config{})
	code := generateTOTP("JBSWY3DPEHPK3PXP", time.Now().Unix()/30)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := svc.VerifyAndEnable(ctx, 1, code, ""); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent enable generated %d backup-code sets", success)
	}
}
