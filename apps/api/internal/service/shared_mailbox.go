package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// MaxSharedMailboxMembers caps per-member copies: each member stores its own
// copy of every message body.
const MaxSharedMailboxMembers = 50

// SharedMailbox is a shared identity whose mail is copied to each reading
// member. Legacy rows (no identity) are listed as inactive and deliver nothing.
type SharedMailbox struct {
	ID           int       `json:"id"`
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	Description  string    `json:"description,omitempty"`
	IdentityUUID string    `json:"identityUuid,omitempty"`
	Active       bool      `json:"active"`
	MemberCount  int       `json:"memberCount"`
	CanRead      bool      `json:"canRead"`
	CanSend      bool      `json:"canSend"`
	CanManage    bool      `json:"canManage"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// SharedMailboxMember is one member and their permissions.
type SharedMailboxMember struct {
	UserUUID  string    `json:"userUuid"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CanRead   bool      `json:"canRead"`
	CanSend   bool      `json:"canSend"`
	CanManage bool      `json:"canManage"`
	CreatedAt time.Time `json:"createdAt"`
}

type CreateSharedMailboxInput struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Description string `json:"description,omitempty"`
}

type AddMemberInput struct {
	UserUUID  string `json:"userUuid"`
	CanRead   bool   `json:"canRead"`
	CanSend   bool   `json:"canSend"`
	CanManage bool   `json:"canManage"`
}

type UpdateMemberInput struct {
	CanRead   bool `json:"canRead"`
	CanSend   bool `json:"canSend"`
	CanManage bool `json:"canManage"`
}

// SharedMailboxService manages shared mailboxes. Errors meant for the caller
// are *OrgError; anything else is internal.
type SharedMailboxService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewSharedMailboxService(db *sql.DB, cfg *config.Config) *SharedMailboxService {
	return &SharedMailboxService{db: db, cfg: cfg}
}

var errSharedMailboxNotFound = orgError(http.StatusNotFound, "Shared mailbox not found")

func uniqueViolationOn(err error, constraint string) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == constraint
}

func isOrgAdminRole(role string) bool { return role == "owner" || role == "admin" }

// The caller's own permissions are joined in as m.
const sharedMailboxSelect = `SELECT sm.id, sm.uuid::text, sm.name, sm.email, COALESCE(sm.description,''),
	COALESCE(i.uuid::text,''), sm.identity_id IS NOT NULL,
	(SELECT COUNT(*) FROM shared_mailbox_members c WHERE c.shared_mailbox_id=sm.id),
	COALESCE(m.can_read,false), COALESCE(m.can_send,false), COALESCE(m.can_manage,false), sm.created_at, sm.updated_at
	FROM shared_mailboxes sm LEFT JOIN identities i ON i.id=sm.identity_id
	LEFT JOIN shared_mailbox_members m ON m.shared_mailbox_id=sm.id AND m.user_id=$1`

func scanSharedMailbox(row interface{ Scan(...any) error }) (*SharedMailbox, error) {
	var mb SharedMailbox
	err := row.Scan(&mb.ID, &mb.UUID, &mb.Name, &mb.Email, &mb.Description, &mb.IdentityUUID, &mb.Active,
		&mb.MemberCount, &mb.CanRead, &mb.CanSend, &mb.CanManage, &mb.CreatedAt, &mb.UpdatedAt)
	return &mb, err
}

// Create makes a shared identity on an org receiving domain, its mailbox, and
// adds the creator as a member with every permission. Admin only.
func (s *SharedMailboxService) Create(ctx context.Context, a OrgActor, input *CreateSharedMailboxInput) (*SharedMailbox, error) {
	if !isOrgAdminRole(a.Role) {
		return nil, orgError(http.StatusForbidden, "Only owners and admins can create shared mailboxes")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "\r\n") {
		return nil, orgError(http.StatusBadRequest, "Name is required (at most 255 characters)")
	}
	if len(input.Description) > 2000 {
		return nil, orgError(http.StatusBadRequest, "Description must be at most 2000 characters")
	}
	raw := strings.TrimSpace(input.Email)
	parsed, err := mail.ParseAddress(raw)
	if err != nil || parsed.Address != raw || len(raw) > 255 || strings.ContainsAny(raw, "\r\n") {
		return nil, orgError(http.StatusBadRequest, "Enter a valid email address without a display name")
	}
	email := strings.ToLower(raw)
	domainName := email[strings.LastIndexByte(email, '@')+1:]

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Same lock order as identity creation: user, domain, organization.
	if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, a.UserID); err != nil {
		return nil, err
	}
	var domainID int64
	var active, sesVerified, receiving bool
	err = tx.QueryRowContext(ctx, `SELECT id,status='active',COALESCE(ses_verified,false),COALESCE(receiving_enabled,false)
		FROM domains WHERE org_id=$1 AND lower(name)=$2 FOR UPDATE`, a.OrgID, domainName).Scan(&domainID, &active, &sesVerified, &receiving)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, orgError(http.StatusBadRequest, "The address must be on one of your organization's domains")
	}
	if err != nil {
		return nil, err
	}
	if !active || !receiving || (s.cfg.EmailProvider == "ses" && !sesVerified) {
		return nil, orgError(http.StatusBadRequest, "The domain must be verified and have receiving enabled")
	}
	if !s.cfg.DisableAppLimits {
		var limit, count int
		if err = tx.QueryRowContext(ctx, `SELECT max_identities FROM organizations WHERE id=$1 FOR UPDATE`, a.OrgID).Scan(&limit); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM identities i JOIN users u ON u.id=i.user_id WHERE u.org_id=$1`, a.OrgID).Scan(&count); err != nil {
			return nil, err
		}
		if limit > 0 && count >= limit {
			return nil, orgError(http.StatusConflict, "Organization identity limit reached")
		}
	}
	var taken bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1)`, email).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, orgError(http.StatusConflict, "That address is already in use")
	}
	// Recreate path: an unlinked legacy row with this address is replaced.
	if _, err = tx.ExecContext(ctx, `DELETE FROM shared_mailboxes WHERE org_id=$1 AND lower(email)=$2 AND identity_id IS NULL`, a.OrgID, email); err != nil {
		return nil, err
	}
	var identityID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO identities(uuid,user_id,domain_id,email,display_name,kind,is_default,is_catch_all,can_send,can_receive,color,updated_at)
		VALUES($1,$2,$3,$4,$5,'shared',false,false,true,true,'#64748B',now()) RETURNING id`,
		uuid.NewString(), a.UserID, domainID, email, name).Scan(&identityID)
	if uniqueViolationOn(err, "identities_email_key") {
		return nil, orgError(http.StatusConflict, "That address is already in use")
	}
	if err != nil {
		return nil, err
	}
	var mailboxID int
	err = tx.QueryRowContext(ctx, `INSERT INTO shared_mailboxes(org_id,name,email,description,identity_id,updated_at)
		VALUES($1,$2,$3,NULLIF($4,''),$5,now()) RETURNING id`, a.OrgID, name, email, input.Description, identityID).Scan(&mailboxID)
	if uniqueViolationOn(err, "shared_mailboxes_email_key") {
		return nil, orgError(http.StatusConflict, "That address is already in use")
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send,can_manage) VALUES($1,$2,true,true,true)`, mailboxID, a.UserID); err != nil {
		return nil, err
	}
	mb, err := scanSharedMailbox(tx.QueryRowContext(ctx, sharedMailboxSelect+` WHERE sm.id=$2`, a.UserID, mailboxID))
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "shared_mailbox_create", "shared_mailbox", mb.UUID, "Created shared mailbox "+email, nil); err != nil {
		return nil, err
	}
	return mb, tx.Commit()
}

// Get returns a mailbox to an admin or one of its members.
func (s *SharedMailboxService) Get(ctx context.Context, a OrgActor, mailboxID int) (*SharedMailbox, error) {
	mb, err := scanSharedMailbox(s.db.QueryRowContext(ctx, sharedMailboxSelect+` WHERE sm.id=$2 AND sm.org_id=$3 AND ($4 OR m.id IS NOT NULL)`,
		a.UserID, mailboxID, a.OrgID, isOrgAdminRole(a.Role)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errSharedMailboxNotFound
	}
	return mb, err
}

// List returns every org mailbox to admins and only the caller's memberships
// to everyone else.
func (s *SharedMailboxService) List(ctx context.Context, a OrgActor) ([]*SharedMailbox, error) {
	rows, err := s.db.QueryContext(ctx, sharedMailboxSelect+` WHERE sm.org_id=$2 AND ($3 OR m.id IS NOT NULL) ORDER BY sm.name, sm.id`,
		a.UserID, a.OrgID, isOrgAdminRole(a.Role))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SharedMailbox{}
	for rows.Next() {
		mb, err := scanSharedMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mb)
	}
	return out, rows.Err()
}

// Delete removes the mailbox, its identity and every member copy. Admin only.
func (s *SharedMailboxService) Delete(ctx context.Context, a OrgActor, mailboxID int) error {
	if !isOrgAdminRole(a.Role) {
		return orgError(http.StatusForbidden, "Only owners and admins can delete shared mailboxes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var mbUUID string
	var identityID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT uuid::text,identity_id FROM shared_mailboxes WHERE id=$1 AND org_id=$2 FOR UPDATE`, mailboxID, a.OrgID).Scan(&mbUUID, &identityID)
	if errors.Is(err, sql.ErrNoRows) {
		return errSharedMailboxNotFound
	}
	if err != nil {
		return err
	}
	if identityID.Valid {
		if err = queueMailStorageCleanup(ctx, tx, `e.identity_id=$1`, identityID.Int64); err != nil {
			return err
		}
		// Cascades to the mailbox row, memberships, copies and arrival jobs.
		if _, err = tx.ExecContext(ctx, `DELETE FROM identities WHERE id=$1 AND kind='shared'`, identityID.Int64); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM shared_mailboxes WHERE id=$1`, mailboxID); err != nil {
		return err
	}
	if err = auditTx(ctx, tx, a, "shared_mailbox_delete", "shared_mailbox", mbUUID, "Deleted shared mailbox", nil); err != nil {
		return err
	}
	return tx.Commit()
}

// lockManaged locks the mailbox row (serializing membership changes) and
// checks that the actor is an admin or a can_manage member.
func lockManaged(ctx context.Context, tx *sql.Tx, a OrgActor, mailboxID int) (mbUUID string, identityID sql.NullInt64, err error) {
	var canManage bool
	err = tx.QueryRowContext(ctx, `SELECT sm.uuid::text, sm.identity_id, EXISTS(SELECT 1 FROM shared_mailbox_members m
			WHERE m.shared_mailbox_id=sm.id AND m.user_id=$3 AND m.can_manage)
		FROM shared_mailboxes sm WHERE sm.id=$1 AND sm.org_id=$2 FOR UPDATE OF sm`, mailboxID, a.OrgID, a.UserID).Scan(&mbUUID, &identityID, &canManage)
	if errors.Is(err, sql.ErrNoRows) {
		return "", identityID, errSharedMailboxNotFound
	}
	if err != nil {
		return "", identityID, err
	}
	if !canManage && !isOrgAdminRole(a.Role) {
		return "", identityID, orgError(http.StatusForbidden, "You cannot manage this shared mailbox")
	}
	return mbUUID, identityID, nil
}

// Mailbox users may read and send from shared mailboxes but never manage them.
var errMailboxCannotManage = orgError(http.StatusBadRequest, "Mailbox accounts cannot manage shared mailboxes")

func validPermissions(canRead, canSend bool) error {
	if !canRead && !canSend {
		return orgError(http.StatusBadRequest, "A member needs read or send access")
	}
	return nil
}

// AddMember adds an active user of the same org. New members only receive
// mail that arrives after they are added.
func (s *SharedMailboxService) AddMember(ctx context.Context, a OrgActor, mailboxID int, input *AddMemberInput) (*SharedMailboxMember, error) {
	if err := validPermissions(input.CanRead, input.CanSend); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(input.UserUUID); err != nil {
		return nil, orgError(http.StatusBadRequest, "userUuid must be an active member of this organization")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	mbUUID, identityID, err := lockManaged(ctx, tx, a, mailboxID)
	if err != nil {
		return nil, err
	}
	if !identityID.Valid {
		return nil, orgError(http.StatusConflict, "This shared mailbox is not active; recreate it first")
	}
	var userID int64
	var role string
	err = tx.QueryRowContext(ctx, `SELECT id,role FROM users WHERE uuid=$1 AND org_id=$2 AND status='active'`, input.UserUUID, a.OrgID).Scan(&userID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, orgError(http.StatusBadRequest, "userUuid must be an active member of this organization")
	}
	if err != nil {
		return nil, err
	}
	if role == "mailbox" && input.CanManage {
		return nil, errMailboxCannotManage
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shared_mailbox_members WHERE shared_mailbox_id=$1`, mailboxID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= MaxSharedMailboxMembers {
		return nil, orgError(http.StatusConflict, "A shared mailbox can have at most 50 members")
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send,can_manage)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT (shared_mailbox_id,user_id) DO NOTHING`, mailboxID, userID, input.CanRead, input.CanSend, input.CanManage)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, orgError(http.StatusConflict, "That user is already a member; update their permissions instead")
	}
	member, err := loadSharedMember(ctx, tx, mailboxID, userID)
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "shared_member_add", "shared_mailbox", mbUUID, "Added shared mailbox member "+member.Email,
		map[string]any{"user": member.UserUUID, "canRead": member.CanRead, "canSend": member.CanSend, "canManage": member.CanManage}); err != nil {
		return nil, err
	}
	return member, tx.Commit()
}

// lockMember locks the actor-managed mailbox and resolves the target member.
func lockMember(ctx context.Context, tx *sql.Tx, a OrgActor, mailboxID int, userUUID string) (mbUUID string, identityID sql.NullInt64, userID int64, canRead bool, err error) {
	if mbUUID, identityID, err = lockManaged(ctx, tx, a, mailboxID); err != nil {
		return
	}
	if _, perr := uuid.Parse(userUUID); perr != nil {
		err = orgError(http.StatusNotFound, "Member not found")
		return
	}
	err = tx.QueryRowContext(ctx, `SELECT u.id,m.can_read FROM shared_mailbox_members m JOIN users u ON u.id=m.user_id
		WHERE m.shared_mailbox_id=$1 AND u.uuid=$2`, mailboxID, userUUID).Scan(&userID, &canRead)
	if errors.Is(err, sql.ErrNoRows) {
		err = orgError(http.StatusNotFound, "Member not found")
	}
	return
}

// otherReaders counts readers other than userID. The mailbox row lock held by
// the caller keeps the count stable.
func otherReaders(ctx context.Context, tx *sql.Tx, mailboxID int, userID int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shared_mailbox_members WHERE shared_mailbox_id=$1 AND can_read AND user_id<>$2`, mailboxID, userID).Scan(&n)
	return n, err
}

// UpdateMember changes a member's permissions. A linked mailbox always keeps
// at least one reader; a member who loses read access loses its copies.
func (s *SharedMailboxService) UpdateMember(ctx context.Context, a OrgActor, mailboxID int, userUUID string, input *UpdateMemberInput) (*SharedMailboxMember, error) {
	if err := validPermissions(input.CanRead, input.CanSend); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	mbUUID, identityID, userID, wasReader, err := lockMember(ctx, tx, a, mailboxID, userUUID)
	if err != nil {
		return nil, err
	}
	if input.CanManage {
		var mailboxUser bool
		if err = tx.QueryRowContext(ctx, `SELECT role='mailbox' FROM users WHERE id=$1`, userID).Scan(&mailboxUser); err != nil {
			return nil, err
		}
		if mailboxUser {
			return nil, errMailboxCannotManage
		}
	}
	if identityID.Valid && wasReader && !input.CanRead {
		n, err := otherReaders(ctx, tx, mailboxID, userID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, orgError(http.StatusConflict, "A shared mailbox needs at least one member who can read it")
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE shared_mailbox_members SET can_read=$3,can_send=$4,can_manage=$5 WHERE shared_mailbox_id=$1 AND user_id=$2`,
		mailboxID, userID, input.CanRead, input.CanSend, input.CanManage); err != nil {
		return nil, err
	}
	// Losing read access removes the delivered copies, as removal does, but
	// keeps the member's own sent mail. The UPDATE above waits for a
	// concurrent ingest's FOR SHARE, so this DELETE sees its copies.
	if identityID.Valid && wasReader && !input.CanRead {
		copies := `e.identity_id=$1 AND e.mailbox_owner_id=$2 AND e.direction='inbound'`
		if err = queueMailStorageCleanup(ctx, tx, copies, identityID.Int64, userID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM received_emails e WHERE `+copies, identityID.Int64, userID); err != nil {
			return nil, err
		}
	}
	member, err := loadSharedMember(ctx, tx, mailboxID, userID)
	if err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "shared_member_update", "shared_mailbox", mbUUID, "Changed shared mailbox member "+member.Email,
		map[string]any{"user": member.UserUUID, "canRead": member.CanRead, "canSend": member.CanSend, "canManage": member.CanManage}); err != nil {
		return nil, err
	}
	return member, tx.Commit()
}

// RemoveMember deletes the membership, then that member's copies. Ingest holds
// FOR SHARE on the membership rows it delivers to, so the first DELETE waits
// for a concurrent ingest to commit and the second statement sees its copies.
func (s *SharedMailboxService) RemoveMember(ctx context.Context, a OrgActor, mailboxID int, userUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	mbUUID, identityID, userID, wasReader, err := lockMember(ctx, tx, a, mailboxID, userUUID)
	if err != nil {
		return err
	}
	if identityID.Valid && wasReader {
		n, err := otherReaders(ctx, tx, mailboxID, userID)
		if err != nil {
			return err
		}
		if n == 0 {
			return orgError(http.StatusConflict, "The last member who can read a shared mailbox cannot be removed")
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM shared_mailbox_members WHERE shared_mailbox_id=$1 AND user_id=$2`, mailboxID, userID); err != nil {
		return err
	}
	if identityID.Valid {
		copies := `e.identity_id=$1 AND e.mailbox_owner_id=$2`
		if err = queueMailStorageCleanup(ctx, tx, copies, identityID.Int64, userID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM received_emails e WHERE `+copies, identityID.Int64, userID); err != nil {
			return err
		}
	}
	if err = auditTx(ctx, tx, a, "shared_member_remove", "shared_mailbox", mbUUID, "Removed shared mailbox member", map[string]any{"user": userUUID}); err != nil {
		return err
	}
	return tx.Commit()
}

// ListMembers lists members to an admin or a member of the mailbox.
func (s *SharedMailboxService) ListMembers(ctx context.Context, a OrgActor, mailboxID int) ([]*SharedMailboxMember, error) {
	if _, err := s.Get(ctx, a, mailboxID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.uuid::text,u.email,COALESCE(u.name,''),m.can_read,m.can_send,m.can_manage,m.created_at
		FROM shared_mailbox_members m JOIN users u ON u.id=m.user_id WHERE m.shared_mailbox_id=$1 ORDER BY u.email`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SharedMailboxMember{}
	for rows.Next() {
		var m SharedMailboxMember
		if err = rows.Scan(&m.UserUUID, &m.Email, &m.Name, &m.CanRead, &m.CanSend, &m.CanManage, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func loadSharedMember(ctx context.Context, q queryer, mailboxID int, userID int64) (*SharedMailboxMember, error) {
	var m SharedMailboxMember
	err := q.QueryRowContext(ctx, `SELECT u.uuid::text,u.email,COALESCE(u.name,''),m.can_read,m.can_send,m.can_manage,m.created_at
		FROM shared_mailbox_members m JOIN users u ON u.id=m.user_id WHERE m.shared_mailbox_id=$1 AND m.user_id=$2`, mailboxID, userID).
		Scan(&m.UserUUID, &m.Email, &m.Name, &m.CanRead, &m.CanSend, &m.CanManage, &m.CreatedAt)
	return &m, err
}

// joinUpdates joins SET clauses; used by push subscription preferences.
func joinUpdates(updates []string) string { return strings.Join(updates, ", ") }
