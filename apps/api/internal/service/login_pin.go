package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/model"
)

// Login PIN: a 4-digit second step after the password (and after TOTP when
// it is on). Reaching it already needs the password, so its limits lock the
// PIN step only, never the account for someone who merely knows the address.
const (
	loginPinChallengeTTL   = 5 * time.Minute
	loginPinChallengeTries = 5
	loginPinShortWindow    = 15 * time.Minute
	loginPinShortLimit     = 5
	loginPinDayWindow      = 24 * time.Hour
	loginPinDayLimit       = 20
	trustedDeviceTTL       = 30 * 24 * time.Hour
	trustedDeviceCap       = 20
	trustedDeviceUAMax     = 255
)

// ErrInvalidCredentials is the one answer for a wrong password, an unknown
// address and an account without a password. Only this error counts toward
// the failed-login limits.
var ErrInvalidCredentials = errors.New("invalid email or password")

// PinError is a PIN step refusal with its HTTP status. AttemptsLeft is set
// after a wrong PIN; RetryAfter when the PIN step is locked (429).
type PinError struct {
	Status       int
	Message      string
	AttemptsLeft *int
	RetryAfter   time.Duration
}

func (e *PinError) Error() string { return e.Message }

func pinError(status int, message string) *PinError {
	return &PinError{Status: status, Message: message}
}

var errPinChallenge = pinError(http.StatusUnauthorized, "This sign-in step expired. Sign in again.")

// DeviceMeta describes the browser that asked to be remembered.
type DeviceMeta struct {
	UserAgent string
	IP        string
}

// LoginPinStatus is the signed-in user's PIN state.
type LoginPinStatus struct {
	Set   bool       `json:"set"`
	SetAt *time.Time `json:"setAt,omitempty"`
}

// TrustedDevice is a browser that skips the PIN step until ExpiresAt.
type TrustedDevice struct {
	UUID       string    `json:"uuid"`
	UserAgent  string    `json:"userAgent"`
	IP         string    `json:"ip,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ValidateLoginPIN accepts exactly four ASCII digits that are not all the
// same (1111) and not a run up or down (1234, 4321).
func ValidateLoginPIN(pin string) error {
	if !fourDigits(pin) {
		return errors.New("the PIN must be exactly 4 digits")
	}
	same, up, down := true, true, true
	for i := 1; i < 4; i++ {
		d := int(pin[i]) - int(pin[i-1])
		same = same && d == 0
		up = up && d == 1
		down = down && d == -1
	}
	if same || up || down {
		return errors.New("this PIN is too easy to guess; avoid repeated digits and runs like 1234")
	}
	return nil
}

func fourDigits(pin string) bool {
	if len(pin) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return false
		}
	}
	return true
}

func newSecretToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// matchTrustedDevice returns the id of the user's live trusted device for
// token, or 0. Called only after the password was correct.
func (s *AuthService) matchTrustedDevice(ctx context.Context, userID, version int64, token string) int64 {
	if len(token) != 64 {
		return 0
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM trusted_devices WHERE token_hash=$1 AND user_id=$2 AND auth_version=$3 AND expires_at>now()`,
		hashToken(token), userID, version).Scan(&id); err != nil {
		return 0
	}
	return id
}

// pinGate is the last step of every sign-in path: password, TOTP, OAuth,
// invite and registration. A live trusted device of this user signs in at
// once; otherwise a PIN challenge (or a PIN setup challenge when none is set)
// is returned and no session exists yet.
func (s *AuthService) pinGate(ctx context.Context, user *model.User, version int64, factorVerified bool, deviceID int64) (*model.LoginResponse, error) {
	if deviceID > 0 {
		var used bool
		err := s.db.QueryRowContext(ctx, `UPDATE trusted_devices SET last_used_at=now() WHERE id=$1 AND user_id=$2 AND auth_version=$3 AND expires_at>now() RETURNING true`,
			deviceID, user.ID, version).Scan(&used)
		if err == nil && used {
			return s.startSession(ctx, user, version, factorVerified)
		}
	}
	var hasPin bool
	if err := s.db.QueryRowContext(ctx, `SELECT login_pin_hash IS NOT NULL FROM users WHERE id=$1 AND status='active' AND auth_version=$2`, user.ID, version).Scan(&hasPin); err != nil {
		return nil, fmt.Errorf("credentials changed; sign in again")
	}
	challenge, err := newSecretToken()
	if err != nil {
		return nil, err
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO auth_challenges(token_hash,user_id,expires_at,auth_version,kind,factor_verified) VALUES($1,$2,now()+make_interval(secs => $3),$4,'pin',$5)`,
		hashToken(challenge), user.ID, loginPinChallengeTTL.Seconds(), version, factorVerified); err != nil {
		return nil, err
	}
	// Bounded retention of consumed/expired challenges for this account.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM auth_challenges WHERE user_id=$1 AND created_at<now()-interval '1 day'`, user.ID); err != nil {
		return nil, err
	}
	return &model.LoginResponse{RequiresPin: hasPin, RequiresPinSetup: !hasPin, ChallengeToken: challenge}, nil
}

// startSession issues the session once every step passed.
func (s *AuthService) startSession(ctx context.Context, user *model.User, version int64, factorVerified bool) (*model.LoginResponse, error) {
	token, err := s.issueSession(ctx, user, version, factorVerified)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, user.ID)
	return &model.LoginResponse{Token: token, User: user}, nil
}

type pinChallenge struct {
	userID, orgID, version int64
	factorVerified         bool
	attempts               int
	pinHash                sql.NullString
}

// lockPinChallenge locks a live PIN challenge and its user, in that order.
func lockPinChallenge(ctx context.Context, tx *sql.Tx, challenge string) (*pinChallenge, error) {
	if len(challenge) != 64 {
		return nil, errPinChallenge
	}
	var c pinChallenge
	err := tx.QueryRowContext(ctx, `SELECT user_id,auth_version,factor_verified,attempts FROM auth_challenges
		WHERE token_hash=$1 AND kind='pin' AND expires_at>now() AND consumed_at IS NULL AND attempts<$2 FOR UPDATE`,
		hashToken(challenge), loginPinChallengeTries).Scan(&c.userID, &c.version, &c.factorVerified, &c.attempts)
	if err == sql.ErrNoRows {
		return nil, errPinChallenge
	}
	if err != nil {
		return nil, err
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT org_id,auth_version,login_pin_hash FROM users WHERE id=$1 AND status='active' FOR UPDATE`, c.userID).
		Scan(&c.orgID, &current, &c.pinHash)
	if err == sql.ErrNoRows || (err == nil && current != c.version) {
		return nil, errPinChallenge
	}
	return &c, err
}

// pinFailures counts the user's wrong PINs in the short and daily windows and
// how long each window stays full once it is at its limit.
func pinFailures(ctx context.Context, tx *sql.Tx, userID int64) (short, day int, shortWait, dayWait time.Duration, err error) {
	var shortSecs, daySecs float64
	err = tx.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE created_at>now()-make_interval(secs => $2)),
		count(*),
		COALESCE(extract(epoch FROM (SELECT created_at FROM login_pin_failures WHERE user_id=$1 AND created_at>now()-make_interval(secs => $2)
			ORDER BY created_at DESC OFFSET $4 LIMIT 1) + make_interval(secs => $2) - now()),0),
		COALESCE(extract(epoch FROM (SELECT created_at FROM login_pin_failures WHERE user_id=$1 AND created_at>now()-make_interval(secs => $3)
			ORDER BY created_at DESC OFFSET $5 LIMIT 1) + make_interval(secs => $3) - now()),0)
		FROM login_pin_failures WHERE user_id=$1 AND created_at>now()-make_interval(secs => $3)`,
		userID, loginPinShortWindow.Seconds(), loginPinDayWindow.Seconds(), loginPinShortLimit-1, loginPinDayLimit-1).
		Scan(&short, &day, &shortSecs, &daySecs)
	return short, day, time.Duration(shortSecs * float64(time.Second)), time.Duration(daySecs * float64(time.Second)), err
}

func pinLocked(short, day int, shortWait, dayWait time.Duration) *PinError {
	wait := time.Duration(0)
	if short >= loginPinShortLimit {
		wait = shortWait
	}
	if day >= loginPinDayLimit && dayWait > wait {
		wait = dayWait
	}
	if wait < time.Second {
		wait = time.Second
	}
	return &PinError{Status: http.StatusTooManyRequests, Message: "Too many wrong PIN attempts. Try again later.", RetryAfter: wait}
}

// VerifyLoginPin completes a PIN challenge. A wrong PIN costs one of the
// challenge's 5 attempts and counts toward the user's 5 per 15 minutes and
// 20 per 24 hours; over either limit the PIN step is refused (429). The 20th
// wrong PIN in a day is written to the audit log.
func (s *AuthService) VerifyLoginPin(ctx context.Context, challenge, pin string, trust bool, meta DeviceMeta) (*model.LoginResponse, error) {
	if !fourDigits(pin) {
		return nil, pinError(http.StatusBadRequest, "Enter your 4-digit PIN")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := lockPinChallenge(ctx, tx, challenge)
	if err != nil {
		return nil, err
	}
	if !c.pinHash.Valid {
		return nil, errPinChallenge
	}
	short, day, shortWait, dayWait, err := pinFailures(ctx, tx, c.userID)
	if err != nil {
		return nil, err
	}
	if short >= loginPinShortLimit || day >= loginPinDayLimit {
		return nil, pinLocked(short, day, shortWait, dayWait)
	}
	if bcrypt.CompareHashAndPassword([]byte(c.pinHash.String), []byte(pin)) != nil {
		if _, err = tx.ExecContext(ctx, `INSERT INTO login_pin_failures(user_id,ip) VALUES($1,NULLIF($2,''))`, c.userID, clipUTF8(meta.IP, 45)); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE auth_challenges SET attempts=attempts+1 WHERE token_hash=$1`, hashToken(challenge)); err != nil {
			return nil, err
		}
		short, day, c.attempts = short+1, day+1, c.attempts+1
		if day == loginPinDayLimit {
			// The password was right but the PIN wrong 20 times in a day: the
			// password is probably known to someone else.
			if err = auditTx(ctx, tx, OrgActor{UserID: c.userID, OrgID: c.orgID, IP: meta.IP}, "login_pin_locked", "user", fmt.Sprint(c.userID),
				"Correct password but 20 wrong login PINs in 24 hours; the PIN step is locked. Change the password.", nil); err != nil {
				return nil, err
			}
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		if short >= loginPinShortLimit || day >= loginPinDayLimit {
			_, _, shortWait, dayWait, _ = s.pinFailuresNow(ctx, c.userID)
			return nil, pinLocked(short, day, shortWait, dayWait)
		}
		left := min(loginPinChallengeTries-c.attempts, loginPinShortLimit-short, loginPinDayLimit-day)
		if left <= 0 {
			return nil, &PinError{Status: http.StatusUnauthorized, Message: "Wrong PIN. Sign in again to try once more.", AttemptsLeft: &left}
		}
		noun := "attempts"
		if left == 1 {
			noun = "attempt"
		}
		return nil, &PinError{Status: http.StatusUnauthorized, Message: fmt.Sprintf("Wrong PIN. %d %s left.", left, noun), AttemptsLeft: &left}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM login_pin_failures WHERE user_id=$1`, c.userID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE token_hash=$1`, hashToken(challenge)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.finishPinStep(ctx, c, trust, meta)
}

// pinFailuresNow reads the windows outside a transaction (for Retry-After
// after the failure committed).
func (s *AuthService) pinFailuresNow(ctx context.Context, userID int64) (int, int, time.Duration, time.Duration, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer tx.Rollback()
	return pinFailures(ctx, tx, userID)
}

// SetupLoginPin sets the first PIN of a user who has none, from a PIN setup
// challenge, and signs in.
func (s *AuthService) SetupLoginPin(ctx context.Context, challenge, pin string, trust bool, meta DeviceMeta) (*model.LoginResponse, error) {
	if err := ValidateLoginPIN(pin); err != nil {
		return nil, pinError(http.StatusBadRequest, err.Error())
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash PIN")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := lockPinChallenge(ctx, tx, challenge)
	if err != nil {
		return nil, err
	}
	if c.pinHash.Valid {
		return nil, pinError(http.StatusConflict, "A PIN is already set for this account. Sign in again.")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET login_pin_hash=$2,login_pin_set_at=now(),updated_at=now() WHERE id=$1`, c.userID, string(hash)); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE token_hash=$1`, hashToken(challenge)); err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, OrgActor{UserID: c.userID, OrgID: c.orgID, IP: meta.IP}, "login_pin_set", "user", fmt.Sprint(c.userID), "Set a login PIN", nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.finishPinStep(ctx, c, trust, meta)
}

// finishPinStep remembers the device when asked, then issues the session. A
// failure to remember the device does not fail the sign-in.
func (s *AuthService) finishPinStep(ctx context.Context, c *pinChallenge, trust bool, meta DeviceMeta) (*model.LoginResponse, error) {
	user, err := s.GetUserByID(ctx, c.userID)
	if err != nil {
		return nil, errPinChallenge
	}
	var device string
	var expires time.Time
	if trust {
		if device, expires, err = s.createTrustedDevice(ctx, c.userID, c.version, meta); err != nil {
			log.Printf("trusted device not stored for user %d: %v", c.userID, err)
			device = ""
		}
	}
	res, err := s.startSession(ctx, user, c.version, c.factorVerified)
	if err != nil {
		if device != "" {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM trusted_devices WHERE token_hash=$1`, hashToken(device))
		}
		return nil, err
	}
	if device != "" {
		res.DeviceToken, res.DeviceTokenExpiresAt = device, &expires
	}
	return res, nil
}

// createTrustedDevice stores a new 30-day device (fixed expiry) and keeps at
// most 20 live devices per user, dropping the oldest.
func (s *AuthService) createTrustedDevice(ctx context.Context, userID, version int64, meta DeviceMeta) (string, time.Time, error) {
	token, err := newSecretToken()
	if err != nil {
		return "", time.Time{}, err
	}
	ua := strings.TrimSpace(strings.ToValidUTF8(meta.UserAgent, ""))
	if utf8.RuneCountInString(ua) > trustedDeviceUAMax {
		ua = string([]rune(ua)[:trustedDeviceUAMax])
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	var expires time.Time
	if err = tx.QueryRowContext(ctx, `INSERT INTO trusted_devices(user_id,token_hash,auth_version,user_agent,ip,expires_at)
		VALUES($1,$2,$3,$4,NULLIF($5,''),now()+make_interval(secs => $6)) RETURNING expires_at`,
		userID, hashToken(token), version, ua, clipUTF8(meta.IP, 45), trustedDeviceTTL.Seconds()).Scan(&expires); err != nil {
		return "", time.Time{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM trusted_devices WHERE user_id=$1 AND (expires_at<=now() OR auth_version<>$2)`, userID, version); err != nil {
		return "", time.Time{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM trusted_devices WHERE id IN (SELECT id FROM trusted_devices WHERE user_id=$1 ORDER BY created_at DESC, id DESC OFFSET $2)`, userID, trustedDeviceCap); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, tx.Commit()
}

// LoginPinStatus reports whether the user has a PIN and since when.
func (s *AuthService) LoginPinStatus(ctx context.Context, userID int64) (*LoginPinStatus, error) {
	var st LoginPinStatus
	var setAt sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT login_pin_hash IS NOT NULL,login_pin_set_at FROM users WHERE id=$1 AND status='active'`, userID).Scan(&st.Set, &setAt); err != nil {
		return nil, fmt.Errorf("user not found")
	}
	if setAt.Valid {
		st.SetAt = &setAt.Time
	}
	return &st, nil
}

// ChangeLoginPin sets a new PIN after the current password (the current PIN
// is not needed, so a signed-in user who forgot it can recover). Remembered
// devices and pending PIN challenges end; sessions stay.
func (s *AuthService) ChangeLoginPin(ctx context.Context, userID int64, currentPassword, pin string) error {
	if err := ValidateLoginPIN(pin); err != nil {
		return pinError(http.StatusBadRequest, err.Error())
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var passwordHash string
	if err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=$1 AND status='active' FOR UPDATE`, userID).Scan(&passwordHash); err != nil {
		return pinError(http.StatusNotFound, "user not found")
	}
	if passwordHash == "" || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(currentPassword)) != nil {
		return pinError(http.StatusBadRequest, "current password is incorrect")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash PIN")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET login_pin_hash=$2,login_pin_set_at=now(),updated_at=now() WHERE id=$1`, userID, string(hash)); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM trusted_devices WHERE user_id=$1`,
		`DELETE FROM auth_challenges WHERE user_id=$1 AND kind='pin'`,
		`DELETE FROM login_pin_failures WHERE user_id=$1`,
	} {
		if _, err = tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTrustedDevices lists the user's live remembered devices, newest first.
func (s *AuthService) ListTrustedDevices(ctx context.Context, userID int64) ([]*TrustedDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.uuid::text,d.user_agent,COALESCE(d.ip,''),d.created_at,d.last_used_at,d.expires_at
		FROM trusted_devices d JOIN users u ON u.id=d.user_id AND u.auth_version=d.auth_version
		WHERE d.user_id=$1 AND d.expires_at>now() ORDER BY d.created_at DESC, d.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TrustedDevice{}
	for rows.Next() {
		var d TrustedDevice
		if err = rows.Scan(&d.UUID, &d.UserAgent, &d.IP, &d.CreatedAt, &d.LastUsedAt, &d.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// ForgetTrustedDevice removes one of the user's remembered devices.
func (s *AuthService) ForgetTrustedDevice(ctx context.Context, userID int64, deviceUUID string) error {
	if !validUUID(deviceUUID) {
		return pinError(http.StatusNotFound, "Device not found")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM trusted_devices WHERE uuid=$1 AND user_id=$2`, deviceUUID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pinError(http.StatusNotFound, "Device not found")
	}
	return nil
}

// ForgetAllTrustedDevices removes every remembered device of the user, so
// each asks for the PIN at the next sign-in.
func (s *AuthService) ForgetAllTrustedDevices(ctx context.Context, userID int64) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM trusted_devices WHERE user_id=$1`, userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CleanupLoginState drops expired trusted devices and day-old PIN failures.
func (s *AuthService) CleanupLoginState(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM trusted_devices WHERE expires_at<now()`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_pin_failures WHERE created_at<now()-make_interval(secs => $1)`, loginPinDayWindow.Seconds())
	return err
}

// RunCleanup runs CleanupLoginState hourly until ctx is cancelled.
func (s *AuthService) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.CleanupLoginState(ctx); err != nil && ctx.Err() == nil {
				log.Printf("login state cleanup failed: %v", err)
			}
		}
	}
}
