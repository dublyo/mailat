package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/config"
)

// ComplianceService handles GDPR/CAN-SPAM compliance features
type ComplianceService struct {
	db  *sql.DB
	cfg *config.Config
}

// UnsubscribeData contains encoded unsubscribe information
type UnsubscribeData struct {
	ContactID int64  `json:"c"`
	OrgID     int64  `json:"o"`
	ListID    int    `json:"l,omitempty"`
	EmailID   int64  `json:"e,omitempty"`
}

// ConsentRecord tracks consent changes for audit trail
type ConsentRecord struct {
	ID           int64     `json:"id"`
	ContactID    int64     `json:"contactId"`
	OrgID        int64     `json:"orgId"`
	Action       string    `json:"action"` // subscribe, unsubscribe, resubscribe, consent_given
	Source       string    `json:"source"` // api, form, import, one-click, preference-center
	ListID       *int      `json:"listId,omitempty"`
	IPAddress    string    `json:"ipAddress,omitempty"`
	UserAgent    string    `json:"userAgent,omitempty"`
	Details      string    `json:"details,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// PreferenceData contains subscriber preferences
type PreferenceData struct {
	ContactID      int64    `json:"contactId"`
	Email          string   `json:"email"`
	SubscribedLists []int   `json:"subscribedLists"`
	AllLists       []ListInfo `json:"allLists"`
}

// ListInfo contains list information for preference center
type ListInfo struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Subscribed  bool   `json:"subscribed"`
}

func NewComplianceService(db *sql.DB, cfg *config.Config) *ComplianceService {
	return &ComplianceService{db: db, cfg: cfg}
}

// GenerateListUnsubscribeHeader generates List-Unsubscribe headers for RFC 8058
func (s *ComplianceService) GenerateListUnsubscribeHeader(contactID int64, orgID int64, emailID int64) (string, string) {
	data := UnsubscribeData{
		ContactID: contactID,
		OrgID:     orgID,
		EmailID:   emailID,
	}
	token := s.encodeUnsubscribeData(data)

	baseURL := s.cfg.APIUrl
	if baseURL == "" {
		baseURL = "http://localhost:3001"
	}

	// List-Unsubscribe header (RFC 2369)
	unsubscribeURL := fmt.Sprintf("%s/api/v1/unsubscribe/%s", baseURL, token)
	unsubscribeEmail := fmt.Sprintf("unsubscribe@%s", s.cfg.AppDomain)
	listUnsubscribe := fmt.Sprintf("<%s>, <mailto:%s?subject=unsubscribe-%s>", unsubscribeURL, unsubscribeEmail, token)

	// List-Unsubscribe-Post header (RFC 8058 one-click)
	listUnsubscribePost := "List-Unsubscribe=One-Click"

	return listUnsubscribe, listUnsubscribePost
}

// ProcessOneClickUnsubscribe handles RFC 8058 one-click unsubscribe
func (s *ComplianceService) ProcessOneClickUnsubscribe(ctx context.Context, token string, ipAddress string, userAgent string) error {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return fmt.Errorf("invalid unsubscribe token")
	}

	// Update contact status
	_, err = s.db.ExecContext(ctx, `
		UPDATE contacts SET status = 'unsubscribed', updated_at = NOW()
		WHERE id = $1 AND org_id = $2
	`, data.ContactID, data.OrgID)
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}

	// Get email for suppression list
	var email string
	s.db.QueryRowContext(ctx, "SELECT email FROM contacts WHERE id = $1", data.ContactID).Scan(&email)

	// Add to suppression list
	s.db.ExecContext(ctx, `
		INSERT INTO suppressions (org_id, email, reason, source_type, source_id, created_at)
		VALUES ($1, $2, 'unsubscribe', 'email', $3, NOW())
		ON CONFLICT (org_id, email) DO NOTHING
	`, data.OrgID, email, fmt.Sprintf("%d", data.EmailID))

	// Record consent change for audit trail
	s.recordConsentChange(ctx, data.ContactID, data.OrgID, "unsubscribe", "one-click", nil, ipAddress, userAgent, "One-click unsubscribe from email")

	return nil
}

// GetUnsubscribePage returns data for the unsubscribe landing page
func (s *ComplianceService) GetUnsubscribePage(ctx context.Context, token string) (map[string]interface{}, error) {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return nil, fmt.Errorf("invalid unsubscribe token")
	}

	var contact struct {
		Email     string
		FirstName string
		Status    string
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT email, first_name, status FROM contacts WHERE id = $1 AND org_id = $2
	`, data.ContactID, data.OrgID).Scan(&contact.Email, &contact.FirstName, &contact.Status)
	if err != nil {
		return nil, fmt.Errorf("contact not found")
	}

	return map[string]interface{}{
		"email":     contact.Email,
		"firstName": contact.FirstName,
		"status":    contact.Status,
		"token":     token,
	}, nil
}

// ConfirmUnsubscribe handles confirmed unsubscribe from landing page
func (s *ComplianceService) ConfirmUnsubscribe(ctx context.Context, token string, reason string, ipAddress string, userAgent string) error {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return fmt.Errorf("invalid unsubscribe token")
	}

	// Update contact status
	_, err = s.db.ExecContext(ctx, `
		UPDATE contacts SET status = 'unsubscribed', updated_at = NOW()
		WHERE id = $1 AND org_id = $2
	`, data.ContactID, data.OrgID)
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}

	// Get email for suppression list
	var email string
	s.db.QueryRowContext(ctx, "SELECT email FROM contacts WHERE id = $1", data.ContactID).Scan(&email)

	// Add to suppression list
	s.db.ExecContext(ctx, `
		INSERT INTO suppressions (org_id, email, reason, source_type, created_at)
		VALUES ($1, $2, 'unsubscribe', 'landing_page', NOW())
		ON CONFLICT (org_id, email) DO NOTHING
	`, data.OrgID, email)

	// Record consent change
	details := "Unsubscribe from landing page"
	if reason != "" {
		details = fmt.Sprintf("Unsubscribe from landing page. Reason: %s", reason)
	}
	s.recordConsentChange(ctx, data.ContactID, data.OrgID, "unsubscribe", "landing_page", nil, ipAddress, userAgent, details)

	return nil
}

// GetPreferenceCenter returns data for the preference center
func (s *ComplianceService) GetPreferenceCenter(ctx context.Context, token string) (*PreferenceData, error) {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token")
	}

	// Get contact info
	var contactEmail string
	err = s.db.QueryRowContext(ctx, `
		SELECT email FROM contacts WHERE id = $1 AND org_id = $2
	`, data.ContactID, data.OrgID).Scan(&contactEmail)
	if err != nil {
		return nil, fmt.Errorf("contact not found")
	}

	// Get all lists for the org
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.name, COALESCE(l.description, ''),
			CASE WHEN lc.contact_id IS NOT NULL THEN true ELSE false END as subscribed
		FROM lists l
		LEFT JOIN list_contacts lc ON lc.list_id = l.id AND lc.contact_id = $1
		WHERE l.org_id = $2
		ORDER BY l.name
	`, data.ContactID, data.OrgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get lists: %w", err)
	}
	defer rows.Close()

	var lists []ListInfo
	var subscribedLists []int
	for rows.Next() {
		var li ListInfo
		if err := rows.Scan(&li.ID, &li.Name, &li.Description, &li.Subscribed); err != nil {
			continue
		}
		lists = append(lists, li)
		if li.Subscribed {
			subscribedLists = append(subscribedLists, li.ID)
		}
	}

	return &PreferenceData{
		ContactID:       data.ContactID,
		Email:           contactEmail,
		SubscribedLists: subscribedLists,
		AllLists:        lists,
	}, nil
}

// UpdatePreferences updates subscriber preferences from the preference center
func (s *ComplianceService) UpdatePreferences(ctx context.Context, token string, newListIDs []int, ipAddress string, userAgent string) error {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return fmt.Errorf("invalid token")
	}

	// Get current list memberships
	rows, _ := s.db.QueryContext(ctx, `
		SELECT list_id FROM list_contacts WHERE contact_id = $1
	`, data.ContactID)
	defer rows.Close()

	currentLists := make(map[int]bool)
	for rows.Next() {
		var listID int
		rows.Scan(&listID)
		currentLists[listID] = true
	}

	newListSet := make(map[int]bool)
	for _, id := range newListIDs {
		newListSet[id] = true
	}

	// Add to new lists
	for listID := range newListSet {
		if !currentLists[listID] {
			s.db.ExecContext(ctx, `
				INSERT INTO list_contacts (list_id, contact_id, created_at)
				VALUES ($1, $2, NOW())
				ON CONFLICT DO NOTHING
			`, listID, data.ContactID)
			s.recordConsentChange(ctx, data.ContactID, data.OrgID, "subscribe", "preference-center", &listID, ipAddress, userAgent, "Subscribed via preference center")
		}
	}

	// Remove from old lists
	for listID := range currentLists {
		if !newListSet[listID] {
			s.db.ExecContext(ctx, `
				DELETE FROM list_contacts WHERE list_id = $1 AND contact_id = $2
			`, listID, data.ContactID)
			s.recordConsentChange(ctx, data.ContactID, data.OrgID, "unsubscribe", "preference-center", &listID, ipAddress, userAgent, "Unsubscribed via preference center")
		}
	}

	// Update list counts
	s.db.ExecContext(ctx, `
		UPDATE lists SET contact_count = (
			SELECT COUNT(*) FROM list_contacts WHERE list_id = lists.id
		) WHERE org_id = $1
	`, data.OrgID)

	// If no lists selected, mark contact as unsubscribed
	if len(newListIDs) == 0 {
		s.db.ExecContext(ctx, `
			UPDATE contacts SET status = 'unsubscribed', updated_at = NOW()
			WHERE id = $1
		`, data.ContactID)
	} else {
		// Ensure contact is active if subscribing to lists
		s.db.ExecContext(ctx, `
			UPDATE contacts SET status = 'active', updated_at = NOW()
			WHERE id = $1 AND status = 'unsubscribed'
		`, data.ContactID)
	}

	return nil
}

// GenerateDoubleOptInToken generates a token for double opt-in confirmation
func (s *ComplianceService) GenerateDoubleOptInToken(contactID int64, orgID int64, listIDs []int) string {
	data := map[string]interface{}{
		"c":  contactID,
		"o":  orgID,
		"l":  listIDs,
		"ts": time.Now().Unix(),
	}
	jsonData, _ := json.Marshal(data)

	// Sign the data
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write(jsonData)
	signature := mac.Sum(nil)

	combined := append(jsonData, signature[:8]...)
	return base64.URLEncoding.EncodeToString(combined)
}

// ConfirmDoubleOptIn processes double opt-in confirmation
func (s *ComplianceService) ConfirmDoubleOptIn(ctx context.Context, token string, ipAddress string, userAgent string) error {
	// Decode token
	combined, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return fmt.Errorf("invalid token")
	}

	if len(combined) < 9 {
		return fmt.Errorf("invalid token")
	}

	jsonData := combined[:len(combined)-8]
	providedSig := combined[len(combined)-8:]

	// Verify signature
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write(jsonData)
	expectedSig := mac.Sum(nil)[:8]

	if !hmac.Equal(providedSig, expectedSig) {
		return fmt.Errorf("invalid token")
	}

	var data struct {
		ContactID int64   `json:"c"`
		OrgID     int64   `json:"o"`
		ListIDs   []int   `json:"l"`
		Timestamp int64   `json:"ts"`
	}
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return fmt.Errorf("invalid token")
	}

	// Check token age (24 hours)
	if time.Now().Unix()-data.Timestamp > 86400 {
		return fmt.Errorf("token expired")
	}

	// Activate contact
	_, err = s.db.ExecContext(ctx, `
		UPDATE contacts SET
			status = 'active',
			consent_timestamp = NOW(),
			consent_source = COALESCE(consent_source, 'double_opt_in'),
			consent_ip = $1,
			consent_user_agent = $2,
			updated_at = NOW()
		WHERE id = $3 AND org_id = $4
	`, ipAddress, userAgent, data.ContactID, data.OrgID)
	if err != nil {
		return fmt.Errorf("failed to activate contact: %w", err)
	}

	// Add to lists
	for _, listID := range data.ListIDs {
		s.db.ExecContext(ctx, `
			INSERT INTO list_contacts (list_id, contact_id, created_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT DO NOTHING
		`, listID, data.ContactID)
	}

	// Update list counts
	s.db.ExecContext(ctx, `
		UPDATE lists SET contact_count = (
			SELECT COUNT(*) FROM list_contacts WHERE list_id = lists.id
		) WHERE id = ANY($1)
	`, data.ListIDs)

	// Record consent
	s.recordConsentChange(ctx, data.ContactID, data.OrgID, "consent_given", "double_opt_in", nil, ipAddress, userAgent, "Double opt-in confirmed")

	return nil
}

// ExportContactData exports all data for a contact (GDPR right to portability)
func (s *ComplianceService) ExportContactData(ctx context.Context, orgID int64, contactUUID string) (map[string]interface{}, error) {
	var contactID int64
	var contact struct {
		UUID       string
		Email      string
		FirstName  string
		LastName   string
		Attributes json.RawMessage
		Status     string
		ConsentSource string
		ConsentTimestamp *time.Time
		CreatedAt  time.Time
	}

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, email, first_name, last_name, attributes, status,
			consent_source, consent_timestamp, created_at
		FROM contacts WHERE uuid = $1 AND org_id = $2
	`, contactUUID, orgID).Scan(
		&contactID, &contact.UUID, &contact.Email, &contact.FirstName, &contact.LastName,
		&contact.Attributes, &contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
		&contact.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("contact not found")
	}

	// Get list memberships
	listRows, _ := s.db.QueryContext(ctx, `
		SELECT l.name, lc.created_at FROM list_contacts lc
		JOIN lists l ON l.id = lc.list_id
		WHERE lc.contact_id = $1
	`, contactID)
	defer listRows.Close()

	var lists []map[string]interface{}
	for listRows.Next() {
		var name string
		var joinedAt time.Time
		listRows.Scan(&name, &joinedAt)
		lists = append(lists, map[string]interface{}{
			"name":     name,
			"joinedAt": joinedAt,
		})
	}

	// Get consent history
	consentRows, _ := s.db.QueryContext(ctx, `
		SELECT action, source, details, created_at FROM consent_audit
		WHERE contact_id = $1 ORDER BY created_at DESC
	`, contactID)
	defer consentRows.Close()

	var consentHistory []map[string]interface{}
	for consentRows.Next() {
		var action, source, details string
		var createdAt time.Time
		consentRows.Scan(&action, &source, &details, &createdAt)
		consentHistory = append(consentHistory, map[string]interface{}{
			"action":    action,
			"source":    source,
			"details":   details,
			"timestamp": createdAt,
		})
	}

	// Get email history
	emailRows, _ := s.db.QueryContext(ctx, `
		SELECT subject, status, sent_at, created_at FROM emails
		WHERE contact_id = $1 ORDER BY created_at DESC LIMIT 100
	`, contactID)
	defer emailRows.Close()

	var emails []map[string]interface{}
	for emailRows.Next() {
		var subject, status string
		var sentAt *time.Time
		var createdAt time.Time
		emailRows.Scan(&subject, &status, &sentAt, &createdAt)
		emails = append(emails, map[string]interface{}{
			"subject":   subject,
			"status":    status,
			"sentAt":    sentAt,
			"createdAt": createdAt,
		})
	}

	var attributes map[string]interface{}
	json.Unmarshal(contact.Attributes, &attributes)

	return map[string]interface{}{
		"contact": map[string]interface{}{
			"uuid":             contact.UUID,
			"email":            contact.Email,
			"firstName":        contact.FirstName,
			"lastName":         contact.LastName,
			"attributes":       attributes,
			"status":           contact.Status,
			"consentSource":    contact.ConsentSource,
			"consentTimestamp": contact.ConsentTimestamp,
			"createdAt":        contact.CreatedAt,
		},
		"lists":          lists,
		"consentHistory": consentHistory,
		"emailHistory":   emails,
		"exportedAt":     time.Now(),
	}, nil
}

// DeleteContactData deletes all data for a contact (GDPR right to erasure)
func (s *ComplianceService) DeleteContactData(ctx context.Context, orgID int64, contactUUID string) error {
	// Get contact ID
	var contactID int64
	var email string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email FROM contacts WHERE uuid = $1 AND org_id = $2
	`, contactUUID, orgID).Scan(&contactID, &email)
	if err != nil {
		return fmt.Errorf("contact not found")
	}

	// Begin transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete from list_contacts
	tx.ExecContext(ctx, "DELETE FROM list_contacts WHERE contact_id = $1", contactID)

	// Delete consent audit records
	tx.ExecContext(ctx, "DELETE FROM consent_audit WHERE contact_id = $1", contactID)

	// Anonymize emails (keep for deliverability metrics but remove PII)
	tx.ExecContext(ctx, `
		UPDATE emails SET
			contact_id = NULL,
			to_emails = ARRAY['[redacted]'],
			updated_at = NOW()
		WHERE contact_id = $1
	`, contactID)

	// Delete the contact
	tx.ExecContext(ctx, "DELETE FROM contacts WHERE id = $1", contactID)

	// Add to suppression list to prevent future mailings
	tx.ExecContext(ctx, `
		INSERT INTO suppressions (org_id, email, reason, source_type, created_at)
		VALUES ($1, $2, 'gdpr_erasure', 'gdpr', NOW())
		ON CONFLICT (org_id, email) DO UPDATE SET reason = 'gdpr_erasure'
	`, orgID, email)

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to delete contact data: %w", err)
	}

	return nil
}

// GetConsentAuditTrail retrieves the consent audit trail for a contact
func (s *ComplianceService) GetConsentAuditTrail(ctx context.Context, orgID int64, contactUUID string) ([]ConsentRecord, error) {
	var contactID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM contacts WHERE uuid = $1 AND org_id = $2
	`, contactUUID, orgID).Scan(&contactID)
	if err != nil {
		return nil, fmt.Errorf("contact not found")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, contact_id, org_id, action, source, list_id, ip_address, user_agent, details, created_at
		FROM consent_audit
		WHERE contact_id = $1
		ORDER BY created_at DESC
	`, contactID)
	if err != nil {
		return nil, fmt.Errorf("failed to get audit trail: %w", err)
	}
	defer rows.Close()

	var records []ConsentRecord
	for rows.Next() {
		var r ConsentRecord
		if err := rows.Scan(&r.ID, &r.ContactID, &r.OrgID, &r.Action, &r.Source, &r.ListID, &r.IPAddress, &r.UserAgent, &r.Details, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, r)
	}

	return records, nil
}

// InjectComplianceFooter adds required compliance footer to email content
func (s *ComplianceService) InjectComplianceFooter(htmlContent string, textContent string, orgID int64, contactID int64, emailID int64) (string, string) {
	// Get org info for physical address
	var orgName string
	s.db.QueryRow("SELECT name FROM organizations WHERE id = $1", orgID).Scan(&orgName)

	// Generate unsubscribe token
	data := UnsubscribeData{
		ContactID: contactID,
		OrgID:     orgID,
		EmailID:   emailID,
	}
	token := s.encodeUnsubscribeData(data)

	baseURL := s.cfg.APIUrl
	if baseURL == "" {
		baseURL = "http://localhost:3001"
	}
	unsubscribeURL := fmt.Sprintf("%s/api/v1/unsubscribe/%s", baseURL, token)
	preferencesURL := fmt.Sprintf("%s/api/v1/preferences/%s", baseURL, token)

	// HTML footer
	htmlFooter := fmt.Sprintf(`
<div style="margin-top: 40px; padding-top: 20px; border-top: 1px solid #eee; font-size: 12px; color: #666; text-align: center;">
	<p>%s</p>
	<p>
		<a href="%s" style="color: #666;">Unsubscribe</a> |
		<a href="%s" style="color: #666;">Manage Preferences</a>
	</p>
</div>
`, orgName, unsubscribeURL, preferencesURL)

	// Inject before </body> or append
	if strings.Contains(htmlContent, "</body>") {
		htmlContent = strings.Replace(htmlContent, "</body>", htmlFooter+"</body>", 1)
	} else {
		htmlContent = htmlContent + htmlFooter
	}

	// Text footer
	textFooter := fmt.Sprintf(`

---
%s

Unsubscribe: %s
Manage Preferences: %s
`, orgName, unsubscribeURL, preferencesURL)

	textContent = textContent + textFooter

	return htmlContent, textContent
}

// recordConsentChange records a consent change in the audit trail
func (s *ComplianceService) recordConsentChange(ctx context.Context, contactID int64, orgID int64, action string, source string, listID *int, ipAddress string, userAgent string, details string) {
	s.db.ExecContext(ctx, `
		INSERT INTO consent_audit (contact_id, org_id, action, source, list_id, ip_address, user_agent, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
	`, contactID, orgID, action, source, listID, ipAddress, userAgent, details)
}

// encodeUnsubscribeData encodes unsubscribe data to a URL-safe token
func (s *ComplianceService) encodeUnsubscribeData(data UnsubscribeData) string {
	// Add a unique ID to prevent token reuse tracking
	fullData := struct {
		UnsubscribeData
		Nonce string `json:"n"`
	}{
		UnsubscribeData: data,
		Nonce:           uuid.New().String()[:8],
	}

	jsonData, _ := json.Marshal(fullData)

	// Sign the data
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write(jsonData)
	signature := mac.Sum(nil)

	combined := append(jsonData, signature[:8]...)
	return base64.URLEncoding.EncodeToString(combined)
}

// decodeUnsubscribeData decodes and verifies an unsubscribe token
func (s *ComplianceService) decodeUnsubscribeData(token string) (*UnsubscribeData, error) {
	combined, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding")
	}

	if len(combined) < 9 {
		return nil, fmt.Errorf("token too short")
	}

	jsonData := combined[:len(combined)-8]
	providedSig := combined[len(combined)-8:]

	// Verify signature
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write(jsonData)
	expectedSig := mac.Sum(nil)[:8]

	if !hmac.Equal(providedSig, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	var data UnsubscribeData
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("invalid token data")
	}

	return &data, nil
}
