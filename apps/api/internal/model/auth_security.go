package model

import "github.com/golang-jwt/jwt/v5"

// A password challenge is not an access token and cannot authorize API calls.
type LoginResponse struct {
	Token             string `json:"token,omitempty"`
	User              *User  `json:"user,omitempty"`
	RequiresTwoFactor bool   `json:"requiresTwoFactor,omitempty"`
	ChallengeToken    string `json:"challengeToken,omitempty"`
}
type AccessClaims struct {
	UserID      int64  `json:"userId"`
	OrgID       int64  `json:"orgId"`
	Purpose     string `json:"purpose"`
	SessionHash string `json:"sessionHash,omitempty"`
	jwt.RegisteredClaims
}
