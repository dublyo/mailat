package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// testLoginPin is the PIN fixtures choose at their first sign-in.
const testLoginPin = "2580"

// passPinStep completes a PIN (or PIN setup) step with testLoginPin. Results
// that are already a session, or a TOTP step, are returned unchanged.
func passPinStep(auth *AuthService, res *model.LoginResponse) (*model.LoginResponse, error) {
	if res == nil || res.Token != "" || res.RequiresTwoFactor {
		return res, nil
	}
	switch {
	case res.RequiresPinSetup:
		return auth.SetupLoginPin(context.Background(), res.ChallengeToken, testLoginPin, false, DeviceMeta{})
	case res.RequiresPin:
		return auth.VerifyLoginPin(context.Background(), res.ChallengeToken, testLoginPin, false, DeviceMeta{})
	}
	return nil, errors.New("sign-in returned neither a session nor a step")
}

// loginWithPin signs in with the password and testLoginPin.
func loginWithPin(auth *AuthService, email, password string) (*model.LoginResponse, error) {
	res, err := auth.Login(context.Background(), &model.LoginRequest{Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	return passPinStep(auth, res)
}

// acceptedSession completes the PIN step that follows an accepted link.
func acceptedSession(t *testing.T, auth *AuthService, acc *AcceptInviteResult) *model.LoginResponse {
	t.Helper()
	if acc.SignedIn || acc.Token != "" {
		t.Fatalf("accept issued a session before the PIN step: %+v", acc)
	}
	res, err := passPinStep(auth, &model.LoginResponse{RequiresPin: acc.RequiresPin, RequiresPinSetup: acc.RequiresPinSetup, ChallengeToken: acc.ChallengeToken})
	if err != nil || res == nil || res.Token == "" {
		t.Fatalf("PIN step after accept: %+v %v", res, err)
	}
	return res
}

func TestValidateLoginPIN(t *testing.T) {
	for _, pin := range []string{"2580", "1357", "0102", "9081", "1123", "7890"} {
		if err := ValidateLoginPIN(pin); err != nil {
			t.Errorf("%s rejected: %v", pin, err)
		}
	}
	for _, pin := range []string{"", "123", "12345", "12a4", "١٢٣٤", " 258", "0000", "1111", "9999", "1234", "0123", "6789", "4321", "9876", "3210"} {
		if err := ValidateLoginPIN(pin); err == nil {
			t.Errorf("%q accepted", pin)
		}
	}
}

type pinFixture struct {
	db   *sql.DB
	auth *AuthService
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	db := testutil.Database(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,name,password_hash,role,updated_at) VALUES(1,1,'user@example.test','User',$1,'owner',now()),(2,1,'other@example.test','Other',$1,'member',now())`, string(hash)); err != nil {
		t.Fatal(err)
	}
	return &pinFixture{db: db, auth: NewAuthService(db, &config.Config{JWTSecret: "pin-fixture-secret"})}
}

func (f *pinFixture) login(t *testing.T, email, deviceToken string) *model.LoginResponse {
	t.Helper()
	res, err := f.auth.Login(context.Background(), &model.LoginRequest{Email: email, Password: "fixture-password", DeviceToken: deviceToken})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *pinFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantPinStatus(t *testing.T, err error, status int) *PinError {
	t.Helper()
	var pinErr *PinError
	if !errors.As(err, &pinErr) || pinErr.Status != status {
		t.Fatalf("want PIN error %d, got %v", status, err)
	}
	return pinErr
}

// A password alone never issues a session: first login sets the PIN, later
// logins verify it, and a remembered device skips it for that user only.
func TestLoginPinSetupVerifyAndTrustedDevice(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	first := f.login(t, "user@example.test", "")
	if first.Token != "" || !first.RequiresPinSetup || first.RequiresPin || len(first.ChallengeToken) != 64 {
		t.Fatalf("first login: %+v", first)
	}
	if n := f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=1`); n != 0 {
		t.Fatal("session before the PIN step")
	}
	// The PIN challenge is not a TOTP challenge.
	if _, err := f.auth.CompleteChallenge(ctx, first.ChallengeToken, "123456"); err == nil {
		t.Fatal("PIN challenge accepted as a TOTP challenge")
	}
	_, err := f.auth.SetupLoginPin(ctx, first.ChallengeToken, "1234", true, DeviceMeta{})
	wantPinStatus(t, err, http.StatusBadRequest)
	_, err = f.auth.VerifyLoginPin(ctx, first.ChallengeToken, testLoginPin, false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusUnauthorized)
	ua := "Mozilla/5.0 " + strings.Repeat("x", 300)
	set, err := f.auth.SetupLoginPin(ctx, first.ChallengeToken, testLoginPin, true, DeviceMeta{UserAgent: ua, IP: "198.51.100.4"})
	if err != nil || set.Token == "" || set.User == nil || set.User.Email != "user@example.test" || len(set.DeviceToken) != 64 || set.DeviceTokenExpiresAt == nil {
		t.Fatalf("setup: %+v %v", set, err)
	}
	if d := time.Until(*set.DeviceTokenExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("device expiry %v", d)
	}
	if n := f.count(t, `SELECT count(*) FROM trusted_devices WHERE user_id=1 AND token_hash<>$1 AND char_length(user_agent)=255`, set.DeviceToken); n != 1 {
		t.Fatal("device token stored in clear or user agent not trimmed")
	}
	// The setup challenge is single use.
	_, err = f.auth.SetupLoginPin(ctx, first.ChallengeToken, "1357", false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusUnauthorized)
	st, err := f.auth.LoginPinStatus(ctx, 1)
	if err != nil || !st.Set || st.SetAt == nil {
		t.Fatalf("status %+v %v", st, err)
	}

	// A remembered device skips the PIN; without it the PIN is asked.
	if res := f.login(t, "user@example.test", set.DeviceToken); res.Token == "" {
		t.Fatalf("trusted device did not skip the PIN: %+v", res)
	}
	plain := f.login(t, "user@example.test", "")
	if !plain.RequiresPin || plain.Token != "" {
		t.Fatalf("no device: %+v", plain)
	}
	// Another user's device token, or a malformed one, does not skip it.
	if _, err = loginWithPin(f.auth, "other@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	if res := f.login(t, "other@example.test", set.DeviceToken); !res.RequiresPin {
		t.Fatalf("device token used for another user: %+v", res)
	}
	if res := f.login(t, "user@example.test", "short"); !res.RequiresPin {
		t.Fatalf("malformed device token: %+v", res)
	}
	ok, err := f.auth.VerifyLoginPin(ctx, plain.ChallengeToken, testLoginPin, false, DeviceMeta{})
	if err != nil || ok.Token == "" || ok.DeviceToken != "" {
		t.Fatalf("verify: %+v %v", ok, err)
	}
	// An expired device asks for the PIN again (fixed expiry, not sliding).
	if _, err = f.db.Exec(`UPDATE trusted_devices SET expires_at=now()-interval '1 second' WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
	if res := f.login(t, "user@example.test", set.DeviceToken); !res.RequiresPin {
		t.Fatalf("expired device skipped the PIN: %+v", res)
	}
	if err = f.auth.CleanupLoginState(ctx); err != nil || f.count(t, `SELECT count(*) FROM trusted_devices WHERE user_id=1`) != 0 {
		t.Fatal("expired device not cleaned up", err)
	}
}

// A wrong PIN costs a challenge attempt and counts toward 5 per 15 minutes
// and 20 per 24 hours, after which even the right PIN is refused (429).
func TestLoginPinAttemptLimits(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	ch := f.login(t, "user@example.test", "").ChallengeToken
	for i := 1; i <= 4; i++ {
		_, err := f.auth.VerifyLoginPin(ctx, ch, "1357", false, DeviceMeta{IP: "203.0.113.9"})
		pe := wantPinStatus(t, err, http.StatusUnauthorized)
		if pe.AttemptsLeft == nil || *pe.AttemptsLeft != 5-i {
			t.Fatalf("attempt %d: %+v", i, pe)
		}
	}
	_, err := f.auth.VerifyLoginPin(ctx, ch, "abcd", false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusBadRequest)
	// The fifth wrong PIN reaches the 15-minute limit.
	_, err = f.auth.VerifyLoginPin(ctx, ch, "1357", false, DeviceMeta{})
	if pe := wantPinStatus(t, err, http.StatusTooManyRequests); pe.RetryAfter <= 0 || pe.RetryAfter > 15*time.Minute {
		t.Fatalf("retry after %v", pe.RetryAfter)
	}
	// A fresh challenge with the right PIN is still refused while locked.
	_, err = f.auth.VerifyLoginPin(ctx, f.login(t, "user@example.test", "").ChallengeToken, testLoginPin, false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusTooManyRequests)
	if n := f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=1 AND active`); n != 1 {
		t.Fatalf("sessions %d", n)
	}
	// Other users are unaffected.
	if _, err = loginWithPin(f.auth, "other@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}

	// Once the short window passed, the right PIN works and clears failures.
	if _, err = f.db.Exec(`UPDATE login_pin_failures SET created_at=created_at-interval '16 minutes'`); err != nil {
		t.Fatal(err)
	}
	if res, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil || res.Token == "" {
		t.Fatalf("after window: %+v %v", res, err)
	}
	if n := f.count(t, `SELECT count(*) FROM login_pin_failures WHERE user_id=1`); n != 0 {
		t.Fatal("success kept failures")
	}

	// 19 failures earlier today, then the 20th locks the day and is audited.
	if _, err = f.db.Exec(`INSERT INTO login_pin_failures(user_id,created_at) SELECT 1,now()-interval '2 hours' FROM generate_series(1,19)`); err != nil {
		t.Fatal(err)
	}
	ch = f.login(t, "user@example.test", "").ChallengeToken
	_, err = f.auth.VerifyLoginPin(ctx, ch, "1357", false, DeviceMeta{IP: "203.0.113.9"})
	if pe := wantPinStatus(t, err, http.StatusTooManyRequests); pe.RetryAfter < 21*time.Hour {
		t.Fatalf("daily lock retry after %v", pe.RetryAfter)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE user_id=1 AND action='login_pin_locked'`); n != 1 {
		t.Fatalf("audit rows %d", n)
	}
	_, err = f.auth.VerifyLoginPin(ctx, f.login(t, "user@example.test", "").ChallengeToken, testLoginPin, false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusTooManyRequests)
}

// The challenge allows 5 attempts and expires after 5 minutes.
func TestLoginPinChallengeAttemptsAndExpiry(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	ch := f.login(t, "user@example.test", "").ChallengeToken
	if _, err := f.db.Exec(`UPDATE auth_challenges SET attempts=4 WHERE kind='pin' AND consumed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	_, err := f.auth.VerifyLoginPin(ctx, ch, "1357", false, DeviceMeta{})
	if pe := wantPinStatus(t, err, http.StatusUnauthorized); pe.AttemptsLeft == nil || *pe.AttemptsLeft != 0 {
		t.Fatalf("%+v", pe)
	}
	_, err = f.auth.VerifyLoginPin(ctx, ch, testLoginPin, false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusUnauthorized)
	ch = f.login(t, "user@example.test", "").ChallengeToken
	if _, err = f.db.Exec(`UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE kind='pin' AND consumed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	_, err = f.auth.VerifyLoginPin(ctx, ch, testLoginPin, false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusUnauthorized)
	// A credential change between password and PIN voids the challenge.
	ch = f.login(t, "user@example.test", "").ChallengeToken
	if _, err = f.db.Exec(`UPDATE users SET auth_version=auth_version+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	_, err = f.auth.VerifyLoginPin(ctx, ch, testLoginPin, false, DeviceMeta{})
	wantPinStatus(t, err, http.StatusUnauthorized)
}

// TOTP users pass TOTP, then the PIN; a device token sent with the password
// skips only the PIN.
func TestLoginPinAfterTOTP(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	set := f.login(t, "user@example.test", "")
	trusted, err := f.auth.VerifyLoginPin(ctx, set.ChallengeToken, testLoginPin, true, DeviceMeta{UserAgent: "UA"})
	if err != nil || trusted.DeviceToken == "" {
		t.Fatal(trusted, err)
	}
	if _, err = f.db.Exec(`UPDATE users SET totp_enabled=true,totp_secret='JBSWY3DPEHPK3PXP' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	step := time.Now().Unix() / 30
	totp := f.login(t, "user@example.test", "")
	if !totp.RequiresTwoFactor || totp.RequiresPin {
		t.Fatalf("%+v", totp)
	}
	sessions := f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=1`)
	pin, err := f.auth.CompleteChallenge(ctx, totp.ChallengeToken, generateTOTP("JBSWY3DPEHPK3PXP", step))
	if err != nil || pin.Token != "" || !pin.RequiresPin || pin.ChallengeToken == totp.ChallengeToken {
		t.Fatalf("after TOTP: %+v %v", pin, err)
	}
	if n := f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=1`); n != sessions {
		t.Fatal("TOTP alone issued a session")
	}
	done, err := f.auth.VerifyLoginPin(ctx, pin.ChallengeToken, testLoginPin, false, DeviceMeta{})
	if err != nil || done.Token == "" {
		t.Fatal(done, err)
	}
	// The trusted device was made before TOTP was turned on (auth_version
	// bump on enable is not used here), so it still matches: TOTP, no PIN.
	if _, err = f.db.Exec(`UPDATE users SET totp_last_step=NULL WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	withDevice := f.login(t, "user@example.test", trusted.DeviceToken)
	if !withDevice.RequiresTwoFactor {
		t.Fatalf("%+v", withDevice)
	}
	skip, err := f.auth.CompleteChallenge(ctx, withDevice.ChallengeToken, generateTOTP("JBSWY3DPEHPK3PXP", step+1))
	if err != nil || skip.Token == "" {
		t.Fatalf("trusted device after TOTP: %+v %v", skip, err)
	}
}

// Changing the PIN, signing out everywhere and a password change forget
// remembered devices; the device list shows only live devices.
func TestLoginPinChangeAndDeviceRevocation(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	remember := func() string {
		t.Helper()
		res, err := f.auth.VerifyLoginPin(ctx, f.login(t, "user@example.test", "").ChallengeToken, testLoginPin, true, DeviceMeta{UserAgent: "Browser"})
		if err != nil || res.DeviceToken == "" {
			t.Fatal(res, err)
		}
		return res.DeviceToken
	}
	a, _ := remember(), remember()
	list, err := f.auth.ListTrustedDevices(ctx, 1)
	if err != nil || len(list) != 2 || list[0].UserAgent != "Browser" {
		t.Fatalf("list %+v %v", list, err)
	}
	if err = f.auth.ForgetTrustedDevice(ctx, 2, list[0].UUID); err == nil {
		t.Fatal("forgot another user's device")
	}
	if err = f.auth.ForgetTrustedDevice(ctx, 1, list[0].UUID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.auth.ForgetAllTrustedDevices(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if res := f.login(t, "user@example.test", a); !res.RequiresPin {
		t.Fatal("forgotten device skipped the PIN")
	}

	// Change PIN: the current password is required; devices are forgotten.
	a = remember()
	err = f.auth.ChangeLoginPin(ctx, 1, "wrong-password", "1357")
	wantPinStatus(t, err, http.StatusBadRequest)
	err = f.auth.ChangeLoginPin(ctx, 1, "fixture-password", "4444")
	wantPinStatus(t, err, http.StatusBadRequest)
	if err = f.auth.ChangeLoginPin(ctx, 1, "fixture-password", "1357"); err != nil {
		t.Fatal(err)
	}
	if res := f.login(t, "user@example.test", a); !res.RequiresPin {
		t.Fatal("device survived a PIN change")
	} else if _, err = f.auth.VerifyLoginPin(ctx, res.ChallengeToken, "1357", false, DeviceMeta{}); err != nil {
		t.Fatal("new PIN rejected", err)
	}
	if err = f.auth.ChangeLoginPin(ctx, 1, "fixture-password", testLoginPin); err != nil {
		t.Fatal(err)
	}

	// Sign out everywhere forgets devices.
	a = remember()
	if _, err = NewSessionService(f.db, f.auth.cfg).RevokeAllSessions(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}
	if res := f.login(t, "user@example.test", a); !res.RequiresPin {
		t.Fatal("device survived sign out everywhere")
	}
	// A password change bumps auth_version and forgets devices.
	a = remember()
	if err = NewSessionService(f.db, f.auth.cfg).ChangePassword(ctx, 1, "fixture-password", "fixture-password-2"); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM trusted_devices WHERE user_id=1`) != 0 {
		t.Fatal("password change kept devices")
	}
	res, err := f.auth.Login(ctx, &model.LoginRequest{Email: "user@example.test", Password: "fixture-password-2", DeviceToken: a})
	if err != nil || !res.RequiresPin {
		t.Fatalf("device survived a password change: %+v %v", res, err)
	}
}

// At most 20 remembered devices per user; the oldest goes first.
func TestTrustedDeviceCap(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	var first string
	for i := 0; i < trustedDeviceCap+2; i++ {
		token, _, err := f.auth.createTrustedDevice(ctx, 1, 0, DeviceMeta{UserAgent: fmt.Sprint("device ", i)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = token
		}
		if _, err = f.db.Exec(`UPDATE trusted_devices SET created_at=created_at-make_interval(secs => $1) WHERE token_hash=$2`, trustedDeviceCap+2-i, hashToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM trusted_devices WHERE user_id=1`); n != trustedDeviceCap {
		t.Fatalf("devices %d", n)
	}
	if f.count(t, `SELECT count(*) FROM trusted_devices WHERE token_hash=$1`, hashToken(first)) != 0 {
		t.Fatal("oldest device kept")
	}
}

// Registration of the first owner ends at PIN setup, not a session.
func TestRegisterRequiresPinSetup(t *testing.T) {
	db := testutil.Database(t)
	auth := NewAuthService(db, &config.Config{JWTSecret: "pin-fixture-secret"})
	res, err := auth.Register(context.Background(), &model.RegisterRequest{Email: "first@example.test", Password: "first-password", Name: "First"})
	if err != nil || res.Token != "" || !res.RequiresPinSetup || res.ChallengeToken == "" {
		t.Fatalf("register: %+v %v", res, err)
	}
	done, err := passPinStep(auth, res)
	if err != nil || done.Token == "" || done.User.Role != "owner" {
		t.Fatalf("setup: %+v %v", done, err)
	}
}

// Admin PIN reset: owners reset admins, members and mailbox users; admins
// reset members only; nobody resets the owner or themselves here.
func TestResetLoginPinGuards(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE users SET login_pin_hash='x',login_pin_set_at=now() WHERE org_id=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	var ownerUUID string
	if err := f.db.QueryRow(`SELECT uuid::text FROM users WHERE id=$1`, f.owner.UserID).Scan(&ownerUUID); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.ResetLoginPin(ctx, f.admin, ownerUUID)
	wantStatus(t, err, http.StatusForbidden)
	_, err = f.svc.ResetLoginPin(ctx, f.owner, ownerUUID)
	wantStatus(t, err, http.StatusConflict)
	_, err = f.svc.ResetLoginPin(ctx, f.admin, f.adminUUID)
	wantStatus(t, err, http.StatusConflict)
	_, err = f.svc.ResetLoginPin(ctx, f.owner, "not-a-uuid")
	wantStatus(t, err, http.StatusNotFound)

	// A member with a session and a remembered device.
	if _, err = f.db.Exec(`INSERT INTO user_sessions(user_id,org_id,token_hash,expires_at) VALUES($1,$2,'member-session',now()+interval '1 day')`, f.member.UserID, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO trusted_devices(user_id,token_hash,auth_version,expires_at) VALUES($1,'member-device',0,now()+interval '1 day')`, f.member.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO login_pin_failures(user_id) SELECT $1 FROM generate_series(1,20)`, f.member.UserID); err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.ResetLoginPin(ctx, f.admin, f.memberUUID)
	if err != nil || m.UUID != f.memberUUID {
		t.Fatal(m, err)
	}
	if n := f.count(t, `SELECT count(*) FROM login_pin_failures WHERE user_id=$1`, f.member.UserID); n != 0 {
		t.Fatal("PIN failures survived a PIN reset")
	}
	var hash sql.NullString
	var version int
	if err = f.db.QueryRow(`SELECT login_pin_hash,auth_version FROM users WHERE id=$1`, f.member.UserID).Scan(&hash, &version); err != nil || hash.Valid || version != 1 {
		t.Fatal("PIN not cleared", hash, version, err)
	}
	if n := f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`, f.member.UserID); n != 0 {
		t.Fatal("sessions survived a PIN reset")
	}
	if n := f.count(t, `SELECT count(*) FROM trusted_devices WHERE user_id=$1`, f.member.UserID); n != 0 {
		t.Fatal("devices survived a PIN reset")
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='login_pin_reset' AND resource_id=$1`, f.memberUUID); n != 1 {
		t.Fatal("not audited")
	}
	_, err = f.svc.ResetLoginPin(ctx, f.admin, f.memberUUID)
	wantStatus(t, err, http.StatusConflict)

	// Admin PINs are the owner's call only.
	if _, err = f.db.Exec(`INSERT INTO users(org_id,email,password_hash,name,role,login_pin_hash,updated_at) VALUES($1,'admin2@acme.test','unused','Second admin','admin','x',now())`, f.org); err != nil {
		t.Fatal(err)
	}
	var admin2 string
	if err = f.db.QueryRow(`SELECT uuid::text FROM users WHERE email='admin2@acme.test'`).Scan(&admin2); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ResetLoginPin(ctx, f.admin, admin2)
	wantStatus(t, err, http.StatusForbidden)
	if _, err = f.svc.ResetLoginPin(ctx, f.owner, admin2); err != nil {
		t.Fatal(err)
	}
}

// A password change ends a 24-hour PIN lock (the lock means the old password
// leaked); revoking sign-ins alone (suspend, 2FA reset) keeps it, and
// clearPinFailures (admin password set, PIN reset) ends it.
func TestPinLockClearedByPasswordChange(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO login_pin_failures(user_id) SELECT u FROM generate_series(1,20), (VALUES (1),(2)) v(u)`); err != nil {
		t.Fatal(err)
	}
	_, err := loginWithPin(f.auth, "user@example.test", "fixture-password")
	wantPinStatus(t, err, http.StatusTooManyRequests)
	sessions := NewSessionService(f.db, &config.Config{JWTSecret: "pin-fixture-secret"})
	if err = sessions.ChangePassword(ctx, 1, "fixture-password", "second-password"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM login_pin_failures WHERE user_id=1`); n != 0 {
		t.Fatalf("password change kept %d PIN failures", n)
	}
	if res, err := loginWithPin(f.auth, "user@example.test", "second-password"); err != nil || res.Token == "" {
		t.Fatalf("still PIN-locked after a password change: %+v %v", res, err)
	}
	if n := f.count(t, `SELECT count(*) FROM login_pin_failures WHERE user_id=2`); n != 20 {
		t.Fatalf("other user's failures touched: %d", n)
	}

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = revokeSignIns(ctx, tx, 2); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM login_pin_failures WHERE user_id=2`); n != 20 {
		t.Fatalf("revokeSignIns alone must keep the PIN lock, have %d failures", n)
	}
	if tx, err = f.db.BeginTx(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err = clearPinFailures(ctx, tx, 2); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM login_pin_failures WHERE user_id=2`); n != 0 {
		t.Fatalf("clearPinFailures kept %d PIN failures", n)
	}
}

// HasTrustedDevice matches only a live device of the active account with that
// address; it runs before the password check.
func TestHasTrustedDevice(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	res, err := f.auth.SetupLoginPin(ctx, f.login(t, "user@example.test", "").ChallengeToken, testLoginPin, true, DeviceMeta{})
	if err != nil || res.DeviceToken == "" {
		t.Fatalf("setup: %+v %v", res, err)
	}
	device := res.DeviceToken
	if !f.auth.HasTrustedDevice(ctx, " User@Example.test", device) {
		t.Fatal("own device not matched")
	}
	for _, c := range []struct{ email, token string }{
		{"other@example.test", device},
		{"missing@example.test", device},
		{"user@example.test", strings.Repeat("a", 64)},
		{"user@example.test", device[:63]},
		{"user@example.test", ""},
	} {
		if f.auth.HasTrustedDevice(ctx, c.email, c.token) {
			t.Fatalf("matched %q with %q", c.email, c.token)
		}
	}
	for _, q := range []string{
		`UPDATE users SET auth_version=auth_version+1 WHERE id=1`,
		`UPDATE users SET auth_version=0, status='suspended' WHERE id=1`,
		`UPDATE users SET status='active'; UPDATE trusted_devices SET expires_at=now()-interval '1 second'`,
	} {
		if _, err = f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
		if f.auth.HasTrustedDevice(ctx, "user@example.test", device) {
			t.Fatalf("matched after %s", q)
		}
	}
}

// The PIN step locks the user row before the challenge, like ResetLoginPin
// and ChangeLoginPin, so the two cannot deadlock. While another transaction
// holds the user row the PIN step waits without holding the challenge, and it
// re-checks the challenge once it gets the lock.
func TestLockPinChallengeLocksUserFirst(t *testing.T) {
	f := newPinFixture(t)
	ctx := context.Background()
	if _, err := loginWithPin(f.auth, "user@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	ch := f.login(t, "user@example.test", "").ChallengeToken
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT id FROM users WHERE id=1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.auth.VerifyLoginPin(ctx, ch, testLoginPin, false, DeviceMeta{})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("PIN step did not wait for the user row: %v", err)
	default:
	}
	// The challenge is free: the old order (challenge, then user) held it here.
	if _, err = tx.Exec(`SELECT token_hash FROM auth_challenges WHERE token_hash=$1 FOR UPDATE NOWAIT`, hashToken(ch)); err != nil {
		t.Fatalf("challenge locked while waiting for the user: %v", err)
	}
	if _, err = tx.Exec(`DELETE FROM auth_challenges WHERE user_id=1 AND kind='pin'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != errPinChallenge {
			t.Fatalf("deleted challenge accepted: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PIN step still waiting")
	}
}
