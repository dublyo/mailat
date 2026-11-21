package model

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A password challenge is not an access token and cannot authorize API calls.
// Sign-in ends with a token, or asks for the next step: requiresTwoFactor
// (POST /auth/2fa/challenge), requiresPin (POST /auth/pin/verify) or
// requiresPinSetup (POST /auth/pin/setup), each with its challengeToken.
type LoginResponse struct {
	Token             string `json:"token,omitempty"`
	User              *User  `json:"user,omitempty"`
	RequiresTwoFactor bool   `json:"requiresTwoFactor,omitempty"`
	RequiresPin       bool   `json:"requiresPin,omitempty"`
	RequiresPinSetup  bool   `json:"requiresPinSetup,omitempty"`
	ChallengeToken    string `json:"challengeToken,omitempty"`
	// Returned once when the PIN step asked to remember this device: send it
	// as deviceToken with later logins of the same account to skip the PIN.
	DeviceToken          string     `json:"deviceToken,omitempty"`
	DeviceTokenExpiresAt *time.Time `json:"deviceTokenExpiresAt,omitempty"`
}
type AccessClaims struct {
	UserID      int64  `json:"userId"`
	OrgID       int64  `json:"orgId"`
	Purpose     string `json:"purpose"`
	SessionHash string `json:"sessionHash,omitempty"`
	jwt.RegisteredClaims
}
