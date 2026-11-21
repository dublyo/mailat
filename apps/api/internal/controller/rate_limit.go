package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
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
// allows everything, which keeps narrow controller fixtures simple. A client
// IP subject is bucketed by service.RateLimitIPKey (IPv6 per /64).
func rateLimited(r *ghttp.Request, l *service.RateLimiter, rule service.RateRule, subject string) bool {
	if l == nil {
		return false
	}
	allowed, retryAfter, err := l.Allow(r.Context(), rule, service.RateLimitIPKey(subject))
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

// reserveHit spends one hit of a failure-only limit before the check it
// guards, so concurrent requests cannot together exceed the limit. Give it
// back with refundHit when the check passed. allowed is false (with the time
// left in the window) when the limit is already reached; nothing is spent then.
func reserveHit(r *ghttp.Request, l *service.RateLimiter, rule service.RateRule, subject string) (allowed bool, retryAfter time.Duration, err error) {
	if l == nil {
		return true, 0, nil
	}
	return l.Allow(r.Context(), rule, subject)
}

// refundHit returns a hit taken by reserveHit. A database error is logged,
// not returned: the request already has its answer.
func refundHit(r *ghttp.Request, l *service.RateLimiter, rule service.RateRule, subject string) {
	if l == nil {
		return
	}
	if err := l.Refund(context.WithoutCancel(r.Context()), rule, subject); err != nil {
		log.Printf("rate limit %s refund failed: %v", rule.Name, err)
	}
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
