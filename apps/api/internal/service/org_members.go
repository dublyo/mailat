package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/worker"
)

// Organization membership: owners and admins invite people by email, change
// roles, remove members and move personal identities between members. Admins
// never gain access to another member's mail.
const (
	inviteDefaultTTLHours = 168
	inviteResendCooldown  = 60 * time.Second
	inviteMaxSends        = 5
	inviteMaxFailures     = 10
)

// OrgError carries the HTTP status a controller should answer with.
type OrgError struct {
	Status  int
	Message string
}

func (e *OrgError) Error() string { return e.Message }

func orgError(status int, message string) error { return &OrgError{Status: status, Message: message} }

// ErrInviteInvalid is the single answer for every unusable invite token, so
// public callers cannot tell expired, revoked, accepted and unknown apart.
var ErrInviteInvalid = errors.New("this invite link is invalid or has expired")

// OrgActor is the authenticated owner or admin making a membership change.
type OrgActor struct {
	UserID, OrgID int64
	Role, IP      string
}

type OrgMember struct {
	UUID        string     `json:"uuid"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"` // active or disabled (removed)
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type OrgInvite struct {
	UUID      string    `json:"uuid"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"` // pending, expired, accepted or revoked
	ExpiresAt time.Time `json:"expiresAt"`
	InvitedBy string    `json:"invitedBy"` // inviter email; empty once the inviter is deleted
	SendCount int       `json:"sendCount"`
	CreatedAt time.Time `json:"createdAt"`
}

type InviteLookup struct {
	OrgName     string    `json:"orgName"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	InviterName string    `json:"inviterName"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type OrgIdentity struct {
	UUID       string `json:"uuid"`
	Email      string `json:"email"`
	Kind       string `json:"kind"` // personal or shared
	OwnerUUID  string `json:"ownerUuid"`
	OwnerEmail string `json:"ownerEmail"`
	CanSend    bool   `json:"canSend"`
	CanReceive bool   `json:"canReceive"`
	IsCatchAll bool   `json:"isCatchAll"`
}

type RemoveMemberResult struct {
	Removed               bool `json:"removed"`
	IdentitiesTransferred int  `json:"identitiesTransferred"`
	IdentitiesDisabled    int  `json:"identitiesDisabled"`
}

type CreateInviteRequest struct {
	Email              string `json:"email" v:"required|email"`
	Role               string `json:"role" v:"required|in:member,admin"`
	SenderIdentityUuid string `json:"senderIdentityUuid"` // An identity you own; defaults to your default sending identity.
}

type InviteTokenRequest struct {
	Token string `json:"token" v:"required"`
}

type AcceptInviteRequest struct {
	Token    string `json:"token" v:"required"`
	Name     string `json:"name" v:"required|length:2,255"`
	Password string `json:"password" v:"required|length:8,72"`
}

type ChangeRoleRequest struct {
	Role string `json:"role" v:"required|in:member,admin"`
}

type RemoveMemberRequest struct {
	TransferIdentitiesTo string `json:"transferIdentitiesTo"` // Active member that receives the removed user's personal identities; otherwise they are disabled.
}

type TransferIdentityRequest struct {
	UserUuid string `json:"userUuid" v:"required"`
}

type OrgMemberService struct {
	db       *sql.DB
	cfg      *config.Config
	auth     *AuthService
	sender   *TransactionalService
	dispatch func(*worker.EmailSendPayload)
}

func NewOrgMemberService(db *sql.DB, cfg *config.Config, auth *AuthService) *OrgMemberService {
	return &OrgMemberService{db: db, cfg: cfg, auth: auth, dispatch: func(*worker.EmailSendPayload) {}}
}

// SetSender enables invite mail; without it creating an invite fails.
func (s *OrgMemberService) SetSender(sender *TransactionalService) {
	s.sender, s.dispatch = sender, sender.Dispatch
}

func (s *OrgMemberService) inviteTTL() time.Duration {
	hours := s.cfg.InviteTTLHours
	if hours <= 0 {
		hours = inviteDefaultTTLHours
	}
	return time.Duration(hours) * time.Hour
}

func validUUID(v string) bool {
	_, err := uuid.Parse(v)
	return err == nil
}

// auditTx records a membership event in the same transaction as the change.
func auditTx(ctx context.Context, tx *sql.Tx, a OrgActor, action, resource, resourceID, description string, values map[string]any) error {
	var actor *int64
	if a.UserID > 0 {
		actor = &a.UserID
	}
	var data any
	if values != nil {
		b, err := json.Marshal(values)
		if err != nil {
			return err
		}
		data = string(b)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(org_id,user_id,action,resource,resource_id,description,ip_address,new_values,status) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,'success')`,
		a.OrgID, actor, action, resource, resourceID, description, clipUTF8(a.IP, 45), data)
	return err
}

// checkSeats counts active users plus open, unexpired invites (except one being
// accepted or resent) against max_users. The caller holds the org row lock.
func (s *OrgMemberService) checkSeats(ctx context.Context, tx *sql.Tx, orgID, excludeInvite int64) error {
	if s.cfg.DisableAppLimits {
		return nil
	}
	var limit, used int
	err := tx.QueryRowContext(ctx, `SELECT o.max_users,
		(SELECT count(*) FROM users WHERE org_id=o.id AND status='active') +
		(SELECT count(*) FROM org_invites WHERE org_id=o.id AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now() AND id<>$2)
		FROM organizations o WHERE o.id=$1`, orgID, excludeInvite).Scan(&limit, &used)
	if err != nil {
		return err
	}
	if limit > 0 && used >= limit {
		return orgError(http.StatusConflict, "The organization has no free seats. Remove a member or revoke an invite first.")
	}
	return nil
}

func lockOrg(ctx context.Context, tx *sql.Tx, orgID int64) error {
	_, err := tx.ExecContext(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID)
	return err
}

func newInviteToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ListMembers returns every user of the organization, including removed ones.
func (s *OrgMemberService) ListMembers(ctx context.Context, orgID int64) ([]*OrgMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uuid::text,email,COALESCE(name,''),role,status,last_login_at,created_at FROM users WHERE org_id=$1
		ORDER BY CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, status, lower(email)`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []*OrgMember{}
	for rows.Next() {
		var m OrgMember
		if err = rows.Scan(&m.UUID, &m.Email, &m.Name, &m.Role, &m.Status, &m.LastLoginAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, &m)
	}
	return members, rows.Err()
}

func (s *OrgMemberService) member(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, orgID int64, userUUID string) (*OrgMember, error) {
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	var m OrgMember
	err := q.QueryRowContext(ctx, `SELECT uuid::text,email,COALESCE(name,''),role,status,last_login_at,created_at FROM users WHERE uuid=$1 AND org_id=$2`, userUUID, orgID).
		Scan(&m.UUID, &m.Email, &m.Name, &m.Role, &m.Status, &m.LastLoginAt, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	return &m, err
}

// ChangeRole sets a member's role. Only the owner may do this, and the owner's
// own role never changes.
func (s *OrgMemberService) ChangeRole(ctx context.Context, a OrgActor, userUUID, role string) (*OrgMember, error) {
	if a.Role != "owner" {
		return nil, orgError(http.StatusForbidden, "Only the organization owner can change roles")
	}
	if role != "member" && role != "admin" {
		return nil, orgError(http.StatusBadRequest, "role must be member or admin")
	}
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	var current, status string
	err = tx.QueryRowContext(ctx, `SELECT id,role,status FROM users WHERE uuid=$1 AND org_id=$2 FOR UPDATE`, userUUID, a.OrgID).Scan(&id, &current, &status)
	if err == sql.ErrNoRows || (err == nil && status != "active") {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	if err != nil {
		return nil, err
	}
	if current == "owner" || id == a.UserID {
		return nil, orgError(http.StatusConflict, "The owner's role cannot be changed")
	}
	if current != role {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET role=$2,updated_at=now() WHERE id=$1`, id, role); err != nil {
			return nil, err
		}
		if err = auditTx(ctx, tx, a, "member_role_change", "user", userUUID, "Changed member role", map[string]any{"from": current, "to": role}); err != nil {
			return nil, err
		}
	}
	m, err := s.member(ctx, tx, a.OrgID, userUUID)
	if err != nil {
		return nil, err
	}
	return m, tx.Commit()
}

// releaseIdentities pauses the previous owner's forwards on the given
// identities and removes them from that user's auto-reply rules. A rule left
// with no identities would widen to all of the user's identities, so it is
// switched off instead.
func releaseIdentities(ctx context.Context, tx *sql.Tx, previousOwner int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_forwards SET status='paused',active=false,updated_at=now() WHERE identity_id=ANY($1) AND user_id=$2 AND status<>'suspended'`, pq.Array(ids), previousOwner); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE auto_replies SET identity_ids=ARRAY(SELECT x FROM unnest(identity_ids) x WHERE x<>ALL($1::int[])),
		active=active AND cardinality(ARRAY(SELECT x FROM unnest(identity_ids) x WHERE x<>ALL($1::int[])))>0, updated_at=now()
		WHERE user_id=$2 AND identity_ids && $1::int[]`, pq.Array(ids), previousOwner)
	return err
}

// lockIdentitiesInIngestOrder locks the user's personal identities and shared
// memberships in one pass of ascending identity id. Ingest takes FOR SHARE on
// exactly these rows (personal identity rows, shared membership rows) in that
// same order, so removal and ingest wait for each other instead of deadlocking.
func lockIdentitiesInIngestOrder(ctx context.Context, tx *sql.Tx, userID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,kind='shared' FROM identities WHERE user_id=$1 AND kind='personal'
		UNION SELECT sm.identity_id,true FROM shared_mailboxes sm JOIN shared_mailbox_members m ON m.shared_mailbox_id=sm.id
			WHERE m.user_id=$1 AND sm.identity_id IS NOT NULL
		ORDER BY 1`, userID)
	if err != nil {
		return err
	}
	type identity struct {
		id     int64
		shared bool
	}
	var ids []identity
	for rows.Next() {
		var i identity
		if err = rows.Scan(&i.id, &i.shared); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, i)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, i := range ids {
		q := `SELECT id FROM identities WHERE id=$1 AND user_id=$2 AND kind='personal' FOR UPDATE`
		if i.shared {
			q = `SELECT m.id FROM shared_mailboxes sm JOIN shared_mailbox_members m ON m.shared_mailbox_id=sm.id
				WHERE sm.identity_id=$1 AND m.user_id=$2 FOR UPDATE OF sm, m`
		}
		if _, err = tx.ExecContext(ctx, q, i.id, userID); err != nil {
			return err
		}
	}
	return nil
}

// releaseSharedMemberships locks the removed user's shared mailboxes and
// membership rows (linked ones are already locked in ingest order). Where the user is the only
// active reader of a linked mailbox, the org owner becomes a reader and
// manager, so the mailbox keeps receiving instead of silently dropping mail.
// It returns the UUIDs of the mailboxes handed over.
func releaseSharedMemberships(ctx context.Context, tx *sql.Tx, userID, owner int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT sm.id,sm.uuid::text,sm.identity_id IS NOT NULL AND m.can_read FROM shared_mailboxes sm
		JOIN shared_mailbox_members m ON m.shared_mailbox_id=sm.id WHERE m.user_id=$1
		ORDER BY sm.identity_id NULLS LAST, sm.id FOR UPDATE OF sm, m`, userID)
	if err != nil {
		return nil, err
	}
	type mailbox struct {
		id     int64
		uuid   string
		reader bool
	}
	var held []mailbox
	for rows.Next() {
		var mb mailbox
		if err = rows.Scan(&mb.id, &mb.uuid, &mb.reader); err != nil {
			rows.Close()
			return nil, err
		}
		held = append(held, mb)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	handedOver := []string{}
	for _, mb := range held {
		if !mb.reader {
			continue
		}
		var others bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shared_mailbox_members m JOIN users u ON u.id=m.user_id
			WHERE m.shared_mailbox_id=$1 AND m.user_id<>$2 AND m.can_read AND u.status='active')`, mb.id, userID).Scan(&others); err != nil {
			return nil, err
		}
		if others {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send,can_manage) VALUES($1,$2,true,false,true)
			ON CONFLICT (shared_mailbox_id,user_id) DO UPDATE SET can_read=true,can_manage=true`, mb.id, owner); err != nil {
			return nil, err
		}
		handedOver = append(handedOver, mb.uuid)
	}
	return handedOver, nil
}

// RemoveMember disables a user and hands over what they held. The owner can
// remove anyone but themselves; an admin can remove members only. Old mail keeps
// its owner and becomes invisible, never readable by anyone else.
func (s *OrgMemberService) RemoveMember(ctx context.Context, a OrgActor, userUUID, transferTo string) (*RemoveMemberResult, error) {
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	if transferTo != "" && !validUUID(transferTo) {
		return nil, orgError(http.StatusBadRequest, "transferIdentitiesTo must be an active member")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	var role, status string
	// FOR NO KEY UPDATE, not FOR UPDATE: an ingest holding this user's identity
	// or membership locks still needs FOR KEY SHARE on the row for its foreign
	// key checks (received_emails, mailbox_changes, counters). FOR UPDATE would
	// block those while removal waits on the identity lock: a deadlock.
	err = tx.QueryRowContext(ctx, `SELECT id,role,status FROM users WHERE uuid=$1 AND org_id=$2 FOR NO KEY UPDATE`, userUUID, a.OrgID).Scan(&id, &role, &status)
	if err == sql.ErrNoRows || (err == nil && status != "active") {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	if err != nil {
		return nil, err
	}
	switch {
	case id == a.UserID:
		return nil, orgError(http.StatusConflict, "You cannot remove yourself")
	case role == "owner":
		return nil, orgError(http.StatusConflict, "The owner cannot be removed")
	case a.Role != "owner" && role != "member":
		return nil, orgError(http.StatusForbidden, "Only the owner can remove an admin")
	}
	var target int64
	if transferTo != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE uuid=$1 AND org_id=$2 AND status='active' AND id<>$3 FOR SHARE`, transferTo, a.OrgID, id).Scan(&target)
		if err == sql.ErrNoRows {
			return nil, orgError(http.StatusBadRequest, "transferIdentitiesTo must be an active member")
		}
		if err != nil {
			return nil, err
		}
	}
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE org_id=$1 AND role='owner' AND status='active' ORDER BY id LIMIT 1`, a.OrgID).Scan(&owner); err != nil {
		return nil, fmt.Errorf("organization owner not found: %w", err)
	}

	// 1. Disable the account; the middleware rejects it on the next request.
	if _, err = tx.ExecContext(ctx, `UPDATE users SET status='disabled',removed_at=now(),auth_version=auth_version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return nil, err
	}
	// 2. Credentials and devices.
	for _, q := range []string{
		`UPDATE user_sessions SET active=false,revoked_at=now() WHERE user_id=$1 AND active`,
		`DELETE FROM api_keys WHERE user_id=$1`,
		`DELETE FROM push_subscriptions WHERE user_id=$1`,
		`DELETE FROM oauth_connections WHERE user_id=$1`,
	} {
		if _, err = tx.ExecContext(ctx, q, id); err != nil {
			return nil, err
		}
	}
	// 3. Shared memberships go first, then that user's copies, so an ingest
	// holding the membership lock finishes before its copy is deleted.
	if err = lockIdentitiesInIngestOrder(ctx, tx, id); err != nil {
		return nil, err
	}
	handedOver, err := releaseSharedMemberships(ctx, tx, id, owner)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM shared_mailbox_members WHERE user_id=$1`, id); err != nil {
		return nil, err
	}
	sharedCopies := `e.mailbox_owner_id=$1 AND e.identity_id IN (SELECT id FROM identities WHERE kind='shared')`
	if err = queueMailStorageCleanup(ctx, tx, sharedCopies, id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM received_emails e WHERE `+sharedCopies, id); err != nil {
		return nil, err
	}
	// 4. Shared mailboxes keep delivering under the owner as steward.
	if _, err = tx.ExecContext(ctx, `UPDATE identities SET user_id=$2,updated_at=now() WHERE kind='shared' AND user_id=$1`, id, owner); err != nil {
		return nil, err
	}
	// 5. Personal identities move to the transfer target or stop working.
	var ids []int64
	rows, err := tx.QueryContext(ctx, `SELECT id FROM identities WHERE user_id=$1 AND kind='personal' ORDER BY id FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var identityID int64
		if err = rows.Scan(&identityID); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, identityID)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := &RemoveMemberResult{Removed: true}
	if target > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET user_id=$2,is_default=false,updated_at=now() WHERE id=ANY($1)`, pq.Array(ids), target); err != nil {
			return nil, err
		}
		result.IdentitiesTransferred = len(ids)
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET can_receive=false,can_send=false,is_catch_all=false,updated_at=now() WHERE id=ANY($1)`, pq.Array(ids)); err != nil {
			return nil, err
		}
		result.IdentitiesDisabled = len(ids)
	}
	// 6. Nothing the removed user configured keeps acting on these identities;
	// a catch-all rule (no identities) has nothing left to answer for.
	if err = releaseIdentities(ctx, tx, id, ids); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auto_replies SET active=false,updated_at=now() WHERE user_id=$1 AND cardinality(COALESCE(identity_ids,'{}'))=0 AND active`, id); err != nil {
		return nil, err
	}
	// The owner learns about each handover from the alerts list (and its digest).
	if len(handedOver) > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO alerts(org_id,type,severity,title,message,data)
			SELECT $1,'shared_mailbox_handover','info','Shared mailbox handed to the owner',
				'The last reader of the shared mailbox '||i.email||' was removed from the organization. The organization owner is now its reader and manager; add members to it or delete it in Settings.',
				jsonb_build_object('sharedMailboxUuid',sm.uuid::text,'email',i.email,'removedMember',$3::text)
			FROM shared_mailboxes sm JOIN identities i ON i.id=sm.identity_id WHERE sm.uuid::text=ANY($2)`, a.OrgID, pq.Array(handedOver), userUUID); err != nil {
			return nil, err
		}
	}
	if err = auditTx(ctx, tx, a, "member_remove", "user", userUUID, "Removed member", map[string]any{
		"transferredTo": transferTo, "identitiesTransferred": result.IdentitiesTransferred, "identitiesDisabled": result.IdentitiesDisabled,
		"sharedMailboxesHandedOver": handedOver}); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// ListOrgIdentities lists every identity of the organization with its owner,
// for administration. It exposes no mail.
func (s *OrgMemberService) ListOrgIdentities(ctx context.Context, orgID int64) ([]*OrgIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.uuid::text,i.email,i.kind,u.uuid::text,u.email,COALESCE(i.can_send,false),COALESCE(i.can_receive,false),i.is_catch_all
		FROM identities i JOIN domains d ON d.id=i.domain_id JOIN users u ON u.id=i.user_id
		WHERE d.org_id=$1 ORDER BY lower(i.email)`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OrgIdentity{}
	for rows.Next() {
		var i OrgIdentity
		if err = rows.Scan(&i.UUID, &i.Email, &i.Kind, &i.OwnerUUID, &i.OwnerEmail, &i.CanSend, &i.CanReceive, &i.IsCatchAll); err != nil {
			return nil, err
		}
		out = append(out, &i)
	}
	return out, rows.Err()
}

// TransferIdentity gives a personal identity to another active member. Mail
// already received stays with the previous owner.
func (s *OrgMemberService) TransferIdentity(ctx context.Context, a OrgActor, identityUUID, userUUID string) (*OrgIdentity, error) {
	if !validUUID(identityUUID) {
		return nil, orgError(http.StatusNotFound, "Identity not found")
	}
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusBadRequest, "userUuid must be an active member")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var target int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE uuid=$1 AND org_id=$2 AND status='active' FOR SHARE`, userUUID, a.OrgID).Scan(&target)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusBadRequest, "userUuid must be an active member")
	}
	if err != nil {
		return nil, err
	}
	var id, previous int64
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.user_id,i.kind FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.uuid=$1 AND d.org_id=$2 FOR UPDATE OF i`, identityUUID, a.OrgID).Scan(&id, &previous, &kind)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Identity not found")
	}
	if err != nil {
		return nil, err
	}
	if kind != "personal" {
		return nil, orgError(http.StatusBadRequest, "Shared mailbox identities are managed through their members")
	}
	if previous != target {
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET user_id=$2,is_default=false,updated_at=now() WHERE id=$1`, id, target); err != nil {
			return nil, err
		}
		if err = releaseIdentities(ctx, tx, previous, []int64{id}); err != nil {
			return nil, err
		}
		if err = auditTx(ctx, tx, a, "identity_transfer", "identity", identityUUID, "Transferred identity", map[string]any{"toUser": userUUID}); err != nil {
			return nil, err
		}
	}
	var out OrgIdentity
	err = tx.QueryRowContext(ctx, `SELECT i.uuid::text,i.email,i.kind,u.uuid::text,u.email,COALESCE(i.can_send,false),COALESCE(i.can_receive,false),i.is_catch_all
		FROM identities i JOIN users u ON u.id=i.user_id WHERE i.id=$1`, id).Scan(&out.UUID, &out.Email, &out.Kind, &out.OwnerUUID, &out.OwnerEmail, &out.CanSend, &out.CanReceive, &out.IsCatchAll)
	if err != nil {
		return nil, err
	}
	return &out, tx.Commit()
}

const inviteColumns = `i.uuid::text,i.email,i.role,
	CASE WHEN i.accepted_at IS NOT NULL THEN 'accepted' WHEN i.revoked_at IS NOT NULL THEN 'revoked' WHEN i.expires_at<=now() THEN 'expired' ELSE 'pending' END,
	i.expires_at,COALESCE(u.email,''),i.send_count,i.created_at`

func scanInvite(row interface{ Scan(...any) error }) (*OrgInvite, error) {
	var i OrgInvite
	err := row.Scan(&i.UUID, &i.Email, &i.Role, &i.Status, &i.ExpiresAt, &i.InvitedBy, &i.SendCount, &i.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// ListInvites returns the organization's invites, newest first. Tokens are never returned.
func (s *OrgMemberService) ListInvites(ctx context.Context, orgID int64) ([]*OrgInvite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inviteColumns+` FROM org_invites i LEFT JOIN users u ON u.id=i.invited_by WHERE i.org_id=$1 ORDER BY i.created_at DESC, i.id DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OrgInvite{}
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *OrgMemberService) loadInvite(ctx context.Context, tx *sql.Tx, id int64) (*OrgInvite, error) {
	return scanInvite(tx.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM org_invites i LEFT JOIN users u ON u.id=i.invited_by WHERE i.id=$1`, id))
}

// inviteSender picks the identity that sends the invite: the chosen one, or
// the inviter's default sending identity. It must be the inviter's own.
func inviteSender(ctx context.Context, tx *sql.Tx, a OrgActor, chosen string) (int64, error) {
	var id int64
	var err error
	if chosen != "" {
		if !validUUID(chosen) {
			return 0, orgError(http.StatusBadRequest, "senderIdentityUuid must be one of your sending identities")
		}
		err = tx.QueryRowContext(ctx, `SELECT id FROM identities WHERE uuid=$1 AND user_id=$2 AND kind='personal' AND can_send`, chosen, a.UserID).Scan(&id)
		if err == sql.ErrNoRows {
			return 0, orgError(http.StatusBadRequest, "senderIdentityUuid must be one of your sending identities")
		}
		return id, err
	}
	err = tx.QueryRowContext(ctx, `SELECT i.id FROM identities i JOIN domains d ON d.id=i.domain_id
		WHERE i.user_id=$1 AND i.kind='personal' AND i.can_send AND d.status='active' ORDER BY i.is_default DESC, i.id LIMIT 1`, a.UserID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, orgError(http.StatusConflict, "Add a sending identity first")
	}
	return id, err
}

// sendInvite queues the invite mail in tx. The token travels in the URL
// fragment, so it never reaches server logs or a Referer header.
func (s *OrgMemberService) sendInvite(ctx context.Context, tx *sql.Tx, a OrgActor, inviteID int64, token string) (*worker.EmailSendPayload, error) {
	if s.sender == nil {
		return nil, orgError(http.StatusConflict, "Email sending is not configured")
	}
	var inviteUUID, email, role, orgName, inviter string
	var senderID sql.NullInt64
	var sends int
	var expires time.Time
	err := tx.QueryRowContext(ctx, `SELECT i.uuid::text,i.email,i.role,o.name,COALESCE(NULLIF(u.name,''),u.email,''),i.sender_identity_id,i.send_count,i.expires_at
		FROM org_invites i JOIN organizations o ON o.id=i.org_id LEFT JOIN users u ON u.id=i.invited_by WHERE i.id=$1`, inviteID).
		Scan(&inviteUUID, &email, &role, &orgName, &inviter, &senderID, &sends, &expires)
	if err != nil {
		return nil, err
	}
	if !senderID.Valid {
		return nil, orgError(http.StatusConflict, "Add a sending identity first")
	}
	link := strings.TrimRight(s.cfg.WebUrl, "/") + "/invite#token=" + token
	days := int(s.inviteTTL().Hours()+23) / 24
	text := fmt.Sprintf("%s invited you to join %s on Mailat as %s.\n\nTo accept, open this link within %d days and choose a password:\n%s\n\nIf you did not expect this, ignore this email.\n",
		inviter, orgName, article(role), days, link)
	htmlBody := fmt.Sprintf(`<p>%s invited you to join <strong>%s</strong> on Mailat as %s.</p><p><a href="%s">Accept the invite</a> (the link works for %d days).</p><p>If you did not expect this, ignore this email.</p>`,
		html.EscapeString(inviter), html.EscapeString(orgName), article(role), html.EscapeString(link), days)
	_, payload, err := s.sender.SendAutomated(ctx, tx, &AutomatedSend{
		OrgID: a.OrgID, IdentityID: senderID.Int64, ActingUserID: a.UserID, To: email,
		Subject: clipUTF8(inviter+" invited you to "+orgName+" on Mailat", 200), Text: text, HTML: htmlBody,
		Headers: map[string]string{"Auto-Submitted": "auto-generated", "X-Auto-Response-Suppress": "All"},
		Kind:    "invite", Ref: inviteUUID, DedupeKey: fmt.Sprintf("mailat:inv:%s:%d", inviteUUID, sends),
	})
	if err != nil {
		if errors.Is(err, ErrProviderNotConfigured) {
			return nil, orgError(http.StatusConflict, "Email sending is not configured")
		}
		if errors.Is(err, ErrSystemRecipientSuppressed) {
			return nil, orgError(http.StatusConflict, "This address is on the suppression list")
		}
		if errors.Is(err, ErrMonthlySendQuota) {
			return nil, orgError(http.StatusConflict, "The monthly sending limit is reached")
		}
		return nil, err
	}
	return payload, nil
}

func article(role string) string {
	if role == "admin" {
		return "an admin"
	}
	return "a member"
}

// CreateInvite invites an email address to the organization. Admins may only
// invite members. The token is mailed and only its hash is stored.
func (s *OrgMemberService) CreateInvite(ctx context.Context, a OrgActor, req *CreateInviteRequest) (*OrgInvite, error) {
	email, err := normalizeAccountEmail(req.Email)
	if err != nil {
		return nil, orgError(http.StatusBadRequest, "Enter a valid email address")
	}
	if req.Role != "member" && req.Role != "admin" {
		return nil, orgError(http.StatusBadRequest, "role must be member or admin")
	}
	if req.Role == "admin" && a.Role != "owner" {
		return nil, orgError(http.StatusForbidden, "Only the owner can invite an admin")
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
	if err = lockOrg(ctx, tx, a.OrgID); err != nil {
		return nil, err
	}
	var userOrg int64
	var userStatus string
	err = tx.QueryRowContext(ctx, `SELECT org_id,status FROM users WHERE email=$1`, email).Scan(&userOrg, &userStatus)
	switch {
	case err == nil && (userOrg != a.OrgID || userStatus == "active"):
		// Emails are globally unique; a removed user of this org may come back.
		return nil, orgError(http.StatusConflict, "This email already has a Mailat account")
	case err != nil && err != sql.ErrNoRows:
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE org_invites SET revoked_at=now() WHERE org_id=$1 AND email=$2 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at<=now()`, a.OrgID, email); err != nil {
		return nil, err
	}
	var open bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM org_invites WHERE org_id=$1 AND email=$2 AND accepted_at IS NULL AND revoked_at IS NULL)`, a.OrgID, email).Scan(&open); err != nil {
		return nil, err
	}
	if open {
		return nil, orgError(http.StatusConflict, "An invite for this email is already pending; resend or revoke it")
	}
	if err = s.checkSeats(ctx, tx, a.OrgID, 0); err != nil {
		return nil, err
	}
	sender, err := inviteSender(ctx, tx, a, req.SenderIdentityUuid)
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO org_invites(org_id,email,role,token_hash,invited_by,sender_identity_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+make_interval(secs => $7)) RETURNING id`,
		a.OrgID, email, req.Role, hashToken(token), a.UserID, sender, s.inviteTTL().Seconds()).Scan(&id)
	if err != nil {
		return nil, err
	}
	payload, err := s.sendInvite(ctx, tx, a, id, token)
	if err != nil {
		return nil, err
	}
	invite, err := s.loadInvite(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "invite_create", "invite", invite.UUID, "Invited "+email, map[string]any{"email": email, "role": req.Role}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(payload)
	return invite, nil
}

// lockOpenInvite locks the org, then the invite (the same order as create and
// accept), and returns the invite id.
func lockOpenInvite(ctx context.Context, tx *sql.Tx, orgID int64, inviteUUID string) (id int64, open bool, sends int, err error) {
	if !validUUID(inviteUUID) {
		return 0, false, 0, orgError(http.StatusNotFound, "Invite not found")
	}
	if err = lockOrg(ctx, tx, orgID); err != nil {
		return 0, false, 0, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id,accepted_at IS NULL AND revoked_at IS NULL,send_count FROM org_invites WHERE uuid=$1 AND org_id=$2 FOR UPDATE`, inviteUUID, orgID).Scan(&id, &open, &sends)
	if err == sql.ErrNoRows {
		return 0, false, 0, orgError(http.StatusNotFound, "Invite not found")
	}
	return id, open, sends, err
}

// ResendInvite mails a fresh link (the previous one stops working) and restarts
// the expiry. 60 s cooldown, at most 5 sends per invite.
func (s *OrgMemberService) ResendInvite(ctx context.Context, a OrgActor, inviteUUID string) (*OrgInvite, error) {
	token, err := newInviteToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, open, sends, err := lockOpenInvite(ctx, tx, a.OrgID, inviteUUID)
	if err != nil {
		return nil, err
	}
	if !open {
		return nil, orgError(http.StatusConflict, "This invite was already accepted or revoked")
	}
	var cooling bool
	if err = tx.QueryRowContext(ctx, `SELECT last_sent_at>now()-make_interval(secs => $2) FROM org_invites WHERE id=$1`, id, inviteResendCooldown.Seconds()).Scan(&cooling); err != nil {
		return nil, err
	}
	if cooling || sends >= inviteMaxSends {
		return nil, orgError(http.StatusTooManyRequests, "This invite was sent recently or too many times")
	}
	if err = s.checkSeats(ctx, tx, a.OrgID, id); err != nil {
		return nil, err
	}
	// An admin cannot resend (and so revive) an admin invite.
	var role string
	if err = tx.QueryRowContext(ctx, `SELECT role FROM org_invites WHERE id=$1`, id).Scan(&role); err != nil {
		return nil, err
	}
	if role == "admin" && a.Role != "owner" {
		return nil, orgError(http.StatusForbidden, "Only the owner can resend an admin invite")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE org_invites SET token_hash=$2,expires_at=now()+make_interval(secs => $3),send_count=send_count+1,last_sent_at=now(),failed_attempts=0 WHERE id=$1`,
		id, hashToken(token), s.inviteTTL().Seconds()); err != nil {
		return nil, err
	}
	payload, err := s.sendInvite(ctx, tx, a, id, token)
	if err != nil {
		return nil, err
	}
	invite, err := s.loadInvite(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "invite_resend", "invite", invite.UUID, "Resent invite to "+invite.Email, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(payload)
	return invite, nil
}

// RevokeInvite closes an open invite so its link stops working.
func (s *OrgMemberService) RevokeInvite(ctx context.Context, a OrgActor, inviteUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, open, _, err := lockOpenInvite(ctx, tx, a.OrgID, inviteUUID)
	if err != nil {
		return err
	}
	if !open {
		return orgError(http.StatusNotFound, "Invite not found")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE org_invites SET revoked_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if err = auditTx(ctx, tx, a, "invite_revoke", "invite", inviteUUID, "Revoked invite", nil); err != nil {
		return err
	}
	return tx.Commit()
}

type inviteRow struct {
	id, orgID      int64
	email, role    string
	hash           string
	usable         bool
	expires        time.Time
	orgName, owner string
}

// findInvite resolves a raw token. Every unusable state is ErrInviteInvalid.
func findInvite(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, token string, lock bool) (*inviteRow, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrInviteInvalid
	}
	hash := hashToken(token)
	query := `SELECT i.id,i.org_id,i.email,i.role,i.token_hash,
		i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now() AND i.failed_attempts<$2,
		i.expires_at,o.name,COALESCE(NULLIF(u.name,''),u.email,'')
		FROM org_invites i JOIN organizations o ON o.id=i.org_id LEFT JOIN users u ON u.id=i.invited_by WHERE i.token_hash=$1`
	if lock {
		query += ` FOR UPDATE OF i`
	}
	var r inviteRow
	err := q.QueryRowContext(ctx, query, hash, inviteMaxFailures).Scan(&r.id, &r.orgID, &r.email, &r.role, &r.hash, &r.usable, &r.expires, &r.orgName, &r.owner)
	if err == sql.ErrNoRows {
		return nil, ErrInviteInvalid
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(r.hash), []byte(hash)) != 1 || !r.usable {
		return nil, ErrInviteInvalid
	}
	return &r, nil
}

// LookupInvite shows the invite behind a token before it is accepted.
func (s *OrgMemberService) LookupInvite(ctx context.Context, token string) (*InviteLookup, error) {
	r, err := findInvite(ctx, s.db, token, false)
	if err != nil {
		return nil, err
	}
	return &InviteLookup{OrgName: r.orgName, Email: r.email, Role: r.role, InviterName: r.owner, ExpiresAt: r.expires}, nil
}

// AcceptInvite creates the invited user and signs them in. A removed user of
// the org with the same address stays removed, under a placeholder address,
// so the new account never inherits their mailbox.
func (s *OrgMemberService) AcceptInvite(ctx context.Context, req *AcceptInviteRequest, ip string) (*model.AuthResponse, error) {
	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 2 || n > 255 || strings.ContainsAny(name, "\r\n") {
		return nil, orgError(http.StatusBadRequest, "name must be 2 to 255 characters")
	}
	if len(req.Password) < 8 || len(req.Password) > 72 {
		return nil, orgError(http.StatusBadRequest, "password must be 8 to 72 bytes long")
	}
	// Cheap rejection before bcrypt for tokens that cannot work.
	pre, err := findInvite(ctx, s.db, req.Token, false)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockOrg(ctx, tx, pre.orgID); err != nil {
		return nil, err
	}
	inv, err := findInvite(ctx, tx, req.Token, true)
	if err != nil {
		return nil, err
	}
	if err = s.checkSeats(ctx, tx, inv.orgID, inv.id); err != nil {
		return nil, err
	}
	var userID, userOrg int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT id,org_id,status FROM users WHERE email=$1 FOR UPDATE`, inv.email).Scan(&userID, &userOrg, &status)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return nil, err
	case userOrg != inv.orgID || status == "active":
		return nil, ErrInviteInvalid
	default:
		// A login address is often reassigned to someone new, so the removed
		// account never comes back: it keeps its retained mail, identities and
		// history under a placeholder address, readable by no one, and the
		// invite creates a fresh account.
		if _, err = tx.ExecContext(ctx, `UPDATE users SET email='removed+'||uuid::text||'@invalid',updated_at=now() WHERE id=$1`, userID); err != nil {
			return nil, err
		}
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO users(org_id,email,password_hash,name,role,status,email_verified,email_verified_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'active',true,now(),now()) RETURNING id`, inv.orgID, inv.email, string(hash), name, inv.role).Scan(&userID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE org_invites SET accepted_at=now(),accepted_user_id=$2 WHERE id=$1`, inv.id, userID); err != nil {
		return nil, err
	}
	var inviteUUID string
	if err = tx.QueryRowContext(ctx, `SELECT uuid::text FROM org_invites WHERE id=$1`, inv.id).Scan(&inviteUUID); err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, OrgActor{UserID: userID, OrgID: inv.orgID, IP: ip}, "invite_accept", "invite", inviteUUID, "Accepted invite as "+inv.role, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	user, err := s.auth.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	token, err := s.auth.IssueSessionForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	return &model.AuthResponse{Token: token, User: user}, nil
}
