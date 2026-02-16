package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type AutomationController struct {
	automationService *service.AutomationService
}

func NewAutomationController(automationService *service.AutomationService) *AutomationController {
	return &AutomationController{automationService: automationService}
}

// Create creates a new automation
// POST /api/v1/automations
func (c *AutomationController) Create(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateAutomationRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	automation, err := c.automationService.CreateAutomation(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Automation created", automation)
}

// Get retrieves an automation by UUID
// GET /api/v1/automations/:uuid
func (c *AutomationController) Get(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	automation, err := c.automationService.GetAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, automation)
}

// List retrieves automations with pagination
// GET /api/v1/automations
func (c *AutomationController) List(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	page := r.GetQuery("page", 1).Int()
	pageSize := r.GetQuery("pageSize", 20).Int()
	status := r.GetQuery("status", "").String()

	result, err := c.automationService.ListAutomations(r.Context(), claims.OrgID, page, pageSize, status)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, result)
}

// Update updates an automation
// PUT /api/v1/automations/:uuid
func (c *AutomationController) Update(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	var req model.UpdateAutomationRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	automation, err := c.automationService.UpdateAutomation(r.Context(), claims.OrgID, automationUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Automation updated", automation)
}

// Delete deletes an automation
// DELETE /api/v1/automations/:uuid
func (c *AutomationController) Delete(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	err := c.automationService.DeleteAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Automation deleted", nil)
}

// Activate activates an automation
// POST /api/v1/automations/:uuid/activate
func (c *AutomationController) Activate(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	automation, err := c.automationService.ActivateAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Automation activated", automation)
}

// Pause pauses an automation
// POST /api/v1/automations/:uuid/pause
func (c *AutomationController) Pause(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	automation, err := c.automationService.PauseAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Automation paused", automation)
}

// GetStats retrieves automation statistics
// GET /api/v1/automations/:uuid/stats
func (c *AutomationController) GetStats(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	stats, err := c.automationService.GetAutomationStats(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, stats)
}

// EnrollContact enrolls a contact in an automation
// POST /api/v1/automations/:uuid/enroll
func (c *AutomationController) EnrollContact(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return
	}

	var req struct {
		ContactUUID string `json:"contactUuid" v:"required"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.automationService.EnrollContact(r.Context(), claims.OrgID, automationUUID, req.ContactUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact enrolled in automation", nil)
}
