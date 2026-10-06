package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	IdentityID    int64  `json:"identityId"`
	IdentityUUID  string `json:"identityUuid"`
	IdentityEmail string `json:"identityEmail"`
	DomainID      int64  `json:"domainId"`
	DomainName    string `json:"domainName,omitempty"`
}

// UnifiedEmail represents an email with identity and domain info
type UnifiedEmail struct {
	ID            string          `json:"id"`
	BlobID        string          `json:"blobId"`
	ThreadID      string          `json:"threadId"`
	MailboxIDs    map[string]bool `json:"mailboxIds"`
	Keywords      map[string]bool `json:"keywords"`
	Size          int             `json:"size"`
	ReceivedAt    time.Time       `json:"receivedAt"`
	From          []EmailAddress  `json:"from"`
	To            []EmailAddress  `json:"to"`
	Cc            []EmailAddress  `json:"cc,omitempty"`
	Subject       string          `json:"subject"`
	Preview       string          `json:"preview"`
	HasAttachment bool            `json:"hasAttachment"`
	// Computed fields
	IsRead      bool `json:"isRead"`
	IsFlagged   bool `json:"isFlagged"`
	IsDraft     bool `json:"isDraft"`
	ThreadCount int  `json:"threadCount,omitempty"`
	// Identity info
	IdentityID    int64  `json:"identityId"`
	IdentityUUID  string `json:"identityUuid"`
	IdentityEmail string `json:"identityEmail"`
	DomainID      int64  `json:"domainId"`
	DomainName    string `json:"domainName"`
	DomainColor   string `json:"domainColor,omitempty"`
}

// UnifiedInboxResponse represents paginated inbox response
type UnifiedInboxResponse struct {
	Emails   []UnifiedEmail `json:"emails"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
	HasMore  bool           `json:"hasMore"`
}

// IdentityCredentials stores credentials for JMAP access
type IdentityCredentials struct {
	Identity  *model.Identity
	Password  string // Retrieved from vault or stored securely
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
		return nil, ErrMailboxNotFound
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
		SELECT i.id, i.uuid, i.user_id, i.domain_id, i.email, i.display_name, i.is_default,
		       i.stalwart_account_id, i.quota_bytes, i.used_bytes, i.created_at, i.updated_at
		FROM identities i
		WHERE i.id = $1 AND `+identityAccessSQL("i", "$2", identityCanRead), identityID, userID).Scan(
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
	if err := s.validateMailboxScope(ctx, userID, req.IdentityID, req.DomainID); err != nil {
		return nil, err
	}
	baseQuery, args, err := receivedListQuery(userID, req)
	if err != nil {
		return nil, err
	}
	var total, unreadCount int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*),COUNT(*) FILTER(WHERE re.is_read=false AND re.is_trashed=false) "+baseQuery, args...).Scan(&total, &unreadCount); err != nil {
		return nil, fmt.Errorf("count inbox: %w", err)
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

	query := fmt.Sprintf(`SELECT %s %s ORDER BY %s %s, re.id DESC LIMIT %d OFFSET %d`, receivedListColumns, baseQuery, sortBy, sortOrder, pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query emails: %w", err)
	}
	defer rows.Close()

	emails := []model.ReceivedEmail{}
	for rows.Next() {
		email, err := scanReceivedListRow(rows)
		if err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}

	if err := rows.Err(); err != nil {
		return nil, err
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
	if _, err := uuid.Parse(emailUUID); err != nil {
		return nil, ErrInvalidMailboxInput
	}
	var email model.ReceivedEmail
	var inReplyTo, threadID, fromName, snippet, textBody, htmlBody sql.NullString
	var rawS3Key, rawS3Bucket sql.NullString
	var spamVerdict, virusVerdict, spfVerdict, dkimVerdict, dmarcVerdict sql.NullString
	var sesMessageID, replyTo, sendError sql.NullString
	var readAt, trashedAt sql.NullTime
	var spamScore sql.NullFloat64
	var toEmails, ccEmails, bccEmails, references, labels []string
	var remoteAllowed bool

	// Remote images load only for outbound mail, the "always" setting, or a
	// trusted sender whose DMARC passed outside Spam (D1). A missing settings
	// row means "ask".
	err := s.db.QueryRowContext(ctx, `
		SELECT re.id, re.uuid, re.org_id, re.domain_id, re.identity_id, re.message_id,
			   re.in_reply_to, re.references, re.thread_id, re.from_email, re.from_name,
			   re.to_emails, re.cc_emails, re.bcc_emails, re.reply_to, re.subject,
			   re.text_body, re.html_body, re.snippet, re.raw_s3_key, re.raw_s3_bucket,
			   re.size_bytes, re.has_attachments, re.folder,
			   re.is_read, re.is_starred, re.is_archived, re.is_trashed, re.is_spam,
			   re.labels, re.spam_score, re.spam_verdict, re.virus_verdict,
			   re.spf_verdict, re.dkim_verdict, re.dmarc_verdict, re.ses_message_id,
			   re.received_at, re.read_at, re.trashed_at, re.created_at, re.updated_at, re.envelope_recipients,re.direction,re.send_status,re.send_error,re.draft_version,
			   trusted.yes,
			   (re.direction='outbound'
			    OR COALESCE(us.remote_images,'ask')='always'
			    OR (trusted.yes AND upper(COALESCE(re.dmarc_verdict,''))='PASS' AND re.folder<>'spam'))
		FROM received_emails re
		LEFT JOIN user_settings us ON us.user_id = re.mailbox_owner_id
		CROSS JOIN LATERAL (SELECT EXISTS(SELECT 1 FROM mailbox_trusted_senders ts WHERE ts.user_id = re.mailbox_owner_id
			AND ts.sender IN (lower(re.from_email), '@'||split_part(lower(re.from_email),'@',2)))) AS trusted(yes)
		WHERE re.uuid = $1 AND re.mailbox_owner_id = $2
	`, emailUUID, userID).Scan(
		&email.ID, &email.UUID, &email.OrgID, &email.DomainID, &email.IdentityID, &email.MessageID,
		&inReplyTo, pq.Array(&references), &threadID, &email.FromEmail, &fromName,
		pq.Array(&toEmails), pq.Array(&ccEmails), pq.Array(&bccEmails), &replyTo, &email.Subject,
		&textBody, &htmlBody, &snippet, &rawS3Key, &rawS3Bucket,
		&email.SizeBytes, &email.HasAttachments, &email.Folder,
		&email.IsRead, &email.IsStarred, &email.IsArchived, &email.IsTrashed, &email.IsSpam,
		pq.Array(&labels), &spamScore, &spamVerdict, &virusVerdict,
		&spfVerdict, &dkimVerdict, &dmarcVerdict, &sesMessageID,
		&email.ReceivedAt, &readAt, &trashedAt, &email.CreatedAt, &email.UpdatedAt, pq.Array(&email.EnvelopeRecipients), &email.Direction, &email.SendStatus, &sendError, &email.DraftVersion,
		&email.TrustedSender, &remoteAllowed,
	)
	if err == sql.ErrNoRows {
		return nil, ErrMailboxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get email: %w", err)
	}

	email.SendError = sendError.String
	email.RemoteImages = "blocked"
	if remoteAllowed {
		email.RemoteImages = "allowed"
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
	// Storage locations remain server-side; download URLs enforce ownership.
	email.RawS3Key = ""
	email.RawS3Bucket = ""
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
			if err = attachmentRows.Scan(
				&att.ID, &att.UUID, &att.Filename, &att.ContentType, &att.SizeBytes,
				&att.S3Key, &att.S3Bucket, &contentID, &att.IsInline, &checksum, &att.CreatedAt,
			); err != nil {
				return nil, err
			}
			att.DownloadURL = "/api/v1/inbox/received/" + email.UUID + "/attachments/" + att.UUID
			att.S3Bucket = ""
			att.S3Key = ""
			att.ContentID = contentID.String
			att.Checksum = checksum.String
			email.Attachments = append(email.Attachments, att)
		}
		if err = attachmentRows.Err(); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	// Reading a message is non-mutating; the inbox UI marks it explicitly.

	return &email, nil
}

// All bulk mutations first lock and validate the complete selection. Partial
// cross-user selections cannot silently mutate the permitted subset.
func (s *InboxService) ownedMessageTransaction(ctx context.Context, userID int64, ids []string) (*sql.Tx, error) {
	if len(ids) == 0 || len(ids) > 500 {
		return nil, fmt.Errorf("%w: select between 1 and 500 messages", ErrInvalidMailboxInput)
	}
	unique := map[string]bool{}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return nil, ErrInvalidMailboxInput
		}
		if unique[id] {
			return nil, fmt.Errorf("%w: duplicate message id", ErrInvalidMailboxInput)
		}
		unique[id] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = lockMailboxLabels(ctx, tx, userID); err != nil {
		tx.Rollback()
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT re.uuid,re.send_status FROM received_emails re WHERE re.mailbox_owner_id=$1 AND re.uuid=ANY($2::uuid[]) FOR UPDATE OF re`, userID, pq.Array(ids))
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	count := 0
	sending := false
	for rows.Next() {
		var id, status string
		if err = rows.Scan(&id, &status); err != nil {
			rows.Close()
			tx.Rollback()
			return nil, err
		}
		count++
		sending = sending || status == "sending"
	}
	err = rows.Err()
	rows.Close()
	if err != nil || count != len(ids) {
		tx.Rollback()
		return nil, ErrMailboxNotFound
	}
	// The provider may already be accepting this message. Preserve its durable
	// outbox record until the send completes or is marked uncertain.
	if sending {
		tx.Rollback()
		return nil, ErrMailboxConflict
	}
	return tx, nil
}
func (s *InboxService) updateReceived(ctx context.Context, userID int64, ids []string, assignment string, args ...interface{}) error {
	tx, err := s.ownedMessageTransaction(ctx, userID, ids)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	args = append(args, pq.Array(ids))
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE received_emails SET %s,updated_at=NOW() WHERE uuid=ANY($%d::uuid[])`, assignment, len(args)), args...)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *InboxService) MarkReceivedEmails(ctx context.Context, userID int64, ids []string, read bool) error {
	return s.updateReceived(ctx, userID, ids, "is_read=$1,read_at=CASE WHEN $1 THEN NOW() ELSE NULL END", read)
}
func (s *InboxService) StarReceivedEmails(ctx context.Context, userID int64, ids []string, star bool) error {
	return s.updateReceived(ctx, userID, ids, "is_starred=$1", star)
}
func (s *InboxService) MoveReceivedEmails(ctx context.Context, userID int64, ids []string, folder string) error {
	switch folder {
	case "inbox", "archive", "spam", "trash", DMARCReportsFolder:
	default:
		return fmt.Errorf("%w: invalid destination folder", ErrInvalidMailboxInput)
	}
	return s.updateReceived(ctx, userID, ids, "folder=$1::varchar,is_archived=($1::varchar='archive'),is_spam=($1::varchar='spam'),is_trashed=($1::varchar='trash'),trashed_at=CASE WHEN $1::varchar='trash' THEN NOW() ELSE NULL END", folder)
}
func (s *InboxService) TrashReceivedEmails(ctx context.Context, userID int64, ids []string, permanent bool) error {
	if !permanent {
		return s.MoveReceivedEmails(ctx, userID, ids, "trash")
	}
	tx, err := s.ownedMessageTransaction(ctx, userID, ids)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Historical messages may predate the ingestion ledger. Record their identity
	// before deletion so a delayed signed notification cannot restore the copy.
	_, err = tx.ExecContext(ctx, `INSERT INTO received_ingestions(org_id,topic_arn,ses_message_id,identity_id)
 SELECT re.org_id,rc.sns_topic_arn,re.ses_message_id,re.identity_id FROM received_emails re
 JOIN receiving_configs rc ON rc.org_id=re.org_id WHERE re.uuid=ANY($1::uuid[]) AND re.direction='inbound' AND COALESCE(re.ses_message_id,'')!=''
 ON CONFLICT DO NOTHING`, pq.Array(ids))
	if err != nil {
		return err
	}
	// Cleanup jobs are committed with row deletion; objects shared by another copy
	// are rechecked by the cleanup worker before any S3 operation.
	_, err = tx.ExecContext(ctx, `INSERT INTO storage_cleanup_jobs(bucket,object_key)
 SELECT raw_s3_bucket,raw_s3_key FROM received_emails WHERE uuid=ANY($1::uuid[]) AND COALESCE(raw_s3_bucket,'')!='' AND COALESCE(raw_s3_key,'')!=''
 UNION SELECT a.s3_bucket,a.s3_key FROM email_attachments a JOIN received_emails e ON e.id=a.received_email_id WHERE e.uuid=ANY($1::uuid[]) AND a.s3_bucket!='' AND a.s3_key!=''
 ON CONFLICT(bucket,object_key) DO UPDATE SET next_attempt_at=NOW()`, pq.Array(ids))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM received_emails WHERE uuid=ANY($1::uuid[])`, pq.Array(ids)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *InboxService) validateMailboxScope(ctx context.Context, userID, identityID, domainID int64) error {
	if identityID < 0 || domainID < 0 {
		return fmt.Errorf("invalid mailbox filter")
	}
	if identityID > 0 {
		var own bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities i WHERE i.id=$1 AND `+identityAccessSQL("i", "$2", identityCanRead)+`)`, identityID, userID).Scan(&own)
		if err != nil {
			return err
		}
		if !own {
			return fmt.Errorf("identity not found")
		}
	}
	if domainID > 0 {
		var own bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities i WHERE i.domain_id=$1 AND `+identityAccessSQL("i", "$2", identityCanRead)+`)`, domainID, userID).Scan(&own)
		if err != nil {
			return err
		}
		if !own {
			return fmt.Errorf("domain not found")
		}
	}
	return nil
}
func (s *InboxService) GetReceivedEmailCounts(ctx context.Context, userID, identityID int64) (*model.InboxCountsResponse, error) {
	if err := s.validateMailboxScope(ctx, userID, identityID, 0); err != nil {
		return nil, err
	}
	result := &model.InboxCountsResponse{Labels: map[string]int{}}
	err := s.db.QueryRowContext(ctx, `SELECT
 COUNT(*) FILTER(WHERE folder='inbox' AND NOT is_trashed AND NOT is_archived),
 COUNT(*) FILTER(WHERE NOT is_read AND NOT is_trashed),
 COUNT(*) FILTER(WHERE is_starred AND NOT is_trashed),
 COUNT(*) FILTER(WHERE folder='sent' AND NOT is_trashed),
 COUNT(*) FILTER(WHERE folder='drafts' AND NOT is_trashed),
 COUNT(*) FILTER(WHERE (folder='spam' OR is_spam) AND NOT is_trashed),
 COUNT(*) FILTER(WHERE is_trashed),
 COUNT(*) FILTER(WHERE folder='inbox' AND NOT is_read AND NOT is_trashed AND NOT is_archived),
 COUNT(*) FILTER(WHERE folder='dmarc-reports' AND NOT is_trashed AND NOT is_spam AND NOT is_archived),
 COUNT(*) FILTER(WHERE folder='dmarc-reports' AND NOT is_read AND NOT is_trashed AND NOT is_spam AND NOT is_archived)
 FROM received_emails re WHERE re.mailbox_owner_id=$1 AND ($2::bigint=0 OR re.identity_id=$2)`, userID, identityID).Scan(&result.Inbox, &result.Unread, &result.Starred, &result.Sent, &result.Drafts, &result.Spam, &result.Trash, &result.InboxUnread, &result.DMARCReports, &result.DMARCReportsUnread)

	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT label,COUNT(*) FROM received_emails re CROSS JOIN LATERAL unnest(re.labels) AS label WHERE re.mailbox_owner_id=$1 AND ($2::bigint=0 OR re.identity_id=$2) AND NOT re.is_trashed GROUP BY label`, userID, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var count int
		if err = rows.Scan(&label, &count); err != nil {
			return nil, err
		}
		result.Labels[label] = count
	}
	return result, rows.Err()
}

// receivedListColumns is the mailbox list projection; scanReceivedListRow reads it.
const receivedListColumns = `re.id, re.uuid, re.org_id, re.domain_id, re.identity_id, re.message_id,
	re.in_reply_to, re.thread_id, re.from_email, re.from_name,
	re.to_emails, re.cc_emails, re.subject, re.snippet,
	re.size_bytes, re.has_attachments, re.folder,
	re.is_read, re.is_starred, re.is_archived, re.is_trashed, re.is_spam,
	re.labels, re.spam_verdict, re.spf_verdict, re.dkim_verdict, re.dmarc_verdict,
	re.received_at, re.read_at, re.created_at, re.updated_at,
	i.email, i.display_name, i.color, re.envelope_recipients, re.direction, re.send_status, re.send_error, re.draft_version`

func scanReceivedListRow(row rowScanner) (model.ReceivedEmail, error) {
	var email model.ReceivedEmail
	var inReplyTo, threadID, fromName, snippet sql.NullString
	var spamVerdict, spfVerdict, dkimVerdict, dmarcVerdict sql.NullString
	var readAt sql.NullTime
	var toEmails, ccEmails, labels []string
	var identityEmail, identityDisplayName sql.NullString
	var identityColor, sendError sql.NullString

	err := row.Scan(
		&email.ID, &email.UUID, &email.OrgID, &email.DomainID, &email.IdentityID,
		&email.MessageID, &inReplyTo, &threadID, &email.FromEmail, &fromName,
		pq.Array(&toEmails), pq.Array(&ccEmails), &email.Subject, &snippet,
		&email.SizeBytes, &email.HasAttachments, &email.Folder,
		&email.IsRead, &email.IsStarred, &email.IsArchived, &email.IsTrashed, &email.IsSpam,
		pq.Array(&labels), &spamVerdict, &spfVerdict, &dkimVerdict, &dmarcVerdict,
		&email.ReceivedAt, &readAt, &email.CreatedAt, &email.UpdatedAt,
		&identityEmail, &identityDisplayName, &identityColor, pq.Array(&email.EnvelopeRecipients), &email.Direction, &email.SendStatus, &sendError, &email.DraftVersion,
	)
	if err != nil {
		return email, fmt.Errorf("scan inbox row: %w", err)
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
	email.SendError = sendError.String

	return email, nil
}

// MailboxSummaries returns the list projection of the given messages that the
// user still owns, keyed by UUID. A missing key means the copy is gone.
func (s *InboxService) MailboxSummaries(ctx context.Context, userID int64, uuids []string) (map[string]model.ReceivedEmail, error) {
	out := make(map[string]model.ReceivedEmail, len(uuids))
	if len(uuids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+receivedListColumns+` FROM received_emails re JOIN identities i ON i.id=re.identity_id
		WHERE re.mailbox_owner_id=$1 AND re.uuid=ANY($2::uuid[])`, userID, pq.Array(uuids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		email, err := scanReceivedListRow(rows)
		if err != nil {
			return nil, err
		}
		out[email.UUID] = email
	}
	return out, rows.Err()
}

func receivedListQuery(userID int64, req *model.InboxListRequest) (string, []interface{}, error) {
	query := `FROM received_emails re JOIN identities i ON i.id=re.identity_id WHERE re.mailbox_owner_id=$1`
	args := []interface{}{userID}
	add := func(condition string, value interface{}) {
		args = append(args, value)
		query += fmt.Sprintf(condition, len(args))
	}
	if req.IdentityID > 0 {
		add(" AND re.identity_id=$%d", req.IdentityID)
	}
	if req.DomainID > 0 {
		add(" AND re.domain_id=$%d", req.DomainID)
	}
	switch req.Folder {
	case "", "inbox":
		query += " AND re.folder='inbox' AND NOT re.is_trashed AND NOT re.is_archived"
	case "sent", "drafts", "outbox":
		add(" AND re.folder=$%d AND NOT re.is_trashed", req.Folder)
	case DMARCReportsFolder:
		add(" AND re.folder=$%d AND NOT re.is_trashed AND NOT re.is_spam AND NOT re.is_archived", req.Folder)
	case "spam":
		query += " AND (re.folder='spam' OR re.is_spam) AND NOT re.is_trashed"
	case "trash":
		query += " AND re.is_trashed"
	case "starred":
		query += " AND re.is_starred AND NOT re.is_trashed"
	case "archive":
		query += " AND re.is_archived AND NOT re.is_trashed"
	case "all":
		query += " AND NOT re.is_trashed"
	default:
		return "", nil, fmt.Errorf("invalid folder")
	}
	if req.IsRead != nil {
		add(" AND re.is_read=$%d", *req.IsRead)
	}
	if req.IsStarred != nil {
		add(" AND re.is_starred=$%d", *req.IsStarred)
	}
	if req.HasAttachments != nil {
		add(" AND re.has_attachments=$%d", *req.HasAttachments)
	}
	if len(req.Search) > 500 || len(req.Sender) > 255 {
		return "", nil, fmt.Errorf("search filter too long")
	}
	if req.Search != "" {
		args = append(args, "%"+escapeLike(strings.ToLower(req.Search))+"%")
		n := len(args)
		query += fmt.Sprintf(" AND (lower(re.subject) LIKE $%d OR lower(re.from_email) LIKE $%d OR lower(COALESCE(re.from_name,'')) LIKE $%d OR lower(COALESCE(re.snippet,'')) LIKE $%d)", n, n, n, n)
	}
	if req.Sender != "" {
		add(" AND lower(re.from_email) LIKE $%d", "%"+escapeLike(strings.ToLower(req.Sender))+"%")
	}
	for _, bound := range []struct {
		value string
		end   bool
	}{{req.DateFrom, false}, {req.DateTo, true}} {
		if bound.value == "" {
			continue
		}
		stamp, err := time.Parse(time.RFC3339, bound.value)
		dateOnly := false
		if err != nil {
			stamp, err = time.Parse("2006-01-02", bound.value)
			dateOnly = true
		}
		if err != nil {
			return "", nil, fmt.Errorf("invalid date filter")
		}
		if bound.end {
			if dateOnly {
				stamp = stamp.AddDate(0, 0, 1)
				add(" AND re.received_at<$%d", stamp)
			} else {
				add(" AND re.received_at<=$%d", stamp)
			}
		} else {
			add(" AND re.received_at>=$%d", stamp)
		}
	}
	if len(req.Labels) > 0 {
		if len(req.Labels) > 50 {
			return "", nil, fmt.Errorf("too many label filters")
		}
		add(" AND re.labels @> $%d::text[]", pq.Array(req.Labels))
	}
	return query, args, nil
}
func escapeLike(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}

// TrustedSender is an address or @domain whose DMARC-passing mail may load
// remote images for one user.
type TrustedSender struct {
	UUID      string    `json:"uuid"`
	Sender    string    `json:"sender"`
	CreatedAt time.Time `json:"createdAt"`
}

const maxTrustedSenders = 500

var trustedDomainPattern = regexp.MustCompile(`^@[a-z0-9.-]+\.[a-z]{2,}$`)

func normalizeTrustedSender(sender string) (string, error) {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if len(sender) < 3 || len(sender) > 320 {
		return "", fmt.Errorf("%w: enter an email address or @domain", ErrInvalidMailboxInput)
	}
	if strings.HasPrefix(sender, "@") {
		if !trustedDomainPattern.MatchString(sender) {
			return "", fmt.Errorf("%w: enter a domain as @example.com", ErrInvalidMailboxInput)
		}
		return sender, nil
	}
	addr, err := mail.ParseAddress(sender)
	if err != nil || addr.Name != "" || addr.Address != sender {
		return "", fmt.Errorf("%w: enter an email address or @domain", ErrInvalidMailboxInput)
	}
	return sender, nil
}

func (s *InboxService) ListTrustedSenders(ctx context.Context, userID int64) ([]TrustedSender, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uuid,sender,created_at FROM mailbox_trusted_senders WHERE user_id=$1 ORDER BY sender`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrustedSender{}
	for rows.Next() {
		var t TrustedSender
		if err = rows.Scan(&t.UUID, &t.Sender, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTrustedSender is idempotent; created is false when the sender already exists.
func (s *InboxService) AddTrustedSender(ctx context.Context, orgID, userID int64, sender string) (*TrustedSender, bool, error) {
	sender, err := normalizeTrustedSender(sender)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// Serialize per user so the 500-row cap cannot be raced past.
	if err = lockMailboxLabels(ctx, tx, userID); err != nil {
		return nil, false, err
	}
	var t TrustedSender
	err = tx.QueryRowContext(ctx, `SELECT uuid,sender,created_at FROM mailbox_trusted_senders WHERE user_id=$1 AND sender=$2`, userID, sender).Scan(&t.UUID, &t.Sender, &t.CreatedAt)
	if err == nil {
		return &t, false, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_trusted_senders WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= maxTrustedSenders {
		return nil, false, fmt.Errorf("%w: limit_reached: remove a trusted sender before adding another", ErrMailboxConflict)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO mailbox_trusted_senders(user_id,org_id,sender) VALUES($1,$2,$3) RETURNING uuid,sender,created_at`, userID, orgID, sender).Scan(&t.UUID, &t.Sender, &t.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return &t, true, nil
}

func (s *InboxService) DeleteTrustedSender(ctx context.Context, userID int64, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrInvalidMailboxInput
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM mailbox_trusted_senders WHERE uuid=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMailboxNotFound
	}
	return nil
}
