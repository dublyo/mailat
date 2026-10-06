package controller

import (
	"context"
	"errors"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/pkg/response"
)

type HealthController struct{}

// BuildVersion is injected by the image build so a rollout can be verified.
var BuildVersion = "development"

func NewHealthController() *HealthController {
	return &HealthController{}
}

var errNotConnected = errors.New("not connected")

// dependencyChecks pings Postgres and Redis. Values are "ok" or
// "unavailable"; error details are logged server-side and never returned.
func dependencyChecks(ctx context.Context) (map[string]string, bool) {
	checks := map[string]string{"postgresql": "ok", "redis": "ok"}
	healthy := true
	fail := func(name string, err error) {
		checks[name] = "unavailable"
		healthy = false
		g.Log().Warningf(ctx, "health check %s failed: %v", name, err)
	}
	if database.DB == nil {
		fail("postgresql", errNotConnected)
	} else if err := database.DB.PingContext(ctx); err != nil {
		fail("postgresql", err)
	}
	if database.Redis == nil {
		fail("redis", errNotConnected)
	} else if err := database.Redis.Ping(ctx).Err(); err != nil {
		fail("redis", err)
	}
	return checks, healthy
}

// Health reports dependency status for monitoring. data.status is "healthy"
// or "unhealthy" and data.version is the build; checks values are "ok" or
// "unavailable". Unhealthy responses use HTTP 503 with the same data.
// GET /api/v1/health
func (c *HealthController) Health(r *ghttp.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	checks, healthy := dependencyChecks(ctx)
	status := "healthy"
	if !healthy {
		status = "unhealthy"
	}
	result := map[string]interface{}{
		"version":   BuildVersion,
		"status":    status,
		"checks":    checks,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if !healthy {
		response.WithStatus(r, 503, 503, "unhealthy", result)
		return
	}
	response.Success(r, result)
}

// Ready reports whether the API can serve requests: Postgres and Redis must
// answer within 2 seconds, otherwise HTTP 503 with ready=false.
// GET /api/v1/ready
func (c *HealthController) Ready(r *ghttp.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	_, healthy := dependencyChecks(ctx)
	result := map[string]interface{}{
		"ready":     healthy,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if !healthy {
		response.WithStatus(r, 503, 503, "not ready", result)
		return
	}
	response.Success(r, result)
}
