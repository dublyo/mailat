package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/response"
)

type contextKey string

const (
	UserContextKey   contextKey = "user"
	ClaimsContextKey contextKey = "claims"
)

// Auth middleware validates JWT tokens or API keys
func Auth(r *ghttp.Request) {
	var token string

	// First check Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && parts[0] == "Bearer" {
			token = parts[1]
		}
	}

	// If no header, check query parameter (for SSE connections)
	if token == "" {
		token = r.Get("token").String()
	}

	if token == "" {
		response.Unauthorized(r, "Authorization required")
		return
	}

	// Check if it's an API key (starts with "ue_")
	if strings.HasPrefix(token, "ue_") {
		validateAPIKey(r, token)
		return
	}

	// Otherwise, validate as JWT
	validateJWT(r, token)
}

func validateJWT(r *ghttp.Request, tokenString string) {
	cfg := config.Cfg

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(cfg.JWTSecret), nil
	})

	if err != nil || !token.Valid {
		response.Unauthorized(r, "Invalid or expired token")
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		response.Unauthorized(r, "Invalid token claims")
		return
	}

	// Extract user info from claims
	userID, _ := claims["userId"].(float64)
	orgID, _ := claims["orgId"].(float64)
	email, _ := claims["email"].(string)
	role, _ := claims["role"].(string)

	jwtClaims := &model.JWTClaims{
		UserID: int64(userID),
		OrgID:  int64(orgID),
		Email:  email,
		Role:   role,
	}

	// Set claims in context
	ctx := context.WithValue(r.Context(), ClaimsContextKey, jwtClaims)
	r.SetCtx(ctx)

	r.Middleware.Next()
}

func validateAPIKey(r *ghttp.Request, apiKey string) {
	// Hash the API key
	hash := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(hash[:])

	// Lookup in database (Prisma schema has no status column)
	var orgID int64
	var expiresAt sql.NullTime

	err := database.DB.QueryRowContext(r.Context(), `
		SELECT org_id, expires_at
		FROM api_keys
		WHERE key_hash = $1
	`, keyHash).Scan(&orgID, &expiresAt)

	if err == sql.ErrNoRows {
		response.Unauthorized(r, "Invalid API key")
		return
	}
	if err != nil {
		response.InternalError(r, "Failed to validate API key")
		return
	}

	if expiresAt.Valid && expiresAt.Time.Before(time.Now()) {
		response.Unauthorized(r, "API key has expired")
		return
	}

	// Update last used timestamp (async)
	go func() {
		database.DB.ExecContext(context.Background(), `
			UPDATE api_keys SET last_used_at = NOW() WHERE key_hash = $1
		`, keyHash)
	}()

	// Create claims for API key auth
	claims := &model.JWTClaims{
		OrgID: orgID,
		Role:  "api", // API keys have special role
	}

	ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
	r.SetCtx(ctx)

	r.Middleware.Next()
}

// GetClaims extracts JWT claims from request context
func GetClaims(r *ghttp.Request) *model.JWTClaims {
	claims, ok := r.Context().Value(ClaimsContextKey).(*model.JWTClaims)
	if !ok {
		return nil
	}
	return claims
}

// ExtractToken extracts the raw token from the Authorization header
func ExtractToken(r *ghttp.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}

	return parts[1]
}

// RequireRole middleware checks if user has required role
func RequireRole(roles ...string) func(r *ghttp.Request) {
	return func(r *ghttp.Request) {
		claims := GetClaims(r)
		if claims == nil {
			response.Unauthorized(r, "Authentication required")
			return
		}

		for _, role := range roles {
			if claims.Role == role {
				r.Middleware.Next()
				return
			}
		}

		response.Forbidden(r, "Insufficient permissions")
	}
}
