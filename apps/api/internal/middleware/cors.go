package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/gogf/gf/v2/net/ghttp"
)

const (
	corsAllowMethods  = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowHeaders  = "Authorization, Content-Type, Idempotency-Key, X-API-Key"
	corsExposeHeaders = "Retry-After"
)

// corsPublicPrefixes are token- or form-addressed endpoints that embedded
// signup forms and mail clients call from any site. They never use cookies or
// bearer credentials, so a wildcard origin is safe there.
var corsPublicPrefixes = []string{
	"/api/v1/public/",
	"/api/v1/unsubscribe/",
	"/api/v1/preferences/",
	"/api/v1/tracking/",
}

// CORS replaces ghttp.MiddlewareCORS, which reflected any Origin. Authenticated
// routes only echo the WEB_URL origin and the CORS_ORIGINS allowlist.
// Credentials are never allowed: the SPA sends a bearer token, not cookies.
func CORS(r *ghttp.Request) {
	h := r.Response.Header()
	origin := r.Header.Get("Origin")
	allowed := ""
	if isCORSPublicPath(r.URL.Path) {
		allowed = "*"
	} else {
		h.Add("Vary", "Origin")
		if origin != "" && corsOriginAllowed(origin) {
			allowed = origin
		}
	}
	if allowed != "" {
		h.Set("Access-Control-Allow-Origin", allowed)
		h.Set("Access-Control-Allow-Methods", corsAllowMethods)
		h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
		h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		h.Set("Access-Control-Max-Age", "600")
	}
	if r.Method == http.MethodOptions {
		// Preflights end here whether or not the origin is allowed; a missing
		// ACAO header is what makes the browser refuse a foreign origin.
		r.Response.WriteHeader(http.StatusNoContent)
		r.ExitAll()
		return
	}
	r.Middleware.Next()
}

func isCORSPublicPath(path string) bool {
	for _, prefix := range corsPublicPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func corsOriginAllowed(origin string) bool {
	cfg := config.Cfg
	if cfg == nil {
		return false
	}
	if web := originOf(cfg.WebUrl); web != "" && strings.EqualFold(origin, web) {
		return true
	}
	for _, allowed := range cfg.CORSOrigins {
		if strings.EqualFold(origin, strings.TrimSuffix(allowed, "/")) {
			return true
		}
	}
	return false
}

// originOf reduces a URL such as https://app.example.com/path to its origin.
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
