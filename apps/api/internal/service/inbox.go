package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/crypto"
)

// InboxService handles unified inbox operations
type InboxService struct {
	db       *sql.DB
	cfg      *config.Config
	jmap     *JMAPClient
	identity *IdentityService
}

// NewInboxService creates a new inbox service
func NewInboxService(db *sql.DB, cfg *config.Config, identityService *IdentityService) *InboxService {
	return &InboxService{
		db:       db,
		cfg:      cfg,
		jmap:     NewJMAPClient(cfg.StalwartURL),
		identity: identityService,
	}
}

// UnifiedMailbox represents a mailbox with identity info
type UnifiedMailbox struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	ParentID      *string `json:"parentId,omitempty"`
	Role          *string `json:"role,omitempty"`
	SortOrder     int     `json:"sortOrder"`
	TotalEmails   int     `json:"totalEmails"`
	UnreadEmails  int     `json:"unreadEmails"`
	TotalThreads  int     `json:"totalThreads"`
	UnreadThreads int     `json:"unreadThreads"`
	// Identity info
	IdentityID    int64   `json:"identityId"`
	IdentityUUID  string  `json:"identityUuid"`
	IdentityEmail string  `json:"identityEmail"`
	DomainID      int64   `json:"domainId"`
	DomainName    string  `json:"domainName,omitempty"`
}

// UnifiedEmail represents an email with identity and domain info
type UnifiedEmail struct {
	ID            string                  `json:"id"`
	BlobID        string                  `json:"blobId"`
	ThreadID      string                  `json:"threadId"`
	MailboxIDs    map[string]bool         `json:"mailboxIds"`
	Keywords      map[string]bool         `json:"keywords"`
	Size          int                     `json:"size"`
	ReceivedAt    time.Time               `json:"receivedAt"`
	From          []EmailAddress          `json:"from"`
	To            []EmailAddress          `json:"to"`
	Cc            []EmailAddress          `json:"cc,omitempty"`
	Subject       string                  `json:"subject"`
	Preview       string                  `json:"preview"`
	HasAttachment bool                    `json:"hasAttachment"`
	// Computed fields
	IsRead        bool                    `json:"isRead"`
	IsFlagged     bool                    `json:"isFlagged"`
	IsDraft       bool                    `json:"isDraft"`
	ThreadCount   int                     `json:"threadCount,omitempty"`
	// Identity info
	IdentityID    int64                   `json:"identityId"`
	IdentityUUID  string                  `json:"identityUuid"`
	IdentityEmail string                  `json:"identityEmail"`
	DomainID      int64                   `json:"domainId"`
	DomainName    string                  `json:"domainName"`
	DomainColor   string                  `json:"domainColor,omitempty"`
}

// UnifiedInboxResponse represents paginated inbox response
type UnifiedInboxResponse struct {
	Emails     []UnifiedEmail `json:"emails"`
	Total      int            `json:"total"`
	Page       int            `json:"page"`
	PageSize   int            `json:"pageSize"`
	HasMore    bool           `json:"hasMore"`
}

// IdentityCredentials stores credentials for JMAP access
type IdentityCredentials struct {
	Identity *model.Identity
	Password string // Retrieved from vault or stored securely
	AccountID string
}

// GetUnifiedMailboxes retrieves all mailboxes across all user's identities
func (s *InboxService) GetUnifiedMailboxes(ctx context.Context, userID int64) ([]UnifiedMailbox, error) {
	// Get all identities for user
	identities, err := s.identity.ListIdentities(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list identities: %w", err)
	}

	if len(identities) == 0 {
		return []UnifiedMailbox{}, nil
	}

	// Get domain info for each identity
	domainNames := make(map[int64]string)
	for _, identity := range identities {
		if _, ok := domainNames[identity.DomainID]; !ok {
			var domainName string
			err := s.db.QueryRowContext(ctx, "SELECT name FROM domains WHERE id = $1", identity.DomainID).Scan(&domainName)
			if err == nil {
				domainNames[identity.DomainID] = domainName
			}
		}
	}

	// Fetch mailboxes from each identity in parallel
	var wg sync.WaitGroup
	mailboxesChan := make(chan []UnifiedMailbox, len(identities))
	errorsChan := make(chan error, len(identities))

	for _, identity := range identities {
		if identity.StalwartAcctID == "" {
			continue // Skip identities without Stalwart account
		}

		wg.Add(1)
		go func(ident *model.Identity) {
			defer wg.Done()

			// Get stored password for identity
			password, err := s.getIdentityPassword(ctx, ident.ID)
			if err != nil {
				errorsChan <- fmt.Errorf("failed to get password for %s: %w", ident.Email, err)
				return
			}

			// Get JMAP session to find account ID
			session, err := s.jmap.GetSession(ctx, ident.Email, password)
			if err != nil {
				errorsChan <- fmt.Errorf("failed to get JMAP session for %s: %w", ident.Email, err)
				return
			}

			// Find the account ID
			var accountID string
			for accID := range session.Accounts {
				accountID = accID
				break
			}
			if accountID == "" {
				errorsChan <- fmt.Errorf("no account found for %s", ident.Email)
				return
			}

			// Get mailboxes
			mailboxes, err := s.jmap.GetMailboxes(ctx, ident.Email, password, accountID)
			if err != nil {
				errorsChan <- fmt.Errorf("failed to get mailboxes for %s: %w", ident.Email, err)
				return
			}

			// Convert to unified mailboxes
			unified := make([]UnifiedMailbox, len(mailboxes))
			for i, mb := range mailboxes {
				unified[i] = UnifiedMailbox{
					ID:            fmt.Sprintf("%d:%s", ident.ID, mb.ID),
					Name:          mb.Name,
					ParentID:      mb.ParentID,
					Role:          mb.Role,
					SortOrder:     mb.SortOrder,
					TotalEmails:   mb.TotalEmails,
					UnreadEmails:  mb.UnreadEmails,
					TotalThreads:  mb.TotalThreads,
					UnreadThreads: mb.UnreadThreads,
					IdentityID:    ident.ID,
					IdentityUUID:  ident.UUID,
					IdentityEmail: ident.Email,
					DomainID:      ident.DomainID,
					DomainName:    domainNames[ident.DomainID],
				}
			}
			mailboxesChan <- unified
		}(identity)
	}

	// Wait for all goroutines
	go func() {
		wg.Wait()
		close(mailboxesChan)
		close(errorsChan)
	}()

	// Collect results
	var allMailboxes []UnifiedMailbox
	for mailboxes := range mailboxesChan {
		allMailboxes = append(allMailboxes, mailboxes...)
	}

	// Check for errors (log but don't fail)
	for err := range errorsChan {
		fmt.Printf("Warning: %v\n", err)
	}

	return allMailboxes, nil
}

// GetUnifiedInbox retrieves emails from all identities
func (s *InboxService) GetUnifiedInbox(ctx context.Context, userID int64, req *model.UnifiedInboxRequest) (*UnifiedInboxResponse, error) {
	// Get all identities for user
	identities, err := s.identity.ListIdentities(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list identities: %w", err)
	}

	if len(identities) == 0 {
		return &UnifiedInboxResponse{
			Emails:   []UnifiedEmail{},
			Total:    0,
			Page:     req.Page,
			PageSize: req.PageSize,
			HasMore:  false,
		}, nil
	}

	// Get domain info
	domainInfo := make(map[int64]struct {
		Name  string
		Color string
	})
	for _, identity := range identities {
		if _, ok := domainInfo[identity.DomainID]; !ok {
			var name string
			err := s.db.QueryRowContext(ctx, "SELECT name FROM domains WHERE id = $1", identity.DomainID).Scan(&name)
			if err == nil {
				// Generate a color based on domain name
				color := generateDomainColor(name)
				domainInfo[identity.DomainID] = struct {
					Name  string
					Color string
				}{Name: name, Color: color}
			}
		}
	}

	// Build JMAP filter
	filter := make(map[string]interface{})
	if req.MailboxID != "" {
		filter["inMailbox"] = req.MailboxID
	}
	if req.Search != "" {
		filter["text"] = req.Search
	}
	if req.Unread {
		filter["notKeyword"] = "$seen"
	}
	if req.Flagged {
		filter["hasKeyword"] = "$flagged"
	}

	// Determine sort order
	sort := []map[string]interface{}{
		{"property": "receivedAt", "isAscending": false},
	}

	// Fetch emails from each identity
	var allEmails []UnifiedEmail
	totalCount := 0
	position := (req.Page - 1) * req.PageSize

	// If filtering by specific identity
	if req.IdentityID != 0 {
		for _, identity := range identities {
			if identity.ID == req.IdentityID {
				identities = []*model.Identity{identity}
				break
			}
		}
	}

	for _, identity := range identities {
		if identity.StalwartAcctID == "" {
			continue
		}

		password, err := s.getIdentityPassword(ctx, identity.ID)
		if err != nil {
			fmt.Printf("Warning: failed to get password for %s: %v\n", identity.Email, err)
			continue
		}

		session, err := s.jmap.GetSession(ctx, identity.Email, password)
		if err != nil {
			fmt.Printf("Warning: failed to get JMAP session for %s: %v\n", identity.Email, err)
			continue
		}

		var accountID string
		for accID := range session.Accounts {
			accountID = accID
			break
		}
		if accountID == "" {
			continue
		}

		// Query emails
		emails, total, err := s.jmap.QueryAndGetEmails(ctx, identity.Email, password, accountID, filter, sort, position, req.PageSize, nil)
		if err != nil {
			fmt.Printf("Warning: failed to get emails for %s: %v\n", identity.Email, err)
			continue
		}

		totalCount += total
		info := domainInfo[identity.DomainID]

		// Convert to unified emails
		for _, email := range emails {
			unified := UnifiedEmail{
				ID:            fmt.Sprintf("%d:%s", identity.ID, email.ID),
				BlobID:        email.BlobID,
				ThreadID:      fmt.Sprintf("%d:%s", identity.ID, email.ThreadID),
				MailboxIDs:    email.MailboxIDs,
				Keywords:      email.Keywords,
				Size:          email.Size,
				ReceivedAt:    email.ReceivedAt,
				From:          email.From,
				To:            email.To,
				Cc:            email.Cc,
				Subject:       email.Subject,
				Preview:       email.Preview,
				HasAttachment: email.HasAttachment,
				IsRead:        email.Keywords["$seen"],
				IsFlagged:     email.Keywords["$flagged"],
				IsDraft:       email.Keywords["$draft"],
				IdentityID:    identity.ID,
				IdentityUUID:  identity.UUID,
				IdentityEmail: identity.Email,
				DomainID:      identity.DomainID,
				DomainName:    info.Name,
				DomainColor:   info.Color,
			}
			allEmails = append(allEmails, unified)
		}
	}

	// Sort all emails by receivedAt
	sortEmailsByDate(allEmails)

	// Paginate
	start := 0
	end := len(allEmails)
	if end > req.PageSize {
		end = req.PageSize
	}

	return &UnifiedInboxResponse{
		Emails:   allEmails[start:end],
		Total:    totalCount,
		Page:     req.Page,
		PageSize: req.PageSize,
		HasMore:  totalCount > req.Page*req.PageSize,
	}, nil
}

// GetEmail retrieves a single email
func (s *InboxService) GetEmail(ctx context.Context, userID int64, emailID string) (*UnifiedEmail, error) {
	// Parse identity ID and email ID
	identityID, jmapEmailID, err := parseUnifiedID(emailID)
	if err != nil {
		return nil, err
	}

	// Verify identity belongs to user
	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return nil, err
	}

	password, err := s.getIdentityPassword(ctx, identity.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity password: %w", err)
	}

	session, err := s.jmap.GetSession(ctx, identity.Email, password)
	if err != nil {
		return nil, fmt.Errorf("failed to get JMAP session: %w", err)
	}

	var accountID string
	for accID := range session.Accounts {
		accountID = accID
		break
	}

	// Get email with full body
	emails, err := s.jmap.GetEmails(ctx, identity.Email, password, accountID, []string{jmapEmailID}, []string{
		"id", "blobId", "threadId", "mailboxIds", "keywords", "size",
		"receivedAt", "messageId", "inReplyTo", "references",
		"from", "to", "cc", "bcc", "replyTo", "subject", "sentAt",
		"hasAttachment", "preview", "textBody", "htmlBody", "bodyStructure",
	})
	if err != nil {
		return nil, err
	}

	if len(emails) == 0 {
		return nil, fmt.Errorf("email not found")
	}

	email := emails[0]

	// Get domain info
	var domainName string
	s.db.QueryRowContext(ctx, "SELECT name FROM domains WHERE id = $1", identity.DomainID).Scan(&domainName)

	return &UnifiedEmail{
		ID:            emailID,
		BlobID:        email.BlobID,
		ThreadID:      fmt.Sprintf("%d:%s", identity.ID, email.ThreadID),
		MailboxIDs:    email.MailboxIDs,
		Keywords:      email.Keywords,
		Size:          email.Size,
		ReceivedAt:    email.ReceivedAt,
		From:          email.From,
		To:            email.To,
		Cc:            email.Cc,
		Subject:       email.Subject,
		Preview:       email.Preview,
		HasAttachment: email.HasAttachment,
		IsRead:        email.Keywords["$seen"],
		IsFlagged:     email.Keywords["$flagged"],
		IsDraft:       email.Keywords["$draft"],
		IdentityID:    identity.ID,
		IdentityUUID:  identity.UUID,
		IdentityEmail: identity.Email,
		DomainID:      identity.DomainID,
		DomainName:    domainName,
		DomainColor:   generateDomainColor(domainName),
	}, nil
}

// MarkEmailsRead marks emails as read or unread
func (s *InboxService) MarkEmailsRead(ctx context.Context, userID int64, emailIDs []string, read bool) error {
	// Group emails by identity
	grouped := make(map[int64][]string)
	for _, id := range emailIDs {
		identityID, jmapID, err := parseUnifiedID(id)
		if err != nil {
			continue
		}
		grouped[identityID] = append(grouped[identityID], jmapID)
	}

	for identityID, jmapIDs := range grouped {
		identity, err := s.getIdentityByID(ctx, userID, identityID)
		if err != nil {
			continue
		}

		password, err := s.getIdentityPassword(ctx, identity.ID)
		if err != nil {
			continue
		}

		session, err := s.jmap.GetSession(ctx, identity.Email, password)
		if err != nil {
			continue
		}

		var accountID string
		for accID := range session.Accounts {
			accountID = accID
			break
		}

		updates := make(map[string]map[string]interface{})
		for _, jmapID := range jmapIDs {
			if read {
				updates[jmapID] = map[string]interface{}{
					"keywords/$seen": true,
				}
			} else {
				updates[jmapID] = map[string]interface{}{
					"keywords/$seen": nil,
				}
			}
		}

		if err := s.jmap.SetEmailKeywords(ctx, identity.Email, password, accountID, updates); err != nil {
			fmt.Printf("Warning: failed to update emails for %s: %v\n", identity.Email, err)
		}
	}

	return nil
}

// ToggleEmailFlag toggles the flagged status of emails
func (s *InboxService) ToggleEmailFlag(ctx context.Context, userID int64, emailIDs []string, flagged bool) error {
	grouped := make(map[int64][]string)
	for _, id := range emailIDs {
		identityID, jmapID, err := parseUnifiedID(id)
		if err != nil {
			continue
		}
		grouped[identityID] = append(grouped[identityID], jmapID)
	}

	for identityID, jmapIDs := range grouped {
		identity, err := s.getIdentityByID(ctx, userID, identityID)
		if err != nil {
			continue
		}

		password, err := s.getIdentityPassword(ctx, identity.ID)
		if err != nil {
			continue
		}

		session, err := s.jmap.GetSession(ctx, identity.Email, password)
		if err != nil {
			continue
		}

		var accountID string
		for accID := range session.Accounts {
			accountID = accID
			break
		}

		updates := make(map[string]map[string]interface{})
		for _, jmapID := range jmapIDs {
			if flagged {
				updates[jmapID] = map[string]interface{}{
					"keywords/$flagged": true,
				}
			} else {
				updates[jmapID] = map[string]interface{}{
					"keywords/$flagged": nil,
				}
			}
		}

		if err := s.jmap.SetEmailKeywords(ctx, identity.Email, password, accountID, updates); err != nil {
			fmt.Printf("Warning: failed to flag emails for %s: %v\n", identity.Email, err)
		}
	}

	return nil
}

// DeleteEmails deletes or moves emails to trash
func (s *InboxService) DeleteEmails(ctx context.Context, userID int64, emailIDs []string, permanent bool) error {
	grouped := make(map[int64][]string)
	for _, id := range emailIDs {
		identityID, jmapID, err := parseUnifiedID(id)
		if err != nil {
			continue
		}
		grouped[identityID] = append(grouped[identityID], jmapID)
	}

	for identityID, jmapIDs := range grouped {
		identity, err := s.getIdentityByID(ctx, userID, identityID)
		if err != nil {
			continue
		}

		password, err := s.getIdentityPassword(ctx, identity.ID)
		if err != nil {
			continue
		}

		session, err := s.jmap.GetSession(ctx, identity.Email, password)
		if err != nil {
			continue
		}

		var accountID string
		for accID := range session.Accounts {
			accountID = accID
			break
		}

		if err := s.jmap.DeleteEmails(ctx, identity.Email, password, accountID, jmapIDs, permanent); err != nil {
			fmt.Printf("Warning: failed to delete emails for %s: %v\n", identity.Email, err)
		}
	}

	return nil
}

// MoveEmails moves emails to a different mailbox
func (s *InboxService) MoveEmails(ctx context.Context, userID int64, emailIDs []string, targetMailboxID string) error {
	// Parse target mailbox
	targetIdentityID, targetJmapMailboxID, err := parseUnifiedID(targetMailboxID)
	if err != nil {
		return fmt.Errorf("invalid target mailbox ID: %w", err)
	}

	grouped := make(map[int64][]string)
	for _, id := range emailIDs {
		identityID, jmapID, err := parseUnifiedID(id)
		if err != nil {
			continue
		}
		// Can only move within same identity
		if identityID == targetIdentityID {
			grouped[identityID] = append(grouped[identityID], jmapID)
		}
	}

	for identityID, jmapIDs := range grouped {
		identity, err := s.getIdentityByID(ctx, userID, identityID)
		if err != nil {
			continue
		}

		password, err := s.getIdentityPassword(ctx, identity.ID)
		if err != nil {
			continue
		}

		session, err := s.jmap.GetSession(ctx, identity.Email, password)
		if err != nil {
			continue
		}

		var accountID string
		for accID := range session.Accounts {
			accountID = accID
			break
		}

		// Set new mailbox
		updates := make(map[string]map[string]interface{})
		for _, jmapID := range jmapIDs {
			updates[jmapID] = map[string]interface{}{
				"mailboxIds": map[string]bool{targetJmapMailboxID: true},
			}
		}

		if err := s.jmap.SetEmailKeywords(ctx, identity.Email, password, accountID, updates); err != nil {
			fmt.Printf("Warning: failed to move emails for %s: %v\n", identity.Email, err)
		}
	}

	return nil
}

// GetThread retrieves all emails in a thread
func (s *InboxService) GetThread(ctx context.Context, userID int64, threadID string) ([]UnifiedEmail, error) {
	identityID, jmapThreadID, err := parseUnifiedID(threadID)
	if err != nil {
		return nil, err
	}

	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return nil, err
	}

	password, err := s.getIdentityPassword(ctx, identity.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity password: %w", err)
	}

	session, err := s.jmap.GetSession(ctx, identity.Email, password)
	if err != nil {
		return nil, fmt.Errorf("failed to get JMAP session: %w", err)
	}

	var accountID string
	for accID := range session.Accounts {
		accountID = accID
		break
	}

	// Get thread
	threads, err := s.jmap.GetThreads(ctx, identity.Email, password, accountID, []string{jmapThreadID})
	if err != nil {
		return nil, err
	}

	if len(threads) == 0 {
		return nil, fmt.Errorf("thread not found")
	}

	// Get all emails in thread
	emails, err := s.jmap.GetEmails(ctx, identity.Email, password, accountID, threads[0].EmailIDs, nil)
	if err != nil {
		return nil, err
	}

	var domainName string
	s.db.QueryRowContext(ctx, "SELECT name FROM domains WHERE id = $1", identity.DomainID).Scan(&domainName)

	var unified []UnifiedEmail
	for _, email := range emails {
		unified = append(unified, UnifiedEmail{
			ID:            fmt.Sprintf("%d:%s", identity.ID, email.ID),
			BlobID:        email.BlobID,
			ThreadID:      threadID,
			MailboxIDs:    email.MailboxIDs,
			Keywords:      email.Keywords,
			Size:          email.Size,
			ReceivedAt:    email.ReceivedAt,
			From:          email.From,
			To:            email.To,
			Cc:            email.Cc,
			Subject:       email.Subject,
			Preview:       email.Preview,
			HasAttachment: email.HasAttachment,
			IsRead:        email.Keywords["$seen"],
			IsFlagged:     email.Keywords["$flagged"],
			IsDraft:       email.Keywords["$draft"],
			IdentityID:    identity.ID,
			IdentityUUID:  identity.UUID,
			IdentityEmail: identity.Email,
			DomainID:      identity.DomainID,
			DomainName:    domainName,
			DomainColor:   generateDomainColor(domainName),
		})
	}

	return unified, nil
}

// Helper functions

func (s *InboxService) getIdentityPassword(ctx context.Context, identityID int64) (string, error) {
	var encryptedPassword sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT encrypted_password FROM identities WHERE id = $1", identityID).Scan(&encryptedPassword)
	if err != nil {
		return "", fmt.Errorf("failed to get identity password: %w", err)
	}

	if !encryptedPassword.Valid || encryptedPassword.String == "" {
		return "", fmt.Errorf("identity password not configured")
	}

	password, err := crypto.Decrypt(encryptedPassword.String, s.cfg.EncryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt identity password: %w", err)
	}

	return password, nil
}

func (s *InboxService) getIdentityByID(ctx context.Context, userID int64, identityID int64) (*model.Identity, error) {
	var identity model.Identity
	var stalwartAcctID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, domain_id, email, display_name, is_default,
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at
		FROM identities
		WHERE id = $1 AND user_id = $2
	`, identityID, userID).Scan(
		&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
		&identity.Email, &identity.DisplayName, &identity.IsDefault,
		&stalwartAcctID, &identity.QuotaBytes, &identity.UsedBytes,
		&identity.CreatedAt, &identity.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("identity not found")
	}
	if err != nil {
		return nil, err
	}
	if stalwartAcctID.Valid {
		identity.StalwartAcctID = stalwartAcctID.String
	}
	return &identity, nil
}

func parseUnifiedID(id string) (int64, string, error) {
	var identityID int64
	var jmapID string
	_, err := fmt.Sscanf(id, "%d:%s", &identityID, &jmapID)
	if err != nil {
		return 0, "", fmt.Errorf("invalid unified ID format: %s", id)
	}
	return identityID, jmapID, nil
}

func generateDomainColor(domain string) string {
	// Generate a consistent color based on domain name
	colors := []string{
		"#3B82F6", // blue
		"#10B981", // green
		"#F59E0B", // amber
		"#EF4444", // red
		"#8B5CF6", // purple
		"#EC4899", // pink
		"#06B6D4", // cyan
		"#84CC16", // lime
	}

	hash := 0
	for _, c := range domain {
		hash = (hash*31 + int(c)) % len(colors)
	}
	return colors[hash]
}

func sortEmailsByDate(emails []UnifiedEmail) {
	// Simple bubble sort for now - can optimize later
	for i := 0; i < len(emails)-1; i++ {
		for j := 0; j < len(emails)-i-1; j++ {
			if emails[j].ReceivedAt.Before(emails[j+1].ReceivedAt) {
				emails[j], emails[j+1] = emails[j+1], emails[j]
			}
		}
	}
}

// ===================================
// SES Received Emails Methods
// ===================================
// These methods work with the received_emails table for SES-received emails

// ListReceivedEmails returns a paginated list of received emails
// If req.IdentityID is 0, returns emails from all user's identities (unified inbox)
func (s *InboxService) ListReceivedEmails(ctx context.Context, userID int64, req *model.InboxListRequest) (*model.InboxListResponse, error) {
	var args []interface{}
	argNum := 1

	// Build base query with identity info join
	baseQuery := `
		FROM received_emails re
		JOIN identities i ON re.identity_id = i.id
		WHERE i.user_id = $1
	`
	args = append(args, userID)
	argNum++

	// If specific identity requested, filter by it
	if req.IdentityID > 0 {
		baseQuery += fmt.Sprintf(" AND re.identity_id = $%d", argNum)
		args = append(args, req.IdentityID)
		argNum++
	}

	// Apply folder filter
	switch req.Folder {
	case "inbox":
		baseQuery += " AND re.folder = 'inbox' AND re.is_trashed = false AND re.is_archived = false"
	case "sent":
		baseQuery += " AND re.folder = 'sent' AND re.is_trashed = false"
	case "drafts":
		baseQuery += " AND re.folder = 'drafts' AND re.is_trashed = false"
	case "spam":
		baseQuery += " AND (re.folder = 'spam' OR re.is_spam = true) AND re.is_trashed = false"
	case "trash":
		baseQuery += " AND re.is_trashed = true"
	case "starred":
		baseQuery += " AND re.is_starred = true AND re.is_trashed = false"
	case "archive":
		baseQuery += " AND re.is_archived = true AND re.is_trashed = false"
	case "all":
		baseQuery += " AND re.is_trashed = false"
	default:
		if req.Folder != "" {
			baseQuery += fmt.Sprintf(" AND re.folder = $%d AND re.is_trashed = false", argNum)
			args = append(args, req.Folder)
			argNum++
		}
	}

	// Apply read filter
	if req.IsRead != nil {
		baseQuery += fmt.Sprintf(" AND re.is_read = $%d", argNum)
		args = append(args, *req.IsRead)
		argNum++
	}

	// Apply starred filter
	if req.IsStarred != nil {
		baseQuery += fmt.Sprintf(" AND re.is_starred = $%d", argNum)
		args = append(args, *req.IsStarred)
		argNum++
	}

	// Apply search filter
	if req.Search != "" {
		baseQuery += fmt.Sprintf(" AND (LOWER(re.subject) LIKE $%d OR LOWER(re.from_email) LIKE $%d OR LOWER(re.from_name) LIKE $%d OR LOWER(re.snippet) LIKE $%d)", argNum, argNum, argNum, argNum)
		args = append(args, "%"+strings.ToLower(req.Search)+"%")
		argNum++
	}

	// Get total count
	var total int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) "+baseQuery, args...).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("failed to count emails: %w", err)
	}

	// Get unread count (for specific identity or all)
	var unreadCount int
	if req.IdentityID > 0 {
		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM received_emails
			WHERE identity_id = $1 AND is_read = false AND is_trashed = false
		`, req.IdentityID).Scan(&unreadCount)
	} else {
		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.is_read = false AND re.is_trashed = false
		`, userID).Scan(&unreadCount)
	}

	// Apply pagination
	page := req.Page
	if page < 1 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	offset := (page - 1) * pageSize

	// Build final query with ordering and pagination
	sortBy := "re.received_at"
	sortOrder := "DESC"
	if req.SortBy != "" && (req.SortBy == "subject" || req.SortBy == "from_email" || req.SortBy == "size_bytes") {
		sortBy = "re." + req.SortBy
	}
	if req.SortOrder == "asc" {
		sortOrder = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT re.id, re.uuid, re.org_id, re.domain_id, re.identity_id, re.message_id,
			   re.in_reply_to, re.thread_id, re.from_email, re.from_name,
			   re.to_emails, re.cc_emails, re.subject, re.snippet,
			   re.size_bytes, re.has_attachments, re.folder,
			   re.is_read, re.is_starred, re.is_archived, re.is_trashed, re.is_spam,
			   re.labels, re.spam_verdict, re.spf_verdict, re.dkim_verdict, re.dmarc_verdict,
			   re.received_at, re.read_at, re.created_at, re.updated_at,
			   i.email, i.display_name, i.color
		%s
		ORDER BY %s %s
		LIMIT %d OFFSET %d
	`, baseQuery, sortBy, sortOrder, pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query emails: %w", err)
	}
	defer rows.Close()

	var emails []model.ReceivedEmail
	for rows.Next() {
		var email model.ReceivedEmail
		var inReplyTo, threadID, fromName, snippet sql.NullString
		var spamVerdict, spfVerdict, dkimVerdict, dmarcVerdict sql.NullString
		var readAt sql.NullTime
		var toEmails, ccEmails, labels []string
		var identityEmail, identityDisplayName sql.NullString
		var identityColor sql.NullString

		err := rows.Scan(
			&email.ID, &email.UUID, &email.OrgID, &email.DomainID, &email.IdentityID,
			&email.MessageID, &inReplyTo, &threadID, &email.FromEmail, &fromName,
			pq.Array(&toEmails), pq.Array(&ccEmails), &email.Subject, &snippet,
			&email.SizeBytes, &email.HasAttachments, &email.Folder,
			&email.IsRead, &email.IsStarred, &email.IsArchived, &email.IsTrashed, &email.IsSpam,
			pq.Array(&labels), &spamVerdict, &spfVerdict, &dkimVerdict, &dmarcVerdict,
			&email.ReceivedAt, &readAt, &email.CreatedAt, &email.UpdatedAt,
			&identityEmail, &identityDisplayName, &identityColor,
		)
		if err != nil {
			continue
		}

		email.InReplyTo = inReplyTo.String
		email.ThreadID = threadID.String
		email.FromName = fromName.String
		email.Snippet = snippet.String
		email.ToEmails = toEmails
		email.CcEmails = ccEmails
		email.Labels = labels
		email.SpamVerdict = spamVerdict.String
		email.SPFVerdict = spfVerdict.String
		email.DKIMVerdict = dkimVerdict.String
		email.DMARCVerdict = dmarcVerdict.String
		if readAt.Valid {
			email.ReadAt = &readAt.Time
		}
		// Set identity info for unified inbox display
		email.IdentityEmail = identityEmail.String
		email.IdentityDisplayName = identityDisplayName.String
		email.IdentityColor = identityColor.String

		emails = append(emails, email)
	}

	totalPages := total / pageSize
	if total%pageSize > 0 {
		totalPages++
	}

	return &model.InboxListResponse{
		Emails:     emails,
		Total:      total,
		Unread:     unreadCount,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}

// GetReceivedEmail returns a single received email by UUID
func (s *InboxService) GetReceivedEmail(ctx context.Context, userID int64, emailUUID string) (*model.ReceivedEmail, error) {
	var email model.ReceivedEmail
	var inReplyTo, threadID, fromName, snippet, textBody, htmlBody sql.NullString
	var rawS3Key, rawS3Bucket sql.NullString
	var spamVerdict, virusVerdict, spfVerdict, dkimVerdict, dmarcVerdict sql.NullString
	var sesMessageID, replyTo sql.NullString
	var readAt, trashedAt sql.NullTime
	var spamScore sql.NullFloat64
	var toEmails, ccEmails, bccEmails, references, labels []string

	err := s.db.QueryRowContext(ctx, `
		SELECT re.id, re.uuid, re.org_id, re.domain_id, re.identity_id, re.message_id,
			   re.in_reply_to, re.references, re.thread_id, re.from_email, re.from_name,
			   re.to_emails, re.cc_emails, re.bcc_emails, re.reply_to, re.subject,
			   re.text_body, re.html_body, re.snippet, re.raw_s3_key, re.raw_s3_bucket,
			   re.size_bytes, re.has_attachments, re.folder,
			   re.is_read, re.is_starred, re.is_archived, re.is_trashed, re.is_spam,
			   re.labels, re.spam_score, re.spam_verdict, re.virus_verdict,
			   re.spf_verdict, re.dkim_verdict, re.dmarc_verdict, re.ses_message_id,
			   re.received_at, re.read_at, re.trashed_at, re.created_at, re.updated_at
		FROM received_emails re
		JOIN identities i ON re.identity_id = i.id
		WHERE re.uuid = $1 AND i.user_id = $2
	`, emailUUID, userID).Scan(
		&email.ID, &email.UUID, &email.OrgID, &email.DomainID, &email.IdentityID, &email.MessageID,
		&inReplyTo, pq.Array(&references), &threadID, &email.FromEmail, &fromName,
		pq.Array(&toEmails), pq.Array(&ccEmails), pq.Array(&bccEmails), &replyTo, &email.Subject,
		&textBody, &htmlBody, &snippet, &rawS3Key, &rawS3Bucket,
		&email.SizeBytes, &email.HasAttachments, &email.Folder,
		&email.IsRead, &email.IsStarred, &email.IsArchived, &email.IsTrashed, &email.IsSpam,
		pq.Array(&labels), &spamScore, &spamVerdict, &virusVerdict,
		&spfVerdict, &dkimVerdict, &dmarcVerdict, &sesMessageID,
		&email.ReceivedAt, &readAt, &trashedAt, &email.CreatedAt, &email.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("email not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get email: %w", err)
	}

	email.InReplyTo = inReplyTo.String
	email.References = references
	email.ThreadID = threadID.String
	email.FromName = fromName.String
	email.ToEmails = toEmails
	email.CcEmails = ccEmails
	email.BccEmails = bccEmails
	email.ReplyTo = replyTo.String
	email.TextBody = textBody.String
	email.HTMLBody = htmlBody.String
	email.Snippet = snippet.String
	email.RawS3Key = rawS3Key.String
	email.RawS3Bucket = rawS3Bucket.String
	email.Labels = labels
	email.SpamVerdict = spamVerdict.String
	email.VirusVerdict = virusVerdict.String
	email.SPFVerdict = spfVerdict.String
	email.DKIMVerdict = dkimVerdict.String
	email.DMARCVerdict = dmarcVerdict.String
	email.SESMessageID = sesMessageID.String
	if spamScore.Valid {
		email.SpamScore = &spamScore.Float64
	}
	if readAt.Valid {
		email.ReadAt = &readAt.Time
	}
	if trashedAt.Valid {
		email.TrashedAt = &trashedAt.Time
	}

	// Load attachments
	attachmentRows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, filename, content_type, size_bytes, s3_key, s3_bucket,
			   content_id, is_inline, checksum, created_at
		FROM email_attachments
		WHERE received_email_id = $1
	`, email.ID)
	if err == nil {
		defer attachmentRows.Close()
		for attachmentRows.Next() {
			var att model.EmailAttachment
			var contentID, checksum sql.NullString
			attachmentRows.Scan(
				&att.ID, &att.UUID, &att.Filename, &att.ContentType, &att.SizeBytes,
				&att.S3Key, &att.S3Bucket, &contentID, &att.IsInline, &checksum, &att.CreatedAt,
			)
			att.ContentID = contentID.String
			att.Checksum = checksum.String
			email.Attachments = append(email.Attachments, att)
		}
	}

	// Mark as read if not already
	if !email.IsRead {
		now := time.Now()
		s.db.ExecContext(ctx, `
			UPDATE received_emails
			SET is_read = true, read_at = $1, updated_at = $1
			WHERE id = $2
		`, now, email.ID)
		email.IsRead = true
		email.ReadAt = &now
	}

	return &email, nil
}

// MarkReceivedEmails marks received emails as read/unread
func (s *InboxService) MarkReceivedEmails(ctx context.Context, userID int64, emailUUIDs []string, isRead bool) error {
	if len(emailUUIDs) == 0 {
		return nil
	}

	now := time.Now()
	var readAt interface{}
	if isRead {
		readAt = now
	} else {
		readAt = nil
	}

	// Build query with proper user validation
	placeholders := make([]string, len(emailUUIDs))
	args := []interface{}{isRead, readAt, now, userID}
	for i, uuid := range emailUUIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+5)
		args = append(args, uuid)
	}

	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE received_emails
		SET is_read = $1, read_at = $2, updated_at = $3
		WHERE uuid IN (%s)
		AND identity_id IN (SELECT id FROM identities WHERE user_id = $4)
	`, strings.Join(placeholders, ",")), args...)

	return err
}

// StarReceivedEmails stars/unstars received emails
func (s *InboxService) StarReceivedEmails(ctx context.Context, userID int64, emailUUIDs []string, isStarred bool) error {
	if len(emailUUIDs) == 0 {
		return nil
	}

	now := time.Now()

	placeholders := make([]string, len(emailUUIDs))
	args := []interface{}{isStarred, now, userID}
	for i, uuid := range emailUUIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		args = append(args, uuid)
	}

	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE received_emails
		SET is_starred = $1, updated_at = $2
		WHERE uuid IN (%s)
		AND identity_id IN (SELECT id FROM identities WHERE user_id = $3)
	`, strings.Join(placeholders, ",")), args...)

	return err
}

// MoveReceivedEmails moves received emails to a folder
func (s *InboxService) MoveReceivedEmails(ctx context.Context, userID int64, emailUUIDs []string, folder string) error {
	if len(emailUUIDs) == 0 {
		return nil
	}

	now := time.Now()

	placeholders := make([]string, len(emailUUIDs))
	args := []interface{}{folder, now, userID}
	for i, uuid := range emailUUIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		args = append(args, uuid)
	}

	// Handle special folders
	extraUpdates := ""
	switch folder {
	case "archive":
		extraUpdates = ", is_archived = true"
	case "spam":
		extraUpdates = ", is_spam = true"
	case "inbox":
		extraUpdates = ", is_archived = false, is_spam = false"
	}

	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE received_emails
		SET folder = $1, updated_at = $2%s
		WHERE uuid IN (%s)
		AND identity_id IN (SELECT id FROM identities WHERE user_id = $3)
	`, extraUpdates, strings.Join(placeholders, ",")), args...)

	return err
}

// TrashReceivedEmails moves received emails to trash or permanently deletes
func (s *InboxService) TrashReceivedEmails(ctx context.Context, userID int64, emailUUIDs []string, permanent bool) error {
	if len(emailUUIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(emailUUIDs))
	args := []interface{}{userID}
	for i, uuid := range emailUUIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, uuid)
	}

	if permanent {
		// Delete attachments first
		s.db.ExecContext(ctx, fmt.Sprintf(`
			DELETE FROM email_attachments
			WHERE received_email_id IN (
				SELECT id FROM received_emails
				WHERE uuid IN (%s)
				AND identity_id IN (SELECT id FROM identities WHERE user_id = $1)
			)
		`, strings.Join(placeholders, ",")), args...)

		// Delete emails
		_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
			DELETE FROM received_emails
			WHERE uuid IN (%s)
			AND identity_id IN (SELECT id FROM identities WHERE user_id = $1)
		`, strings.Join(placeholders, ",")), args...)
		return err
	}

	// Move to trash
	now := time.Now()
	args = append([]interface{}{now, userID}, args[1:]...)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE received_emails
		SET is_trashed = true, trashed_at = $1, updated_at = $1
		WHERE uuid IN (%s)
		AND identity_id IN (SELECT id FROM identities WHERE user_id = $2)
	`, strings.Join(placeholders, ",")), args...)

	return err
}

// GetReceivedEmailCounts returns email counts by folder/status
// If identityID is 0, returns counts across all user's identities
func (s *InboxService) GetReceivedEmailCounts(ctx context.Context, userID, identityID int64) (*model.InboxCountsResponse, error) {
	counts := &model.InboxCountsResponse{
		Labels: make(map[string]int),
	}

	if identityID > 0 {
		// Verify identity belongs to user
		var exists bool
		err := s.db.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM identities WHERE id = $1 AND user_id = $2)
		`, identityID, userID).Scan(&exists)
		if err != nil || !exists {
			return nil, fmt.Errorf("identity not found")
		}

		// Get counts for specific identity
		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND folder = 'inbox' AND is_trashed = false AND is_archived = false
		`, identityID).Scan(&counts.Inbox)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND is_read = false AND is_trashed = false
		`, identityID).Scan(&counts.Unread)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND is_starred = true AND is_trashed = false
		`, identityID).Scan(&counts.Starred)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND folder = 'sent' AND is_trashed = false
		`, identityID).Scan(&counts.Sent)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND folder = 'drafts' AND is_trashed = false
		`, identityID).Scan(&counts.Drafts)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND (folder = 'spam' OR is_spam = true) AND is_trashed = false
		`, identityID).Scan(&counts.Spam)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails
			WHERE identity_id = $1 AND is_trashed = true
		`, identityID).Scan(&counts.Trash)
	} else {
		// Get counts across all user's identities (unified inbox)
		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.folder = 'inbox' AND re.is_trashed = false AND re.is_archived = false
		`, userID).Scan(&counts.Inbox)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.is_read = false AND re.is_trashed = false
		`, userID).Scan(&counts.Unread)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.is_starred = true AND re.is_trashed = false
		`, userID).Scan(&counts.Starred)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.folder = 'sent' AND re.is_trashed = false
		`, userID).Scan(&counts.Sent)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.folder = 'drafts' AND re.is_trashed = false
		`, userID).Scan(&counts.Drafts)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND (re.folder = 'spam' OR re.is_spam = true) AND re.is_trashed = false
		`, userID).Scan(&counts.Spam)

		s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM received_emails re
			JOIN identities i ON re.identity_id = i.id
			WHERE i.user_id = $1 AND re.is_trashed = true
		`, userID).Scan(&counts.Trash)
	}

	return counts, nil
}

