package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
)

// AutoReply represents an auto-reply/vacation responder configuration
type AutoReply struct {
	ID                int        `json:"id"`
	UUID              string     `json:"uuid"`
	UserID            int        `json:"userId"`
	OrgID             int        `json:"orgId"`
	Name              string     `json:"name"`
	StartDate         time.Time  `json:"startDate"`
	EndDate           *time.Time `json:"endDate,omitempty"`
	Subject           string     `json:"subject"`
	HTMLContent       string     `json:"htmlContent"`
	TextContent       string     `json:"textContent,omitempty"`
	ReplyOnce         bool       `json:"replyOnce"`
	ReplyToAll        bool       `json:"replyToAll"` // Deprecated: stored but ignored; replies go to the original sender only.
	ReplyIntervalDays int        `json:"replyIntervalDays"`
	ExcludePatterns   []string   `json:"excludePatterns,omitempty"`
	IdentityIDs       []int      `json:"identityIds,omitempty"`
	Active            bool       `json:"active"`
	ReplyCount        int        `json:"replyCount"`
	LastRepliedAt     *time.Time `json:"lastRepliedAt,omitempty"`
	LastError         *string    `json:"lastError,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// CreateAutoReplyInput is the input for creating an auto-reply
type CreateAutoReplyInput struct {
	Name              string     `json:"name"`
	StartDate         time.Time  `json:"startDate"`
	EndDate           *time.Time `json:"endDate,omitempty"`
	Subject           string     `json:"subject"` // Empty replies with "Re: <original subject>".
	HTMLContent       string     `json:"htmlContent"`
	TextContent       string     `json:"textContent,omitempty"`
	ReplyOnce         bool       `json:"replyOnce"`
	ReplyToAll        bool       `json:"replyToAll"`                  // Deprecated: accepted and ignored.
	ReplyIntervalDays int        `json:"replyIntervalDays,omitempty"` // Days before the same sender is answered again (1-30, default 7).
	ExcludePatterns   []string   `json:"excludePatterns,omitempty"`   // Up to 50 sender patterns; "*" is a wildcard, otherwise a substring.
	IdentityIDs       []int      `json:"identityIds,omitempty"`       // Your identities; empty means all of them.
	Active            bool       `json:"active"`
}

// UpdateAutoReplyInput is the input for updating an auto-reply
type UpdateAutoReplyInput struct {
	Name              *string    `json:"name,omitempty"`
	StartDate         *time.Time `json:"startDate,omitempty"`
	EndDate           *time.Time `json:"endDate,omitempty"`
	Subject           *string    `json:"subject,omitempty"`
	HTMLContent       *string    `json:"htmlContent,omitempty"`
	TextContent       *string    `json:"textContent,omitempty"`
	ReplyOnce         *bool      `json:"replyOnce,omitempty"`
	ReplyToAll        *bool      `json:"replyToAll,omitempty"` // Deprecated: accepted and ignored.
	ReplyIntervalDays *int       `json:"replyIntervalDays,omitempty"`
	ExcludePatterns   *[]string  `json:"excludePatterns,omitempty"`
	IdentityIDs       *[]int     `json:"identityIds,omitempty"`
	Active            *bool      `json:"active,omitempty"`
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

// AutoReplyValidationError is a rejected auto-reply; its message is safe to show.
type AutoReplyValidationError struct{ Message string }

func (e *AutoReplyValidationError) Error() string { return e.Message }

// ErrAutoReplyNotFound means no auto-reply with that id belongs to the user.
var ErrAutoReplyNotFound = errors.New("auto-reply not found")

const (
	autoReplyMaxBody     = 256 << 10
	autoReplyMaxPatterns = 50
	autoReplyDefaultDays = 7
)

const autoReplyColumns = `id, uuid, user_id, org_id, name, start_date, end_date, subject, html_content, COALESCE(text_content, ''),
	COALESCE(reply_once, true), COALESCE(reply_to_all, true), reply_interval_days, COALESCE(exclude_patterns, '{}'), COALESCE(identity_ids, '{}'),
	COALESCE(active, false), COALESCE(reply_count, 0), last_replied_at, last_error, created_at, COALESCE(updated_at, created_at)`

func scanAutoReply(row rowScanner) (*AutoReply, error) {
	var ar AutoReply
	var ids []int64
	err := row.Scan(&ar.ID, &ar.UUID, &ar.UserID, &ar.OrgID, &ar.Name, &ar.StartDate, &ar.EndDate, &ar.Subject, &ar.HTMLContent, &ar.TextContent,
		&ar.ReplyOnce, &ar.ReplyToAll, &ar.ReplyIntervalDays, pq.Array(&ar.ExcludePatterns), pq.Array(&ids),
		&ar.Active, &ar.ReplyCount, &ar.LastRepliedAt, &ar.LastError, &ar.CreatedAt, &ar.UpdatedAt)
	if err != nil {
		return nil, err
	}
	ar.IdentityIDs = make([]int, len(ids))
	for i, id := range ids {
		ar.IdentityIDs[i] = int(id)
	}
	return &ar, nil
}

// validateAutoReply normalizes and checks a complete rule. Every identity id
// must be one of the user's own identities; an empty list covers all of them.
func (s *AutoReplyService) validateAutoReply(ctx context.Context, userID int64, ar *AutoReply) error {
	invalid := func(msg string) error { return &AutoReplyValidationError{Message: msg} }
	ar.Name = strings.TrimSpace(ar.Name)
	ar.Subject = strings.TrimSpace(ar.Subject)
	switch {
	case utf8.RuneCountInString(ar.Name) > 255:
		return invalid("name must be at most 255 characters")
	case utf8.RuneCountInString(ar.Subject) > 500:
		return invalid("subject must be at most 500 characters")
	case strings.ContainsAny(ar.Subject, "\r\n"):
		return invalid("subject must be a single line")
	case strings.TrimSpace(ar.HTMLContent) == "":
		return invalid("HTML content is required")
	case len(ar.HTMLContent) > autoReplyMaxBody || len(ar.TextContent) > autoReplyMaxBody:
		return invalid("HTML and text content must each be at most 256 KiB")
	case ar.ReplyIntervalDays < 1 || ar.ReplyIntervalDays > 30:
		return invalid("replyIntervalDays must be between 1 and 30")
	case len(ar.ExcludePatterns) > autoReplyMaxPatterns:
		return invalid("at most 50 exclude patterns are allowed")
	}
	if ar.StartDate.IsZero() {
		ar.StartDate = time.Now()
	}
	if ar.EndDate != nil && !ar.EndDate.After(ar.StartDate) {
		return invalid("endDate must be after startDate")
	}
	patterns := make([]string, 0, len(ar.ExcludePatterns))
	for _, p := range ar.ExcludePatterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(p) > 200 {
			return invalid("exclude patterns must be at most 200 characters")
		}
		patterns = append(patterns, p)
	}
	ar.ExcludePatterns = patterns
	ar.IdentityIDs = uniqueInts(ar.IdentityIDs)
	if len(ar.IdentityIDs) == 0 {
		return nil
	}
	var owned int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities WHERE id = ANY($1::int[]) AND user_id = $2`, pq.Array(ar.IdentityIDs), userID).Scan(&owned); err != nil {
		return fmt.Errorf("failed to validate identities: %w", err)
	}
	if owned != len(ar.IdentityIDs) {
		return invalid("identityIds must be your own identities")
	}
	return nil
}

// CreateAutoReply creates a new auto-reply configuration
func (s *AutoReplyService) CreateAutoReply(ctx context.Context, userID, orgID int64, input *CreateAutoReplyInput) (*AutoReply, error) {
	ar := &AutoReply{
		Name: input.Name, StartDate: input.StartDate, EndDate: input.EndDate, Subject: input.Subject,
		HTMLContent: input.HTMLContent, TextContent: input.TextContent, ReplyOnce: input.ReplyOnce, ReplyToAll: input.ReplyToAll,
		ReplyIntervalDays: input.ReplyIntervalDays, ExcludePatterns: input.ExcludePatterns, IdentityIDs: input.IdentityIDs, Active: input.Active,
	}
	if ar.ReplyIntervalDays == 0 {
		ar.ReplyIntervalDays = autoReplyDefaultDays
	}
	if err := s.validateAutoReply(ctx, userID, ar); err != nil {
		return nil, err
	}
	created, err := scanAutoReply(s.db.QueryRowContext(ctx, `
		INSERT INTO auto_replies (user_id, org_id, name, start_date, end_date, subject, html_content, text_content, reply_once, reply_to_all,
			reply_interval_days, exclude_patterns, identity_ids, active, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW())
		RETURNING `+autoReplyColumns,
		userID, orgID, ar.Name, ar.StartDate, ar.EndDate, ar.Subject, ar.HTMLContent, ar.TextContent, ar.ReplyOnce, ar.ReplyToAll,
		ar.ReplyIntervalDays, pq.Array(ar.ExcludePatterns), pq.Array(ar.IdentityIDs), ar.Active))
	if err != nil {
		return nil, fmt.Errorf("failed to create auto-reply: %w", err)
	}
	return created, nil
}

// GetAutoReply gets an auto-reply by ID
func (s *AutoReplyService) GetAutoReply(ctx context.Context, userID int64, autoReplyID int) (*AutoReply, error) {
	ar, err := scanAutoReply(s.db.QueryRowContext(ctx, `SELECT `+autoReplyColumns+` FROM auto_replies WHERE id = $1 AND user_id = $2`, autoReplyID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAutoReplyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get auto-reply: %w", err)
	}
	return ar, nil
}

// ListAutoReplies lists all auto-replies for a user
func (s *AutoReplyService) ListAutoReplies(ctx context.Context, userID int64) ([]*AutoReply, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+autoReplyColumns+` FROM auto_replies WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list auto-replies: %w", err)
	}
	defer rows.Close()
	autoReplies := []*AutoReply{}
	for rows.Next() {
		ar, err := scanAutoReply(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to read auto-reply: %w", err)
		}
		autoReplies = append(autoReplies, ar)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list auto-replies: %w", err)
	}
	return autoReplies, nil
}

// UpdateAutoReply applies the given fields and re-validates the whole rule.
func (s *AutoReplyService) UpdateAutoReply(ctx context.Context, userID int64, autoReplyID int, input *UpdateAutoReplyInput) (*AutoReply, error) {
	ar, err := s.GetAutoReply(ctx, userID, autoReplyID)
	if err != nil {
		return nil, err
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&ar.Name, input.Name)
	set(&ar.Subject, input.Subject)
	set(&ar.HTMLContent, input.HTMLContent)
	set(&ar.TextContent, input.TextContent)
	if input.StartDate != nil {
		ar.StartDate = *input.StartDate
	}
	if input.EndDate != nil {
		ar.EndDate = input.EndDate
	}
	if input.ReplyOnce != nil {
		ar.ReplyOnce = *input.ReplyOnce
	}
	if input.ReplyToAll != nil {
		ar.ReplyToAll = *input.ReplyToAll
	}
	if input.ReplyIntervalDays != nil {
		ar.ReplyIntervalDays = *input.ReplyIntervalDays
	}
	if input.ExcludePatterns != nil {
		ar.ExcludePatterns = *input.ExcludePatterns
	}
	if input.IdentityIDs != nil {
		ar.IdentityIDs = *input.IdentityIDs
	}
	if input.Active != nil {
		ar.Active = *input.Active
	}
	if err = s.validateAutoReply(ctx, userID, ar); err != nil {
		return nil, err
	}
	updated, err := scanAutoReply(s.db.QueryRowContext(ctx, `
		UPDATE auto_replies SET name=$3, start_date=$4, end_date=$5, subject=$6, html_content=$7, text_content=$8, reply_once=$9,
			reply_to_all=$10, reply_interval_days=$11, exclude_patterns=$12, identity_ids=$13, active=$14, updated_at=NOW()
		WHERE id = $1 AND user_id = $2
		RETURNING `+autoReplyColumns,
		autoReplyID, userID, ar.Name, ar.StartDate, ar.EndDate, ar.Subject, ar.HTMLContent, ar.TextContent, ar.ReplyOnce,
		ar.ReplyToAll, ar.ReplyIntervalDays, pq.Array(ar.ExcludePatterns), pq.Array(ar.IdentityIDs), ar.Active))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAutoReplyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update auto-reply: %w", err)
	}
	return updated, nil
}

// DeleteAutoReply deletes an auto-reply
func (s *AutoReplyService) DeleteAutoReply(ctx context.Context, userID int64, autoReplyID int) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM auto_replies WHERE id = $1 AND user_id = $2`, autoReplyID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete auto-reply: %w", err)
	}
	if rowsAffected, _ := result.RowsAffected(); rowsAffected == 0 {
		return ErrAutoReplyNotFound
	}
	return nil
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
