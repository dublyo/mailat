package controller

import (
	"fmt"
	"log"
	"math"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

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

// peekLimited checks rule/subject without counting this request; use it with
// recordHit for limits that count failures only.
func peekLimited(r *ghttp.Request, l *service.RateLimiter, rule service.RateRule, subject string) bool {
	if l == nil {
		return false
	}
	allowed, retryAfter, err := l.Peek(r.Context(), rule, subject)
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

// recordHit counts one failure. A database error is logged, not returned:
// the request already failed for its own reason.
func recordHit(r *ghttp.Request, l *service.RateLimiter, rule service.RateRule, subject string) {
	if l == nil {
		return
	}
	if err := l.Record(r.Context(), rule, subject); err != nil {
		log.Printf("rate limit %s record failed: %v", rule.Name, err)
	}
}

func tooManyAttemptsMessage(retryAfter time.Duration) string {
	minutes := int(math.Ceil(retryAfter.Minutes()))
	if minutes <= 1 {
		return "Too many attempts. Try again in 1 minute."
	}
	return fmt.Sprintf("Too many attempts. Try again in %d minutes.", minutes)
}
