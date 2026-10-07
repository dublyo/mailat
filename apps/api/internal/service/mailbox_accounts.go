package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/worker"
)

// Mailbox users (Migadu-style): an owner or admin gives an address on a
// verified domain its own login that sees only its own mail. The user row is
// created at once (pending until the setup link is used), so mail to the
// address accumulates from the start. Mailbox users use no seat.
//
// Admin password and 2FA control over a mailbox login is a deliberate, audited
// exception to "admins never gain access to another member's mail": it is
// limited to role='mailbox', revokes every session and open link, notifies
// the recovery email and never records a password.
//
// Lock order: user -> domain -> org, the same as CreateIdentity and
// SharedMailboxService.Create; accepting a mailbox link takes user -> org too.
type MailboxService struct {
	db      *sql.DB
	members *OrgMemberService
}

func NewMailboxService(db *sql.DB, members *OrgMemberService) *MailboxService {
	return &MailboxService{db: db, members: members}
}

type MailboxAccount struct {
	UserUUID       string     `json:"userUuid"`
	IdentityUUID   string     `json:"identityUuid"`
	Address        string     `json:"address"`
	Name           string     `json:"name"`
	Status         string     `json:"status"` // active, invited, invite_expired, suspended or removed
	MaySend        bool       `json:"maySend"`
	MayReceive     bool       `json:"mayReceive"`
	WildcardSender bool       `json:"wildcardSender"`
	AliasCount     int        `json:"aliasCount"`
	IsCatchAll     bool       `json:"isCatchAll"`
	LastLoginAt    *time.Time `json:"lastLoginAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// MailboxLink is a setup or password-reset link; the token is never returned.
type MailboxLink struct {
	UUID      string    `json:"uuid"`
	Purpose   string    `json:"purpose"` // mailbox_setup or password_reset
	Status    string    `json:"status"`  // pending, expired, accepted or revoked
	ExpiresAt time.Time `json:"expiresAt"`
	SendCount int       `json:"sendCount"`
}

type MailboxAccessRequest struct {
	Mode               string `json:"mode" v:"required|in:invite,password"`
	InviteEmail        string `json:"inviteEmail"`        // invite mode: the user's external address that gets the setup link
	SenderIdentityUuid string `json:"senderIdentityUuid"` // invite mode: one of your sending identities; defaults to your default one
	Password           string `json:"password"`           // password mode: 8-72 bytes
}

type CreateMailboxRequest struct {
	LocalPart  string               `json:"localPart" v:"required"` // the part before @; no '+'
	Name       string               `json:"name" v:"required|length:2,255"`
	Access     MailboxAccessRequest `json:"access"`
	MaySend    *bool                `json:"maySend"`    // default true
	MayReceive *bool                `json:"mayReceive"` // default true
}

type CreateMailboxResult struct {
	Mailbox  *MailboxAccount `json:"mailbox"`
	Warnings []string        `json:"warnings"` // receiving_disabled when the domain does not receive mail yet
}

type MailboxPasswordResult struct {
	SessionsRevoked int `json:"sessionsRevoked"`
}

// mailboxStatusSQL (aliases u: users) is the status shown for a mailbox.
const mailboxStatusSQL = `CASE u.status WHEN 'active' THEN 'active' WHEN 'suspended' THEN 'suspended' WHEN 'pending' THEN
	CASE WHEN EXISTS(SELECT 1 FROM org_invites v WHERE v.user_id=u.id AND v.purpose='mailbox_setup' AND v.accepted_at IS NULL AND v.revoked_at IS NULL AND v.expires_at>now())
	THEN 'invited' ELSE 'invite_expired' END ELSE 'removed' END`

// mailboxLocalPart is an RFC 5322 dot-atom without '+', which stays reserved
// for local+tag sending and routing.
var mailboxLocalPart = regexp.MustCompile("^[a-z0-9!#$%&'*/=?^_`{|}~-]+(\\.[a-z0-9!#$%&'*/=?^_`{|}~-]+)*$")

func mailboxAddress(localPart, domain string) (string, error) {
	lp := strings.ToLower(strings.TrimSpace(localPart))
	if strings.Contains(lp, "+") {
		return "", orgError(http.StatusBadRequest, "The address cannot contain '+'")
	}
	if len(lp) < 1 || len(lp) > 64 || !mailboxLocalPart.MatchString(lp) {
		return "", orgError(http.StatusBadRequest, "Enter a valid local part (1-64 characters, before the @)")
	}
	address := lp + "@" + strings.ToLower(domain)
	if parsed, err := mail.ParseAddress(address); err != nil || parsed.Address != address || len(address) > 255 {
		return "", orgError(http.StatusBadRequest, "Enter a valid local part (1-64 characters, before the @)")
	}
	return address, nil
}

func validMailboxName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 2 || n > 255 || strings.ContainsAny(name, "\r\n") {
		return "", orgError(http.StatusBadRequest, "name must be 2 to 255 characters")
	}
	return name, nil
}

func hashMailboxPassword(password string) (string, error) {
	if len(password) < 8 || len(password) > 72 {
		return "", orgError(http.StatusBadRequest, "password must be 8 to 72 bytes long")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password")
	}
	return string(hash), nil
}

// CreateMailbox creates the mailbox user, its default identity and either a
// setup link mailed to an external address (user pending) or an active login
// with the admin's initial password. A removed address is reused: its
// identity moves to the new user, its old mail stays with the removed one.
func (s *MailboxService) CreateMailbox(ctx context.Context, a OrgActor, domainUUID string, req *CreateMailboxRequest) (*CreateMailboxResult, error) {
	if !validUUID(domainUUID) {
		return nil, orgError(http.StatusNotFound, "Domain not found")
	}
	name, err := validMailboxName(req.Name)
	if err != nil {
		return nil, err
	}
	maySend, mayReceive := req.MaySend == nil || *req.MaySend, req.MayReceive == nil || *req.MayReceive
	var token, inviteEmail, passwordHash string
	switch req.Access.Mode {
	case "invite":
		if inviteEmail, err = normalizeAccountEmail(req.Access.InviteEmail); err != nil {
			return nil, orgError(http.StatusBadRequest, "Enter a valid invite email address")
		}
		if token, err = newInviteToken(); err != nil {
			return nil, err
		}
	case "password":
		if passwordHash, err = hashMailboxPassword(req.Access.Password); err != nil {
			return nil, err
		}
	default:
		return nil, orgError(http.StatusBadRequest, "access.mode must be invite or password")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var domainID int64
	var domainName, domainStatus string
	var sesVerified, receiving bool
	err = tx.QueryRowContext(ctx, `SELECT id,name,COALESCE(status,''),COALESCE(ses_verified,false),COALESCE(receiving_enabled,false) FROM domains WHERE uuid=$1 AND org_id=$2 FOR UPDATE`,
		domainUUID, a.OrgID).Scan(&domainID, &domainName, &domainStatus, &sesVerified, &receiving)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Domain not found")
	}
	if err != nil {
		return nil, err
	}
	if domainStatus != "active" || !sesVerified {
		return nil, orgError(http.StatusBadRequest, "Verify the domain with SES first")
	}
	address, err := mailboxAddress(req.LocalPart, domainName)
	if err != nil {
		return nil, err
	}
	if inviteEmail == address {
		return nil, orgError(http.StatusBadRequest, "Send the invite to another address; this one is the new mailbox")
	}
	if err = lockOrg(ctx, tx, a.OrgID); err != nil {
		return nil, err
	}

	// The address must be free, or an identity left behind by a removed user.
	var reuseID int64
	var kind, ownerStatus string
	var identityDomain int64
	var live bool
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.kind,i.domain_id,u.status,EXISTS(SELECT 1 FROM mailbox_accounts ma WHERE ma.identity_id=i.id AND ma.removed_at IS NULL)
		FROM identities i JOIN users u ON u.id=i.user_id WHERE lower(i.email)=$1 FOR UPDATE OF i`, address).Scan(&reuseID, &kind, &identityDomain, &ownerStatus, &live)
	switch {
	case err == sql.ErrNoRows:
		reuseID = 0
	case err != nil:
		return nil, err
	case kind != "personal" || ownerStatus != "disabled" || identityDomain != domainID || live:
		return nil, orgError(http.StatusConflict, "That address is already in use; transfer the identity from Team → Identities instead")
	}
	var isAlias bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identity_send_aliases WHERE address=$1)`, address).Scan(&isAlias); err != nil {
		return nil, err
	}
	if isAlias {
		return nil, orgError(http.StatusConflict, "That address is a send-as alias; remove the alias first")
	}
	var loginID, loginOrg int64
	var loginStatus string
	err = tx.QueryRowContext(ctx, `SELECT id,org_id,status FROM users WHERE email=$1 FOR NO KEY UPDATE`, address).Scan(&loginID, &loginOrg, &loginStatus)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return nil, err
	case loginOrg != a.OrgID || loginStatus != "disabled":
		return nil, orgError(http.StatusConflict, "This address already has a Mailat account")
	default:
		// The removed login keeps its history under a placeholder address.
		if _, err = tx.ExecContext(ctx, `UPDATE users SET email='removed+'||uuid::text||'@invalid',updated_at=now() WHERE id=$1`, loginID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE org_invites SET revoked_at=now() WHERE org_id=$1 AND email=$2 AND purpose='join' AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at<=now()`, a.OrgID, address); err != nil {
		return nil, err
	}
	var open bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM org_invites WHERE org_id=$1 AND email=$2 AND accepted_at IS NULL AND revoked_at IS NULL)`, a.OrgID, address).Scan(&open); err != nil {
		return nil, err
	}
	if open {
		return nil, orgError(http.StatusConflict, "An invite for this address is pending")
	}
	if reuseID == 0 && !s.members.cfg.DisableAppLimits {
		var limit, count int
		if err = tx.QueryRowContext(ctx, `SELECT max_identities,(SELECT count(*) FROM identities i JOIN users u ON u.id=i.user_id WHERE u.org_id=o.id) FROM organizations o WHERE o.id=$1`, a.OrgID).Scan(&limit, &count); err != nil {
			return nil, err
		}
		if limit > 0 && count >= limit {
			return nil, orgError(http.StatusConflict, "The organization identity limit is reached")
		}
	}
	var sender int64
	if token != "" {
		if sender, err = inviteSender(ctx, tx, a, req.Access.SenderIdentityUuid); err != nil {
			return nil, err
		}
	}

	status := "active"
	if token != "" {
		status = "pending"
	}
	var userID int64
	var userUUID string
	if err = tx.QueryRowContext(ctx, `INSERT INTO users(org_id,email,password_hash,name,role,status,email_verified,email_verified_at,updated_at)
		VALUES($1,$2,$3,$4,'mailbox',$5,$6,CASE WHEN $6 THEN now() END,now()) RETURNING id,uuid::text`,
		a.OrgID, address, passwordHash, name, status, token == "").Scan(&userID, &userUUID); err != nil {
		return nil, err
	}
	identityID := reuseID
	if reuseID > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE identities SET user_id=$2,display_name=$3,is_default=true,is_catch_all=false,wildcard_sender=false,can_send=$4,can_receive=$5,updated_at=now() WHERE id=$1 AND kind='personal'`,
			reuseID, userID, name, maySend, mayReceive)
	} else {
		err = tx.QueryRowContext(ctx, `INSERT INTO identities(user_id,domain_id,email,display_name,kind,is_default,can_send,can_receive,color,updated_at)
			VALUES($1,$2,$3,$4,'personal',true,$5,$6,'#3B82F6',now()) RETURNING id`, userID, domainID, address, name, maySend, mayReceive).Scan(&identityID)
	}
	if err != nil {
		return nil, mailboxConflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mailbox_accounts(user_id,org_id,identity_id,domain_id,recovery_email,created_by) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)`,
		userID, a.OrgID, identityID, domainID, inviteEmail, a.UserID); err != nil {
		return nil, mailboxConflict(err)
	}
	var payload *worker.EmailSendPayload
	if token != "" {
		var inviteID int64
		if err = tx.QueryRowContext(ctx, `INSERT INTO org_invites(org_id,email,role,token_hash,invited_by,sender_identity_id,expires_at,purpose,user_id,delivery_email)
			VALUES($1,$2,'mailbox',$3,$4,$5,now()+make_interval(secs => $6),'mailbox_setup',$7,$8) RETURNING id`,
			a.OrgID, address, hashToken(token), a.UserID, sender, mailboxLinkTTL.Seconds(), userID, inviteEmail).Scan(&inviteID); err != nil {
			return nil, mailboxConflict(err)
		}
		if payload, err = s.members.sendInvite(ctx, tx, a, inviteID, token); err != nil {
			return nil, err
		}
	}
	if err = auditTx(ctx, tx, a, "mailbox_create", "user", userUUID, "Created mailbox "+address, map[string]any{
		"address": address, "mode": req.Access.Mode, "maySend": maySend, "mayReceive": mayReceive, "reusedIdentity": reuseID > 0}); err != nil {
		return nil, err
	}
	mb, err := loadMailbox(ctx, tx, a.OrgID, userID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, mailboxConflict(err)
	}
	if payload != nil {
		s.members.dispatch(payload)
	}
	warnings := []string{}
	if !receiving {
		warnings = append(warnings, "receiving_disabled")
	}
	return &CreateMailboxResult{Mailbox: mb, Warnings: warnings}, nil
}

// mailboxConflict maps a unique violation (a concurrent create, or the
// identity/alias exclusion trigger) to 409.
func mailboxConflict(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return orgError(http.StatusConflict, "That address is already in use")
	}
	return err
}

// loadMailbox reads a live mailbox. The identity must still belong to the
// mailbox user; anything else is reported, never silently papered over.
func loadMailbox(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, orgID, userID int64) (*MailboxAccount, error) {
	var m MailboxAccount
	err := q.QueryRowContext(ctx, `SELECT u.uuid::text,i.uuid::text,i.email,COALESCE(u.name,''),`+mailboxStatusSQL+`,
		i.can_send,i.can_receive,i.wildcard_sender,(SELECT count(*) FROM identity_send_aliases a WHERE a.identity_id=i.id),i.is_catch_all,u.last_login_at,ma.created_at
		FROM mailbox_accounts ma JOIN users u ON u.id=ma.user_id JOIN identities i ON i.id=ma.identity_id AND i.user_id=ma.user_id AND i.kind='personal'
		WHERE ma.user_id=$1 AND ma.org_id=$2 AND ma.removed_at IS NULL`, userID, orgID).
		Scan(&m.UserUUID, &m.IdentityUUID, &m.Address, &m.Name, &m.Status, &m.MaySend, &m.MayReceive, &m.WildcardSender, &m.AliasCount, &m.IsCatchAll, &m.LastLoginAt, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, errMailboxInconsistent
	}
	return &m, err
}

var errMailboxInconsistent = orgError(http.StatusConflict, "mailbox is inconsistent")

type mailboxUser struct {
	id, identityID, domainID int64
	uuid, email, status      string
	recovery                 sql.NullString
}

// lockMailboxUser locks a live mailbox user of the actor's org, then the org
// (the user -> org order). FOR NO KEY UPDATE leaves ingest's foreign-key
// share locks on the user row free.
func lockMailboxUser(ctx context.Context, tx *sql.Tx, a OrgActor, userUUID string) (*mailboxUser, error) {
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Mailbox not found")
	}
	m := mailboxUser{uuid: userUUID}
	var consistent bool
	err := tx.QueryRowContext(ctx, `SELECT u.id,u.email,u.status,ma.identity_id,ma.domain_id,ma.recovery_email,
		EXISTS(SELECT 1 FROM identities i WHERE i.id=ma.identity_id AND i.user_id=u.id AND i.kind='personal')
		FROM users u JOIN mailbox_accounts ma ON ma.user_id=u.id AND ma.removed_at IS NULL
		WHERE u.uuid=$1 AND u.org_id=$2 AND u.role='mailbox' FOR NO KEY UPDATE OF u`, userUUID, a.OrgID).
		Scan(&m.id, &m.email, &m.status, &m.identityID, &m.domainID, &m.recovery, &consistent)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Mailbox not found")
	}
	if err != nil {
		return nil, err
	}
	if !consistent {
		return nil, errMailboxInconsistent
	}
	return &m, lockOrg(ctx, tx, a.OrgID)
}

// revokeSignIns ends every session and pending second-factor challenge.
func revokeSignIns(ctx context.Context, tx *sql.Tx, userID int64) (int, error) {
	res, err := tx.ExecContext(ctx, `UPDATE user_sessions SET active=false,revoked_at=now() WHERE user_id=$1 AND active`, userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = tx.ExecContext(ctx, `DELETE FROM auth_challenges WHERE user_id=$1`, userID)
	return int(n), err
}

func revokeResetLinks(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE org_invites SET revoked_at=now() WHERE user_id=$1 AND purpose='password_reset' AND accepted_at IS NULL AND revoked_at IS NULL`, userID)
	return err
}

// notifyRecovery tells the mailbox's recovery address about an admin change
// to its sign-in. It fails closed: without a way to notify, the change is not
// made.
func (s *MailboxService) notifyRecovery(ctx context.Context, tx *sql.Tx, a OrgActor, m *mailboxUser, to, subject, what string) (*worker.EmailSendPayload, error) {
	if to == "" {
		return nil, nil
	}
	sender, err := inviteSender(ctx, tx, a, "")
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("An administrator %s for the mailbox %s.\n\nIf you did not expect this, contact your administrator.\n", what, m.email)
	htmlBody := fmt.Sprintf(`<p>An administrator %s for the mailbox <strong>%s</strong>.</p><p>If you did not expect this, contact your administrator.</p>`, html.EscapeString(what), html.EscapeString(m.email))
	return s.members.sendSystemMail(ctx, tx, a, sender, to, subject, text, htmlBody, m.uuid, "mailat:notice:"+uuid.New().String())
}

func (s *MailboxService) dispatch(payloads ...*worker.EmailSendPayload) {
	for _, p := range payloads {
		if p != nil {
			s.members.dispatch(p)
		}
	}
}

// SetPassword sets a new password for an active or suspended mailbox user.
// Every session and open reset link is revoked; TOTP stays on.
func (s *MailboxService) SetPassword(ctx context.Context, a OrgActor, userUUID, password string) (*MailboxPasswordResult, error) {
	hash, err := hashMailboxPassword(password)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return nil, err
	}
	if m.status == "pending" {
		return nil, orgError(http.StatusConflict, "The mailbox is not set up yet; resend the setup link instead")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET password_hash=$2,auth_version=auth_version+1,updated_at=now() WHERE id=$1`, m.id, hash); err != nil {
		return nil, err
	}
	n, err := revokeSignIns(ctx, tx, m.id)
	if err != nil {
		return nil, err
	}
	if err = revokeResetLinks(ctx, tx, m.id); err != nil {
		return nil, err
	}
	notice, err := s.notifyRecovery(ctx, tx, a, m, m.recovery.String, "Your Mailat password was changed", "set a new password")
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "mailbox_password_set", "user", m.uuid, "Set the password of mailbox "+m.email, map[string]any{"sessionsRevoked": n}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(notice)
	return &MailboxPasswordResult{SessionsRevoked: n}, nil
}

func loadMailboxLink(ctx context.Context, tx *sql.Tx, id int64) (*MailboxLink, error) {
	var l MailboxLink
	err := tx.QueryRowContext(ctx, `SELECT uuid::text,purpose,
		CASE WHEN accepted_at IS NOT NULL THEN 'accepted' WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at<=now() THEN 'expired' ELSE 'pending' END,
		expires_at,send_count FROM org_invites WHERE id=$1`, id).Scan(&l.UUID, &l.Purpose, &l.Status, &l.ExpiresAt, &l.SendCount)
	return &l, err
}

// SendPasswordLink mails a password-reset link (72 h) to the recovery email or
// an address the admin enters. Only for active users; a new link revokes the
// previous one. At most 5 links per user in 24 h, 60 s apart. When the link
// goes elsewhere, the recovery email is told.
func (s *MailboxService) SendPasswordLink(ctx context.Context, a OrgActor, userUUID, email string) (*MailboxLink, error) {
	override := ""
	if strings.TrimSpace(email) != "" {
		var err error
		if override, err = normalizeAccountEmail(email); err != nil {
			return nil, orgError(http.StatusBadRequest, "Enter a valid email address")
		}
	}
	token, err := newInviteToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return nil, err
	}
	switch m.status {
	case "pending":
		return nil, orgError(http.StatusConflict, "The mailbox is not set up yet; resend the setup link instead")
	case "suspended":
		return nil, orgError(http.StatusConflict, "Reactivate the mailbox first")
	}
	delivery := m.recovery.String
	if override != "" {
		delivery = override
	}
	if delivery == "" {
		return nil, orgError(http.StatusBadRequest, "The mailbox has no recovery email; enter an address for the link")
	}
	if delivery == m.email {
		return nil, orgError(http.StatusBadRequest, "Send the link to another address; this one is the mailbox itself")
	}
	var recent int
	var cooling bool
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(created_at)>now()-make_interval(secs => $2),false) FROM org_invites
		WHERE user_id=$1 AND purpose='password_reset' AND created_at>now()-interval '24 hours'`, m.id, inviteResendCooldown.Seconds()).Scan(&recent, &cooling); err != nil {
		return nil, err
	}
	if cooling || recent >= inviteMaxSends {
		return nil, orgError(http.StatusTooManyRequests, "A reset link was sent recently or too many times; try again later")
	}
	if err = revokeResetLinks(ctx, tx, m.id); err != nil {
		return nil, err
	}
	sender, err := inviteSender(ctx, tx, a, "")
	if err != nil {
		return nil, err
	}
	var inviteID int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO org_invites(org_id,email,role,token_hash,invited_by,sender_identity_id,expires_at,purpose,user_id,delivery_email)
		VALUES($1,$2,'mailbox',$3,$4,$5,now()+make_interval(secs => $6),'password_reset',$7,$8) RETURNING id`,
		a.OrgID, m.email, hashToken(token), a.UserID, sender, mailboxLinkTTL.Seconds(), m.id, delivery).Scan(&inviteID); err != nil {
		return nil, mailboxConflict(err)
	}
	payload, err := s.members.sendInvite(ctx, tx, a, inviteID, token)
	if err != nil {
		return nil, err
	}
	var notice *worker.EmailSendPayload
	if m.recovery.Valid && m.recovery.String != delivery {
		if notice, err = s.notifyRecovery(ctx, tx, a, m, m.recovery.String, "A Mailat password reset link was sent", "sent a password reset link to another address"); err != nil {
			return nil, err
		}
	}
	if err = auditTx(ctx, tx, a, "mailbox_password_link", "user", m.uuid, "Sent a password reset link for mailbox "+m.email,
		map[string]any{"deliveredTo": delivery, "overridden": override != ""}); err != nil {
		return nil, err
	}
	link, err := loadMailboxLink(ctx, tx, inviteID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(payload, notice)
	return link, nil
}

// ResendSetup mails a fresh setup link (the previous one stops working) and
// restarts its 72 h expiry, from the acting admin's sending identity. 60 s
// cooldown, at most 5 sends.
func (s *MailboxService) ResendSetup(ctx context.Context, a OrgActor, userUUID string) (*MailboxLink, error) {
	token, err := newInviteToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return nil, err
	}
	if m.status != "pending" {
		return nil, orgError(http.StatusConflict, "The mailbox is already set up")
	}
	var id int64
	var sends int
	var cooling bool
	err = tx.QueryRowContext(ctx, `SELECT id,send_count,last_sent_at>now()-make_interval(secs => $2) FROM org_invites
		WHERE user_id=$1 AND purpose='mailbox_setup' AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`, m.id, inviteResendCooldown.Seconds()).Scan(&id, &sends, &cooling)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusConflict, "This mailbox has no setup link; remove and re-create it")
	}
	if err != nil {
		return nil, err
	}
	if cooling || sends >= inviteMaxSends {
		return nil, orgError(http.StatusTooManyRequests, "The setup link was sent recently or too many times")
	}
	if err = resendSender(ctx, tx, a, id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE org_invites SET token_hash=$2,expires_at=now()+make_interval(secs => $3),send_count=send_count+1,last_sent_at=now(),failed_attempts=0 WHERE id=$1`,
		id, hashToken(token), mailboxLinkTTL.Seconds()); err != nil {
		return nil, err
	}
	payload, err := s.members.sendInvite(ctx, tx, a, id, token)
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "mailbox_invite_resend", "user", m.uuid, "Resent the setup link for mailbox "+m.email, nil); err != nil {
		return nil, err
	}
	link, err := loadMailboxLink(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(payload)
	return link, nil
}

// Suspend blocks sign-in while mail keeps arriving. Sessions, challenges,
// push subscriptions, OAuth connections and reset links end; forwards pause
// and stay paused after reactivation, so a forward an attacker added never
// resumes by itself. Auto-replies stop because their owner is not active.
func (s *MailboxService) Suspend(ctx context.Context, a OrgActor, userUUID string) (*MailboxAccount, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return nil, err
	}
	if m.status != "active" {
		return nil, orgError(http.StatusConflict, "Only an active mailbox can be suspended")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET status='suspended',auth_version=auth_version+1,updated_at=now() WHERE id=$1`, m.id); err != nil {
		return nil, err
	}
	if _, err = revokeSignIns(ctx, tx, m.id); err != nil {
		return nil, err
	}
	for _, q := range []string{
		`DELETE FROM api_keys WHERE user_id=$1`,
		`DELETE FROM push_subscriptions WHERE user_id=$1`,
		`DELETE FROM oauth_connections WHERE user_id=$1`,
		`UPDATE email_forwards SET status='paused',active=false,updated_at=now() WHERE user_id=$1 AND status<>'suspended' AND (active OR status<>'paused')`,
	} {
		if _, err = tx.ExecContext(ctx, q, m.id); err != nil {
			return nil, err
		}
	}
	if err = revokeResetLinks(ctx, tx, m.id); err != nil {
		return nil, err
	}
	return s.finishStatusChange(ctx, tx, a, m, "mailbox_suspend", "Suspended mailbox ")
}

// Reactivate lets a suspended mailbox user sign in again.
func (s *MailboxService) Reactivate(ctx context.Context, a OrgActor, userUUID string) (*MailboxAccount, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return nil, err
	}
	if m.status != "suspended" {
		return nil, orgError(http.StatusConflict, "Only a suspended mailbox can be reactivated")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET status='active',updated_at=now() WHERE id=$1`, m.id); err != nil {
		return nil, err
	}
	return s.finishStatusChange(ctx, tx, a, m, "mailbox_reactivate", "Reactivated mailbox ")
}

func (s *MailboxService) finishStatusChange(ctx context.Context, tx *sql.Tx, a OrgActor, m *mailboxUser, action, description string) (*MailboxAccount, error) {
	if err := auditTx(ctx, tx, a, action, "user", m.uuid, description+m.email, nil); err != nil {
		return nil, err
	}
	mb, err := loadMailbox(ctx, tx, a.OrgID, m.id)
	if err != nil {
		return nil, err
	}
	return mb, tx.Commit()
}

// ResetTwoFactor turns off a mailbox user's TOTP and backup codes after a lost
// device (owner decision). Every session ends; the user signs in with the
// password and enrolls again.
func (s *MailboxService) ResetTwoFactor(ctx context.Context, a OrgActor, userUUID string) (*MailboxAccount, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return nil, err
	}
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT totp_enabled FROM users WHERE id=$1`, m.id).Scan(&enabled); err != nil {
		return nil, err
	}
	if !enabled {
		return nil, orgError(http.StatusConflict, "Two-factor authentication is not on for this mailbox")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET totp_enabled=false,totp_secret=NULL,totp_verified_at=NULL,totp_last_step=NULL,backup_codes='{}',
		auth_version=auth_version+1,updated_at=now() WHERE id=$1`, m.id); err != nil {
		return nil, err
	}
	if _, err = revokeSignIns(ctx, tx, m.id); err != nil {
		return nil, err
	}
	notice, err := s.notifyRecovery(ctx, tx, a, m, m.recovery.String, "Two-factor sign-in was turned off", "turned off two-factor sign-in")
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "mailbox_2fa_reset", "user", m.uuid, "Reset two-factor sign-in of mailbox "+m.email, nil); err != nil {
		return nil, err
	}
	mb, err := loadMailbox(ctx, tx, a.OrgID, m.id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(notice)
	return mb, nil
}

// Remove removes a mailbox user (pending, active or suspended) through the
// member removal: the identity stops receiving, so new mail falls back to
// the catch-all, and old mail stays hidden. transferTo may take the
// identities instead.
func (s *MailboxService) Remove(ctx context.Context, a OrgActor, userUUID, transferTo string) (*RemoveMemberResult, error) {
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Mailbox not found")
	}
	var isMailbox bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE uuid=$1 AND org_id=$2 AND role='mailbox' AND status<>'disabled')`, userUUID, a.OrgID).Scan(&isMailbox); err != nil {
		return nil, err
	}
	if !isMailbox {
		return nil, orgError(http.StatusNotFound, "Mailbox not found")
	}
	return s.members.RemoveMember(ctx, a, userUUID, transferTo)
}
