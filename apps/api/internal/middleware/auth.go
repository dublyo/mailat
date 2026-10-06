package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lib/pq"
	"strings"
	"time"
)

type contextKey string

const (
	UserContextKey       contextKey = "user"
	ClaimsContextKey     contextKey = "claims"
	credentialContextKey contextKey = "credential"
)

type credential struct {
	SessionHash string
	KeyID       int64
	ExpiresAt   time.Time
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func Auth(r *ghttp.Request) {
	token := ExtractToken(r)
	// EventSource cannot set Authorization. Only a short-lived, audience-restricted
	// stream ticket may be supplied here; a session JWT/API key in a URL is rejected.
	query := false
	if token == "" && r.Method == "GET" && r.URL.Path == "/api/v1/sse/connect" {
		token = r.URL.Query().Get("token")
		query = token != ""
	}
	if token == "" {
		response.Unauthorized(r, "Authorization required")
		return
	}
	if strings.HasPrefix(token, "ue_") {
		if query {
			response.Unauthorized(r, "Use an Authorization header for API keys")
			return
		}
		validateAPIKey(r, token)
		return
	}
	validateJWT(r, token, query)
}
func validateJWT(r *ghttp.Request, raw string, query bool) {
	claims := &model.AccessClaims{}
	audience := "mailat-api"
	if query {
		audience = "mailat-sse"
	}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (interface{}, error) { return []byte(config.Cfg.JWTSecret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("mailat"), jwt.WithAudience(audience))
	if err != nil || !parsed.Valid || claims.UserID <= 0 || claims.OrgID <= 0 || claims.ID == "" || (!query && claims.Purpose != "session") || (query && claims.Purpose != "stream") {
		response.Unauthorized(r, "Invalid or expired token")
		return
	}
	hash := tokenHash(raw)
	if query {
		hash = claims.SessionHash
	}
	if hash == "" {
		response.Unauthorized(r, "Invalid session")
		return
	}
	user := &model.JWTClaims{}
	err = database.DB.QueryRowContext(r.Context(), `SELECT u.id,u.org_id,u.email,u.role FROM user_sessions s JOIN users u ON u.id=s.user_id AND u.org_id=s.org_id WHERE s.token_hash=$1 AND s.active AND s.expires_at>now() AND u.status='active' AND u.id=$2 AND u.org_id=$3`, hash, claims.UserID, claims.OrgID).Scan(&user.UserID, &user.OrgID, &user.Email, &user.Role)
	if err != nil {
		if err != sql.ErrNoRows {
			response.InternalError(r, "Unable to validate session")
		} else {
			response.Unauthorized(r, "Session expired or revoked")
		}
		return
	}
	ctx := context.WithValue(r.Context(), ClaimsContextKey, user)
	ctx = context.WithValue(ctx, credentialContextKey, credential{SessionHash: hash, ExpiresAt: claims.ExpiresAt.Time})
	r.SetCtx(ctx)
	r.Middleware.Next()
}
func validateAPIKey(r *ghttp.Request, raw string) {
	var id int64
	var expires sql.NullTime
	var permissions []string
	claims := &model.JWTClaims{Role: "api"}
	err := database.DB.QueryRowContext(r.Context(), `SELECT k.id,k.org_id,k.user_id,k.expires_at,k.permissions,u.email FROM api_keys k JOIN users u ON u.id=k.user_id AND u.org_id=k.org_id WHERE k.key_hash=$1 AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.status='active'`, tokenHash(raw)).Scan(&id, &claims.OrgID, &claims.UserID, &expires, pq.Array(&permissions), &claims.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			response.Unauthorized(r, "Invalid, expired, or revoked API key")
		} else {
			response.InternalError(r, "Unable to validate API key")
		}
		return
	}
	// Atomic fixed-minute window: no replica-local counters and no asynchronous
	// last-used write that could race revocation or overwhelm the database.
	var remaining int
	var retry int
	err = database.DB.QueryRowContext(r.Context(), `UPDATE api_keys SET request_count=CASE WHEN request_window=date_trunc('minute',now()) THEN request_count+1 ELSE 1 END,request_window=date_trunc('minute',now()),last_used_at=now() WHERE id=$1 AND EXISTS(SELECT 1 FROM users u WHERE u.id=api_keys.user_id AND u.org_id=api_keys.org_id AND u.status='active') AND (expires_at IS NULL OR expires_at>now()) AND (request_window IS DISTINCT FROM date_trunc('minute',now()) OR request_count<rate_limit) RETURNING GREATEST(0,rate_limit-request_count),GREATEST(1,ceil(extract(epoch FROM date_trunc('minute',now())+interval '1 minute'-now()))::integer)`, id).Scan(&remaining, &retry)
	if err == sql.ErrNoRows {
		// A revoke/disable racing the rate update is an authentication failure,
		// not a retryable limit error.
		var active bool
		checkErr := database.DB.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM api_keys k JOIN users u ON u.id=k.user_id AND u.org_id=k.org_id WHERE k.id=$1 AND u.status='active' AND (k.expires_at IS NULL OR k.expires_at>now()))`, id).Scan(&active)
		if checkErr != nil {
			response.InternalError(r, "Unable to validate API key")
			return
		}
		if !active {
			response.Unauthorized(r, "Invalid, expired, or revoked API key")
			return
		}
		retry = 60 - time.Now().Second()
		r.Response.Header().Set("Retry-After", fmt.Sprint(retry))
		r.Response.Status = 429
		response.Error(r, 429, "API key request limit exceeded")
		return
	}
	if err != nil {
		response.InternalError(r, "Unable to enforce API request limit")
		return
	}
	r.Response.Header().Set("X-RateLimit-Remaining", fmt.Sprint(remaining))
	scope, ok := APIKeyScope(r.Method, r.URL.Path)
	allowed := false
	for _, p := range permissions {
		if p == scope {
			allowed = true
		}
	}
	if !ok || !allowed {
		response.Forbidden(r, "API key is not permitted for this operation")
		return
	}
	cred := credential{KeyID: id}
	if expires.Valid {
		cred.ExpiresAt = expires.Time
	}
	ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
	ctx = context.WithValue(ctx, credentialContextKey, cred)
	r.SetCtx(ctx)
	r.Middleware.Next()
}

// CredentialActive is used by long-running streams so revocation also stops
// already-open connections at the next heartbeat.
func CredentialActive(ctx context.Context) bool {
	c, ok := ctx.Value(credentialContextKey).(credential)
	if !ok {
		return false
	}
	if !c.ExpiresAt.IsZero() && !time.Now().Before(c.ExpiresAt) {
		return false
	}
	var active bool
	var err error
	if c.KeyID > 0 {
		err = database.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys k JOIN users u ON u.id=k.user_id AND u.org_id=k.org_id WHERE k.id=$1 AND u.status='active' AND (k.expires_at IS NULL OR k.expires_at>now()))`, c.KeyID).Scan(&active)
	} else {
		err = database.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_sessions s JOIN users u ON u.id=s.user_id AND u.org_id=s.org_id WHERE s.token_hash=$1 AND s.active AND s.expires_at>now() AND u.status='active')`, c.SessionHash).Scan(&active)
	}
	return err == nil && active
}

// SessionHash returns the authenticated session's token hash, or "" for API
// keys and unauthenticated contexts.
func SessionHash(ctx context.Context) string {
	c, _ := ctx.Value(credentialContextKey).(credential)
	return c.SessionHash
}
func GetClaims(r *ghttp.Request) *model.JWTClaims {
	c, _ := r.Context().Value(ClaimsContextKey).(*model.JWTClaims)
	return c
}
func ExtractToken(r *ghttp.Request) string {
	p := strings.Fields(r.Header.Get("Authorization"))
	if len(p) == 2 && strings.EqualFold(p[0], "Bearer") {
		return p[1]
	}
	return ""
}
func RequireRole(roles ...string) func(*ghttp.Request) {
	return func(r *ghttp.Request) {
		c := GetClaims(r)
		if c == nil {
			response.Unauthorized(r, "Authentication required")
			return
		}
		for _, role := range roles {
			if c.Role == role {
				r.Middleware.Next()
				return
			}
		}
		response.Forbidden(r, "Insufficient permissions")
	}
}
