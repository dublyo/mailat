package controller

import (
	"context"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/pkg/response"
)

type HealthController struct{}

func NewHealthController() *HealthController {
	return &HealthController{}
}

// Health returns the API health status
// GET /api/v1/health
func (c *HealthController) Health(r *ghttp.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	status := "healthy"
	checks := make(map[string]string)

	// Check PostgreSQL
	if err := database.DB.PingContext(ctx); err != nil {
		status = "unhealthy"
		checks["postgresql"] = "error: " + err.Error()
	} else {
		checks["postgresql"] = "ok"
	}

	// Check Redis
	if err := database.Redis.Ping(ctx).Err(); err != nil {
		status = "unhealthy"
		checks["redis"] = "error: " + err.Error()
	} else {
		checks["redis"] = "ok"
	}

	result := map[string]interface{}{
		"status":    status,
		"checks":    checks,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	if status == "unhealthy" {
		r.Response.Status = 503
	}

	response.Success(r, result)
}

// Ready returns whether the API is ready to serve requests
// GET /api/v1/ready
func (c *HealthController) Ready(r *ghttp.Request) {
	response.Success(r, map[string]interface{}{
		"ready":     true,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
