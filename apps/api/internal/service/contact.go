package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/lib/pq"
)

type ContactService struct {
	db                    *sql.DB
	cfg                   *config.Config
	webhookTriggerService *WebhookTriggerService
}

func NewContactService(db *sql.DB, cfg *config.Config) *ContactService {
	return &ContactService{db: db, cfg: cfg}
}

// SetWebhookTriggerService sets the webhook trigger service for firing trigger events
func (s *ContactService) SetWebhookTriggerService(svc *WebhookTriggerService) {
	s.webhookTriggerService = svc
}

// CreateContact creates a new contact. The address is normalized, list IDs
// must belong to the org, and suppressed addresses are refused.
func (s *ContactService) CreateContact(ctx context.Context, orgID int64, req *model.CreateContactRequest) (*model.Contact, error) {
	email, err := normalizeContactEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err = validateContactNames(req.FirstName, req.LastName); err != nil {
		return nil, err
	}
	listIDs := uniqueInts(req.ListIDs)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	if err = validateOrgLists(ctx, tx, orgID, listIDs); err != nil {
		return nil, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE org_id = $1 AND lower(email) = $2)`, orgID, email).Scan(&exists); err != nil {
		return nil, fmt.Errorf("failed to check existing contact: %w", err)
	}
	if exists {
		return nil, ErrContactExists
	}
	suppressed, err := isSuppressedTx(ctx, tx, orgID, email)
	if err != nil {
		return nil, err
	}
	if suppressed {
		return nil, ErrContactSuppressed
	}

	attributesJSON, err := json.Marshal(req.Attributes)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal attributes: %w", err)
	}
	var consentTimestamp *time.Time
	if req.ConsentSource != "" {
		now := time.Now()
		consentTimestamp = &now
	}

	var contact model.Contact
	err = tx.QueryRowContext(ctx, `
		INSERT INTO contacts (
			org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, NOW(), NOW())
		RETURNING id, uuid, org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, engagement_score, created_at, updated_at
	`,
		orgID, email, req.FirstName, req.LastName, attributesJSON, req.ConsentSource, consentTimestamp,
	).Scan(
		&contact.ID, &contact.UUID, &contact.OrgID, &contact.Email,
		&contact.FirstName, &contact.LastName, &attributesJSON,
		&contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
		&contact.EngagementScore, &contact.CreatedAt, &contact.UpdatedAt,
	)
	if isUniqueViolation(err) {
		return nil, ErrContactExists
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create contact: %w", err)
	}
	if len(attributesJSON) > 0 {
		json.Unmarshal(attributesJSON, &contact.Attributes)
	}

	for _, listID := range listIDs {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO list_contacts (list_id, contact_id, created_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (list_id, contact_id) DO NOTHING
		`, listID, contact.ID); err != nil {
			return nil, fmt.Errorf("failed to add contact to list: %w", err)
		}
	}
	if err = recountLists(ctx, tx, orgID, listIDs); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to create contact: %w", err)
	}

	// Fire webhook trigger
	if s.webhookTriggerService != nil {
		go s.webhookTriggerService.Fire(context.Background(), orgID, TriggerContactCreated, map[string]interface{}{
			"contact_id": contact.UUID,
			"email":      contact.Email,
			"first_name": contact.FirstName,
			"last_name":  contact.LastName,
		})
	}

	return &contact, nil
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

// GetContact retrieves a contact by UUID
func (s *ContactService) GetContact(ctx context.Context, orgID int64, contactUUID string) (*model.Contact, error) {
	var contact model.Contact
	var attributesJSON []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, last_engaged_at,
			engagement_score, created_at, updated_at
		FROM contacts
		WHERE org_id = $1 AND uuid = $2
	`, orgID, contactUUID).Scan(
		&contact.ID, &contact.UUID, &contact.OrgID, &contact.Email,
		&contact.FirstName, &contact.LastName, &attributesJSON,
		&contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
		&contact.LastEngagedAt, &contact.EngagementScore,
		&contact.CreatedAt, &contact.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("contact not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get contact: %w", err)
	}

	if len(attributesJSON) > 0 {
		json.Unmarshal(attributesJSON, &contact.Attributes)
	}

	// Get list memberships
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.name, lc.created_at
		FROM list_contacts lc
		JOIN lists l ON l.id = lc.list_id
		WHERE lc.contact_id = $1
	`, contact.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get list memberships: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m model.ListMembership
		if err := rows.Scan(&m.ListID, &m.ListName, &m.JoinedAt); err != nil {
			continue
		}
		contact.Lists = append(contact.Lists, m)
	}

	return &contact, nil
}

// contactSortColumns maps API sort keys to columns; anything else sorts by created_at.
var contactSortColumns = map[string]string{
	"createdAt": "created_at",
	"updatedAt": "updated_at",
	"email":     "email",
	"firstName": "first_name",
	"lastName":  "last_name",
}

// ListContacts retrieves contacts with pagination and filtering
func (s *ContactService) ListContacts(ctx context.Context, orgID int64, req *model.ContactSearchRequest) (*model.ContactListResponse, error) {
	// Build query
	baseQuery := "FROM contacts WHERE org_id = $1"
	args := []interface{}{orgID}
	argIndex := 2

	// Add filters; the query matches literally (LIKE metacharacters escaped).
	if req.Query != "" {
		baseQuery += fmt.Sprintf(` AND (email ILIKE $%d ESCAPE '\' OR first_name ILIKE $%d ESCAPE '\' OR last_name ILIKE $%d ESCAPE '\')`,
			argIndex, argIndex, argIndex)
		args = append(args, likePattern(req.Query))
		argIndex++
	}

	if len(req.Status) > 0 {
		baseQuery += fmt.Sprintf(" AND status = ANY($%d)", argIndex)
		args = append(args, pq.Array(req.Status))
		argIndex++
	}

	if len(req.ListIDs) > 0 {
		baseQuery += fmt.Sprintf(" AND id IN (SELECT contact_id FROM list_contacts WHERE list_id = ANY($%d::int[]))", argIndex)
		args = append(args, pq.Array(req.ListIDs))
		argIndex++
	}

	// Count total
	var total int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) "+baseQuery, args...).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("failed to count contacts: %w", err)
	}

	// Handle pagination
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 50
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}

	offset := (req.Page - 1) * req.PageSize
	totalPages := (total + req.PageSize - 1) / req.PageSize

	// Build sort
	sortColumn, ok := contactSortColumns[req.SortBy]
	if !ok {
		sortColumn = "created_at"
	}
	sortOrder := "DESC"
	if req.SortOrder == "asc" {
		sortOrder = "ASC"
	}

	// Query contacts
	query := fmt.Sprintf(`
		SELECT id, uuid, org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, last_engaged_at,
			engagement_score, created_at, updated_at
		%s
		ORDER BY %s %s, id %s
		LIMIT $%d OFFSET $%d
	`, baseQuery, sortColumn, sortOrder, sortOrder, argIndex, argIndex+1)

	args = append(args, req.PageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query contacts: %w", err)
	}
	defer rows.Close()

	var contacts []model.Contact
	for rows.Next() {
		var c model.Contact
		var attributesJSON []byte
		if err := rows.Scan(
			&c.ID, &c.UUID, &c.OrgID, &c.Email,
			&c.FirstName, &c.LastName, &attributesJSON,
			&c.Status, &c.ConsentSource, &c.ConsentTimestamp,
			&c.LastEngagedAt, &c.EngagementScore,
			&c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			continue
		}
		if len(attributesJSON) > 0 {
			json.Unmarshal(attributesJSON, &c.Attributes)
		}
		contacts = append(contacts, c)
	}

	return &model.ContactListResponse{
		Contacts:   contacts,
		Total:      total,
		Page:       req.Page,
		PageSize:   req.PageSize,
		TotalPages: totalPages,
	}, nil
}

// UpdateContact updates a contact. Admins may unsubscribe anyone, and may only
// reactivate a bounced address that is not suppressed; every status change is
// recorded in consent_audit with the acting user.
func (s *ContactService) UpdateContact(ctx context.Context, orgID int64, actor ContactActor, contactUUID string, req *model.UpdateContactRequest) (*model.Contact, error) {
	if err := validateContactNames(req.FirstName, req.LastName); err != nil {
		return nil, err
	}
	var newEmail string
	if req.Email != "" {
		var err error
		if newEmail, err = normalizeContactEmail(req.Email); err != nil {
			return nil, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	var id int64
	var email, status string
	err = tx.QueryRowContext(ctx, `SELECT id, email, status FROM contacts WHERE org_id = $1 AND uuid = $2 FOR UPDATE`, orgID, contactUUID).Scan(&id, &email, &status)
	if err == sql.ErrNoRows {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load contact: %w", err)
	}

	updates := []string{}
	args := []interface{}{}
	add := func(column string, value any) {
		args = append(args, value)
		updates = append(updates, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	actorDetails := fmt.Sprintf("Changed by user %d", actor.UserID)

	switch req.Status {
	case "":
	case "unsubscribed":
		if status != "unsubscribed" {
			add("status", "unsubscribed")
			if err = recordConsentChangeTx(ctx, tx, id, orgID, "unsubscribe", "admin", nil, actor.IP, actor.UA, actorDetails); err != nil {
				return nil, err
			}
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO suppressions (org_id, email, reason, source_type, created_at)
			VALUES ($1, $2, 'unsubscribe', 'admin', NOW())
			ON CONFLICT (org_id, email) DO NOTHING
		`, orgID, strings.ToLower(email)); err != nil {
			return nil, fmt.Errorf("failed to suppress address: %w", err)
		}
	case "active":
		if status != "active" {
			if status != "bounced" {
				return nil, ErrReactivationBlocked
			}
			suppressed, err := isSuppressedTx(ctx, tx, orgID, email)
			if err != nil {
				return nil, err
			}
			if suppressed {
				return nil, ErrReactivationBlocked
			}
			add("status", "active")
			if err = recordConsentChangeTx(ctx, tx, id, orgID, "status_change", "admin", nil, actor.IP, actor.UA, actorDetails+": bounced -> active"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, ErrSystemStatus
	}

	if newEmail != "" && newEmail != email {
		// Legacy rows may differ only in case, which the unique index misses;
		// one address must stay one contact.
		var taken bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE org_id = $1 AND lower(email) = $2 AND id <> $3)`, orgID, newEmail, id).Scan(&taken); err != nil {
			return nil, fmt.Errorf("failed to check email: %w", err)
		}
		if taken {
			return nil, ErrDuplicateEmail
		}
		add("email", newEmail)
	}
	if req.FirstName != "" {
		add("first_name", req.FirstName)
	}
	if req.LastName != "" {
		add("last_name", req.LastName)
	}
	if req.Attributes != nil {
		attributesJSON, err := json.Marshal(req.Attributes)
		if err != nil {
			return nil, invalidContact("invalid attributes")
		}
		add("attributes", attributesJSON)
	}

	if len(updates) > 0 {
		updates = append(updates, "updated_at = NOW()")
		args = append(args, id)
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE contacts SET %s WHERE id = $%d`, strings.Join(updates, ", "), len(args)), args...)
		if isUniqueViolation(err) {
			return nil, ErrDuplicateEmail
		}
		if err != nil {
			return nil, fmt.Errorf("failed to update contact: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to update contact: %w", err)
	}

	contact, err := s.GetContact(ctx, orgID, contactUUID)
	if err != nil {
		return nil, err
	}

	// Fire webhook trigger
	if len(updates) > 0 && s.webhookTriggerService != nil {
		go s.webhookTriggerService.Fire(context.Background(), orgID, TriggerContactUpdated, map[string]interface{}{
			"contact_id": contactUUID,
			"email":      contact.Email,
		})
	}

	return contact, nil
}

// DeleteContact deletes a contact
func (s *ContactService) DeleteContact(ctx context.Context, orgID int64, contactUUID string) error {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM contacts WHERE org_id = $1 AND uuid = $2",
		orgID, contactUUID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete contact: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("contact not found")
	}

	// Fire webhook trigger
	if s.webhookTriggerService != nil {
		go s.webhookTriggerService.Fire(context.Background(), orgID, TriggerContactDeleted, map[string]interface{}{
			"contact_id": contactUUID,
		})
	}

	return nil
}

// MaxContactImportRows caps one POST /contacts/import request.
const MaxContactImportRows = 10000

// ImportContacts bulk imports contacts. Each row runs in its own SAVEPOINT, so
// a bad row is reported with its 1-based number and never aborts the batch.
// Suppressed addresses and existing non-active contacts are never added to
// lists; they are counted in Suppressed.
func (s *ContactService) ImportContacts(ctx context.Context, orgID int64, req *model.ImportContactsRequest) (*model.ImportContactsResponse, error) {
	if len(req.Contacts) > MaxContactImportRows {
		return nil, invalidContact(fmt.Sprintf("cannot import more than %d contacts at once", MaxContactImportRows))
	}
	listIDs := uniqueInts(req.ListIDs)
	response := &model.ImportContactsResponse{}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	if err = validateOrgLists(ctx, tx, orgID, listIDs); err != nil {
		return nil, err
	}

	for i, row := range req.Contacts {
		n := i + 1
		email, err := normalizeContactEmail(row.Email)
		if err == nil {
			err = validateContactNames(row.FirstName, row.LastName)
		}
		if err != nil {
			response.Errors = append(response.Errors, fmt.Sprintf("row %d: %s", n, err.Error()))
			continue
		}
		if _, err = tx.ExecContext(ctx, `SAVEPOINT import_row`); err != nil {
			return nil, fmt.Errorf("failed to import contacts: %w", err)
		}
		outcome, err := importContactRow(ctx, tx, orgID, email, row, listIDs, req.UpdateExisting, req.ConsentSource)
		if err != nil {
			if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT import_row`); rbErr != nil {
				return nil, fmt.Errorf("failed to import contacts: %w", rbErr)
			}
			response.Errors = append(response.Errors, fmt.Sprintf("row %d: could not be saved", n))
			continue
		}
		if _, err = tx.ExecContext(ctx, `RELEASE SAVEPOINT import_row`); err != nil {
			return nil, fmt.Errorf("failed to import contacts: %w", err)
		}
		switch outcome {
		case importImported:
			response.Imported++
		case importUpdated:
			response.Updated++
		case importSkipped:
			response.Skipped++
		case importSuppressed:
			response.Suppressed++
		}
	}

	if err = recountLists(ctx, tx, orgID, listIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return response, nil
}

type importOutcome int

const (
	importImported importOutcome = iota
	importUpdated
	importSkipped
	importSuppressed
)

// importContactRow upserts one normalized row and adds it to listIDs when the
// address may receive marketing mail. Shared by contact and list imports.
func importContactRow(ctx context.Context, tx *sql.Tx, orgID int64, email string, row model.ImportContactRow, listIDs []int, updateExisting bool, consentSource string) (importOutcome, error) {
	// nil (SQL NULL) keeps existing attributes on update.
	var attributesJSON any
	if row.Attributes != nil {
		b, err := json.Marshal(row.Attributes)
		if err != nil {
			return 0, err
		}
		attributesJSON = string(b)
	}
	suppressed, err := isSuppressedTx(ctx, tx, orgID, email)
	if err != nil {
		return 0, err
	}

	var contactID int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT id, status FROM contacts WHERE org_id = $1 AND lower(email) = $2 ORDER BY id LIMIT 1 FOR UPDATE`, orgID, email).Scan(&contactID, &status)
	var outcome importOutcome
	switch {
	case err == sql.ErrNoRows:
		if suppressed {
			return importSuppressed, nil
		}
		var consentTimestamp *time.Time
		if consentSource != "" {
			now := time.Now()
			consentTimestamp = &now
		}
		if err = tx.QueryRowContext(ctx, `
			INSERT INTO contacts (
				org_id, email, first_name, last_name, attributes,
				status, consent_source, consent_timestamp, created_at, updated_at
			) VALUES ($1, $2, $3, $4, COALESCE($5::jsonb, '{}'::jsonb), 'active', $6, $7, NOW(), NOW())
			RETURNING id
		`, orgID, email, row.FirstName, row.LastName, attributesJSON, consentSource, consentTimestamp).Scan(&contactID); err != nil {
			return 0, err
		}
		if err = recordConsentChangeTx(ctx, tx, contactID, orgID, "consent_given", "import", nil, "", "", consentSource); err != nil {
			return 0, err
		}
		outcome = importImported
	case err != nil:
		return 0, err
	default:
		outcome = importSkipped
		if updateExisting {
			if _, err = tx.ExecContext(ctx, `
				UPDATE contacts SET
					first_name = COALESCE(NULLIF($1, ''), first_name),
					last_name = COALESCE(NULLIF($2, ''), last_name),
					attributes = COALESCE($3::jsonb, attributes),
					updated_at = NOW()
				WHERE id = $4
			`, row.FirstName, row.LastName, attributesJSON, contactID); err != nil {
				return 0, err
			}
			outcome = importUpdated
		}
		if suppressed || status != "active" {
			return importSuppressed, nil
		}
	}

	for _, listID := range listIDs {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO list_contacts (list_id, contact_id, created_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (list_id, contact_id) DO NOTHING
		`, listID, contactID); err != nil {
			return 0, err
		}
	}
	return outcome, nil
}

// Unsubscribe marks a contact as unsubscribed
func (s *ContactService) Unsubscribe(ctx context.Context, orgID int64, email string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE contacts SET status = 'unsubscribed', updated_at = NOW()
		WHERE org_id = $1 AND lower(email) = $2
	`, orgID, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("contact not found")
	}

	// Also add to suppression list
	s.db.ExecContext(ctx, `
		INSERT INTO suppressions (org_id, email, reason, source_type, created_at)
		VALUES ($1, $2, 'unsubscribe', 'contact', NOW())
		ON CONFLICT (org_id, email) DO NOTHING
	`, orgID, strings.ToLower(strings.TrimSpace(email)))

	return nil
}

// ExportContacts exports contacts based on filters
func (s *ContactService) ExportContacts(ctx context.Context, orgID int64, req *model.ExportContactsRequest) ([]model.Contact, error) {
	baseQuery := "FROM contacts WHERE org_id = $1"
	args := []interface{}{orgID}
	argIndex := 2

	// Filter by status
	if len(req.Status) > 0 {
		baseQuery += fmt.Sprintf(" AND status = ANY($%d)", argIndex)
		args = append(args, pq.Array(req.Status))
		argIndex++
	}

	// Filter by list membership
	if len(req.ListIDs) > 0 {
		baseQuery += fmt.Sprintf(" AND id IN (SELECT contact_id FROM list_contacts WHERE list_id = ANY($%d::int[]))", argIndex)
		args = append(args, pq.Array(req.ListIDs))
	}

	// Query all matching contacts (no pagination for export)
	query := fmt.Sprintf(`
		SELECT id, uuid, org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, last_engaged_at,
			engagement_score, created_at, updated_at
		%s
		ORDER BY created_at DESC
	`, baseQuery)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query contacts: %w", err)
	}
	defer rows.Close()

	var contacts []model.Contact
	for rows.Next() {
		var c model.Contact
		var attributesJSON []byte
		if err := rows.Scan(
			&c.ID, &c.UUID, &c.OrgID, &c.Email,
			&c.FirstName, &c.LastName, &attributesJSON,
			&c.Status, &c.ConsentSource, &c.ConsentTimestamp,
			&c.LastEngagedAt, &c.EngagementScore,
			&c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			continue
		}
		if len(attributesJSON) > 0 {
			json.Unmarshal(attributesJSON, &c.Attributes)
		}
		contacts = append(contacts, c)
	}

	return contacts, nil
}
