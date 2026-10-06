package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
)

// ComplianceService handles GDPR/CAN-SPAM compliance features
type ComplianceService struct {
	db  *sql.DB
	cfg *config.Config
}

// UnsubscribeData contains encoded unsubscribe information
type UnsubscribeData struct {
	ContactID int64 `json:"c"`
	OrgID     int64 `json:"o"`
	ListID    int   `json:"l,omitempty"`
	EmailID   int64 `json:"e,omitempty"`
}

// ConsentRecord tracks consent changes for audit trail
type ConsentRecord struct {
	ID        int64     `json:"id"`
	ContactID int64     `json:"contactId"`
	OrgID     int64     `json:"orgId"`
	Action    string    `json:"action"` // subscribe, unsubscribe, resubscribe, consent_given
	Source    string    `json:"source"` // api, form, import, one-click, preference-center
	ListID    *int      `json:"listId,omitempty"`
	IPAddress string    `json:"ipAddress,omitempty"`
	UserAgent string    `json:"userAgent,omitempty"`
	Details   string    `json:"details,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// PreferenceData contains subscriber preferences
type PreferenceData struct {
	ContactID       int64      `json:"contactId"`
	Email           string     `json:"email"`
	SubscribedLists []int      `json:"subscribedLists"`
	AllLists        []ListInfo `json:"allLists"`
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

// ValidUnsubscribeToken reports whether token carries a valid signature. It
// lets the one-click endpoint skip per-IP limits for genuine links.
func (s *ComplianceService) ValidUnsubscribeToken(token string) bool {
	_, err := s.decodeUnsubscribeData(token)
	return err == nil
}

// ProcessOneClickUnsubscribe handles RFC 8058 one-click unsubscribe
func (s *ComplianceService) ProcessOneClickUnsubscribe(ctx context.Context, token string, ipAddress string, userAgent string) error {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return fmt.Errorf("invalid unsubscribe token")
	}
	return s.unsubscribeContact(ctx, data, "email", fmt.Sprintf("%d", data.EmailID), "one-click", ipAddress, userAgent, "One-click unsubscribe from email")
}

// unsubscribeContact marks the token's contact unsubscribed, suppresses the
// address and records consent in one transaction. A missing contact (deleted
// or erased) is treated as already unsubscribed.
func (s *ComplianceService) unsubscribeContact(ctx context.Context, data *UnsubscribeData, suppressionSource, sourceID, consentSource, ipAddress, userAgent, details string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}
	defer tx.Rollback()
	var email string
	err = tx.QueryRowContext(ctx, `
		UPDATE contacts SET status = 'unsubscribed', updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING email
	`, data.ContactID, data.OrgID).Scan(&email)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO suppressions (org_id, email, reason, source_type, source_id, created_at)
		VALUES ($1, $2, 'unsubscribe', $3, NULLIF($4, ''), NOW())
		ON CONFLICT (org_id, email) DO NOTHING
	`, data.OrgID, email, suppressionSource, sourceID); err != nil {
		return fmt.Errorf("failed to suppress address: %w", err)
	}
	if err = recordConsentChangeTx(ctx, tx, data.ContactID, data.OrgID, "unsubscribe", consentSource, nil, ipAddress, userAgent, details); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}
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
	details := "Unsubscribe from landing page"
	if reason != "" {
		details = fmt.Sprintf("Unsubscribe from landing page. Reason: %s", reason)
	}
	return s.unsubscribeContact(ctx, data, "landing_page", "", "landing_page", ipAddress, userAgent, details)
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

// UpdatePreferences applies a preference-center selection. Removals are always
// allowed; additions never reactivate an unsubscribed or suppressed address,
// and an empty selection unsubscribes and suppresses. The signed token proves
// mailbox control, so double opt-in lists may be joined directly.
func (s *ComplianceService) UpdatePreferences(ctx context.Context, token string, newListIDs []int, ipAddress string, userAgent string) error {
	data, err := s.decodeUnsubscribeData(token)
	if err != nil {
		return fmt.Errorf("invalid token")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to update preferences: %w", err)
	}
	defer tx.Rollback()

	var email, status string
	err = tx.QueryRowContext(ctx, `SELECT email, status FROM contacts WHERE id = $1 AND org_id = $2 FOR UPDATE`, data.ContactID, data.OrgID).Scan(&email, &status)
	if err == sql.ErrNoRows {
		return ErrContactNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to load contact: %w", err)
	}

	requested := uniqueInts(newListIDs)
	if err = validateOrgLists(ctx, tx, data.OrgID, requested); err != nil {
		return err
	}
	current := map[int]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT lc.list_id FROM list_contacts lc JOIN lists l ON l.id = lc.list_id WHERE lc.contact_id = $1 AND l.org_id = $2`, data.ContactID, data.OrgID)
	if err != nil {
		return fmt.Errorf("failed to load memberships: %w", err)
	}
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("failed to load memberships: %w", err)
		}
		current[id] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("failed to load memberships: %w", err)
	}

	wanted := map[int]bool{}
	var additions, changed []int
	for _, id := range requested {
		wanted[id] = true
		if !current[id] {
			additions = append(additions, id)
		}
	}
	if len(additions) > 0 {
		if status != "active" {
			return ErrReactivationBlocked
		}
		suppressed, err := isSuppressedTx(ctx, tx, data.OrgID, email)
		if err != nil {
			return err
		}
		if suppressed {
			return ErrReactivationBlocked
		}
	}

	for id := range current {
		if wanted[id] {
			continue
		}
		listID := id
		if _, err = tx.ExecContext(ctx, `DELETE FROM list_contacts WHERE list_id = $1 AND contact_id = $2`, listID, data.ContactID); err != nil {
			return fmt.Errorf("failed to leave list: %w", err)
		}
		if err = recordConsentChangeTx(ctx, tx, data.ContactID, data.OrgID, "unsubscribe", "preference-center", &listID, ipAddress, userAgent, "Unsubscribed via preference center"); err != nil {
			return err
		}
		changed = append(changed, listID)
	}
	for _, id := range additions {
		listID := id
		if _, err = tx.ExecContext(ctx, `INSERT INTO list_contacts (list_id, contact_id, created_at) VALUES ($1, $2, NOW()) ON CONFLICT DO NOTHING`, listID, data.ContactID); err != nil {
			return fmt.Errorf("failed to join list: %w", err)
		}
		if err = recordConsentChangeTx(ctx, tx, data.ContactID, data.OrgID, "subscribe", "preference-center", &listID, ipAddress, userAgent, "Subscribed via preference center"); err != nil {
			return err
		}
		changed = append(changed, listID)
	}
	if err = recountLists(ctx, tx, data.OrgID, changed); err != nil {
		return err
	}

	if len(requested) == 0 {
		if status != "unsubscribed" {
			if _, err = tx.ExecContext(ctx, `UPDATE contacts SET status = 'unsubscribed', updated_at = NOW() WHERE id = $1 AND org_id = $2`, data.ContactID, data.OrgID); err != nil {
				return fmt.Errorf("failed to unsubscribe: %w", err)
			}
			if err = recordConsentChangeTx(ctx, tx, data.ContactID, data.OrgID, "unsubscribe", "preference-center", nil, ipAddress, userAgent, "Unsubscribed from all lists via preference center"); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO suppressions (org_id, email, reason, source_type, created_at)
			VALUES ($1, $2, 'unsubscribe', 'preference-center', NOW())
			ON CONFLICT (org_id, email) DO NOTHING
		`, data.OrgID, email); err != nil {
			return fmt.Errorf("failed to suppress address: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to update preferences: %w", err)
	}
	return nil
}

// ExportContactData exports all data for a contact (GDPR right to portability)
func (s *ComplianceService) ExportContactData(ctx context.Context, orgID int64, contactUUID string) (map[string]interface{}, error) {
	var contactID int64
	var contact struct {
		UUID             string
		Email            string
		FirstName        string
		LastName         string
		Attributes       json.RawMessage
		Status           string
		ConsentSource    string
		ConsentTimestamp *time.Time
		CreatedAt        time.Time
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

	// Include the disclosure and request history without exporting token digests.
	var signupHistory json.RawMessage
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('formId',f.uuid,'email',r.email,'firstName',r.first_name,'status',r.status,'confirmationMode',r.confirmation_mode,'disclosure',r.disclosure,'formVersion',r.form_version,'createdAt',r.created_at,'confirmedAt',r.confirmed_at)),'[]'::jsonb) FROM signup_requests r JOIN signup_forms f ON f.id=r.form_id WHERE f.org_id=$1 AND lower(r.email)=lower($2)`, orgID, contact.Email).Scan(&signupHistory); err != nil {
		return nil, fmt.Errorf("failed to export signup history: %w", err)
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
		"signupHistory":  signupHistory,
		"emailHistory":   emails,
		"exportedAt":     time.Now(),
	}, nil
}

// DeleteContactData erases a contact (GDPR right to erasure) in one
// transaction: every case variant of the address in the org, automation
// enrollments, campaign email content, webhook payloads, signup history,
// consent rows and list memberships. A hash-only suppression is kept so the
// address can never be mailed again. Transactional suppression_list rows,
// the org users' own mailboxes and raw S3 mail are not touched.
func (s *ComplianceService) DeleteContactData(ctx context.Context, orgID int64, actor ContactActor, contactUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM contacts WHERE uuid = $1 AND org_id = $2`, contactUUID, orgID).Scan(&email)
	if err == sql.ErrNoRows {
		return ErrContactNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to load contact: %w", err)
	}
	addr := strings.ToLower(strings.TrimSpace(email))
	hash := emailSHA256(addr)

	var ids []int64
	var uuids []string
	rows, err := tx.QueryContext(ctx, `SELECT id, uuid::text FROM contacts WHERE org_id = $1 AND lower(email) = $2 FOR UPDATE`, orgID, addr)
	if err != nil {
		return fmt.Errorf("failed to lock contacts: %w", err)
	}
	for rows.Next() {
		var id int64
		var u string
		if err = rows.Scan(&id, &u); err != nil {
			rows.Close()
			return fmt.Errorf("failed to lock contacts: %w", err)
		}
		ids, uuids = append(ids, id), append(uuids, u)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("failed to lock contacts: %w", err)
	}

	exec := func(step, query string, args ...any) error {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("erasure failed (%s): %w", step, err)
		}
		return nil
	}
	collect := func(step, query string, args ...any) ([]string, error) {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("erasure failed (%s): %w", step, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, fmt.Errorf("erasure failed (%s): %w", step, err)
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}

	// Automation has no FK to contacts, so enrollments go explicitly.
	if err = exec("automation logs", `DELETE FROM automation_logs WHERE enrollment_id IN (SELECT id FROM automation_enrollments WHERE org_id = $1 AND contact_id = ANY($2))`, orgID, pq.Array(ids)); err != nil {
		return err
	}
	if err = exec("automation enrollments", `DELETE FROM automation_enrollments WHERE org_id = $1 AND contact_id = ANY($2)`, orgID, pq.Array(ids)); err != nil {
		return err
	}

	// Whole-address match: bob@x.test must not hit jimbob@x.test or bob@x.test.au.
	// strpos stays as a cheap prefilter before the regex.
	addrPattern := `(^|[^a-z0-9._%+-])` + regexp.QuoteMeta(addr) + `(?![a-z0-9-]|\.[a-z0-9])`

	// Campaign/marketing mail: keep counters and statuses, drop content and recipients.
	emailIDs, err := collect("emails", `
		UPDATE emails SET contact_id = NULL, to_emails = ARRAY['[redacted]'], cc_emails = '{}', bcc_emails = '{}',
			reply_to = NULL, subject = '[redacted]', html_content = NULL, text_content = NULL,
			metadata = '{}', headers = '{}', updated_at = NOW()
		WHERE org_id = $1 AND (contact_id = ANY($2)
			OR EXISTS(SELECT 1 FROM unnest(COALESCE(to_emails,'{}') || COALESCE(cc_emails,'{}') || COALESCE(bcc_emails,'{}')) a
				WHERE strpos(lower(a), $3) > 0 AND lower(a) ~ $4))
		RETURNING id::text`, orgID, pq.Array(ids), addr, addrPattern)
	if err != nil {
		return err
	}
	if err = exec("delivery events", `UPDATE delivery_events SET data = '{}' WHERE email_id = ANY($1::bigint[])`, pq.Array(emailIDs)); err != nil {
		return err
	}

	// Webhook outbox: redact payloads naming the address or a contact uuid and
	// stop deliveries that have not started. One already "delivering" may still go out.
	eventIDs, err := collect("webhook events", `
		UPDATE webhook_events SET payload = jsonb_build_object('redacted', true, 'reason', 'gdpr_erasure')
		WHERE org_id = $1 AND ((strpos(lower(payload::text), $2) > 0 AND lower(payload::text) ~ $4)
			OR EXISTS(SELECT 1 FROM unnest($3::text[]) u WHERE strpos(payload::text, u) > 0))
		RETURNING id::text`, orgID, addr, pq.Array(uuids), addrPattern)
	if err != nil {
		return err
	}
	if err = exec("webhook deliveries", `UPDATE webhook_deliveries SET status = 'cancelled', updated_at = NOW() WHERE event_id = ANY($1::uuid[]) AND status IN ('pending','retry')`, pq.Array(eventIDs)); err != nil {
		return err
	}
	if err = exec("webhook attempts", `UPDATE webhook_delivery_attempts SET response_body = '' WHERE delivery_id IN (SELECT id FROM webhook_deliveries WHERE event_id = ANY($1::uuid[]))`, pq.Array(eventIDs)); err != nil {
		return err
	}
	if err = exec("legacy webhook calls", `
		UPDATE webhook_calls c SET payload = '{"redacted":true}', response_body = NULL
		FROM webhooks w WHERE w.id = c.webhook_id AND w.org_id = $1 AND strpos(lower(c.payload::text), $2) > 0 AND lower(c.payload::text) ~ $3`, orgID, addr, addrPattern); err != nil {
		return err
	}

	// Signup history also revokes outstanding confirmation links.
	if err = exec("signup history", `DELETE FROM signup_requests r USING signup_forms f WHERE r.form_id = f.id AND f.org_id = $1 AND lower(r.email) = $2`, orgID, addr); err != nil {
		return err
	}
	if err = exec("consent audit", `DELETE FROM consent_audit WHERE org_id = $1 AND contact_id = ANY($2)`, orgID, pq.Array(ids)); err != nil {
		return err
	}
	listIDs, err := collect("list memberships", `DELETE FROM list_contacts WHERE contact_id = ANY($1) RETURNING list_id::text`, pq.Array(ids))
	if err != nil {
		return err
	}
	if err = exec("contacts", `DELETE FROM contacts WHERE org_id = $1 AND id = ANY($2)`, orgID, pq.Array(ids)); err != nil {
		return err
	}
	if err = exec("list counts", `UPDATE lists SET contact_count = (SELECT COUNT(*) FROM list_contacts WHERE list_id = lists.id), updated_at = NOW() WHERE org_id = $1 AND id = ANY($2::int[])`, orgID, pq.Array(listIDs)); err != nil {
		return err
	}

	// Replace any plaintext suppression with a hash-only row that still blocks sends.
	if err = exec("suppression cleanup", `DELETE FROM suppressions WHERE org_id = $1 AND email_sha256 = $2`, orgID, hash); err != nil {
		return err
	}
	if err = exec("suppression", `
		INSERT INTO suppressions (org_id, email, email_sha256, reason, source_type, created_at)
		VALUES ($1, 'erased:' || $2, $2, 'gdpr_erasure', 'gdpr', NOW())
		ON CONFLICT (org_id, email) DO NOTHING`, orgID, hash); err != nil {
		return err
	}
	var userID any
	if actor.UserID > 0 {
		userID = actor.UserID
	}
	if err = exec("audit", `
		INSERT INTO audit_logs (org_id, user_id, action, resource, description, ip_address, user_agent, new_values, status)
		VALUES ($1, $2, 'contact_erased', 'contact', 'contact erased', $3, $4, jsonb_build_object('emailSha256', $5::text, 'contacts', $6::int), 'success')`,
		orgID, userID, actor.IP, actor.UA, hash, len(ids)); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
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

// recordConsentChangeTx records a consent change in the audit trail as part of
// the caller's transaction, so the evidence commits with the change it proves.
func recordConsentChangeTx(ctx context.Context, tx *sql.Tx, contactID int64, orgID int64, action string, source string, listID *int, ipAddress string, userAgent string, details string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO consent_audit (contact_id, org_id, action, source, list_id, ip_address, user_agent, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
	`, contactID, orgID, action, source, listID, ipAddress, userAgent, details); err != nil {
		return fmt.Errorf("failed to record consent: %w", err)
	}
	return nil
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
