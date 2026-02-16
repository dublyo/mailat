package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// SharedMailbox represents a shared mailbox
type SharedMailbox struct {
	ID               int       `json:"id"`
	UUID             string    `json:"uuid"`
	OrgID            int       `json:"orgId"`
	Name             string    `json:"name"`
	Email            string    `json:"email"`
	Description      string    `json:"description,omitempty"`
	AutoReplyEnabled bool      `json:"autoReplyEnabled"`
	MemberCount      int       `json:"memberCount,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// SharedMailboxMember represents a member of a shared mailbox
type SharedMailboxMember struct {
	ID              int       `json:"id"`
	SharedMailboxID int       `json:"sharedMailboxId"`
	UserID          int       `json:"userId"`
	UserEmail       string    `json:"userEmail,omitempty"`
	UserName        string    `json:"userName,omitempty"`
	CanRead         bool      `json:"canRead"`
	CanSend         bool      `json:"canSend"`
	CanManage       bool      `json:"canManage"`
	CreatedAt       time.Time `json:"createdAt"`
}

// CreateSharedMailboxInput is the input for creating a shared mailbox
type CreateSharedMailboxInput struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Description string `json:"description,omitempty"`
}

// AddMemberInput is the input for adding a member
type AddMemberInput struct {
	UserID    int  `json:"userId"`
	CanRead   bool `json:"canRead"`
	CanSend   bool `json:"canSend"`
	CanManage bool `json:"canManage"`
}

// SharedMailboxService handles shared mailbox operations
type SharedMailboxService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewSharedMailboxService creates a new shared mailbox service
func NewSharedMailboxService(db *sql.DB, cfg *config.Config) *SharedMailboxService {
	return &SharedMailboxService{db: db, cfg: cfg}
}

// Create creates a new shared mailbox
func (s *SharedMailboxService) Create(ctx context.Context, orgID int64, input *CreateSharedMailboxInput) (*SharedMailbox, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if input.Email == "" {
		return nil, fmt.Errorf("email is required")
	}

	var mailbox SharedMailbox
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO shared_mailboxes (org_id, name, email, description)
		VALUES ($1, $2, $3, $4)
		RETURNING id, uuid, org_id, name, email, COALESCE(description, ''), auto_reply_enabled, created_at, updated_at
	`, orgID, input.Name, input.Email, input.Description,
	).Scan(&mailbox.ID, &mailbox.UUID, &mailbox.OrgID, &mailbox.Name, &mailbox.Email,
		&mailbox.Description, &mailbox.AutoReplyEnabled, &mailbox.CreatedAt, &mailbox.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create shared mailbox: %w", err)
	}

	return &mailbox, nil
}

// Get gets a shared mailbox by ID
func (s *SharedMailboxService) Get(ctx context.Context, orgID int64, mailboxID int) (*SharedMailbox, error) {
	var mailbox SharedMailbox
	err := s.db.QueryRowContext(ctx, `
		SELECT sm.id, sm.uuid, sm.org_id, sm.name, sm.email, COALESCE(sm.description, ''),
		       sm.auto_reply_enabled, sm.created_at, sm.updated_at,
		       (SELECT COUNT(*) FROM shared_mailbox_members WHERE shared_mailbox_id = sm.id) as member_count
		FROM shared_mailboxes sm
		WHERE sm.id = $1 AND sm.org_id = $2
	`, mailboxID, orgID).Scan(&mailbox.ID, &mailbox.UUID, &mailbox.OrgID, &mailbox.Name, &mailbox.Email,
		&mailbox.Description, &mailbox.AutoReplyEnabled, &mailbox.CreatedAt, &mailbox.UpdatedAt, &mailbox.MemberCount)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("shared mailbox not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get shared mailbox: %w", err)
	}

	return &mailbox, nil
}

// List lists all shared mailboxes for an organization
func (s *SharedMailboxService) List(ctx context.Context, orgID int64) ([]*SharedMailbox, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sm.id, sm.uuid, sm.org_id, sm.name, sm.email, COALESCE(sm.description, ''),
		       sm.auto_reply_enabled, sm.created_at, sm.updated_at,
		       (SELECT COUNT(*) FROM shared_mailbox_members WHERE shared_mailbox_id = sm.id) as member_count
		FROM shared_mailboxes sm
		WHERE sm.org_id = $1
		ORDER BY sm.name ASC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list shared mailboxes: %w", err)
	}
	defer rows.Close()

	var mailboxes []*SharedMailbox
	for rows.Next() {
		var mb SharedMailbox
		if err := rows.Scan(&mb.ID, &mb.UUID, &mb.OrgID, &mb.Name, &mb.Email,
			&mb.Description, &mb.AutoReplyEnabled, &mb.CreatedAt, &mb.UpdatedAt, &mb.MemberCount); err != nil {
			continue
		}
		mailboxes = append(mailboxes, &mb)
	}

	return mailboxes, nil
}

// ListForUser lists shared mailboxes the user has access to
func (s *SharedMailboxService) ListForUser(ctx context.Context, userID int64) ([]*SharedMailbox, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sm.id, sm.uuid, sm.org_id, sm.name, sm.email, COALESCE(sm.description, ''),
		       sm.auto_reply_enabled, sm.created_at, sm.updated_at
		FROM shared_mailboxes sm
		JOIN shared_mailbox_members smm ON smm.shared_mailbox_id = sm.id
		WHERE smm.user_id = $1
		ORDER BY sm.name ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list shared mailboxes: %w", err)
	}
	defer rows.Close()

	var mailboxes []*SharedMailbox
	for rows.Next() {
		var mb SharedMailbox
		if err := rows.Scan(&mb.ID, &mb.UUID, &mb.OrgID, &mb.Name, &mb.Email,
			&mb.Description, &mb.AutoReplyEnabled, &mb.CreatedAt, &mb.UpdatedAt); err != nil {
			continue
		}
		mailboxes = append(mailboxes, &mb)
	}

	return mailboxes, nil
}

// Update updates a shared mailbox
func (s *SharedMailboxService) Update(ctx context.Context, orgID int64, mailboxID int, name, description *string, autoReplyEnabled *bool) (*SharedMailbox, error) {
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argNum))
		args = append(args, *name)
		argNum++
	}

	if description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", argNum))
		args = append(args, *description)
		argNum++
	}

	if autoReplyEnabled != nil {
		updates = append(updates, fmt.Sprintf("auto_reply_enabled = $%d", argNum))
		args = append(args, *autoReplyEnabled)
		argNum++
	}

	if len(updates) == 0 {
		return s.Get(ctx, orgID, mailboxID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE shared_mailboxes
		SET %s
		WHERE id = $%d AND org_id = $%d
	`, joinUpdates(updates), argNum, argNum+1)

	args = append(args, mailboxID, orgID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update shared mailbox: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("shared mailbox not found")
	}

	return s.Get(ctx, orgID, mailboxID)
}

// Delete deletes a shared mailbox
func (s *SharedMailboxService) Delete(ctx context.Context, orgID int64, mailboxID int) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM shared_mailboxes WHERE id = $1 AND org_id = $2
	`, mailboxID, orgID)
	if err != nil {
		return fmt.Errorf("failed to delete shared mailbox: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("shared mailbox not found")
	}

	return nil
}

// AddMember adds a member to a shared mailbox
func (s *SharedMailboxService) AddMember(ctx context.Context, orgID int64, mailboxID int, input *AddMemberInput) (*SharedMailboxMember, error) {
	// Verify mailbox belongs to org
	var mbOrgID int
	err := s.db.QueryRowContext(ctx, `SELECT org_id FROM shared_mailboxes WHERE id = $1`, mailboxID).Scan(&mbOrgID)
	if err != nil || int64(mbOrgID) != orgID {
		return nil, fmt.Errorf("shared mailbox not found")
	}

	var member SharedMailboxMember
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO shared_mailbox_members (shared_mailbox_id, user_id, can_read, can_send, can_manage)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (shared_mailbox_id, user_id) DO UPDATE
		SET can_read = EXCLUDED.can_read, can_send = EXCLUDED.can_send, can_manage = EXCLUDED.can_manage
		RETURNING id, shared_mailbox_id, user_id, can_read, can_send, can_manage, created_at
	`, mailboxID, input.UserID, input.CanRead, input.CanSend, input.CanManage,
	).Scan(&member.ID, &member.SharedMailboxID, &member.UserID, &member.CanRead, &member.CanSend, &member.CanManage, &member.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to add member: %w", err)
	}

	return &member, nil
}

// RemoveMember removes a member from a shared mailbox
func (s *SharedMailboxService) RemoveMember(ctx context.Context, orgID int64, mailboxID, userID int) error {
	// Verify mailbox belongs to org
	var mbOrgID int
	err := s.db.QueryRowContext(ctx, `SELECT org_id FROM shared_mailboxes WHERE id = $1`, mailboxID).Scan(&mbOrgID)
	if err != nil || int64(mbOrgID) != orgID {
		return fmt.Errorf("shared mailbox not found")
	}

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM shared_mailbox_members WHERE shared_mailbox_id = $1 AND user_id = $2
	`, mailboxID, userID)
	if err != nil {
		return fmt.Errorf("failed to remove member: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("member not found")
	}

	return nil
}

// ListMembers lists all members of a shared mailbox
func (s *SharedMailboxService) ListMembers(ctx context.Context, orgID int64, mailboxID int) ([]*SharedMailboxMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT smm.id, smm.shared_mailbox_id, smm.user_id, u.email, COALESCE(u.name, ''),
		       smm.can_read, smm.can_send, smm.can_manage, smm.created_at
		FROM shared_mailbox_members smm
		JOIN users u ON u.id = smm.user_id
		JOIN shared_mailboxes sm ON sm.id = smm.shared_mailbox_id
		WHERE smm.shared_mailbox_id = $1 AND sm.org_id = $2
		ORDER BY u.email ASC
	`, mailboxID, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list members: %w", err)
	}
	defer rows.Close()

	var members []*SharedMailboxMember
	for rows.Next() {
		var m SharedMailboxMember
		if err := rows.Scan(&m.ID, &m.SharedMailboxID, &m.UserID, &m.UserEmail, &m.UserName,
			&m.CanRead, &m.CanSend, &m.CanManage, &m.CreatedAt); err != nil {
			continue
		}
		members = append(members, &m)
	}

	return members, nil
}

// HasAccess checks if a user has access to a shared mailbox
func (s *SharedMailboxService) HasAccess(ctx context.Context, userID int64, mailboxID int) (bool, bool, bool, error) {
	var canRead, canSend, canManage bool
	err := s.db.QueryRowContext(ctx, `
		SELECT can_read, can_send, can_manage
		FROM shared_mailbox_members
		WHERE shared_mailbox_id = $1 AND user_id = $2
	`, mailboxID, userID).Scan(&canRead, &canSend, &canManage)
	if err == sql.ErrNoRows {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, err
	}
	return canRead, canSend, canManage, nil
}

func joinUpdates(updates []string) string {
	result := ""
	for i, u := range updates {
		if i > 0 {
			result += ", "
		}
		result += u
	}
	return result
}
