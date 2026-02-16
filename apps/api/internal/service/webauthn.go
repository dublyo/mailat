package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// WebAuthn constants
const (
	ChallengeLength = 32
)

// WebAuthnCredential represents a stored WebAuthn credential
type WebAuthnCredential struct {
	ID              int64     `json:"id"`
	UUID            string    `json:"uuid"`
	UserID          int       `json:"userId"`
	CredentialID    string    `json:"credentialId"` // Base64 encoded
	Name            string    `json:"name"`
	DeviceType      string    `json:"deviceType,omitempty"`
	Transports      []string  `json:"transports,omitempty"`
	LastUsedAt      *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

// RegistrationOptions represents WebAuthn registration options
type RegistrationOptions struct {
	Challenge        string                 `json:"challenge"`
	RP               RPEntity               `json:"rp"`
	User             UserEntity             `json:"user"`
	PubKeyCredParams []PubKeyCredParam      `json:"pubKeyCredParams"`
	Timeout          int                    `json:"timeout"`
	Attestation      string                 `json:"attestation"`
	AuthenticatorSelection AuthenticatorSelection `json:"authenticatorSelection"`
	ExcludeCredentials []CredentialDescriptor `json:"excludeCredentials,omitempty"`
}

// AuthenticationOptions represents WebAuthn authentication options
type AuthenticationOptions struct {
	Challenge          string                 `json:"challenge"`
	Timeout            int                    `json:"timeout"`
	RPID               string                 `json:"rpId"`
	AllowCredentials   []CredentialDescriptor `json:"allowCredentials,omitempty"`
	UserVerification   string                 `json:"userVerification"`
}

// RPEntity represents the Relying Party
type RPEntity struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// UserEntity represents the user for WebAuthn
type UserEntity struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// PubKeyCredParam represents allowed credential algorithms
type PubKeyCredParam struct {
	Type string `json:"type"`
	Alg  int    `json:"alg"`
}

// AuthenticatorSelection for registration
type AuthenticatorSelection struct {
	AuthenticatorAttachment string `json:"authenticatorAttachment,omitempty"`
	ResidentKey             string `json:"residentKey"`
	UserVerification        string `json:"userVerification"`
}

// CredentialDescriptor for existing credentials
type CredentialDescriptor struct {
	Type       string   `json:"type"`
	ID         string   `json:"id"`
	Transports []string `json:"transports,omitempty"`
}

// RegistrationResponse from the authenticator
type RegistrationResponse struct {
	ID                string `json:"id"`
	RawID             string `json:"rawId"`
	Type              string `json:"type"`
	AttestationObject string `json:"attestationObject"`
	ClientDataJSON    string `json:"clientDataJSON"`
	Transports        []string `json:"transports,omitempty"`
}

// AuthenticationResponse from the authenticator
type AuthenticationResponse struct {
	ID                string `json:"id"`
	RawID             string `json:"rawId"`
	Type              string `json:"type"`
	AuthenticatorData string `json:"authenticatorData"`
	ClientDataJSON    string `json:"clientDataJSON"`
	Signature         string `json:"signature"`
	UserHandle        string `json:"userHandle,omitempty"`
}

// WebAuthnService handles WebAuthn operations
type WebAuthnService struct {
	db  *sql.DB
	cfg *config.Config
	challenges map[string]challengeData // In-memory challenge storage (use Redis in production)
}

type challengeData struct {
	Challenge string
	UserID    int64
	ExpiresAt time.Time
	Type      string // "registration" or "authentication"
}

// NewWebAuthnService creates a new WebAuthn service
func NewWebAuthnService(db *sql.DB, cfg *config.Config) *WebAuthnService {
	return &WebAuthnService{
		db:         db,
		cfg:        cfg,
		challenges: make(map[string]challengeData),
	}
}

// GetRPID returns the Relying Party ID (domain)
func (s *WebAuthnService) GetRPID() string {
	return s.cfg.AppDomain
}

// GetRPName returns the Relying Party name
func (s *WebAuthnService) GetRPName() string {
	return s.cfg.AppName
}

// BeginRegistration starts the WebAuthn registration process
func (s *WebAuthnService) BeginRegistration(ctx context.Context, userID int64, email, name string) (*RegistrationOptions, error) {
	// Generate challenge
	challenge, err := generateChallenge()
	if err != nil {
		return nil, fmt.Errorf("failed to generate challenge: %w", err)
	}

	// Get existing credentials to exclude
	existingCreds, err := s.ListCredentials(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing credentials: %w", err)
	}

	excludeCredentials := make([]CredentialDescriptor, len(existingCreds))
	for i, cred := range existingCreds {
		excludeCredentials[i] = CredentialDescriptor{
			Type:       "public-key",
			ID:         cred.CredentialID,
			Transports: cred.Transports,
		}
	}

	// Create user ID (hash of actual user ID for privacy)
	userIDHash := sha256.Sum256([]byte(fmt.Sprintf("%d", userID)))
	userIDBase64 := base64.RawURLEncoding.EncodeToString(userIDHash[:])

	options := &RegistrationOptions{
		Challenge: challenge,
		RP: RPEntity{
			Name: s.GetRPName(),
			ID:   s.GetRPID(),
		},
		User: UserEntity{
			ID:          userIDBase64,
			Name:        email,
			DisplayName: name,
		},
		PubKeyCredParams: []PubKeyCredParam{
			{Type: "public-key", Alg: -7},   // ES256
			{Type: "public-key", Alg: -257}, // RS256
		},
		Timeout:     60000, // 60 seconds
		Attestation: "none",
		AuthenticatorSelection: AuthenticatorSelection{
			ResidentKey:      "preferred",
			UserVerification: "preferred",
		},
		ExcludeCredentials: excludeCredentials,
	}

	// Store challenge
	s.challenges[challenge] = challengeData{
		Challenge: challenge,
		UserID:    userID,
		ExpiresAt: time.Now().Add(5 * time.Minute),
		Type:      "registration",
	}

	return options, nil
}

// FinishRegistration completes the WebAuthn registration
func (s *WebAuthnService) FinishRegistration(ctx context.Context, userID int64, credentialName string, response *RegistrationResponse) (*WebAuthnCredential, error) {
	// Decode client data
	clientDataJSON, err := base64.RawURLEncoding.DecodeString(response.ClientDataJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid clientDataJSON: %w", err)
	}

	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(clientDataJSON, &clientData); err != nil {
		return nil, fmt.Errorf("failed to parse clientDataJSON: %w", err)
	}

	// Verify challenge
	challengeInfo, ok := s.challenges[clientData.Challenge]
	if !ok || challengeInfo.ExpiresAt.Before(time.Now()) || challengeInfo.Type != "registration" {
		return nil, fmt.Errorf("invalid or expired challenge")
	}
	if challengeInfo.UserID != userID {
		return nil, fmt.Errorf("challenge does not match user")
	}
	delete(s.challenges, clientData.Challenge)

	// Verify type
	if clientData.Type != "webauthn.create" {
		return nil, fmt.Errorf("invalid ceremony type")
	}

	// Decode attestation object (simplified - full implementation would parse CBOR)
	attestationObject, err := base64.RawURLEncoding.DecodeString(response.AttestationObject)
	if err != nil {
		return nil, fmt.Errorf("invalid attestationObject: %w", err)
	}

	// Decode credential ID
	credentialID, err := base64.RawURLEncoding.DecodeString(response.RawID)
	if err != nil {
		return nil, fmt.Errorf("invalid rawId: %w", err)
	}

	// Determine device type based on transports
	deviceType := "cross-platform"
	for _, t := range response.Transports {
		if t == "internal" {
			deviceType = "platform"
			break
		}
	}

	// Convert transports to PostgreSQL array
	transportsArray := "{}"
	if len(response.Transports) > 0 {
		transportsArray = "{" + joinStringsForPgArray(response.Transports) + "}"
	}

	// Store credential
	var cred WebAuthnCredential
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO webauthn_credentials (user_id, credential_id, public_key, transports, name, device_type)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, uuid, user_id, name, COALESCE(device_type, ''), last_used_at, created_at
	`, userID, credentialID, attestationObject, transportsArray, credentialName, deviceType,
	).Scan(&cred.ID, &cred.UUID, &cred.UserID, &cred.Name, &cred.DeviceType, &cred.LastUsedAt, &cred.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to store credential: %w", err)
	}

	cred.CredentialID = response.RawID
	cred.Transports = response.Transports

	return &cred, nil
}

// BeginAuthentication starts the WebAuthn authentication process
func (s *WebAuthnService) BeginAuthentication(ctx context.Context, userID int64) (*AuthenticationOptions, error) {
	// Generate challenge
	challenge, err := generateChallenge()
	if err != nil {
		return nil, fmt.Errorf("failed to generate challenge: %w", err)
	}

	// Get user's credentials
	credentials, err := s.ListCredentials(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials: %w", err)
	}

	if len(credentials) == 0 {
		return nil, fmt.Errorf("no security keys registered")
	}

	allowCredentials := make([]CredentialDescriptor, len(credentials))
	for i, cred := range credentials {
		allowCredentials[i] = CredentialDescriptor{
			Type:       "public-key",
			ID:         cred.CredentialID,
			Transports: cred.Transports,
		}
	}

	options := &AuthenticationOptions{
		Challenge:        challenge,
		Timeout:          60000,
		RPID:             s.GetRPID(),
		AllowCredentials: allowCredentials,
		UserVerification: "preferred",
	}

	// Store challenge
	s.challenges[challenge] = challengeData{
		Challenge: challenge,
		UserID:    userID,
		ExpiresAt: time.Now().Add(5 * time.Minute),
		Type:      "authentication",
	}

	return options, nil
}

// FinishAuthentication completes the WebAuthn authentication
func (s *WebAuthnService) FinishAuthentication(ctx context.Context, userID int64, response *AuthenticationResponse) error {
	// Decode client data
	clientDataJSON, err := base64.RawURLEncoding.DecodeString(response.ClientDataJSON)
	if err != nil {
		return fmt.Errorf("invalid clientDataJSON: %w", err)
	}

	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(clientDataJSON, &clientData); err != nil {
		return fmt.Errorf("failed to parse clientDataJSON: %w", err)
	}

	// Verify challenge
	challengeInfo, ok := s.challenges[clientData.Challenge]
	if !ok || challengeInfo.ExpiresAt.Before(time.Now()) || challengeInfo.Type != "authentication" {
		return fmt.Errorf("invalid or expired challenge")
	}
	if challengeInfo.UserID != userID {
		return fmt.Errorf("challenge does not match user")
	}
	delete(s.challenges, clientData.Challenge)

	// Verify type
	if clientData.Type != "webauthn.get" {
		return fmt.Errorf("invalid ceremony type")
	}

	// Decode credential ID
	credentialID, err := base64.RawURLEncoding.DecodeString(response.RawID)
	if err != nil {
		return fmt.Errorf("invalid rawId: %w", err)
	}

	// Verify credential exists for user
	var storedCredID int64
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM webauthn_credentials
		WHERE user_id = $1 AND credential_id = $2
	`, userID, credentialID).Scan(&storedCredID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("credential not found")
	}
	if err != nil {
		return fmt.Errorf("failed to verify credential: %w", err)
	}

	// Update last used and sign count
	_, err = s.db.ExecContext(ctx, `
		UPDATE webauthn_credentials
		SET last_used_at = NOW(), sign_count = sign_count + 1
		WHERE id = $1
	`, storedCredID)
	if err != nil {
		return fmt.Errorf("failed to update credential: %w", err)
	}

	return nil
}

// ListCredentials returns all WebAuthn credentials for a user
func (s *WebAuthnService) ListCredentials(ctx context.Context, userID int64) ([]*WebAuthnCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, credential_id, name, COALESCE(device_type, ''), transports, last_used_at, created_at
		FROM webauthn_credentials
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list credentials: %w", err)
	}
	defer rows.Close()

	var credentials []*WebAuthnCredential
	for rows.Next() {
		var cred WebAuthnCredential
		var credentialIDBytes []byte
		var transportsStr string

		if err := rows.Scan(&cred.ID, &cred.UUID, &cred.UserID, &credentialIDBytes, &cred.Name,
			&cred.DeviceType, &transportsStr, &cred.LastUsedAt, &cred.CreatedAt); err != nil {
			continue
		}

		cred.CredentialID = base64.RawURLEncoding.EncodeToString(credentialIDBytes)
		cred.Transports = parsePostgresArray(transportsStr)

		credentials = append(credentials, &cred)
	}

	return credentials, nil
}

// DeleteCredential deletes a WebAuthn credential
func (s *WebAuthnService) DeleteCredential(ctx context.Context, userID int64, credentialUUID string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM webauthn_credentials
		WHERE uuid = $1 AND user_id = $2
	`, credentialUUID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete credential: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("credential not found")
	}

	return nil
}

// RenameCredential renames a WebAuthn credential
func (s *WebAuthnService) RenameCredential(ctx context.Context, userID int64, credentialUUID, newName string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE webauthn_credentials
		SET name = $3
		WHERE uuid = $1 AND user_id = $2
	`, credentialUUID, userID, newName)
	if err != nil {
		return fmt.Errorf("failed to rename credential: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("credential not found")
	}

	return nil
}

// GetCredentialCount returns the number of credentials for a user
func (s *WebAuthnService) GetCredentialCount(ctx context.Context, userID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM webauthn_credentials WHERE user_id = $1
	`, userID).Scan(&count)
	return count, err
}

// generateChallenge creates a random challenge
func generateChallenge() (string, error) {
	b := make([]byte, ChallengeLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// joinStringsForPgArray joins strings for PostgreSQL array
func joinStringsForPgArray(strs []string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += ","
		}
		result += "\"" + s + "\""
	}
	return result
}

// parsePostgresArray parses a PostgreSQL array string
func parsePostgresArray(s string) []string {
	if s == "{}" || s == "" {
		return []string{}
	}
	// Remove braces
	s = s[1 : len(s)-1]
	if s == "" {
		return []string{}
	}
	// Simple split (doesn't handle escaped commas)
	parts := make([]string, 0)
	current := ""
	inQuotes := false
	for _, c := range s {
		if c == '"' {
			inQuotes = !inQuotes
		} else if c == ',' && !inQuotes {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
