package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type HealthOpsController struct {
	healthService *service.HealthService
}

func NewHealthOpsController(healthService *service.HealthService) *HealthOpsController {
	return &HealthOpsController{healthService: healthService}
}

// CheckBlacklists checks if an IP is listed on blacklists
// POST /api/v1/health/blacklist-check
// If ipAddress is not provided, auto-detects the server's public IP
func (c *HealthOpsController) CheckBlacklists(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		IPAddress string `json:"ipAddress"` // Optional - will auto-detect if not provided
	}
	// Parse but ignore error - ipAddress is optional
	r.Parse(&req)

	result, err := c.healthService.CheckBlacklists(r.Context(), req.IPAddress)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, result)
}

// GetReputationMetrics retrieves sender reputation metrics
// GET /api/v1/health/reputation
func (c *HealthOpsController) GetReputationMetrics(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	period := r.GetQuery("period", "30days").String()

	metrics, err := c.healthService.GetReputationMetrics(r.Context(), claims.OrgID, period)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, metrics)
}

// GetSESLimits retrieves AWS SES account limits
// GET /api/v1/health/ses-limits
func (c *HealthOpsController) GetSESLimits(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	limits, err := c.healthService.GetSESAccountLimits(r.Context())
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, limits)
}

// GetEmailHealthSummary retrieves comprehensive email health metrics
// GET /api/v1/health/summary
func (c *HealthOpsController) GetEmailHealthSummary(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	summary, err := c.healthService.GetEmailHealthSummary(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, summary)
}

// GetWarmupSchedules returns available warmup schedules
// GET /api/v1/health/warmup/schedules
func (c *HealthOpsController) GetWarmupSchedules(r *ghttp.Request) {
	schedules := c.healthService.GetWarmupSchedules()
	response.Success(r, schedules)
}

// StartWarmup starts IP warmup
// POST /api/v1/health/warmup
func (c *HealthOpsController) StartWarmup(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		IPAddress    string `json:"ipAddress" v:"required"`
		ScheduleName string `json:"scheduleName" d:"conservative"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	status, err := c.healthService.StartWarmup(r.Context(), claims.OrgID, req.IPAddress, req.ScheduleName)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Warmup started", status)
}

// GetWarmupStatus returns current warmup status
// GET /api/v1/health/warmup/:ip
func (c *HealthOpsController) GetWarmupStatus(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	ipAddress := r.Get("ip").String()
	if ipAddress == "" {
		response.BadRequest(r, "IP address required")
		return
	}

	status, err := c.healthService.GetWarmupStatus(r.Context(), claims.OrgID, ipAddress)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, status)
}

// GetQuotaStatus returns quota usage
// GET /api/v1/health/quota
func (c *HealthOpsController) GetQuotaStatus(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	status, err := c.healthService.GetQuotaStatus(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, status)
}

// GetAlerts returns system alerts
// GET /api/v1/health/alerts
func (c *HealthOpsController) GetAlerts(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	unacknowledgedOnly := r.GetQuery("unacknowledged", false).Bool()

	alerts, err := c.healthService.GetAlerts(r.Context(), claims.OrgID, unacknowledgedOnly)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, alerts)
}

// AcknowledgeAlert acknowledges an alert
// POST /api/v1/health/alerts/:id/acknowledge
func (c *HealthOpsController) AcknowledgeAlert(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	alertID := r.Get("id").Int64()
	if alertID == 0 {
		response.BadRequest(r, "Alert ID required")
		return
	}

	err := c.healthService.AcknowledgeAlert(r.Context(), claims.OrgID, alertID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Alert acknowledged", nil)
}

// GetDeliveryLogs returns delivery logs
// GET /api/v1/health/logs
func (c *HealthOpsController) GetDeliveryLogs(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	page := r.GetQuery("page", 1).Int()
	pageSize := r.GetQuery("pageSize", 50).Int()
	status := r.GetQuery("status", "").String()
	emailID := r.GetQuery("emailId", 0).Int64()

	logs, err := c.healthService.GetDeliveryLogs(r.Context(), claims.OrgID, page, pageSize, status, emailID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, logs)
}
