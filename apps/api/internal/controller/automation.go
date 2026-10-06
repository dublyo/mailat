package controller

import (
	"errors"
	"log"
	"net/http"

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

// writeAutomationError maps service errors to statuses. Validation failures
// carry data.errors; unclassified failures are logged and never echoed.
func writeAutomationError(r *ghttp.Request, err error) {
	var invalid *service.AutomationInvalidError
	var state *service.AutomationError
	switch {
	case errors.As(err, &invalid):
		response.ErrorWithData(r, http.StatusBadRequest, "Automation is not valid", map[string]interface{}{"errors": invalid.Errors})
	case errors.Is(err, service.ErrAutomationNotFound):
		response.NotFound(r, "Automation not found")
	case errors.Is(err, service.ErrAutomationEnrollmentNotFound):
		response.NotFound(r, "Enrollment not found")
	case errors.Is(err, service.ErrAutomationConflict):
		r.Response.Status = http.StatusConflict
		response.Error(r, http.StatusConflict, "Contact already has an active enrollment")
	case errors.As(err, &state):
		response.BadRequest(r, state.Message)
	default:
		log.Printf("automation request failed: %v", err)
		response.InternalError(r, "Automation request failed")
	}
}

// automationRequest returns the claims and the :uuid parameter, or writes the error.
func automationRequest(r *ghttp.Request) (*model.JWTClaims, string, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return nil, "", false
	}
	automationUUID := r.Get("uuid").String()
	if automationUUID == "" {
		response.BadRequest(r, "Automation UUID required")
		return nil, "", false
	}
	return claims, automationUUID, true
}

// Create saves a new draft; only structural problems are rejected.
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
	automation, err := c.automationService.CreateAutomation(r.Context(), claims.OrgID, claims.UserID, &req)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Automation created", automation)
}

// Get returns the draft, the published version number, whether the draft has
// unpublished changes, and live enrollment counts.
// GET /api/v1/automations/:uuid
func (c *AutomationController) Get(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	automation, err := c.automationService.GetAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.Success(r, automation)
}

// List pages automations with live counts. status is draft, active, paused or archived.
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
		writeAutomationError(r, err)
		return
	}
	response.Success(r, result)
}

// Update changes the draft only; running enrollments keep their published
// version. Archived automations cannot be edited.
// PUT /api/v1/automations/:uuid
func (c *AutomationController) Update(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	var req model.UpdateAutomationRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}
	automation, err := c.automationService.UpdateAutomation(r.Context(), claims.OrgID, automationUUID, &req)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Automation updated", automation)
}

// Delete removes a draft or archived automation; archive a live one first.
// DELETE /api/v1/automations/:uuid
func (c *AutomationController) Delete(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	if err := c.automationService.DeleteAutomation(r.Context(), claims.OrgID, automationUUID); err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Automation deleted", nil)
}

// Validate runs the publish checks, including the referenced lists,
// templates, identities and webhooks, without changing anything.
// POST /api/v1/automations/:uuid/validate
func (c *AutomationController) Validate(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	errs, err := c.automationService.ValidateAutomation(r.Context(), claims.OrgID, claims.UserID, automationUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.Success(r, model.AutomationValidationResult{Valid: len(errs) == 0, Errors: errs})
}

// Activate publishes the draft as a new version when it changed and makes the
// automation active. {"publishDraft":false} resumes the current version
// without touching the draft. Used for activate, resume and publish changes.
// POST /api/v1/automations/:uuid/activate
func (c *AutomationController) Activate(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	var req model.ActivateAutomationRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}
	publishDraft := req.PublishDraft == nil || *req.PublishDraft
	result, err := c.automationService.ActivateAutomation(r.Context(), claims.OrgID, claims.UserID, automationUUID, publishDraft)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Automation activated", result)
}

// Pause stops new enrollments and freezes running ones.
// POST /api/v1/automations/:uuid/pause
func (c *AutomationController) Pause(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	automation, err := c.automationService.PauseAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Automation paused", automation)
}

// Archive ends the automation for good and cancels its active enrollments.
// POST /api/v1/automations/:uuid/archive
func (c *AutomationController) Archive(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	automation, cancelled, err := c.automationService.ArchiveAutomation(r.Context(), claims.OrgID, automationUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Automation archived", map[string]interface{}{"automation": automation, "cancelledEnrollments": cancelled})
}

// GetStats returns live enrollment counts and per-node counts for a version
// (default: the published version).
// GET /api/v1/automations/:uuid/stats
func (c *AutomationController) GetStats(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	var version *int
	if raw := r.GetQuery("version"); raw != nil && raw.String() != "" {
		v := raw.Int()
		if v < 1 {
			response.BadRequest(r, "version must be a positive integer")
			return
		}
		version = &v
	}
	stats, err := c.automationService.GetAutomationStats(r.Context(), claims.OrgID, automationUUID, version)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.Success(r, stats)
}

// EnrollContact manually enrolls one contact ({"contactUuid"}) or every member
// of a list ({"listUuid"}) into the published version of an active
// automation. Inactive, suppressed and already-enrolled contacts are skipped.
// POST /api/v1/automations/:uuid/enroll
func (c *AutomationController) EnrollContact(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	var req model.EnrollRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}
	result, err := c.automationService.Enroll(r.Context(), claims.OrgID, automationUUID, &req)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Contacts enrolled", result)
}

// ListEnrollments pages enrollments with the contact's email; status also
// accepts the derived "waiting" (active with a future step).
// GET /api/v1/automations/:uuid/enrollments
func (c *AutomationController) ListEnrollments(r *ghttp.Request) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return
	}
	page := r.GetQuery("page", 1).Int()
	pageSize := r.GetQuery("pageSize", 20).Int()
	status := r.GetQuery("status", "").String()
	result, err := c.automationService.ListEnrollments(r.Context(), claims.OrgID, automationUUID, status, page, pageSize)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.Success(r, result)
}

// enrollmentRequest returns the claims and both path UUIDs, or writes the error.
func enrollmentRequest(r *ghttp.Request) (*model.JWTClaims, string, string, bool) {
	claims, automationUUID, ok := automationRequest(r)
	if !ok {
		return nil, "", "", false
	}
	enrollmentUUID := r.Get("enrollmentUuid").String()
	if enrollmentUUID == "" {
		response.BadRequest(r, "Enrollment UUID required")
		return nil, "", "", false
	}
	return claims, automationUUID, enrollmentUUID, true
}

// GetEnrollment returns one enrollment with its step timeline.
// GET /api/v1/automations/:uuid/enrollments/:enrollmentUuid
func (c *AutomationController) GetEnrollment(r *ghttp.Request) {
	claims, automationUUID, enrollmentUUID, ok := enrollmentRequest(r)
	if !ok {
		return
	}
	enrollment, err := c.automationService.GetEnrollment(r.Context(), claims.OrgID, automationUUID, enrollmentUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.Success(r, enrollment)
}

// CancelEnrollment stops an active enrollment; queued emails not yet claimed
// by the sender are not sent.
// POST /api/v1/automations/:uuid/enrollments/:enrollmentUuid/cancel
func (c *AutomationController) CancelEnrollment(r *ghttp.Request) {
	claims, automationUUID, enrollmentUUID, ok := enrollmentRequest(r)
	if !ok {
		return
	}
	enrollment, err := c.automationService.CancelEnrollment(r.Context(), claims.OrgID, automationUUID, enrollmentUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Enrollment cancelled", enrollment)
}

// RetryEnrollment resumes a failed enrollment at its failed step on the same
// version. The automation must be active; 409 if the contact already has
// another active enrollment.
// POST /api/v1/automations/:uuid/enrollments/:enrollmentUuid/retry
func (c *AutomationController) RetryEnrollment(r *ghttp.Request) {
	claims, automationUUID, enrollmentUUID, ok := enrollmentRequest(r)
	if !ok {
		return
	}
	enrollment, err := c.automationService.RetryEnrollment(r.Context(), claims.OrgID, automationUUID, enrollmentUUID)
	if err != nil {
		writeAutomationError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Enrollment retried", enrollment)
}
