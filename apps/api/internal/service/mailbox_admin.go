package service

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/dublyo/mailat/api/internal/worker"
)

// Admin reads and edits of mailbox users: the per-domain list, the detail
// page, the may-send/may-receive/wildcard switches and send-as aliases.

type MailboxDomain struct {
	UUID             string `json:"uuid"`
	Name             string `json:"name"`
	SESVerified      bool   `json:"sesVerified"`
	ReceivingEnabled bool   `json:"receivingEnabled"`
}

// MailboxCatchAll is the identity that gets mail for addresses on the domain
// with no mailbox, identity or alias.
type MailboxCatchAll struct {
	Email      string `json:"email"`
	OwnerEmail string `json:"ownerEmail"`
	IsMailbox  bool   `json:"isMailbox"` // the catch-all belongs to a mailbox user
}

type DomainMailboxes struct {
	Domain    MailboxDomain     `json:"domain"`
	CatchAll  *MailboxCatchAll  `json:"catchAll"` // null when the domain has no catch-all
	Mailboxes []*MailboxAccount `json:"mailboxes"`
}

// MailboxOverview says yes/no only: forwarding and auto-reply destinations or
// content are never shown to admins.
type MailboxOverview struct {
	MaySend         bool   `json:"maySend"`
	MayReceive      bool   `json:"mayReceive"`
	WildcardSender  bool   `json:"wildcardSender"`
	IsCatchAll      bool   `json:"isCatchAll"`
	ForwardsActive  bool   `json:"forwardsActive"`
	AutoReplyActive bool   `json:"autoReplyActive"`
	TwoFactor       bool   `json:"twoFactor"`
	RecoveryEmail   string `json:"recoveryEmail"` // empty when none is set
}

type MailboxAlias struct {
	UUID    string `json:"uuid"`
	Address string `json:"address"`
}

type MailboxDetail struct {
	Mailbox  *MailboxAccount `json:"mailbox"`
	Domain   MailboxDomain   `json:"domain"`
	Overview MailboxOverview `json:"overview"`
	Aliases  []MailboxAlias  `json:"aliases"`
	Invite   *MailboxLink    `json:"invite"` // the latest open setup or reset link (possibly expired); null when none
}

type UpdateMailboxRequest struct {
	Name           *string `json:"name"`           // 2-255 characters; also the identity's display name
	MaySend        *bool   `json:"maySend"`        // identity can_send
	MayReceive     *bool   `json:"mayReceive"`     // identity can_receive; off sends new mail to the catch-all
	WildcardSender *bool   `json:"wildcardSender"` // send as any unused address on the domain
	RecoveryEmail  *string `json:"recoveryEmail"`  // external address for reset links and notices; empty clears it
}

type MailboxPasswordRequest struct {
	Mode     string `json:"mode" v:"required|in:set,link"`
	Password string `json:"password"` // mode set: 8-72 bytes
	Email    string `json:"email"`    // mode link: send the link here instead of the recovery email
}

// MailboxLinkResult wraps a setup or reset link that was just mailed.
type MailboxLinkResult struct {
	Invite *MailboxLink `json:"invite"`
}

type AddSendAliasRequest struct {
	LocalPart string `json:"localPart" v:"required"` // the part before @ on the mailbox's domain; no '+'
}

// ListDomainMailboxes lists a domain's live mailboxes (and removed ones with
// removed=true) with the domain's catch-all.
func (s *MailboxService) ListDomainMailboxes(ctx context.Context, orgID int64, domainUUID string, removed bool) (*DomainMailboxes, error) {
	if !validUUID(domainUUID) {
		return nil, orgError(http.StatusNotFound, "Domain not found")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := &DomainMailboxes{Mailboxes: []*MailboxAccount{}}
	var domainID int64
	err = tx.QueryRowContext(ctx, `SELECT id,uuid::text,name,COALESCE(ses_verified,false),COALESCE(receiving_enabled,false) FROM domains WHERE uuid=$1 AND org_id=$2`, domainUUID, orgID).
		Scan(&domainID, &out.Domain.UUID, &out.Domain.Name, &out.Domain.SESVerified, &out.Domain.ReceivingEnabled)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Domain not found")
	}
	if err != nil {
		return nil, err
	}
	var ca MailboxCatchAll
	err = tx.QueryRowContext(ctx, `SELECT i.email,u.email,u.role='mailbox' FROM identities i JOIN users u ON u.id=i.user_id
		WHERE i.domain_id=$1 AND i.is_catch_all AND i.can_receive AND i.kind='personal' AND `+deliverableOwnerSQL+` ORDER BY i.id LIMIT 1`, domainID).
		Scan(&ca.Email, &ca.OwnerEmail, &ca.IsMailbox)
	switch {
	case err == nil:
		out.CatchAll = &ca
	case err != sql.ErrNoRows:
		return nil, err
	}
	// A removed mailbox keeps its row, but its identity may since belong to
	// someone else, so only live rows report the identity's switches.
	rows, err := tx.QueryContext(ctx, `SELECT u.uuid::text,ri.uuid::text,ri.email,COALESCE(u.name,''),
		CASE WHEN ma.removed_at IS NOT NULL THEN 'removed' ELSE `+mailboxStatusSQL+` END,
		ma.removed_at IS NULL AND i.id IS NULL,
		COALESCE(i.can_send,false),COALESCE(i.can_receive,false),COALESCE(i.wildcard_sender,false),
		(SELECT count(*) FROM identity_send_aliases a WHERE a.identity_id=i.id),COALESCE(i.is_catch_all,false),u.last_login_at,ma.created_at
		FROM mailbox_accounts ma JOIN users u ON u.id=ma.user_id JOIN identities ri ON ri.id=ma.identity_id
		LEFT JOIN identities i ON i.id=ma.identity_id AND i.user_id=ma.user_id AND i.kind='personal' AND ma.removed_at IS NULL
		WHERE ma.org_id=$1 AND ma.domain_id=$2 AND ($3 OR ma.removed_at IS NULL)
		ORDER BY ma.removed_at IS NOT NULL, lower(ri.email), ma.created_at`, orgID, domainID, removed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m MailboxAccount
		var inconsistent bool
		if err = rows.Scan(&m.UserUUID, &m.IdentityUUID, &m.Address, &m.Name, &m.Status, &inconsistent,
			&m.MaySend, &m.MayReceive, &m.WildcardSender, &m.AliasCount, &m.IsCatchAll, &m.LastLoginAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		if inconsistent {
			return nil, errMailboxInconsistent
		}
		out.Mailboxes = append(out.Mailboxes, &m)
	}
	return out, rows.Err()
}

// GetMailbox returns a live mailbox with its overview, aliases and open link.
func (s *MailboxService) GetMailbox(ctx context.Context, orgID int64, userUUID string) (*MailboxDetail, error) {
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Mailbox not found")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var userID, identityID int64
	d := &MailboxDetail{Aliases: []MailboxAlias{}}
	err = tx.QueryRowContext(ctx, `SELECT u.id,ma.identity_id,d.uuid::text,d.name,COALESCE(d.ses_verified,false),COALESCE(d.receiving_enabled,false),
		COALESCE(ma.recovery_email,''),u.totp_enabled,
		EXISTS(SELECT 1 FROM email_forwards f WHERE f.user_id=u.id AND f.active AND f.status='active'),
		EXISTS(SELECT 1 FROM auto_replies r WHERE r.user_id=u.id AND r.active AND r.start_date<=now() AND (r.end_date IS NULL OR r.end_date>=now()))
		FROM users u JOIN mailbox_accounts ma ON ma.user_id=u.id AND ma.removed_at IS NULL JOIN domains d ON d.id=ma.domain_id
		WHERE u.uuid=$1 AND u.org_id=$2 AND u.role='mailbox'`, userUUID, orgID).
		Scan(&userID, &identityID, &d.Domain.UUID, &d.Domain.Name, &d.Domain.SESVerified, &d.Domain.ReceivingEnabled,
			&d.Overview.RecoveryEmail, &d.Overview.TwoFactor, &d.Overview.ForwardsActive, &d.Overview.AutoReplyActive)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Mailbox not found")
	}
	if err != nil {
		return nil, err
	}
	if d.Mailbox, err = loadMailbox(ctx, tx, orgID, userID); err != nil {
		return nil, err
	}
	d.Overview.MaySend, d.Overview.MayReceive = d.Mailbox.MaySend, d.Mailbox.MayReceive
	d.Overview.WildcardSender, d.Overview.IsCatchAll = d.Mailbox.WildcardSender, d.Mailbox.IsCatchAll
	rows, err := tx.QueryContext(ctx, `SELECT uuid::text,address FROM identity_send_aliases WHERE identity_id=$1 ORDER BY address`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var al MailboxAlias
		if err = rows.Scan(&al.UUID, &al.Address); err != nil {
			return nil, err
		}
		d.Aliases = append(d.Aliases, al)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var linkID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM org_invites WHERE user_id=$1 AND purpose<>'join' AND accepted_at IS NULL AND revoked_at IS NULL
		ORDER BY created_at DESC,id DESC LIMIT 1`, userID).Scan(&linkID)
	switch {
	case err == nil:
		if d.Invite, err = loadMailboxLink(ctx, tx, linkID); err != nil {
			return nil, err
		}
	case err != sql.ErrNoRows:
		return nil, err
	}
	return d, nil
}

// UpdateMailbox changes the name, the may-send/may-receive/wildcard switches
// and the recovery email. A changed recovery email notifies the previous one.
func (s *MailboxService) UpdateMailbox(ctx context.Context, a OrgActor, userUUID string, req *UpdateMailboxRequest) (*MailboxAccount, error) {
	var name, recovery string
	var err error
	if req.Name != nil {
		if name, err = validMailboxName(*req.Name); err != nil {
			return nil, err
		}
	}
	if req.RecoveryEmail != nil && strings.TrimSpace(*req.RecoveryEmail) != "" {
		if recovery, err = normalizeAccountEmail(*req.RecoveryEmail); err != nil {
			return nil, orgError(http.StatusBadRequest, "Enter a valid recovery email address")
		}
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
	before, err := loadMailbox(ctx, tx, a.OrgID, m.id)
	if err != nil {
		return nil, err
	}
	changed := map[string]any{}
	if req.Name != nil && name != before.Name {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET name=$2,updated_at=now() WHERE id=$1`, m.id, name); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET display_name=$2,updated_at=now() WHERE id=$1`, m.identityID, name); err != nil {
			return nil, err
		}
		changed["name"] = name
	}
	maySend, mayReceive, wildcard := before.MaySend, before.MayReceive, before.WildcardSender
	if req.MaySend != nil && *req.MaySend != maySend {
		maySend, changed["maySend"] = *req.MaySend, *req.MaySend
	}
	if req.MayReceive != nil && *req.MayReceive != mayReceive {
		mayReceive, changed["mayReceive"] = *req.MayReceive, *req.MayReceive
	}
	if req.WildcardSender != nil && *req.WildcardSender != wildcard {
		wildcard, changed["wildcardSender"] = *req.WildcardSender, *req.WildcardSender
	}
	if maySend != before.MaySend || mayReceive != before.MayReceive || wildcard != before.WildcardSender {
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET can_send=$2,can_receive=$3,wildcard_sender=$4,updated_at=now() WHERE id=$1`,
			m.identityID, maySend, mayReceive, wildcard); err != nil {
			return nil, err
		}
	}
	var notice *worker.EmailSendPayload
	if req.RecoveryEmail != nil && recovery != m.recovery.String {
		if recovery == m.email {
			return nil, orgError(http.StatusBadRequest, "The recovery email must be outside this mailbox")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mailbox_accounts SET recovery_email=NULLIF($2,''),updated_at=now() WHERE user_id=$1 AND removed_at IS NULL`, m.id, recovery); err != nil {
			return nil, err
		}
		if notice, err = s.notifyRecovery(ctx, tx, a, m, m.recovery.String, "Your Mailat recovery email was changed", "changed the recovery email"); err != nil {
			return nil, err
		}
		changed["recoveryEmail"] = recovery != ""
	}
	if len(changed) > 0 {
		if err = auditTx(ctx, tx, a, "mailbox_update", "user", m.uuid, "Updated mailbox "+m.email, changed); err != nil {
			return nil, err
		}
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

// AddSendAlias lets the mailbox send as (and, through routing, receive) an
// extra address on its own domain. The address must not be an identity or
// another alias; the database trigger enforces the same under the domain lock.
func (s *MailboxService) AddSendAlias(ctx context.Context, a OrgActor, userUUID, localPart string) (*MailboxAlias, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := lockMailbox(ctx, tx, a, userUUID, true)
	if err != nil {
		return nil, err
	}
	var domain string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM domains WHERE id=$1`, m.domainID).Scan(&domain); err != nil {
		return nil, err
	}
	address, err := mailboxAddress(localPart, domain)
	if err != nil {
		return nil, err
	}
	var identity, alias bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1),EXISTS(SELECT 1 FROM identity_send_aliases WHERE address=$1)`, address).Scan(&identity, &alias); err != nil {
		return nil, err
	}
	if identity {
		return nil, orgError(http.StatusConflict, "That address is already an identity")
	}
	if alias {
		return nil, orgError(http.StatusConflict, "That address is already a send-as alias")
	}
	out := MailboxAlias{Address: address}
	if err = tx.QueryRowContext(ctx, `INSERT INTO identity_send_aliases(identity_id,address,created_by) VALUES($1,$2,$3) RETURNING uuid::text`,
		m.identityID, address, a.UserID).Scan(&out.UUID); err != nil {
		return nil, mailboxConflict(err)
	}
	if err = auditTx(ctx, tx, a, "mailbox_alias_add", "user", m.uuid, "Added send-as alias "+address+" to mailbox "+m.email, map[string]any{"alias": address}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, mailboxConflict(err)
	}
	return &out, nil
}

// DeleteSendAlias removes one of the mailbox's send-as aliases; mail to it
// falls back to the catch-all.
func (s *MailboxService) DeleteSendAlias(ctx context.Context, a OrgActor, userUUID, aliasUUID string) error {
	if !validUUID(aliasUUID) {
		return orgError(http.StatusNotFound, "Alias not found")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := lockMailboxUser(ctx, tx, a, userUUID)
	if err != nil {
		return err
	}
	var address string
	err = tx.QueryRowContext(ctx, `DELETE FROM identity_send_aliases WHERE uuid=$1 AND identity_id=$2 RETURNING address`, aliasUUID, m.identityID).Scan(&address)
	if err == sql.ErrNoRows {
		return orgError(http.StatusNotFound, "Alias not found")
	}
	if err != nil {
		return err
	}
	if err = auditTx(ctx, tx, a, "mailbox_alias_remove", "user", m.uuid, "Removed send-as alias "+address+" from mailbox "+m.email, map[string]any{"alias": address}); err != nil {
		return err
	}
	return tx.Commit()
}
