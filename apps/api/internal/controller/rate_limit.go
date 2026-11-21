package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// rateLimited records a hit for rule/subject. When the request must stop it
// writes the 429 (or 500 on a database error) and returns true. A nil limiter
// allows everything, which keeps narrow controller fixtures simple.
func rateLimited(r *ghttp.Request, l *service.RateLimiter, rule service.RateRule, subject string) bool {
	if l == nil {
		return false
	}
	allowed, retryAfter, err := l.Allow(r.Context(), rule, subject)
	if err != nil {
		response.InternalError(r, "Unable to process the request right now")
		return true
	}
	if !allowed {
		response.TooManyRequests(r, retryAfter, tooManyAttemptsMessage(retryAfter))
		return true
	}
	return false
}

// sendQuotaContext marks a request's sends as user-initiated: API-key requests
// count per key, session requests per user (see service.SendLimiter).
func sendQuotaContext(r *ghttp.Request, claims *model.JWTClaims) context.Context {
	subject := service.SendQuotaSubject{UserID: claims.UserID}
	if id := middleware.APIKeyID(r.Context()); id > 0 {
		subject = service.SendQuotaSubject{APIKeyID: id}
	}
	return service.WithSendQuota(r.Context(), subject)
}

// writeSendLimit answers a *service.SendLimitError with 429 (and Retry-After
// when waiting helps) and reports whether err was one. Nothing was recorded
// or attempted, so the same idempotency key may be reused.
func writeSendLimit(r *ghttp.Request, err error) bool {
	var limit *service.SendLimitError
	if !errors.As(err, &limit) {
		return false
	}
	if limit.RetryAfter > 0 {
		response.TooManyRequests(r, limit.RetryAfter, limit.Message)
	} else {
		r.Response.Status = http.StatusTooManyRequests
		response.Error(r, http.StatusTooManyRequests, limit.Message)
	}
	return true
}

func tooManyAttemptsMessage(retryAfter time.Duration) string {
	minutes := int(math.Ceil(retryAfter.Minutes()))
	if minutes <= 1 {
		return "Too many attempts. Try again in 1 minute."
	}
	return fmt.Sprintf("Too many attempts. Try again in %d minutes.", minutes)
}
