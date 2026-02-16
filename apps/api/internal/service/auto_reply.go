package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// AutoReply represents an auto-reply/vacation responder configuration
type AutoReply struct {
	ID              int        `json:"id"`
	UUID            string     `json:"uuid"`
	UserID          int        `json:"userId"`
	OrgID           int        `json:"orgId"`
	Name            string     `json:"name"`
	StartDate       time.Time  `json:"startDate"`
	EndDate         *time.Time `json:"endDate,omitempty"`
	Subject         string     `json:"subject"`
	HTMLContent     string     `json:"htmlContent"`
	TextContent     string     `json:"textContent,omitempty"`
	ReplyOnce       bool       `json:"replyOnce"`
	ReplyToAll      bool       `json:"replyToAll"`
	ExcludePatterns []string   `json:"excludePatterns,omitempty"`
	IdentityIDs     []int      `json:"identityIds,omitempty"`
	Active          bool       `json:"active"`
	ReplyCount      int        `json:"replyCount"`
	LastRepliedAt   *time.Time `json:"lastRepliedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// CreateAutoReplyInput is the input for creating an auto-reply
type CreateAutoReplyInput struct {
	Name            string     `json:"name"`
	StartDate       time.Time  `json:"startDate"`
	EndDate         *time.Time `json:"endDate,omitempty"`
	Subject         string     `json:"subject"`
	HTMLContent     string     `json:"htmlContent"`
	TextContent     string     `json:"textContent,omitempty"`
	ReplyOnce       bool       `json:"replyOnce"`
	ReplyToAll      bool       `json:"replyToAll"`
	ExcludePatterns []string   `json:"excludePatterns,omitempty"`
	IdentityIDs     []int      `json:"identityIds,omitempty"`
	Active          bool       `json:"active"`
}

// UpdateAutoReplyInput is the input for updating an auto-reply
type UpdateAutoReplyInput struct {
	Name            *string    `json:"name,omitempty"`
	StartDate       *time.Time `json:"startDate,omitempty"`
	EndDate         *time.Time `json:"endDate,omitempty"`
	Subject         *string    `json:"subject,omitempty"`
	HTMLContent     *string    `json:"htmlContent,omitempty"`
	TextContent     *string    `json:"textContent,omitempty"`
	ReplyOnce       *bool      `json:"replyOnce,omitempty"`
	ReplyToAll      *bool      `json:"replyToAll,omitempty"`
	ExcludePatterns *[]string  `json:"excludePatterns,omitempty"`
	IdentityIDs     *[]int     `json:"identityIds,omitempty"`
	Active          *bool      `json:"active,omitempty"`
}

// EmailForward represents an email forwarding configuration
type EmailForward struct {
	ID              int        `json:"id"`
	UUID            string     `json:"uuid"`
	UserID          int        `json:"userId"`
	OrgID           int        `json:"orgId"`
	IdentityID      int        `json:"identityId"`
	ForwardTo       string     `json:"forwardTo"`
	KeepCopy        bool       `json:"keepCopy"`
	Active          bool       `json:"active"`
	Verified        bool       `json:"verified"`
	VerifiedAt      *time.Time `json:"verifiedAt,omitempty"`
	ForwardCount    int        `json:"forwardCount"`
	LastForwardedAt *time.Time `json:"lastForwardedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// CreateEmailForwardInput is the input for creating an email forward
type CreateEmailForwardInput struct {
	IdentityID int    `json:"identityId"`
	ForwardTo  string `json:"forwardTo"`
	KeepCopy   bool   `json:"keepCopy"`
}

// AutoReplyService handles auto-reply and forwarding operations
type AutoReplyService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewAutoReplyService creates a new auto-reply service
func NewAutoReplyService(db *sql.DB, cfg *config.Config) *AutoReplyService {
	return &AutoReplyService{db: db, cfg: cfg}
}

// CreateAutoReply creates a new auto-reply configuration
func (s *AutoReplyService) CreateAutoReply(ctx context.Context, userID, orgID int64, input *CreateAutoReplyInput) (*AutoReply, error) {
	if input.Subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	if input.HTMLContent == "" {
		return nil, fmt.Errorf("HTML content is required")
	}

	// Convert arrays to PostgreSQL array format
	excludePatterns := "{}"
	if len(input.ExcludePatterns) > 0 {
		excludePatterns = "{" + strings.Join(input.ExcludePatterns, ",") + "}"
	}

	identityIDs := "{}"
	if len(input.IdentityIDs) > 0 {
		idStrs := make([]string, len(input.IdentityIDs))
		for i, id := range input.IdentityIDs {
			idStrs[i] = fmt.Sprintf("%d", id)
		}
		identityIDs = "{" + strings.Join(idStrs, ",") + "}"
	}

	var autoReply AutoReply
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO auto_replies (user_id, org_id, name, start_date, end_date, subject, html_content, text_content, reply_once, reply_to_all, exclude_patterns, identity_ids, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, uuid, user_id, org_id, name, start_date, end_date, subject, html_content, COALESCE(text_content, ''), reply_once, reply_to_all, exclude_patterns, identity_ids, active, reply_count, last_replied_at, created_at, updated_at
	`, userID, orgID, input.Name, input.StartDate, input.EndDate, input.Subject, input.HTMLContent, input.TextContent,
		input.ReplyOnce, input.ReplyToAll, excludePatterns, identityIDs, input.Active,
	).Scan(&autoReply.ID, &autoReply.UUID, &autoReply.UserID, &autoReply.OrgID, &autoReply.Name,
		&autoReply.StartDate, &autoReply.EndDate, &autoReply.Subject, &autoReply.HTMLContent, &autoReply.TextContent,
		&autoReply.ReplyOnce, &autoReply.ReplyToAll, pgArr(&autoReply.ExcludePatterns), pgArr(&autoReply.IdentityIDs),
		&autoReply.Active, &autoReply.ReplyCount, &autoReply.LastRepliedAt, &autoReply.CreatedAt, &autoReply.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create auto-reply: %w", err)
	}

	return &autoReply, nil
}

// pgArr is a helper for scanning PostgreSQL arrays
func pgArr[T any](dest *[]T) interface{} {
	return &pgArray[T]{dest: dest}
}

type pgArray[T any] struct {
	dest *[]T
}

func (a *pgArray[T]) Scan(src interface{}) error {
	if src == nil {
		*a.dest = nil
		return nil
	}

	switch v := src.(type) {
	case []byte:
		return a.parseArray(string(v))
	case string:
		return a.parseArray(v)
	default:
		return fmt.Errorf("unsupported type for pgArray: %T", src)
	}
}

func (a *pgArray[T]) parseArray(s string) error {
	// Handle empty array
	if s == "{}" || s == "" {
		*a.dest = []T{}
		return nil
	}

	// Remove braces
	s = strings.Trim(s, "{}")

	// Split by comma (simplistic, doesn't handle quoted values with commas)
	parts := strings.Split(s, ",")

	result := make([]T, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// Type assertion based on T
		var val interface{}
		switch any((*a.dest)).(type) {
		case *[]string:
			val = strings.Trim(p, "\"")
		case *[]int:
			var i int
			fmt.Sscanf(p, "%d", &i)
			val = i
		default:
			val = p
		}

		result = append(result, val.(T))
	}

	*a.dest = result
	return nil
}

// GetAutoReply gets an auto-reply by ID
func (s *AutoReplyService) GetAutoReply(ctx context.Context, userID int64, autoReplyID int) (*AutoReply, error) {
	var autoReply AutoReply

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, start_date, end_date, subject, html_content, COALESCE(text_content, ''), reply_once, reply_to_all, exclude_patterns, identity_ids, active, reply_count, last_replied_at, created_at, updated_at
		FROM auto_replies
		WHERE id = $1 AND user_id = $2
	`, autoReplyID, userID).Scan(&autoReply.ID, &autoReply.UUID, &autoReply.UserID, &autoReply.OrgID, &autoReply.Name,
		&autoReply.StartDate, &autoReply.EndDate, &autoReply.Subject, &autoReply.HTMLContent, &autoReply.TextContent,
		&autoReply.ReplyOnce, &autoReply.ReplyToAll, pgArr(&autoReply.ExcludePatterns), pgArr(&autoReply.IdentityIDs),
		&autoReply.Active, &autoReply.ReplyCount, &autoReply.LastRepliedAt, &autoReply.CreatedAt, &autoReply.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("auto-reply not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get auto-reply: %w", err)
	}

	return &autoReply, nil
}

// ListAutoReplies lists all auto-replies for a user
func (s *AutoReplyService) ListAutoReplies(ctx context.Context, userID int64) ([]*AutoReply, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, start_date, end_date, subject, html_content, COALESCE(text_content, ''), reply_once, reply_to_all, exclude_patterns, identity_ids, active, reply_count, last_replied_at, created_at, updated_at
		FROM auto_replies
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list auto-replies: %w", err)
	}
	defer rows.Close()

	var autoReplies []*AutoReply
	for rows.Next() {
		var ar AutoReply
		if err := rows.Scan(&ar.ID, &ar.UUID, &ar.UserID, &ar.OrgID, &ar.Name,
			&ar.StartDate, &ar.EndDate, &ar.Subject, &ar.HTMLContent, &ar.TextContent,
			&ar.ReplyOnce, &ar.ReplyToAll, pgArr(&ar.ExcludePatterns), pgArr(&ar.IdentityIDs),
			&ar.Active, &ar.ReplyCount, &ar.LastRepliedAt, &ar.CreatedAt, &ar.UpdatedAt); err != nil {
			continue
		}
		autoReplies = append(autoReplies, &ar)
	}

	return autoReplies, nil
}

// UpdateAutoReply updates an auto-reply
func (s *AutoReplyService) UpdateAutoReply(ctx context.Context, userID int64, autoReplyID int, input *UpdateAutoReplyInput) (*AutoReply, error) {
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if input.Name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argNum))
		args = append(args, *input.Name)
		argNum++
	}

	if input.StartDate != nil {
		updates = append(updates, fmt.Sprintf("start_date = $%d", argNum))
		args = append(args, *input.StartDate)
		argNum++
	}

	if input.EndDate != nil {
		updates = append(updates, fmt.Sprintf("end_date = $%d", argNum))
		args = append(args, *input.EndDate)
		argNum++
	}

	if input.Subject != nil {
		updates = append(updates, fmt.Sprintf("subject = $%d", argNum))
		args = append(args, *input.Subject)
		argNum++
	}

	if input.HTMLContent != nil {
		updates = append(updates, fmt.Sprintf("html_content = $%d", argNum))
		args = append(args, *input.HTMLContent)
		argNum++
	}

	if input.TextContent != nil {
		updates = append(updates, fmt.Sprintf("text_content = $%d", argNum))
		args = append(args, *input.TextContent)
		argNum++
	}

	if input.ReplyOnce != nil {
		updates = append(updates, fmt.Sprintf("reply_once = $%d", argNum))
		args = append(args, *input.ReplyOnce)
		argNum++
	}

	if input.ReplyToAll != nil {
		updates = append(updates, fmt.Sprintf("reply_to_all = $%d", argNum))
		args = append(args, *input.ReplyToAll)
		argNum++
	}

	if input.ExcludePatterns != nil {
		excludePatterns := "{}"
		if len(*input.ExcludePatterns) > 0 {
			excludePatterns = "{" + strings.Join(*input.ExcludePatterns, ",") + "}"
		}
		updates = append(updates, fmt.Sprintf("exclude_patterns = $%d", argNum))
		args = append(args, excludePatterns)
		argNum++
	}

	if input.IdentityIDs != nil {
		identityIDs := "{}"
		if len(*input.IdentityIDs) > 0 {
			idStrs := make([]string, len(*input.IdentityIDs))
			for i, id := range *input.IdentityIDs {
				idStrs[i] = fmt.Sprintf("%d", id)
			}
			identityIDs = "{" + strings.Join(idStrs, ",") + "}"
		}
		updates = append(updates, fmt.Sprintf("identity_ids = $%d", argNum))
		args = append(args, identityIDs)
		argNum++
	}

	if input.Active != nil {
		updates = append(updates, fmt.Sprintf("active = $%d", argNum))
		args = append(args, *input.Active)
		argNum++
	}

	if len(updates) == 0 {
		return s.GetAutoReply(ctx, userID, autoReplyID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE auto_replies
		SET %s
		WHERE id = $%d AND user_id = $%d
	`, strings.Join(updates, ", "), argNum, argNum+1)

	args = append(args, autoReplyID, userID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update auto-reply: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("auto-reply not found")
	}

	return s.GetAutoReply(ctx, userID, autoReplyID)
}

// DeleteAutoReply deletes an auto-reply
func (s *AutoReplyService) DeleteAutoReply(ctx context.Context, userID int64, autoReplyID int) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM auto_replies WHERE id = $1 AND user_id = $2
	`, autoReplyID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete auto-reply: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("auto-reply not found")
	}

	return nil
}

// GetActiveAutoReplies gets all active auto-replies that should be triggered now
func (s *AutoReplyService) GetActiveAutoReplies(ctx context.Context, userID int64, identityID int) ([]*AutoReply, error) {
	now := time.Now()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, start_date, end_date, subject, html_content, COALESCE(text_content, ''), reply_once, reply_to_all, exclude_patterns, identity_ids, active, reply_count, last_replied_at, created_at, updated_at
		FROM auto_replies
		WHERE user_id = $1
		  AND active = true
		  AND start_date <= $2
		  AND (end_date IS NULL OR end_date >= $2)
		ORDER BY created_at ASC
	`, userID, now)
	if err != nil {
		return nil, fmt.Errorf("failed to get active auto-replies: %w", err)
	}
	defer rows.Close()

	var autoReplies []*AutoReply
	for rows.Next() {
		var ar AutoReply
		if err := rows.Scan(&ar.ID, &ar.UUID, &ar.UserID, &ar.OrgID, &ar.Name,
			&ar.StartDate, &ar.EndDate, &ar.Subject, &ar.HTMLContent, &ar.TextContent,
			&ar.ReplyOnce, &ar.ReplyToAll, pgArr(&ar.ExcludePatterns), pgArr(&ar.IdentityIDs),
			&ar.Active, &ar.ReplyCount, &ar.LastRepliedAt, &ar.CreatedAt, &ar.UpdatedAt); err != nil {
			continue
		}

		// Check if this auto-reply applies to the identity
		if len(ar.IdentityIDs) > 0 {
			found := false
			for _, id := range ar.IdentityIDs {
				if id == identityID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		autoReplies = append(autoReplies, &ar)
	}

	return autoReplies, nil
}

// ShouldSendAutoReply checks if we should send an auto-reply to this sender
func (s *AutoReplyService) ShouldSendAutoReply(ctx context.Context, autoReplyID int, senderEmail string) (bool, error) {
	// Check if we've already replied to this sender
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM auto_reply_senders
		WHERE auto_reply_id = $1 AND sender_email = $2
	`, autoReplyID, senderEmail).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check auto-reply sender: %w", err)
	}

	return count == 0, nil
}

// RecordAutoReplySent records that we sent an auto-reply to a sender
func (s *AutoReplyService) RecordAutoReplySent(ctx context.Context, autoReplyID int, senderEmail string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auto_reply_senders (auto_reply_id, sender_email)
		VALUES ($1, $2)
		ON CONFLICT (auto_reply_id, sender_email) DO NOTHING
	`, autoReplyID, senderEmail)
	if err != nil {
		return fmt.Errorf("failed to record auto-reply sender: %w", err)
	}

	// Update stats
	_, err = s.db.ExecContext(ctx, `
		UPDATE auto_replies
		SET reply_count = reply_count + 1, last_replied_at = NOW()
		WHERE id = $1
	`, autoReplyID)

	return err
}

// CreateEmailForward creates a new email forward
func (s *AutoReplyService) CreateEmailForward(ctx context.Context, userID, orgID int64, input *CreateEmailForwardInput) (*EmailForward, error) {
	// Verify identity belongs to user
	var identityUserID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id FROM identities WHERE id = $1
	`, input.IdentityID).Scan(&identityUserID)
	if err != nil {
		return nil, fmt.Errorf("identity not found")
	}
	if identityUserID != userID {
		return nil, fmt.Errorf("identity does not belong to user")
	}

	// Generate verification token
	verifyToken := generateRandomToken(32)

	var forward EmailForward
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO email_forwards (user_id, org_id, identity_id, forward_to, keep_copy, verify_token, active, verified)
		VALUES ($1, $2, $3, $4, $5, $6, false, false)
		RETURNING id, uuid, user_id, org_id, identity_id, forward_to, keep_copy, active, verified, verify_token, verified_at, forward_count, last_forwarded_at, created_at, updated_at
	`, userID, orgID, input.IdentityID, input.ForwardTo, input.KeepCopy, verifyToken,
	).Scan(&forward.ID, &forward.UUID, &forward.UserID, &forward.OrgID, &forward.IdentityID,
		&forward.ForwardTo, &forward.KeepCopy, &forward.Active, &forward.Verified, &verifyToken,
		&forward.VerifiedAt, &forward.ForwardCount, &forward.LastForwardedAt, &forward.CreatedAt, &forward.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create email forward: %w", err)
	}

	// TODO: Send verification email to forward_to address

	return &forward, nil
}

// VerifyEmailForward verifies an email forward using the verification token
func (s *AutoReplyService) VerifyEmailForward(ctx context.Context, forwardID int, token string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE email_forwards
		SET verified = true, verified_at = NOW(), active = true, updated_at = NOW()
		WHERE id = $1 AND verify_token = $2 AND verified = false
	`, forwardID, token)
	if err != nil {
		return fmt.Errorf("failed to verify email forward: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("invalid or expired verification token")
	}

	return nil
}

// ListEmailForwards lists all email forwards for a user
func (s *AutoReplyService) ListEmailForwards(ctx context.Context, userID int64) ([]*EmailForward, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, org_id, identity_id, forward_to, keep_copy, active, verified, verified_at, forward_count, last_forwarded_at, created_at, updated_at
		FROM email_forwards
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list email forwards: %w", err)
	}
	defer rows.Close()

	var forwards []*EmailForward
	for rows.Next() {
		var f EmailForward
		if err := rows.Scan(&f.ID, &f.UUID, &f.UserID, &f.OrgID, &f.IdentityID,
			&f.ForwardTo, &f.KeepCopy, &f.Active, &f.Verified, &f.VerifiedAt,
			&f.ForwardCount, &f.LastForwardedAt, &f.CreatedAt, &f.UpdatedAt); err != nil {
			continue
		}
		forwards = append(forwards, &f)
	}

	return forwards, nil
}

// DeleteEmailForward deletes an email forward
func (s *AutoReplyService) DeleteEmailForward(ctx context.Context, userID int64, forwardID int) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM email_forwards WHERE id = $1 AND user_id = $2
	`, forwardID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete email forward: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("email forward not found")
	}

	return nil
}

// GetActiveForwardsForIdentity gets active forwards for an identity
func (s *AutoReplyService) GetActiveForwardsForIdentity(ctx context.Context, identityID int) ([]*EmailForward, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, org_id, identity_id, forward_to, keep_copy, active, verified, verified_at, forward_count, last_forwarded_at, created_at, updated_at
		FROM email_forwards
		WHERE identity_id = $1 AND active = true AND verified = true
	`, identityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get active forwards: %w", err)
	}
	defer rows.Close()

	var forwards []*EmailForward
	for rows.Next() {
		var f EmailForward
		if err := rows.Scan(&f.ID, &f.UUID, &f.UserID, &f.OrgID, &f.IdentityID,
			&f.ForwardTo, &f.KeepCopy, &f.Active, &f.Verified, &f.VerifiedAt,
			&f.ForwardCount, &f.LastForwardedAt, &f.CreatedAt, &f.UpdatedAt); err != nil {
			continue
		}
		forwards = append(forwards, &f)
	}

	return forwards, nil
}

// generateRandomToken generates a cryptographically secure random token
func generateRandomToken(length int) string {
	b := make([]byte, length/2+1)
	if _, err := rand.Read(b); err != nil {
		// Fallback (should never happen)
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)[:length]
}
