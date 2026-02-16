package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

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
	Secret     string `json:"secret"`
	QRCodeURL  string `json:"qrCodeUrl"`
	ManualCode string `json:"manualCode"`
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
		return nil, fmt.Errorf("failed to generate secret: %w", err)
	}

	// Encode as base32 (standard TOTP encoding)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)

	// Store the pending secret (not yet verified)
	_, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET totp_secret = $1, totp_enabled = false, updated_at = NOW()
		WHERE id = $2
	`, secret, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to store secret: %w", err)
	}

	// Generate QR code URL (otpauth format)
	issuer := "Mailat"
	qrURL := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		issuer, email, secret, issuer)

	// Format manual code for easier reading (groups of 4)
	manualCode := formatSecretForDisplay(secret)

	return &TwoFactorSetup{
		Secret:     secret,
		QRCodeURL:  qrURL,
		ManualCode: manualCode,
	}, nil
}

// VerifyAndEnable verifies a TOTP code and enables 2FA for the user
func (s *TwoFactorService) VerifyAndEnable(ctx context.Context, userID int64, code string) ([]string, error) {
	// Get the pending secret
	var secret sql.NullString
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT totp_secret, totp_enabled FROM users WHERE id = $1
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

	// Verify the code
	if !s.verifyTOTP(secret.String, code) {
		return nil, fmt.Errorf("invalid verification code")
	}

	// Generate backup codes
	backupCodes, hashedCodes, err := s.generateBackupCodes()
	if err != nil {
		return nil, fmt.Errorf("failed to generate backup codes: %w", err)
	}

	// Enable 2FA and store backup codes
	_, err = s.db.ExecContext(ctx, `
		UPDATE users
		SET totp_enabled = true, totp_verified_at = NOW(), backup_codes = $2, updated_at = NOW()
		WHERE id = $1
	`, userID, formatPgArray(hashedCodes))
	if err != nil {
		return nil, fmt.Errorf("failed to enable 2FA: %w", err)
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
	// Get stored backup codes
	var backupCodesArray []string
	err := s.db.QueryRowContext(ctx, `
		SELECT backup_codes FROM users WHERE id = $1
	`, userID).Scan(pgStrArr(&backupCodesArray))
	if err != nil {
		return false, fmt.Errorf("user not found")
	}

	// Hash the provided code
	hashedCode := hashBackupCode(code)

	// Find and remove the matching code
	found := false
	newCodes := make([]string, 0, len(backupCodesArray))
	for _, storedHash := range backupCodesArray {
		if storedHash == hashedCode && !found {
			found = true
			continue // Remove this code
		}
		newCodes = append(newCodes, storedHash)
	}

	if !found {
		return false, nil
	}

	// Update backup codes (remove the used one)
	_, err = s.db.ExecContext(ctx, `
		UPDATE users
		SET backup_codes = $2, updated_at = NOW()
		WHERE id = $1
	`, userID, formatPgArray(newCodes))
	if err != nil {
		return false, fmt.Errorf("failed to update backup codes: %w", err)
	}

	return true, nil
}

// Disable disables 2FA for a user
func (s *TwoFactorService) Disable(ctx context.Context, userID int64, password, code string) error {
	// Verify password first
	var passwordHash string
	var secret sql.NullString
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT password_hash, totp_secret, totp_enabled FROM users WHERE id = $1
	`, userID).Scan(&passwordHash, &secret, &enabled)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	if !enabled {
		return fmt.Errorf("2FA is not enabled")
	}

	// Verify password
	if !checkPasswordHash(password, passwordHash) {
		return fmt.Errorf("invalid password")
	}

	// Verify TOTP code
	if !s.verifyTOTP(secret.String, code) {
		// Try backup code
		valid, _ := s.VerifyBackupCode(ctx, userID, code)
		if !valid {
			return fmt.Errorf("invalid verification code")
		}
	}

	// Disable 2FA
	_, err = s.db.ExecContext(ctx, `
		UPDATE users
		SET totp_enabled = false, totp_secret = NULL, totp_verified_at = NULL, backup_codes = '{}', updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("failed to disable 2FA: %w", err)
	}

	return nil
}

// RegenerateBackupCodes generates new backup codes for a user
func (s *TwoFactorService) RegenerateBackupCodes(ctx context.Context, userID int64, password, code string) ([]string, error) {
	// Verify password
	var passwordHash string
	var secret sql.NullString
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT password_hash, totp_secret, totp_enabled FROM users WHERE id = $1
	`, userID).Scan(&passwordHash, &secret, &enabled)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}

	if !enabled {
		return nil, fmt.Errorf("2FA is not enabled")
	}

	// Verify password
	if !checkPasswordHash(password, passwordHash) {
		return nil, fmt.Errorf("invalid password")
	}

	// Verify TOTP code
	if !s.verifyTOTP(secret.String, code) {
		return nil, fmt.Errorf("invalid verification code")
	}

	// Generate new backup codes
	backupCodes, hashedCodes, err := s.generateBackupCodes()
	if err != nil {
		return nil, fmt.Errorf("failed to generate backup codes: %w", err)
	}

	// Store new backup codes
	_, err = s.db.ExecContext(ctx, `
		UPDATE users
		SET backup_codes = $2, updated_at = NOW()
		WHERE id = $1
	`, userID, formatPgArray(hashedCodes))
	if err != nil {
		return nil, fmt.Errorf("failed to store backup codes: %w", err)
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
	// Allow for clock drift: check current time and one period before/after
	currentTime := time.Now().Unix()
	period := int64(30) // Standard TOTP period

	for _, offset := range []int64{-1, 0, 1} {
		counter := (currentTime / period) + offset
		expectedCode := generateTOTP(secret, counter)
		if expectedCode == code {
			return true
		}
	}

	return false
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
