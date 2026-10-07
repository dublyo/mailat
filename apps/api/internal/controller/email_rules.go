package controller

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type EmailRulesController struct {
	rulesService     *service.EmailRulesService
	autoReplyService *service.AutoReplyService
	limiter          *service.RateLimiter
}

func NewEmailRulesController(rulesService *service.EmailRulesService, autoReplyService *service.AutoReplyService, limiter *service.RateLimiter) *EmailRulesController {
	return &EmailRulesController{
		rulesService:     rulesService,
		autoReplyService: autoReplyService,
		limiter:          limiter,
	}
}

// ====================
// EMAIL RULES
// ====================

// CreateRule creates a new email rule
// POST /api/v1/rules
func (c *EmailRulesController) CreateRule(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req service.CreateEmailRuleInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	rule, err := c.rulesService.CreateRule(r.Context(), claims.UserID, claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email rule created", rule)
}

// GetRule gets an email rule by ID
// GET /api/v1/rules/:id
func (c *EmailRulesController) GetRule(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	ruleID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid rule ID")
		return
	}

	rule, err := c.rulesService.GetRule(r.Context(), claims.UserID, ruleID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, rule)
}

// ListRules lists all email rules for the user
// GET /api/v1/rules
func (c *EmailRulesController) ListRules(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	activeOnly := r.Get("active").Bool()

	rules, err := c.rulesService.ListRules(r.Context(), claims.UserID, activeOnly)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, rules)
}

// UpdateRule updates an email rule
// PUT /api/v1/rules/:id
func (c *EmailRulesController) UpdateRule(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	ruleID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid rule ID")
		return
	}

	var req service.UpdateEmailRuleInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	rule, err := c.rulesService.UpdateRule(r.Context(), claims.UserID, ruleID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email rule updated", rule)
}

// DeleteRule deletes an email rule
// DELETE /api/v1/rules/:id
func (c *EmailRulesController) DeleteRule(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	ruleID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid rule ID")
		return
	}

	if err := c.rulesService.DeleteRule(r.Context(), claims.UserID, ruleID); err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email rule deleted", nil)
}

// ReorderRules updates the priority order of rules
// POST /api/v1/rules/reorder
func (c *EmailRulesController) ReorderRules(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		RuleIDs []int `json:"ruleIds"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if err := c.rulesService.ReorderRules(r.Context(), claims.UserID, req.RuleIDs); err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Rules reordered", nil)
}

// TestRule tests a rule against a sample email
// POST /api/v1/rules/:id/test
func (c *EmailRulesController) TestRule(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	ruleID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid rule ID")
		return
	}

	var req service.EmailForTestInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	matches, actions, err := c.rulesService.TestRule(r.Context(), claims.UserID, ruleID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, map[string]interface{}{
		"matches": matches,
		"actions": actions,
	})
}

// ====================
// AUTO-REPLY / VACATION
// ====================

// autoReplyError maps auto-reply service errors without exposing internals.
func autoReplyError(r *ghttp.Request, err error) {
	var invalid *service.AutoReplyValidationError
	switch {
	case errors.As(err, &invalid):
		response.BadRequest(r, invalid.Message)
	case errors.Is(err, service.ErrAutoReplyNotFound):
		response.NotFound(r, "Auto-reply not found")
	default:
		log.Printf("Auto-reply request failed: %v", err)
		response.InternalError(r, "Auto-reply request failed")
	}
}

// CreateAutoReply creates a new auto-reply
// POST /api/v1/auto-replies
func (c *EmailRulesController) CreateAutoReply(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req service.CreateAutoReplyInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	autoReply, err := c.autoReplyService.CreateAutoReply(r.Context(), claims.UserID, claims.OrgID, &req)
	if err != nil {
		autoReplyError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Auto-reply created", autoReply)
}

// GetAutoReply gets an auto-reply by ID
// GET /api/v1/auto-replies/:id
func (c *EmailRulesController) GetAutoReply(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	autoReplyID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid auto-reply ID")
		return
	}

	autoReply, err := c.autoReplyService.GetAutoReply(r.Context(), claims.UserID, autoReplyID)
	if err != nil {
		autoReplyError(r, err)
		return
	}

	response.Success(r, autoReply)
}

// ListAutoReplies lists all auto-replies for the user
// GET /api/v1/auto-replies
func (c *EmailRulesController) ListAutoReplies(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	autoReplies, err := c.autoReplyService.ListAutoReplies(r.Context(), claims.UserID)
	if err != nil {
		autoReplyError(r, err)
		return
	}

	response.Success(r, autoReplies)
}

// UpdateAutoReply updates an auto-reply
// PUT /api/v1/auto-replies/:id
func (c *EmailRulesController) UpdateAutoReply(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	autoReplyID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid auto-reply ID")
		return
	}

	var req service.UpdateAutoReplyInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	autoReply, err := c.autoReplyService.UpdateAutoReply(r.Context(), claims.UserID, autoReplyID, &req)
	if err != nil {
		autoReplyError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Auto-reply updated", autoReply)
}

// DeleteAutoReply deletes an auto-reply
// DELETE /api/v1/auto-replies/:id
func (c *EmailRulesController) DeleteAutoReply(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	autoReplyID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid auto-reply ID")
		return
	}

	if err := c.autoReplyService.DeleteAutoReply(r.Context(), claims.UserID, autoReplyID); err != nil {
		autoReplyError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Auto-reply deleted", nil)
}

// ====================
// EMAIL FORWARDING
// ====================

// forwardError maps forward service errors without exposing internals.
func forwardError(r *ghttp.Request, err error) {
	var invalid *service.ForwardValidationError
	var mailInvalid *provider.MailValidationError
	switch {
	case errors.As(err, &invalid):
		response.BadRequest(r, invalid.Message)
	case errors.Is(err, service.ErrForwardNotFound):
		response.NotFound(r, "Email forward not found")
	case errors.Is(err, service.ErrForwardConflict):
		response.WithStatus(r, http.StatusConflict, http.StatusConflict, strings.TrimPrefix(err.Error(), service.ErrForwardConflict.Error()+": "), nil)
	case errors.Is(err, service.ErrForwardResendLimited):
		response.TooManyRequests(r, time.Minute, "Wait a minute between verification emails; at most 3 are sent per day")
	case errors.Is(err, service.ErrForwardVerifySendLimited):
		response.TooManyRequests(r, time.Hour, "Too many verification emails were sent recently; try again later")
	case errors.Is(err, service.ErrMonthlySendQuota):
		response.TooManyRequests(r, time.Hour, "The monthly send quota is used up")
	case errors.Is(err, service.ErrProviderNotConfigured):
		response.WithStatus(r, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "Email sending is not configured on this server", nil)
	case errors.As(err, &mailInvalid):
		response.BadRequest(r, mailInvalid.Message)
	default:
		log.Printf("Email forward request failed: %v", err)
		response.InternalError(r, "Email forward request failed")
	}
}

// CreateEmailForward creates a pending forward and emails the destination a
// verification link.
// POST /api/v1/forwards
func (c *EmailRulesController) CreateEmailForward(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	var req service.CreateEmailForwardInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid request body")
		return
	}
	forward, err := c.autoReplyService.CreateEmailForward(r.Context(), claims.UserID, claims.OrgID, &req)
	if err != nil {
		forwardError(r, err)
		return
	}
	response.Created(r, forward)
}

// VerifyEmailForward activates a forward from the link in its verification
// email. Public; limited per client IP, and every failure looks the same.
// POST /api/v1/forwards/verify
func (c *EmailRulesController) VerifyEmailForward(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.RuleForwardVerifyIP, middleware.ClientIP(r)) {
		return
	}
	var req struct {
		UUID  string `json:"uuid"`
		Token string `json:"token"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, service.ErrForwardVerifyFailed.Error())
		return
	}
	if err := c.autoReplyService.VerifyEmailForward(r.Context(), req.UUID, req.Token); err != nil {
		if !errors.Is(err, service.ErrForwardVerifyFailed) {
			log.Printf("Forward verification failed: %v", err)
		}
		response.BadRequest(r, service.ErrForwardVerifyFailed.Error())
		return
	}
	response.Success(r, map[string]bool{"verified": true})
}

// ListEmailForwards lists the user's forwards.
// GET /api/v1/forwards
func (c *EmailRulesController) ListEmailForwards(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	forwards, err := c.autoReplyService.ListEmailForwards(r.Context(), claims.UserID)
	if err != nil {
		forwardError(r, err)
		return
	}
	response.Success(r, forwards)
}

// UpdateEmailForward pauses or resumes a forward or changes keepCopy.
// PUT /api/v1/forwards/:uuid
func (c *EmailRulesController) UpdateEmailForward(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	var req service.UpdateEmailForwardInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid request body")
		return
	}
	forward, err := c.autoReplyService.UpdateEmailForward(r.Context(), claims.UserID, r.Get("uuid").String(), &req)
	if err != nil {
		forwardError(r, err)
		return
	}
	response.Success(r, forward)
}

// ResendForwardVerification sends a new verification link; the old one stops working.
// POST /api/v1/forwards/:uuid/resend-verification
func (c *EmailRulesController) ResendForwardVerification(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	forward, err := c.autoReplyService.ResendForwardVerification(r.Context(), claims.UserID, claims.OrgID, r.Get("uuid").String())
	if err != nil {
		forwardError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Verification email sent", forward)
}

// DeleteEmailForward deletes a forward.
// DELETE /api/v1/forwards/:uuid
func (c *EmailRulesController) DeleteEmailForward(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	if err := c.autoReplyService.DeleteEmailForward(r.Context(), claims.UserID, r.Get("uuid").String()); err != nil {
		forwardError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Email forward deleted", nil)
}
