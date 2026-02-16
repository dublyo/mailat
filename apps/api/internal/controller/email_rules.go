package controller

import (
	"strconv"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type EmailRulesController struct {
	rulesService     *service.EmailRulesService
	autoReplyService *service.AutoReplyService
}

func NewEmailRulesController(rulesService *service.EmailRulesService, autoReplyService *service.AutoReplyService) *EmailRulesController {
	return &EmailRulesController{
		rulesService:     rulesService,
		autoReplyService: autoReplyService,
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
		response.BadRequest(r, err.Error())
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
		response.NotFound(r, err.Error())
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
		response.InternalError(r, err.Error())
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
		response.BadRequest(r, err.Error())
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
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Auto-reply deleted", nil)
}

// ====================
// EMAIL FORWARDING
// ====================

// CreateEmailForward creates a new email forward
// POST /api/v1/forwards
func (c *EmailRulesController) CreateEmailForward(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req service.CreateEmailForwardInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	forward, err := c.autoReplyService.CreateEmailForward(r.Context(), claims.UserID, claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email forward created. Please check your email to verify.", forward)
}

// VerifyEmailForward verifies an email forward
// POST /api/v1/forwards/:id/verify
func (c *EmailRulesController) VerifyEmailForward(r *ghttp.Request) {
	forwardID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid forward ID")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if err := c.autoReplyService.VerifyEmailForward(r.Context(), forwardID, req.Token); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email forward verified and activated", nil)
}

// ListEmailForwards lists all email forwards for the user
// GET /api/v1/forwards
func (c *EmailRulesController) ListEmailForwards(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	forwards, err := c.autoReplyService.ListEmailForwards(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, forwards)
}

// DeleteEmailForward deletes an email forward
// DELETE /api/v1/forwards/:id
func (c *EmailRulesController) DeleteEmailForward(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	forwardID, err := strconv.Atoi(r.Get("id").String())
	if err != nil {
		response.BadRequest(r, "Invalid forward ID")
		return
	}

	if err := c.autoReplyService.DeleteEmailForward(r.Context(), claims.UserID, forwardID); err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email forward deleted", nil)
}
