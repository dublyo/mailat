package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type CampaignController struct {
	campaignService *service.CampaignService
}

func NewCampaignController(campaignService *service.CampaignService) *CampaignController {
	return &CampaignController{campaignService: campaignService}
}

// Create creates a new campaign
// POST /api/v1/campaigns
func (c *CampaignController) Create(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateCampaignRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	campaign, err := c.campaignService.CreateCampaign(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign created", campaign)
}

// Get retrieves a campaign by UUID
// GET /api/v1/campaigns/:uuid
func (c *CampaignController) Get(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	campaign, err := c.campaignService.GetCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, campaign)
}

// List retrieves campaigns with pagination
// GET /api/v1/campaigns
func (c *CampaignController) List(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	page := r.GetQuery("page", 1).Int()
	pageSize := r.GetQuery("pageSize", 20).Int()
	status := r.GetQuery("status", "").String()

	result, err := c.campaignService.ListCampaigns(r.Context(), claims.OrgID, page, pageSize, status)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, result)
}

// Update updates a campaign
// PUT /api/v1/campaigns/:uuid
func (c *CampaignController) Update(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	var req model.UpdateCampaignRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	campaign, err := c.campaignService.UpdateCampaign(r.Context(), claims.OrgID, campaignUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign updated", campaign)
}

// Delete deletes a campaign
// DELETE /api/v1/campaigns/:uuid
func (c *CampaignController) Delete(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	err := c.campaignService.DeleteCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign deleted", nil)
}

// Schedule schedules a campaign for future sending
// POST /api/v1/campaigns/:uuid/schedule
func (c *CampaignController) Schedule(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	var req model.ScheduleCampaignRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	campaign, err := c.campaignService.ScheduleCampaign(r.Context(), claims.OrgID, campaignUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign scheduled", campaign)
}

// SendNow starts sending a campaign immediately
// POST /api/v1/campaigns/:uuid/send
func (c *CampaignController) SendNow(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	campaign, err := c.campaignService.SendCampaignNow(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign sending started", campaign)
}

// Pause pauses a sending campaign
// POST /api/v1/campaigns/:uuid/pause
func (c *CampaignController) Pause(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	campaign, err := c.campaignService.PauseCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign paused", campaign)
}

// Resume resumes a paused campaign
// POST /api/v1/campaigns/:uuid/resume
func (c *CampaignController) Resume(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	campaign, err := c.campaignService.ResumeCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign resumed", campaign)
}

// Cancel cancels a campaign
// POST /api/v1/campaigns/:uuid/cancel
func (c *CampaignController) Cancel(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	campaign, err := c.campaignService.CancelCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Campaign cancelled", campaign)
}

// GetStats retrieves campaign statistics
// GET /api/v1/campaigns/:uuid/stats
func (c *CampaignController) GetStats(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	stats, err := c.campaignService.GetCampaignStats(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, stats)
}

// Preview renders a campaign preview
// POST /api/v1/campaigns/:uuid/preview
func (c *CampaignController) Preview(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	preview, err := c.campaignService.PreviewCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, preview)
}

// SendTest sends a test email for a campaign
// POST /api/v1/campaigns/:uuid/test
func (c *CampaignController) SendTest(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return
	}

	var req struct {
		Email string `json:"email" v:"required|email"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.campaignService.SendTestEmail(r.Context(), claims.OrgID, campaignUUID, req.Email)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Test email queued", nil)
}
