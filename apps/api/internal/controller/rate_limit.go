package controller

import (
	"fmt"
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

func tooManyAttemptsMessage(retryAfter time.Duration) string {
	minutes := int(math.Ceil(retryAfter.Minutes()))
	if minutes <= 1 {
		return "Too many attempts. Try again in 1 minute."
	}
	return fmt.Sprintf("Too many attempts. Try again in %d minutes.", minutes)
}
