package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
)

// checkPasswordHash compares a plaintext password with a bcrypt hash
func checkPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// TwoFactorSetup holds the setup information for 2FA
type TwoFactorSetup struct {
	Secret        string `json:"secret"`
	QRCodeURL     string `json:"qrCodeUrl"`
	QRCodeDataURL string `json:"qrCodeDataUrl"`
	ManualCode    string `json:"manualCode"`
}

// TwoFactorService handles two-factor authentication operations
type TwoFactorService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewTwoFactorService creates a new 2FA service
func NewTwoFactorService(db *sql.DB, cfg *config.Config) *TwoFactorService {
	return &TwoFactorService{db: db, cfg: cfg}
}

// GenerateSetup generates a new TOTP secret and QR code URL for a user
func (s *TwoFactorService) GenerateSetup(ctx context.Context, userID int64, email string) (*TwoFactorSetup, error) {
	// Generate a random 20-byte secret (160 bits as recommended by RFC 6238)
	secretBytes := make([]byte, 20)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("failed to generate secret")
	}

	// Encode as base32 (standard TOTP encoding)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)

	// Store the pending secret (not yet verified)
	result, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET totp_secret = $1, totp_last_step=NULL, updated_at = NOW()
		WHERE id = $2 AND NOT totp_enabled AND status='active'
	`, secret, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to store secret")
	}

	if rows, _ := result.RowsAffected(); rows != 1 {
		return nil, fmt.Errorf("disable existing two-factor authentication before setting it up again")
	}

	qrURL := totpURI(email, secret)
	png, err := qrcode.Encode(qrURL, qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("failed to render QR code")
	}

	return &TwoFactorSetup{
		Secret:        secret,
		QRCodeURL:     qrURL,
		QRCodeDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		// Format manual code for easier reading (groups of 4)
		ManualCode: formatSecretForDisplay(secret),
	}, nil
}

// totpURI builds the otpauth:// URI authenticator apps scan. The label is
// path-escaped as one segment so unusual addresses cannot alter the URI.
func totpURI(email, secret string) string {
	q := url.Values{"secret": {secret}, "issuer": {"Mailat"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape("Mailat:"+email) + "?" + q.Encode()
}

// VerifyAndEnable verifies a TOTP code and enables 2FA for the user. In the
// same transaction it bumps auth_version (pending login challenges become
// invalid) and revokes every session except keepSessionHash, so a stolen
// session cannot outlive the new factor. An empty hash revokes all sessions.
func (s *TwoFactorService) VerifyAndEnable(ctx context.Context, userID int64, code, keepSessionHash string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enable 2FA")
	}
	defer tx.Rollback()

	var secret sql.NullString
	var enabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT totp_secret, totp_enabled FROM users WHERE id = $1 AND status='active' FOR UPDATE
	`, userID).Scan(&secret, &enabled)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}

	if !secret.Valid || secret.String == "" {
		return nil, fmt.Errorf("2FA setup not started. Please start setup first.")
	}

	if enabled {
		return nil, fmt.Errorf("2FA is already enabled")
	}

	step, ok := matchTOTPStep(secret.String, code, sql.NullInt64{})
	if !ok {
		return nil, fmt.Errorf("invalid verification code")
	}

	backupCodes, hashedCodes, err := s.generateBackupCodes()
	if err != nil {
		return nil, fmt.Errorf("failed to generate backup codes")
	}

	// The enabling step is recorded so the same code cannot complete a login.
	result, err := tx.ExecContext(ctx, `
		UPDATE users
		SET totp_enabled = true, totp_verified_at = NOW(), totp_last_step = $4, backup_codes = $2,
		    auth_version = auth_version + 1, updated_at = NOW()
		WHERE id = $1 AND NOT totp_enabled AND totp_secret=$3 AND status='active'
	`, userID, formatPgArray(hashedCodes), secret.String, step)
	if err != nil {
		return nil, fmt.Errorf("failed to enable 2FA")
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return nil, fmt.Errorf("two-factor setup changed; start again")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE user_sessions SET active=false,revoked_at=now() WHERE user_id=$1 AND active AND token_hash<>$2`, userID, keepSessionHash); err != nil {
		return nil, fmt.Errorf("failed to enable 2FA")
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to enable 2FA")
	}
	return backupCodes, nil
}

// Verify verifies a TOTP code for a user
func (s *TwoFactorService) Verify(ctx context.Context, userID int64, code string) (bool, error) {
	var secret sql.NullString
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT totp_secret, totp_enabled FROM users WHERE id = $1
	`, userID).Scan(&secret, &enabled)
	if err != nil {
		return false, fmt.Errorf("user not found")
	}

	if !enabled || !secret.Valid {
		return false, fmt.Errorf("2FA is not enabled")
	}

	return s.verifyTOTP(secret.String, code), nil
}

// VerifyBackupCode verifies and consumes a backup code
func (s *TwoFactorService) VerifyBackupCode(ctx context.Context, userID int64, code string) (bool, error) {
	// Atomic consumption prevents simultaneous uses of a recovery code.
	result, err := s.db.ExecContext(ctx, `UPDATE users SET backup_codes=array_remove(backup_codes,$2),updated_at=now() WHERE id=$1 AND totp_enabled AND $2=ANY(backup_codes)`, userID, hashBackupCode(code))
	if err != nil {
		return false, fmt.Errorf("unable to verify recovery code")
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// errInvalidSecondFactor is returned for a wrong, malformed or replayed code.
var errInvalidSecondFactor = fmt.Errorf("invalid verification code")

// lockForFactorChange loads the factor state under a row lock and checks the
// password and code. TOTP steps are replay-checked against totp_last_step,
// the same rule as the login challenge, and the accepted step is stored.
// allowBackup lets an unused backup code stand in for the TOTP code.
func lockForFactorChange(ctx context.Context, tx *sql.Tx, userID int64, password, code string, allowBackup bool) error {
	var passwordHash string
	var secret sql.NullString
	var enabled bool
	var last sql.NullInt64
	var backup []string
	err := tx.QueryRowContext(ctx, `
		SELECT password_hash, totp_secret, totp_enabled, totp_last_step, backup_codes
		FROM users WHERE id = $1 AND status='active' FOR UPDATE
	`, userID).Scan(&passwordHash, &secret, &enabled, &last, pgStrArr(&backup))
	if err != nil {
		return fmt.Errorf("user not found")
	}
	if !enabled {
		return fmt.Errorf("2FA is not enabled")
	}
	if !checkPasswordHash(password, passwordHash) {
		return fmt.Errorf("invalid password")
	}
	if step, ok := matchTOTPStep(secret.String, code, last); ok {
		_, err = tx.ExecContext(ctx, `UPDATE users SET totp_last_step=$2 WHERE id=$1`, userID, step)
		return err
	}
	if allowBackup {
		hashed := hashBackupCode(code)
		for _, stored := range backup {
			if hmac.Equal([]byte(stored), []byte(hashed)) {
				_, err = tx.ExecContext(ctx, `UPDATE users SET backup_codes=array_remove(backup_codes,$2) WHERE id=$1`, userID, hashed)
				return err
			}
		}
	}
	return errInvalidSecondFactor
}

// Disable disables 2FA for a user after a password and an unused TOTP step
// (or backup code).
func (s *TwoFactorService) Disable(ctx context.Context, userID int64, password, code string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to disable 2FA")
	}
	defer tx.Rollback()
	if err = lockForFactorChange(ctx, tx, userID, password, code, true); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE users
		SET totp_enabled = false, totp_secret = NULL, totp_verified_at = NULL, totp_last_step = NULL,
		    backup_codes = '{}', updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("failed to disable 2FA")
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to disable 2FA")
	}
	return nil
}

// RegenerateBackupCodes replaces the backup codes after a password and an
// unused TOTP step.
func (s *TwoFactorService) RegenerateBackupCodes(ctx context.Context, userID int64, password, code string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to store backup codes")
	}
	defer tx.Rollback()
	if err = lockForFactorChange(ctx, tx, userID, password, code, false); err != nil {
		return nil, err
	}
	backupCodes, hashedCodes, err := s.generateBackupCodes()
	if err != nil {
		return nil, fmt.Errorf("failed to generate backup codes")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET backup_codes = $2, updated_at = NOW() WHERE id = $1`, userID, formatPgArray(hashedCodes)); err != nil {
		return nil, fmt.Errorf("failed to store backup codes")
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to store backup codes")
	}
	return backupCodes, nil
}

// GetStatus returns the 2FA status for a user
func (s *TwoFactorService) GetStatus(ctx context.Context, userID int64) (bool, int, error) {
	var enabled bool
	var backupCodesArray []string
	err := s.db.QueryRowContext(ctx, `
		SELECT totp_enabled, backup_codes FROM users WHERE id = $1
	`, userID).Scan(&enabled, pgStrArr(&backupCodesArray))
	if err != nil {
		return false, 0, fmt.Errorf("user not found")
	}

	return enabled, len(backupCodesArray), nil
}

// verifyTOTP verifies a TOTP code against a secret
func (s *TwoFactorService) verifyTOTP(secret, code string) bool {
	if len(code) != 6 || secret == "" {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}

	// Allow for clock drift: check current time and one period before/after
	currentTime := time.Now().Unix()
	period := int64(30) // Standard TOTP period

	for _, offset := range []int64{-1, 0, 1} {
		counter := (currentTime / period) + offset
		expectedCode := generateTOTP(secret, counter)
		if hmac.Equal([]byte(expectedCode), []byte(code)) {
			return true
		}
	}

	return false
}

// matchTOTPStep finds the time step (±1 for clock drift) that produced code.
// Steps at or before last were already used and are rejected (replay).
func matchTOTPStep(secret, code string, last sql.NullInt64) (int64, bool) {
	if len(code) != 6 || secret == "" {
		return 0, false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	now := time.Now().Unix() / 30
	for _, offset := range []int64{-1, 0, 1} {
		step := now + offset
		if last.Valid && step <= last.Int64 {
			continue
		}
		if hmac.Equal([]byte(generateTOTP(secret, step)), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

// generateTOTP generates a TOTP code for a given secret and counter
func generateTOTP(secret string, counter int64) string {
	// Decode base32 secret
	secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}

	// Convert counter to bytes (big-endian)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))

	// Calculate HMAC-SHA1
	h := hmac.New(sha1.New, secretBytes)
	h.Write(buf)
	hash := h.Sum(nil)

	// Dynamic truncation
	offset := hash[len(hash)-1] & 0x0f
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff

	// Return 6-digit code
	return fmt.Sprintf("%06d", code%1000000)
}

// generateBackupCodes generates 10 backup codes
func (s *TwoFactorService) generateBackupCodes() ([]string, []string, error) {
	codes := make([]string, 10)
	hashedCodes := make([]string, 10)

	for i := 0; i < 10; i++ {
		// Generate 8 random bytes
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}

		// Format as readable code (e.g., "ABCD-EFGH-1234")
		code := fmt.Sprintf("%s-%s-%s",
			strings.ToUpper(hex.EncodeToString(b[:3])),
			strings.ToUpper(hex.EncodeToString(b[3:6])),
			strings.ToUpper(hex.EncodeToString(b[6:])),
		)

		codes[i] = code
		hashedCodes[i] = hashBackupCode(code)
	}

	return codes, hashedCodes, nil
}

// hashBackupCode hashes a backup code for storage
func hashBackupCode(code string) string {
	// Normalize the code (remove dashes, uppercase)
	normalized := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])
}

// formatSecretForDisplay formats a base32 secret for easy manual entry
func formatSecretForDisplay(secret string) string {
	var parts []string
	for i := 0; i < len(secret); i += 4 {
		end := i + 4
		if end > len(secret) {
			end = len(secret)
		}
		parts = append(parts, secret[i:end])
	}
	return strings.Join(parts, " ")
}

// formatPgArray formats a string slice as a PostgreSQL array literal
func formatPgArray(arr []string) string {
	if len(arr) == 0 {
		return "{}"
	}
	escaped := make([]string, len(arr))
	for i, s := range arr {
		escaped[i] = strings.ReplaceAll(s, "\"", "\\\"")
	}
	return "{\"" + strings.Join(escaped, "\",\"") + "\"}"
}

// scanPgStringArray scans a PostgreSQL string array
type scanPgStringArray struct {
	dest *[]string
}

func (a *scanPgStringArray) Scan(src interface{}) error {
	if src == nil {
		*a.dest = nil
		return nil
	}

	var s string
	switch v := src.(type) {
	case []byte:
		s = string(v)
	case string:
		s = v
	default:
		return fmt.Errorf("unsupported type for scanPgStringArray: %T", src)
	}

	if s == "{}" || s == "" {
		*a.dest = []string{}
		return nil
	}

	s = strings.Trim(s, "{}")
	parts := strings.Split(s, ",")

	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "\"")
		if p != "" {
			result = append(result, p)
		}
	}

	*a.dest = result
	return nil
}

// pgStrArr returns a scanner for PostgreSQL string arrays
func pgStrArr(dest *[]string) *scanPgStringArray {
	return &scanPgStringArray{dest: dest}
}
