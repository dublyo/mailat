package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/pkg/crypto"
)

// ComposeService handles email composition and sending
type ComposeService struct {
	db            *sql.DB
	cfg           *config.Config
	jmap          *JMAPClient
	identity      *IdentityService
	emailProvider provider.EmailProvider
}

// NewComposeService creates a new compose service
func NewComposeService(db *sql.DB, cfg *config.Config, identityService *IdentityService) *ComposeService {
	svc := &ComposeService{
		db:       db,
		cfg:      cfg,
		jmap:     NewJMAPClient(cfg.StalwartURL),
		identity: identityService,
	}

	// Initialize email provider for sending
	if cfg.EmailProvider == "ses" && cfg.AWSAccessKeyID != "" {
		sesProvider, err := provider.NewSESProvider(context.Background(), &provider.SESConfig{
			Region:           cfg.AWSRegion,
			AccessKeyID:      cfg.AWSAccessKeyID,
			SecretAccessKey:  cfg.AWSSecretAccessKey,
			ConfigurationSet: cfg.SESConfigurationSet,
		})
		if err != nil {
			fmt.Printf("Warning: Failed to initialize SES provider for compose: %v\n", err)
		} else {
			svc.emailProvider = sesProvider
			fmt.Println("ComposeService: Using SES for email sending")
		}
	}

	return svc
}

// ComposeEmail represents an email being composed
type ComposeEmail struct {
	ID            string          `json:"id,omitempty"`
	IdentityID    int64           `json:"identityId"`
	From          EmailAddress    `json:"from"`
	To            []EmailAddress  `json:"to"`
	Cc            []EmailAddress  `json:"cc,omitempty"`
	Bcc           []EmailAddress  `json:"bcc,omitempty"`
	ReplyTo       []EmailAddress  `json:"replyTo,omitempty"`
	Subject       string          `json:"subject"`
	TextBody      string          `json:"textBody,omitempty"`
	HTMLBody      string          `json:"htmlBody,omitempty"`
	InReplyTo     string          `json:"inReplyTo,omitempty"`
	References    []string        `json:"references,omitempty"`
	Attachments   []AttachmentRef `json:"attachments,omitempty"`
	IsDraft       bool            `json:"isDraft"`
}

// AttachmentRef represents an attachment reference
type AttachmentRef struct {
	BlobID      string `json:"blobId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int    `json:"size"`
	Disposition string `json:"disposition,omitempty"` // attachment or inline
	CID         string `json:"cid,omitempty"`         // Content-ID for inline
}

// SendEmailResult represents the result of sending an email
type SendEmailResult struct {
	EmailID    string    `json:"emailId"`
	ThreadID   string    `json:"threadId"`
	SentAt     time.Time `json:"sentAt"`
	MessageID  string    `json:"messageId"`
}

// DraftResult represents a saved draft
type DraftResult struct {
	ID         string    `json:"id"`
	IdentityID int64     `json:"identityId"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// SendEmail sends an email via SES (or falls back to JMAP if SES not configured)
func (s *ComposeService) SendEmail(ctx context.Context, userID int64, email *ComposeEmail) (*SendEmailResult, error) {
	// Validate identity belongs to user
	identity, err := s.getIdentityByID(ctx, userID, email.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("invalid identity: %w", err)
	}

	// Set the From address
	email.From = EmailAddress{
		Name:  identity.DisplayName,
		Email: identity.Email,
	}

	// Use SES if available (preferred method)
	if s.emailProvider != nil {
		return s.sendViaSES(ctx, identity, email)
	}

	// Fall back to JMAP
	return s.sendViaJMAP(ctx, identity, email)
}

// sendViaSES sends email using AWS SES
func (s *ComposeService) sendViaSES(ctx context.Context, identity *model.Identity, email *ComposeEmail) (*SendEmailResult, error) {
	// Build From address
	fromAddr := email.From.Email
	if email.From.Name != "" {
		fromAddr = fmt.Sprintf("%s <%s>", email.From.Name, email.From.Email)
	}

	// Build To addresses
	toAddrs := make([]string, len(email.To))
	for i, addr := range email.To {
		if addr.Name != "" {
			toAddrs[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
		} else {
			toAddrs[i] = addr.Email
		}
	}

	// Build Cc addresses
	ccAddrs := make([]string, len(email.Cc))
	for i, addr := range email.Cc {
		if addr.Name != "" {
			ccAddrs[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
		} else {
			ccAddrs[i] = addr.Email
		}
	}

	// Build Bcc addresses
	bccAddrs := make([]string, len(email.Bcc))
	for i, addr := range email.Bcc {
		if addr.Name != "" {
			bccAddrs[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
		} else {
			bccAddrs[i] = addr.Email
		}
	}

	// Build email message
	msg := &provider.EmailMessage{
		From:     fromAddr,
		To:       toAddrs,
		Cc:       ccAddrs,
		Bcc:      bccAddrs,
		Subject:  email.Subject,
		TextBody: email.TextBody,
		HTMLBody: email.HTMLBody,
		Headers:  make(map[string]string),
	}

	// Add reply-to if specified
	if len(email.ReplyTo) > 0 {
		msg.ReplyTo = email.ReplyTo[0].Email
	}

	// Add threading headers (ensure proper Message-ID format with angle brackets)
	if email.InReplyTo != "" {
		msg.Headers["In-Reply-To"] = formatMessageID(email.InReplyTo)
	}
	if len(email.References) > 0 {
		formattedRefs := make([]string, len(email.References))
		for i, ref := range email.References {
			formattedRefs[i] = formatMessageID(ref)
		}
		msg.Headers["References"] = strings.Join(formattedRefs, " ")
	}

	// Send via SES
	sendResult, err := s.emailProvider.SendEmail(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("failed to send email via SES: %w", err)
	}

	result := &SendEmailResult{
		EmailID:   sendResult.MessageID,
		MessageID: sendResult.MessageID,
		SentAt:    time.Now(),
	}

	return result, nil
}

// sendViaJMAP sends email using JMAP (Stalwart) - fallback method
func (s *ComposeService) sendViaJMAP(ctx context.Context, identity *model.Identity, email *ComposeEmail) (*SendEmailResult, error) {
	// Get identity password
	password, err := s.getIdentityPassword(ctx, identity.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity password: %w", err)
	}

	// Get JMAP session
	session, err := s.jmap.GetSession(ctx, identity.Email, password)
	if err != nil {
		return nil, fmt.Errorf("failed to get JMAP session: %w", err)
	}

	var accountID string
	for accID := range session.Accounts {
		accountID = accID
		break
	}
	if accountID == "" {
		return nil, fmt.Errorf("no account found")
	}

	// Find Sent mailbox
	mailboxes, err := s.jmap.GetMailboxes(ctx, identity.Email, password, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get mailboxes: %w", err)
	}

	var sentMailboxID string
	for _, mb := range mailboxes {
		if mb.Role != nil && *mb.Role == "sent" {
			sentMailboxID = mb.ID
			break
		}
	}

	// Create the email via JMAP Email/set
	emailCreate := s.buildJMAPEmail(email, sentMailboxID)

	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"urn:ietf:params:jmap:submission",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/set",
				map[string]interface{}{
					"accountId": accountID,
					"create": map[string]interface{}{
						"draft": emailCreate,
					},
				},
				"0",
			},
			{
				"EmailSubmission/set",
				map[string]interface{}{
					"accountId": accountID,
					"create": map[string]interface{}{
						"sendIt": map[string]interface{}{
							"emailId":  "#draft",
							"envelope": nil, // Use default envelope from email
						},
					},
					"onSuccessUpdateEmail": map[string]interface{}{
						"#sendIt": map[string]interface{}{
							"mailboxIds/" + sentMailboxID: true,
							"keywords/$draft":             nil,
						},
					},
				},
				"1",
			},
		},
	}

	response, err := s.jmap.Call(ctx, identity.Email, password, request)
	if err != nil {
		return nil, fmt.Errorf("failed to send email: %w", err)
	}

	// Parse the response
	result := &SendEmailResult{
		SentAt: time.Now(),
	}

	// Get email ID from Email/set response
	if len(response.MethodResponses) > 0 {
		if dataMap, ok := response.MethodResponses[0][1].(map[string]interface{}); ok {
			if created, ok := dataMap["created"].(map[string]interface{}); ok {
				if draft, ok := created["draft"].(map[string]interface{}); ok {
					if id, ok := draft["id"].(string); ok {
						result.EmailID = fmt.Sprintf("%d:%s", identity.ID, id)
					}
					if threadID, ok := draft["threadId"].(string); ok {
						result.ThreadID = fmt.Sprintf("%d:%s", identity.ID, threadID)
					}
				}
			}
			// Check for errors
			if notCreated, ok := dataMap["notCreated"].(map[string]interface{}); ok && len(notCreated) > 0 {
				return nil, fmt.Errorf("failed to create email: %v", notCreated)
			}
		}
	}

	// Check submission response
	if len(response.MethodResponses) > 1 {
		if dataMap, ok := response.MethodResponses[1][1].(map[string]interface{}); ok {
			if notCreated, ok := dataMap["notCreated"].(map[string]interface{}); ok && len(notCreated) > 0 {
				return nil, fmt.Errorf("failed to submit email: %v", notCreated)
			}
		}
	}

	return result, nil
}

// SaveDraft saves an email as a draft
func (s *ComposeService) SaveDraft(ctx context.Context, userID int64, email *ComposeEmail) (*DraftResult, error) {
	identity, err := s.getIdentityByID(ctx, userID, email.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("invalid identity: %w", err)
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

	// Find Drafts mailbox
	mailboxes, err := s.jmap.GetMailboxes(ctx, identity.Email, password, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get mailboxes: %w", err)
	}

	var draftsMailboxID string
	for _, mb := range mailboxes {
		if mb.Role != nil && *mb.Role == "drafts" {
			draftsMailboxID = mb.ID
			break
		}
	}

	email.From = EmailAddress{
		Name:  identity.DisplayName,
		Email: identity.Email,
	}
	email.IsDraft = true

	emailCreate := s.buildJMAPEmail(email, draftsMailboxID)

	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/set",
				map[string]interface{}{
					"accountId": accountID,
					"create": map[string]interface{}{
						"draft": emailCreate,
					},
				},
				"0",
			},
		},
	}

	response, err := s.jmap.Call(ctx, identity.Email, password, request)
	if err != nil {
		return nil, fmt.Errorf("failed to save draft: %w", err)
	}

	result := &DraftResult{
		IdentityID: email.IdentityID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if len(response.MethodResponses) > 0 {
		if dataMap, ok := response.MethodResponses[0][1].(map[string]interface{}); ok {
			if created, ok := dataMap["created"].(map[string]interface{}); ok {
				if draft, ok := created["draft"].(map[string]interface{}); ok {
					if id, ok := draft["id"].(string); ok {
						result.ID = fmt.Sprintf("%d:%s", identity.ID, id)
					}
				}
			}
			if notCreated, ok := dataMap["notCreated"].(map[string]interface{}); ok && len(notCreated) > 0 {
				return nil, fmt.Errorf("failed to save draft: %v", notCreated)
			}
		}
	}

	return result, nil
}

// UpdateDraft updates an existing draft
func (s *ComposeService) UpdateDraft(ctx context.Context, userID int64, draftID string, email *ComposeEmail) (*DraftResult, error) {
	identityID, jmapDraftID, err := parseUnifiedID(draftID)
	if err != nil {
		return nil, err
	}

	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return nil, fmt.Errorf("invalid identity: %w", err)
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

	// Find Drafts mailbox
	mailboxes, err := s.jmap.GetMailboxes(ctx, identity.Email, password, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get mailboxes: %w", err)
	}

	var draftsMailboxID string
	for _, mb := range mailboxes {
		if mb.Role != nil && *mb.Role == "drafts" {
			draftsMailboxID = mb.ID
			break
		}
	}

	email.From = EmailAddress{
		Name:  identity.DisplayName,
		Email: identity.Email,
	}
	email.IsDraft = true

	// Delete old draft and create new one (JMAP doesn't support updating email body)
	emailCreate := s.buildJMAPEmail(email, draftsMailboxID)

	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/set",
				map[string]interface{}{
					"accountId": accountID,
					"destroy":   []string{jmapDraftID},
					"create": map[string]interface{}{
						"draft": emailCreate,
					},
				},
				"0",
			},
		},
	}

	response, err := s.jmap.Call(ctx, identity.Email, password, request)
	if err != nil {
		return nil, fmt.Errorf("failed to update draft: %w", err)
	}

	result := &DraftResult{
		IdentityID: identityID,
		UpdatedAt:  time.Now(),
	}

	if len(response.MethodResponses) > 0 {
		if dataMap, ok := response.MethodResponses[0][1].(map[string]interface{}); ok {
			if created, ok := dataMap["created"].(map[string]interface{}); ok {
				if draft, ok := created["draft"].(map[string]interface{}); ok {
					if id, ok := draft["id"].(string); ok {
						result.ID = fmt.Sprintf("%d:%s", identity.ID, id)
					}
				}
			}
		}
	}

	return result, nil
}

// DeleteDraft deletes a draft
func (s *ComposeService) DeleteDraft(ctx context.Context, userID int64, draftID string) error {
	identityID, jmapDraftID, err := parseUnifiedID(draftID)
	if err != nil {
		return err
	}

	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return fmt.Errorf("invalid identity: %w", err)
	}

	password, err := s.getIdentityPassword(ctx, identity.ID)
	if err != nil {
		return fmt.Errorf("failed to get identity password: %w", err)
	}

	session, err := s.jmap.GetSession(ctx, identity.Email, password)
	if err != nil {
		return fmt.Errorf("failed to get JMAP session: %w", err)
	}

	var accountID string
	for accID := range session.Accounts {
		accountID = accID
		break
	}

	return s.jmap.DeleteEmails(ctx, identity.Email, password, accountID, []string{jmapDraftID}, true)
}

// GetReplyContext gets context for replying to an email
func (s *ComposeService) GetReplyContext(ctx context.Context, userID int64, emailID string, replyAll bool) (*ComposeEmail, error) {
	identityID, jmapEmailID, err := parseUnifiedID(emailID)
	if err != nil {
		return nil, err
	}

	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return nil, fmt.Errorf("invalid identity: %w", err)
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

	// Get the original email
	emails, err := s.jmap.GetEmails(ctx, identity.Email, password, accountID, []string{jmapEmailID}, []string{
		"id", "from", "to", "cc", "subject", "messageId", "inReplyTo", "references",
	})
	if err != nil {
		return nil, err
	}

	if len(emails) == 0 {
		return nil, fmt.Errorf("email not found")
	}

	original := emails[0]

	// Build reply context
	reply := &ComposeEmail{
		IdentityID: identityID,
		Subject:    s.buildReplySubject(original.Subject),
		InReplyTo:  s.getFirstMessageID(original.MessageID),
		References: s.buildReferences(original.References, original.MessageID),
	}

	// Set To recipients
	if len(original.From) > 0 {
		reply.To = original.From
	}

	// For reply all, include original To and Cc (excluding our own address)
	if replyAll {
		for _, addr := range original.To {
			if !strings.EqualFold(addr.Email, identity.Email) {
				reply.To = append(reply.To, addr)
			}
		}
		for _, addr := range original.Cc {
			if !strings.EqualFold(addr.Email, identity.Email) {
				reply.Cc = append(reply.Cc, addr)
			}
		}
	}

	return reply, nil
}

// GetForwardContext gets context for forwarding an email
func (s *ComposeService) GetForwardContext(ctx context.Context, userID int64, emailID string) (*ComposeEmail, error) {
	identityID, jmapEmailID, err := parseUnifiedID(emailID)
	if err != nil {
		return nil, err
	}

	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return nil, fmt.Errorf("invalid identity: %w", err)
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

	// Get the original email with body
	emails, err := s.jmap.GetEmails(ctx, identity.Email, password, accountID, []string{jmapEmailID}, []string{
		"id", "from", "to", "cc", "subject", "textBody", "htmlBody", "attachments", "receivedAt",
	})
	if err != nil {
		return nil, err
	}

	if len(emails) == 0 {
		return nil, fmt.Errorf("email not found")
	}

	original := emails[0]

	// Build forward context
	forward := &ComposeEmail{
		IdentityID: identityID,
		Subject:    s.buildForwardSubject(original.Subject),
		// Note: Body would need to be fetched separately via blob download
		// For now, we just provide the context
	}

	return forward, nil
}

// UploadAttachment uploads an attachment and returns a blob reference
func (s *ComposeService) UploadAttachment(ctx context.Context, userID int64, identityID int64, data []byte, filename, contentType string) (*AttachmentRef, error) {
	identity, err := s.getIdentityByID(ctx, userID, identityID)
	if err != nil {
		return nil, fmt.Errorf("invalid identity: %w", err)
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

	// Upload blob via JMAP upload endpoint
	blobID, err := s.uploadBlob(ctx, session.UploadUrl, identity.Email, password, accountID, data, contentType)
	if err != nil {
		return nil, fmt.Errorf("failed to upload attachment: %w", err)
	}

	return &AttachmentRef{
		BlobID: blobID,
		Name:   filename,
		Type:   contentType,
		Size:   len(data),
	}, nil
}

// Helper methods

func (s *ComposeService) buildJMAPEmail(email *ComposeEmail, mailboxID string) map[string]interface{} {
	result := map[string]interface{}{
		"from": []map[string]string{
			{"name": email.From.Name, "email": email.From.Email},
		},
		"subject": email.Subject,
	}

	// Mailbox
	if mailboxID != "" {
		result["mailboxIds"] = map[string]bool{mailboxID: true}
	}

	// Keywords
	keywords := map[string]bool{}
	if email.IsDraft {
		keywords["$draft"] = true
	}
	if len(keywords) > 0 {
		result["keywords"] = keywords
	}

	// Recipients
	if len(email.To) > 0 {
		result["to"] = s.addressesToJMAP(email.To)
	}
	if len(email.Cc) > 0 {
		result["cc"] = s.addressesToJMAP(email.Cc)
	}
	if len(email.Bcc) > 0 {
		result["bcc"] = s.addressesToJMAP(email.Bcc)
	}
	if len(email.ReplyTo) > 0 {
		result["replyTo"] = s.addressesToJMAP(email.ReplyTo)
	}

	// References for threading
	if email.InReplyTo != "" {
		result["inReplyTo"] = []string{email.InReplyTo}
	}
	if len(email.References) > 0 {
		result["references"] = email.References
	}

	// Body
	bodyParts := []map[string]interface{}{}
	if email.TextBody != "" {
		bodyParts = append(bodyParts, map[string]interface{}{
			"partId": "text",
			"type":   "text/plain",
		})
		result["bodyValues"] = map[string]interface{}{
			"text": map[string]string{"value": email.TextBody},
		}
	}
	if email.HTMLBody != "" {
		bodyParts = append(bodyParts, map[string]interface{}{
			"partId": "html",
			"type":   "text/html",
		})
		if result["bodyValues"] == nil {
			result["bodyValues"] = map[string]interface{}{}
		}
		result["bodyValues"].(map[string]interface{})["html"] = map[string]string{"value": email.HTMLBody}
	}

	if len(bodyParts) > 0 {
		if email.HTMLBody != "" {
			result["htmlBody"] = bodyParts[len(bodyParts)-1:]
		}
		if email.TextBody != "" {
			result["textBody"] = bodyParts[:1]
		}
	}

	// Attachments
	if len(email.Attachments) > 0 {
		attachments := []map[string]interface{}{}
		for _, att := range email.Attachments {
			attachment := map[string]interface{}{
				"blobId": att.BlobID,
				"type":   att.Type,
				"name":   att.Name,
			}
			if att.Disposition != "" {
				attachment["disposition"] = att.Disposition
			} else {
				attachment["disposition"] = "attachment"
			}
			if att.CID != "" {
				attachment["cid"] = att.CID
			}
			attachments = append(attachments, attachment)
		}
		result["attachments"] = attachments
	}

	return result
}

func (s *ComposeService) addressesToJMAP(addrs []EmailAddress) []map[string]string {
	result := make([]map[string]string, len(addrs))
	for i, addr := range addrs {
		result[i] = map[string]string{
			"name":  addr.Name,
			"email": addr.Email,
		}
	}
	return result
}

func (s *ComposeService) buildReplySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	lower := strings.ToLower(subject)
	if strings.HasPrefix(lower, "re:") {
		return subject
	}
	return "Re: " + subject
}

func (s *ComposeService) buildForwardSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	lower := strings.ToLower(subject)
	if strings.HasPrefix(lower, "fwd:") || strings.HasPrefix(lower, "fw:") {
		return subject
	}
	return "Fwd: " + subject
}

func (s *ComposeService) getFirstMessageID(messageIDs []string) string {
	if len(messageIDs) > 0 {
		return messageIDs[0]
	}
	return ""
}

func (s *ComposeService) buildReferences(refs, messageIDs []string) []string {
	result := append([]string{}, refs...)
	for _, mid := range messageIDs {
		result = append(result, mid)
	}
	return result
}

func (s *ComposeService) uploadBlob(ctx context.Context, uploadUrl, email, password, accountID string, data []byte, contentType string) (string, error) {
	// Replace {accountId} placeholder in upload URL
	_ = strings.Replace(uploadUrl, "{accountId}", accountID, 1)

	// TODO: Implement actual HTTP upload
	// For now, return a placeholder - this would need proper implementation
	// with multipart form or direct PUT to the upload URL

	// Encode data as base64 for now (simplified)
	_ = base64.StdEncoding.EncodeToString(data)

	// This is a placeholder - actual implementation would POST to upload URL
	return "blob_placeholder_" + fmt.Sprintf("%d", len(data)), nil
}

func (s *ComposeService) getIdentityPassword(ctx context.Context, identityID int64) (string, error) {
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

func (s *ComposeService) getIdentityByID(ctx context.Context, userID int64, identityID int64) (*model.Identity, error) {
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

// formatMessageID ensures a Message-ID is properly formatted with angle brackets
// RFC 5322 requires Message-IDs to be in format: <unique-id@domain>
func formatMessageID(msgID string) string {
	if msgID == "" {
		return ""
	}

	// Already properly formatted
	if strings.HasPrefix(msgID, "<") && strings.HasSuffix(msgID, ">") {
		return msgID
	}

	// If it contains @, just add angle brackets
	if strings.Contains(msgID, "@") {
		return "<" + msgID + ">"
	}

	// If it's just a UUID or ID without domain, add a default domain
	// This makes it a valid Message-ID per RFC 5322
	return "<" + msgID + "@mail.generated>"
}
