package controller

import (
	"errors"
	"io"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// MailboxController manages mailbox users (a login that sees only its own
// mailbox) for owners and admins. Every route is session-only: no API key
// scope reaches it.
type MailboxController struct {
	mailboxes *service.MailboxService
}

func NewMailboxController(mailboxes *service.MailboxService) *MailboxController {
	return &MailboxController{mailboxes: mailboxes}
}

// List lists a domain's mailboxes with the domain's catch-all. Status is
// active, invited, invite_expired, suspended or removed; removed mailboxes
// are included only with ?removed=true.
// GET /api/v1/org/domains/:domainUuid/mailboxes
func (c *MailboxController) List(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	list, err := c.mailboxes.ListDomainMailboxes(r.Context(), a.OrgID, r.Get("domainUuid").String(), r.GetQuery("removed").Bool())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, list)
}

// Create creates a mailbox on an active, SES-verified domain. Invite mode
// mails a 72-hour setup link to an external address (the user stays pending,
// and mail to the address is kept for them); password mode creates an active
// login with the given password. Mailboxes use no seat but count toward the
// identity limit. warnings has receiving_disabled when the domain does not
// receive mail yet. 409 when the address is in use, an invite for it is
// pending, invite mode has no sending identity, or the identity limit is
// reached.
// POST /api/v1/org/domains/:domainUuid/mailboxes
func (c *MailboxController) Create(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.CreateMailboxRequest
	if !decodeBody(r, &req) {
		return
	}
	result, err := c.mailboxes.CreateMailbox(r.Context(), a, r.Get("domainUuid").String(), &req)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Created(r, result)
}

// Import creates mailboxes from a text/csv body (UTF-8, at most 1 MiB and 200
// rows) with a header row of local_part or address, name, and optionally
// invite_email, password, may_send and may_receive. Each row needs exactly one
// of invite_email or password. dryRun (default true) checks every row and
// writes nothing; dryRun=false creates each valid row on its own and reports
// created or error per row. Passwords are never echoed.
// POST /api/v1/org/domains/:domainUuid/mailboxes/import
func (c *MailboxController) Import(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	// The body may hold passwords: it is never logged.
	body, err := io.ReadAll(http.MaxBytesReader(r.Response.Writer, r.Request.Body, service.MailboxImportMaxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			response.BadRequest(r, "The CSV file is larger than 1 MiB")
			return
		}
		response.BadRequest(r, "Unable to read the CSV file")
		return
	}
	result, err := c.mailboxes.ImportCSV(r.Context(), a, r.GetRouter("domainUuid").String(), body, r.GetQuery("dryRun", true).Bool())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, result)
}

// Get returns a live mailbox with a yes/no overview (forwarding and
// auto-reply content is never shown), its send-as aliases and its open setup
// or reset link.
// GET /api/v1/org/mailboxes/:userUuid
func (c *MailboxController) Get(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	detail, err := c.mailboxes.GetMailbox(r.Context(), a.OrgID, r.Get("userUuid").String())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, detail)
}

// Update changes the name, may send, may receive (off sends new mail to the
// catch-all), the wildcard sender switch and the recovery email (empty clears
// it; the previous address is told).
// PUT /api/v1/org/mailboxes/:userUuid
func (c *MailboxController) Update(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.UpdateMailboxRequest
	if !decodeBody(r, &req) {
		return
	}
	mailbox, err := c.mailboxes.UpdateMailbox(r.Context(), a, r.Get("userUuid").String(), &req)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, mailbox)
}

// AddAlias grants a send-as alias on the mailbox's domain (no '+'). Mail to
// the alias is delivered to the mailbox. 409 when the address is an identity
// or another alias.
// POST /api/v1/org/mailboxes/:userUuid/aliases
func (c *MailboxController) AddAlias(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.AddSendAliasRequest
	if !decodeBody(r, &req) {
		return
	}
	alias, err := c.mailboxes.AddSendAlias(r.Context(), a, r.Get("userUuid").String(), req.LocalPart)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Created(r, alias)
}

// DeleteAlias removes a send-as alias; mail to it falls back to the
// catch-all. Answers 204.
// DELETE /api/v1/org/mailboxes/:userUuid/aliases/:aliasUuid
func (c *MailboxController) DeleteAlias(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	if err := c.mailboxes.DeleteSendAlias(r.Context(), a, r.Get("userUuid").String(), r.Get("aliasUuid").String()); err != nil {
		writeOrgError(r, err)
		return
	}
	r.Response.WriteHeader(http.StatusNoContent)
}

// Password sets a new password (mode set: every session and open reset link
// ends, two-factor stays on) or mails a 72-hour reset link (mode link, active
// mailboxes only) to the recovery email or the given email. The recovery
// email is told either way. 409 while the mailbox is pending (resend the
// setup link instead); 429 within 60 seconds of the last link or after 5
// links in 24 hours.
// POST /api/v1/org/mailboxes/:userUuid/password
func (c *MailboxController) Password(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.MailboxPasswordRequest
	if !decodeBody(r, &req) {
		return
	}
	userUUID := r.Get("userUuid").String()
	switch req.Mode {
	case "set":
		result, err := c.mailboxes.SetPassword(r.Context(), a, userUUID, req.Password)
		if err != nil {
			writeOrgError(r, err)
			return
		}
		response.Success(r, result)
	case "link":
		link, err := c.mailboxes.SendPasswordLink(r.Context(), a, userUUID, req.Email)
		if err != nil {
			writeOrgError(r, err)
			return
		}
		response.Success(r, service.MailboxLinkResult{Invite: link})
	default:
		response.BadRequest(r, "mode must be set or link")
	}
}

// ResetTwoFactor turns off the mailbox user's two-factor sign-in after a lost
// device; every session ends and the user enrolls again. 409 when it is off.
// POST /api/v1/org/mailboxes/:userUuid/2fa/reset
func (c *MailboxController) ResetTwoFactor(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	mailbox, err := c.mailboxes.ResetTwoFactor(r.Context(), a, r.Get("userUuid").String())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, mailbox)
}

// ResendInvite mails a fresh 72-hour setup link (the previous one stops
// working) from your sending identity. 409 when the mailbox is already set
// up; 429 within 60 seconds of the last send or after 5 sends.
// POST /api/v1/org/mailboxes/:userUuid/invite/resend
func (c *MailboxController) ResendInvite(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	link, err := c.mailboxes.ResendSetup(r.Context(), a, r.Get("userUuid").String())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, service.MailboxLinkResult{Invite: link})
}

// Suspend blocks sign-in while mail keeps arriving: sessions, API keys, push
// subscriptions, OAuth connections and reset links end, forwards pause (and
// stay paused after reactivation) and auto-replies stop.
// POST /api/v1/org/mailboxes/:userUuid/suspend
func (c *MailboxController) Suspend(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	mailbox, err := c.mailboxes.Suspend(r.Context(), a, r.Get("userUuid").String())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, mailbox)
}

// Reactivate lets a suspended mailbox user sign in again.
// POST /api/v1/org/mailboxes/:userUuid/reactivate
func (c *MailboxController) Reactivate(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	mailbox, err := c.mailboxes.Reactivate(r.Context(), a, r.Get("userUuid").String())
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, mailbox)
}

// Remove removes a pending, active or suspended mailbox: the login is
// disabled, links and aliases end, and new mail to the address falls back to
// the catch-all while old mail stays hidden. transferIdentitiesTo (an active
// user; a mailbox user only for identities on its own domain) takes the
// identities instead.
// DELETE /api/v1/org/mailboxes/:userUuid
func (c *MailboxController) Remove(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.RemoveMemberRequest
	if !decodeBody(r, &req) {
		return
	}
	result, err := c.mailboxes.Remove(r.Context(), a, r.Get("userUuid").String(), req.TransferIdentitiesTo)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, result)
}

// SetCatchAll makes one receiving identity on the domain (a mailbox or a
// staff identity) the catch-all for addresses with no mailbox, moving it from
// the previous one; an empty identityUuid removes the catch-all. Returns the
// new catch-all, or null when removed. 404 for an identity not on the domain,
// 409 when it cannot receive mail.
// PUT /api/v1/org/domains/:domainUuid/catch-all
func (c *MailboxController) SetCatchAll(r *ghttp.Request) {
	a, ok := orgActor(r)
	if !ok {
		return
	}
	var req service.SetDomainCatchAllRequest
	if !decodeBody(r, &req) {
		return
	}
	catchAll, err := c.mailboxes.SetDomainCatchAll(r.Context(), a, r.Get("domainUuid").String(), req.IdentityUUID)
	if err != nil {
		writeOrgError(r, err)
		return
	}
	response.Success(r, catchAll)
}
