package controller

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type CampaignController struct {
	campaignService *service.CampaignService
}

func NewCampaignController(campaignService *service.CampaignService) *CampaignController {
	return &CampaignController{campaignService: campaignService}
}

func campaignActor(claims *model.JWTClaims) service.CampaignActor {
	return service.CampaignActor{UserID: claims.UserID, APIKey: claims.Role == "api"}
}

// campaignErrorDetails maps service errors to statuses; unclassified failures
// get a generic message so internal errors are never echoed.
func campaignErrorDetails(err error) (int, string) {
	var validation *provider.MailValidationError
	switch {
	case errors.Is(err, service.ErrCampaignNotFound):
		return http.StatusNotFound, "Campaign not found"
	case errors.Is(err, service.ErrCampaignForbidden):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, service.ErrCampaignState), errors.Is(err, service.ErrCampaignTestConflict):
		return http.StatusConflict, err.Error()
	case errors.Is(err, service.ErrCampaignTestRateLimited):
		return http.StatusTooManyRequests, err.Error()
	case errors.As(err, &validation):
		return http.StatusBadRequest, validation.Error()
	}
	return http.StatusInternalServerError, "Campaign request failed"
}

func writeCampaignError(r *ghttp.Request, err error) {
	status, message := campaignErrorDetails(err)
	if status == http.StatusInternalServerError {
		log.Printf("campaign request failed: %v", err)
	}
	r.Response.Status = status
	response.Error(r, status, message)
}

// campaignRequest returns the claims and the :uuid parameter, or writes the error.
func campaignRequest(r *ghttp.Request) (*model.JWTClaims, string, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return nil, "", false
	}
	campaignUUID := r.Get("uuid").String()
	if campaignUUID == "" {
		response.BadRequest(r, "Campaign UUID required")
		return nil, "", false
	}
	return claims, campaignUUID, true
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

	campaign, err := c.campaignService.CreateCampaign(r.Context(), claims.OrgID, campaignActor(claims), &req)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign created", campaign)
}

// Get retrieves a campaign by UUID
// GET /api/v1/campaigns/:uuid
func (c *CampaignController) Get(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	campaign, err := c.campaignService.GetCampaign(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
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
		writeCampaignError(r, err)
		return
	}

	response.Success(r, result)
}

// Update updates a campaign; absent fields are unchanged and a JSON null
// replyTo or textContent clears it.
// PUT /api/v1/campaigns/:uuid
func (c *CampaignController) Update(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	var req model.UpdateCampaignRequest
	var present map[string]json.RawMessage
	body := r.GetBody()
	if err := json.Unmarshal(body, &req); err != nil {
		response.BadRequest(r, "Invalid JSON body")
		return
	}
	_ = json.Unmarshal(body, &present)
	clear := ""
	if v, ok := present["replyTo"]; ok && string(v) == "null" {
		req.ReplyTo = &clear
	}
	if v, ok := present["textContent"]; ok && string(v) == "null" {
		req.TextContent = &clear
	}

	campaign, err := c.campaignService.UpdateCampaign(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID, &req)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign updated", campaign)
}

// Delete deletes a draft, or a cancelled campaign that never sent
// DELETE /api/v1/campaigns/:uuid
func (c *CampaignController) Delete(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	if err := c.campaignService.DeleteCampaign(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID); err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign deleted", nil)
}

// Schedule schedules a campaign for future sending
// POST /api/v1/campaigns/:uuid/schedule
func (c *CampaignController) Schedule(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	var req model.ScheduleCampaignRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	campaign, err := c.campaignService.ScheduleCampaign(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID, &req)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign scheduled", campaign)
}

// SendNow starts sending a campaign immediately
// POST /api/v1/campaigns/:uuid/send
func (c *CampaignController) SendNow(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	campaign, err := c.campaignService.SendCampaignNow(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign sending started", campaign)
}

// Pause pauses a sending campaign
// POST /api/v1/campaigns/:uuid/pause
func (c *CampaignController) Pause(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	campaign, err := c.campaignService.PauseCampaign(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign paused", campaign)
}

// Resume resumes a paused campaign
// POST /api/v1/campaigns/:uuid/resume
func (c *CampaignController) Resume(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	campaign, err := c.campaignService.ResumeCampaign(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign resumed", campaign)
}

// Cancel cancels a campaign
// POST /api/v1/campaigns/:uuid/cancel
func (c *CampaignController) Cancel(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	campaign, err := c.campaignService.CancelCampaign(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign cancelled", campaign)
}

// GetStats retrieves campaign statistics
// GET /api/v1/campaigns/:uuid/stats
func (c *CampaignController) GetStats(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	stats, err := c.campaignService.GetCampaignStats(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, stats)
}

// Progress summarises recipient rows by status
// GET /api/v1/campaigns/:uuid/progress
func (c *CampaignController) Progress(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	progress, err := c.campaignService.GetProgress(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, progress)
}

// Audience estimates eligible recipients and lists readiness warnings
// GET /api/v1/campaigns/:uuid/audience
func (c *CampaignController) Audience(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	audience, err := c.campaignService.EstimateAudience(r.Context(), claims.OrgID, campaignUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, audience)
}

// Recipients pages through the campaign's recipients
// GET /api/v1/campaigns/:uuid/recipients
func (c *CampaignController) Recipients(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	status := r.GetQuery("status", "").String()
	page := r.GetQuery("page", 1).Int()
	pageSize := r.GetQuery("pageSize", 50).Int()

	result, err := c.campaignService.ListRecipients(r.Context(), claims.OrgID, campaignUUID, status, page, pageSize)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, result)
}

// Preview renders a campaign preview without tracking
// POST /api/v1/campaigns/:uuid/preview
func (c *CampaignController) Preview(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	var req model.CampaignPreviewRequest
	if body := r.GetBody(); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			response.BadRequest(r, "Invalid JSON body")
			return
		}
	}

	preview, err := c.campaignService.PreviewCampaign(r.Context(), claims.OrgID, campaignUUID, req.ContactUUID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, preview)
}

// SendTest sends a test of the campaign to 1-5 addresses; requires an Idempotency-Key header
// POST /api/v1/campaigns/:uuid/test
func (c *CampaignController) SendTest(r *ghttp.Request) {
	claims, campaignUUID, ok := campaignRequest(r)
	if !ok {
		return
	}

	var req model.CampaignTestRequest
	if err := json.Unmarshal(r.GetBody(), &req); err != nil {
		response.BadRequest(r, "Invalid JSON body")
		return
	}

	result, err := c.campaignService.SendTestEmail(r.Context(), claims.OrgID, campaignActor(claims), campaignUUID, req.Emails, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, result)
}

// GetSettings returns the org's campaign settings (postal address)
// GET /api/v1/campaign-settings
func (c *CampaignController) GetSettings(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	settings, err := c.campaignService.GetCampaignSettings(r.Context(), claims.OrgID)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.Success(r, settings)
}

// UpdateSettings stores the postal address; owner/admin sessions only
// PUT /api/v1/campaign-settings
func (c *CampaignController) UpdateSettings(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CampaignSettings
	if err := json.Unmarshal(r.GetBody(), &req); err != nil {
		response.BadRequest(r, "Invalid JSON body")
		return
	}

	settings, err := c.campaignService.UpdateCampaignSettings(r.Context(), claims.OrgID, campaignActor(claims), &req)
	if err != nil {
		writeCampaignError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Campaign settings updated", settings)
}
