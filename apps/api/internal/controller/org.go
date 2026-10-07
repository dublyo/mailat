package controller

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// OrgController manages organization members and invites. Every /org route is
// owner/admin and human-only (no API key scope); the invite lookup and accept
// routes are public and throttled per client IP.
type OrgController struct {
	orgService *service.OrgMemberService
	limiter    *service.RateLimiter
}

func NewOrgController(orgService *service.OrgMemberService, limiter *service.RateLimiter) *OrgController {
	return &OrgController{orgService: orgService, limiter: limiter}
}

func orgActor(r *ghttp.Request) (service.OrgActor, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return service.OrgActor{}, false
	}
	// Defense in depth behind RequireOrgAdmin: membership changes need a human session.
	if claims.Role != "owner" && claims.Role != "admin" {
		response.Forbidden(r, "An organization owner or admin session is required")
		return service.OrgActor{}, false
	}
	return service.OrgActor{UserID: claims.UserID, OrgID: claims.OrgID, Role: claims.Role, IP: middleware.ClientIP(r)}, true
}

func writeOrgError(r *ghttp.Request, err error) {
	var orgErr *service.OrgError
	if errors.As(err, &orgErr) {
		response.WithStatus(r, orgErr.Status, orgErr.Status, orgErr.Message, nil)
		return
	}
	log.Printf("organization request failed: %v", err)
	response.InternalError(r, "Unable to complete the request")
}

// decodeBody accepts an empty body as an empty request.
func decodeBody(r *ghttp.Request, v any) bool {
	body := r.GetBody()
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		response.BadRequest(r, "Invalid JSON body")
		return false
	}
	return true
}

// ListMembers lists the organization's users, including removed (disabled) ones.
// Mailbox users are included only with ?includeMailboxes=true.
// GET /api/v1/org/members
func (c *OrgController) ListMembers(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	members, err := c.orgService.ListMembers(r.Context(), a.OrgID, r.Get("includeMailboxes").Bool())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, members)
}

// ChangeMemberRole sets a member's role to member or admin. Owner only; the
// owner's own role cannot change.
// PUT /api/v1/org/members/:uuid
func (c *OrgController) ChangeMemberRole(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.ChangeRoleRequest
	if !decodeBody(r, &req) {
		return
	}
	member, err := c.orgService.ChangeRole(r.Context(), a, r.Get("uuid").String(), req.Role)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, member)
}

// RemoveMember disables a member: sessions are revoked and API keys, push
// subscriptions, OAuth connections and shared-mailbox copies are deleted.
// Personal identities move to transferIdentitiesTo or are disabled; the
// removed member's mail is retained but visible to no one. An admin can remove
// members only; nobody can remove the owner or themselves.
// DELETE /api/v1/org/members/:uuid
func (c *OrgController) RemoveMember(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.RemoveMemberRequest
	if !decodeBody(r, &req) {
		return
	}
	result, err := c.orgService.RemoveMember(r.Context(), a, r.Get("uuid").String(), req.TransferIdentitiesTo)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, result)
}

// ListInvites lists invites with their status (pending, expired, accepted or
// revoked). Tokens are never returned.
// GET /api/v1/org/invites
func (c *OrgController) ListInvites(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	invites, err := c.orgService.ListInvites(r.Context(), a.OrgID)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, invites)
}

// CreateInvite emails a single-use link (valid INVITE_TTL_HOURS, default 7
// days) from senderIdentityUuid or your default sending identity. Admins may
// invite members only. 409 for a pending invite, an existing account, no free
// seat or no sending identity.
// POST /api/v1/org/invites
func (c *OrgController) CreateInvite(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.CreateInviteRequest
	if !decodeBody(r, &req) {
		return
	}
	invite, err := c.orgService.CreateInvite(r.Context(), a, &req)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Created(r, invite)
}

// ResendInvite mails a new link (the previous link stops working) and restarts
// the expiry. 429 within 60 seconds of the last send or after 5 sends.
// POST /api/v1/org/invites/:uuid/resend
func (c *OrgController) ResendInvite(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	invite, err := c.orgService.ResendInvite(r.Context(), a, r.Get("uuid").String())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, invite)
}

// RevokeInvite closes a pending invite; its link stops working.
// DELETE /api/v1/org/invites/:uuid
func (c *OrgController) RevokeInvite(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	if err := c.orgService.RevokeInvite(r.Context(), a, r.Get("uuid").String()); err != nil {
		writeOrgError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Invite revoked", nil)
}

// ListOrgIdentities lists every identity of the organization with its owner.
// It never exposes mail.
// GET /api/v1/org/identities
func (c *OrgController) ListOrgIdentities(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	identities, err := c.orgService.ListOrgIdentities(r.Context(), a.OrgID)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, identities)
}

// TransferIdentity gives a personal identity to another active member. The
// previous owner's forwards on it are paused and it leaves their auto-reply
// rules; mail already received stays with the previous owner. 400 for shared
// mailbox identities.
// PUT /api/v1/org/identities/:uuid/owner
func (c *OrgController) TransferIdentity(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.TransferIdentityRequest
	if !decodeBody(r, &req) {
		return
	}
	identity, err := c.orgService.TransferIdentity(r.Context(), a, r.Get("uuid").String(), req.UserUuid)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, identity)
}

// LookupInvite shows the organization, email, role and purpose behind an
// invite token (plus the name for a mailbox setup link). Every unusable token
// gets the same 404. Limited per client IP.
// POST /api/v1/auth/invites/lookup
func (c *OrgController) LookupInvite(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.RuleInviteLookupIP, middleware.ClientIP(r)) {
		return
	}
	var req service.InviteTokenRequest
	_ = json.Unmarshal(r.GetBody(), &req)
	invite, err := c.orgService.LookupInvite(r.Context(), req.Token)
	if err != nil {
		if !errors.Is(err, service.ErrInviteInvalid) {
			log.Printf("invite lookup failed: %v", err)
		}
		response.NotFound(r, service.ErrInviteInvalid.Error())
		return
	}
	response.Success(r, invite)
}

// AcceptInvite completes an invite, mailbox setup or password-reset link
// (password 8-72 bytes; name 2-255 characters unless the link resets a
// password) and signs the user in, or answers signedIn:false when the account
// has a second factor. Every unusable token gets the same 400; 409 when the
// organization has no free seat. Limited per client IP.
// POST /api/v1/auth/invites/accept
func (c *OrgController) AcceptInvite(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.RuleInviteAcceptIP, middleware.ClientIP(r)) {
		return
	}
	var req service.AcceptInviteRequest
	if err := json.Unmarshal(r.GetBody(), &req); err != nil {
		response.BadRequest(r, service.ErrInviteInvalid.Error())
		return
	}
	result, err := c.orgService.AcceptInvite(r.Context(), &req, middleware.ClientIP(r))
	var orgErr *service.OrgError
	switch {
	case err == nil:
		response.Success(r, result)
	case errors.As(err, &orgErr):
		response.WithStatus(r, orgErr.Status, orgErr.Status, orgErr.Message, nil)
	case errors.Is(err, service.ErrInviteInvalid):
		response.BadRequest(r, service.ErrInviteInvalid.Error())
	default:
		log.Printf("invite accept failed: %v", err)
		response.WithStatus(r, http.StatusInternalServerError, http.StatusInternalServerError, "Unable to accept the invite", nil)
	}
}
