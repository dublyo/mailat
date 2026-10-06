package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
)

type ListService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewListService(db *sql.DB, cfg *config.Config) *ListService {
	return &ListService{db: db, cfg: cfg}
}

// CreateList creates a new contact list
func (s *ListService) CreateList(ctx context.Context, orgID int64, req *model.CreateListRequest) (*model.List, error) {
	if req.ConfirmationMode == "" {
		req.ConfirmationMode = "single"
	}
	if req.ConfirmationMode != "single" && req.ConfirmationMode != "double" {
		return nil, fmt.Errorf("confirmationMode must be single or double")
	}
	listType := req.Type
	if listType == "" {
		listType = "static"
	}
	if listType != "static" && listType != "dynamic" {
		return nil, fmt.Errorf("type must be static or dynamic")
	}
	// Handle nullable segment rules
	var segmentRulesJSON interface{}
	if req.SegmentRules != nil {
		jsonBytes, err := json.Marshal(req.SegmentRules)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal segment rules: %w", err)
		}
		segmentRulesJSON = jsonBytes
	}
	if listType == "dynamic" {
		raw, _ := segmentRulesJSON.([]byte)
		if err := ValidateSegmentRules(ctx, s.db, orgID, raw); err != nil {
			return nil, err
		}
	}

	// Handle nullable description
	var description interface{}
	if req.Description != "" {
		description = req.Description
	}

	var list model.List
	var rulesJSON []byte
	var descPtr sql.NullString
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO lists (org_id, name, description, type, segment_rules, contact_count, created_at, updated_at, confirmation_mode)
		VALUES ($1, $2, $3, $4, $5, 0, NOW(), NOW(), $6)
		RETURNING id, uuid, org_id, name, description, type, segment_rules, contact_count, created_at, updated_at, confirmation_mode
	`, orgID, req.Name, description, listType, segmentRulesJSON, req.ConfirmationMode,
	).Scan(
		&list.ID, &list.UUID, &list.OrgID, &list.Name, &descPtr,
		&list.Type, &rulesJSON, &list.ContactCount, &list.CreatedAt, &list.UpdatedAt, &list.ConfirmationMode,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create list: %w", err)
	}

	if descPtr.Valid {
		list.Description = descPtr.String
	}
	if len(rulesJSON) > 0 {
		json.Unmarshal(rulesJSON, &list.SegmentRules)
	}

	return &list, nil
}

// GetList retrieves a list by UUID
func (s *ListService) GetList(ctx context.Context, orgID int64, listUUID string) (*model.List, error) {
	var list model.List
	var rulesJSON []byte
	var descPtr sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, name, description, type, segment_rules, contact_count, created_at, updated_at, confirmation_mode
		FROM lists
		WHERE org_id = $1 AND uuid = $2
	`, orgID, listUUID).Scan(
		&list.ID, &list.UUID, &list.OrgID, &list.Name, &descPtr,
		&list.Type, &rulesJSON, &list.ContactCount, &list.CreatedAt, &list.UpdatedAt, &list.ConfirmationMode,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("list not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get list: %w", err)
	}

	if descPtr.Valid {
		list.Description = descPtr.String
	}
	if len(rulesJSON) > 0 {
		json.Unmarshal(rulesJSON, &list.SegmentRules)
	}

	return &list, nil
}

// ListLists retrieves all lists for an organization
func (s *ListService) ListLists(ctx context.Context, orgID int64) ([]model.List, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, org_id, name, description, type, segment_rules, contact_count, created_at, updated_at, confirmation_mode
		FROM lists
		WHERE org_id = $1
		ORDER BY name ASC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to query lists: %w", err)
	}
	defer rows.Close()

	var lists []model.List
	for rows.Next() {
		var list model.List
		var rulesJSON []byte
		var descPtr sql.NullString
		if err := rows.Scan(
			&list.ID, &list.UUID, &list.OrgID, &list.Name, &descPtr,
			&list.Type, &rulesJSON, &list.ContactCount, &list.CreatedAt, &list.UpdatedAt, &list.ConfirmationMode,
		); err != nil {
			continue
		}
		if descPtr.Valid {
			list.Description = descPtr.String
		}
		if len(rulesJSON) > 0 {
			json.Unmarshal(rulesJSON, &list.SegmentRules)
		}
		lists = append(lists, list)
	}

	return lists, nil
}

// UpdateList updates a list
func (s *ListService) UpdateList(ctx context.Context, orgID int64, listUUID string, req *model.UpdateListRequest) (*model.List, error) {
	// Get existing list
	existing, err := s.GetList(ctx, orgID, listUUID)
	if err != nil {
		return nil, err
	}

	if req.ConfirmationMode != "" && req.ConfirmationMode != "single" && req.ConfirmationMode != "double" {
		return nil, fmt.Errorf("confirmationMode must be single or double")
	}
	mode := existing.ConfirmationMode
	if req.ConfirmationMode != "" {
		mode = req.ConfirmationMode
	}
	// Lock the list while checking senders; public publishing uses the same lock.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM lists WHERE id=$1 AND org_id=$2 FOR UPDATE`, existing.ID, orgID); err != nil {
		return nil, err
	}
	if mode == "double" {
		var invalid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM signup_forms f LEFT JOIN identities i ON i.id=f.identity_id LEFT JOIN domains d ON d.id=i.domain_id LEFT JOIN users u ON u.id=i.user_id WHERE f.list_id=$1 AND f.published AND (i.id IS NULL OR NOT i.can_send OR i.user_id<>f.created_by OR u.status<>'active' OR d.org_id<>f.org_id OR d.status<>'active' OR ($2 AND NOT COALESCE(d.ses_verified,false))))`, existing.ID, s.cfg.EmailProvider == "ses").Scan(&invalid)
		if err != nil {
			return nil, err
		}
		if invalid {
			return nil, fmt.Errorf("choose a verified sender for every published form before enabling double opt-in")
		}
	}
	// Apply updates
	name := existing.Name
	if req.Name != "" {
		name = req.Name
	}
	description := existing.Description
	if req.Description != "" {
		description = req.Description
	}

	var segmentRulesJSON interface{}
	if req.SegmentRules != nil {
		raw, err := json.Marshal(req.SegmentRules)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal segment rules: %w", err)
		}
		if existing.Type == "dynamic" {
			if err = ValidateSegmentRules(ctx, tx, orgID, raw); err != nil {
				return nil, err
			}
		}
		segmentRulesJSON = raw
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE lists SET
			name = $1,
			description = $2,
			segment_rules = COALESCE($3::jsonb, segment_rules),
            confirmation_mode = $6,
			updated_at = NOW()
		WHERE org_id = $4 AND uuid = $5
	`, name, description, segmentRulesJSON, orgID, listUUID, mode)
	if err != nil {
		return nil, fmt.Errorf("failed to update list: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetList(ctx, orgID, listUUID)
}

// DeleteList deletes a list
func (s *ListService) DeleteList(ctx context.Context, orgID int64, listUUID string) error {
	// First check if list is used by any campaigns
	var campaignCount int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM campaigns c
		JOIN lists l ON l.id = c.list_id
		WHERE l.org_id = $1 AND l.uuid = $2 AND c.status IN ('scheduled', 'sending', 'paused')
	`, orgID, listUUID).Scan(&campaignCount)
	if err != nil {
		return fmt.Errorf("failed to check campaign usage: %w", err)
	}
	if campaignCount > 0 {
		return fmt.Errorf("cannot delete list with active campaigns")
	}

	result, err := s.db.ExecContext(ctx,
		"DELETE FROM lists WHERE org_id = $1 AND uuid = $2",
		orgID, listUUID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete list: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("list not found")
	}

	return nil
}

// AddContactsToList adds contacts to a list by their UUIDs
func (s *ListService) AddContactsToList(ctx context.Context, orgID int64, listUUID string, contactUUIDs []string) error {
	// Get list ID
	var listID int
	err := s.db.QueryRowContext(ctx,
		"SELECT id FROM lists WHERE org_id = $1 AND uuid = $2",
		orgID, listUUID,
	).Scan(&listID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("list not found")
	}
	if err != nil {
		return fmt.Errorf("failed to get list: %w", err)
	}

	// Verify contacts belong to org and insert by UUID
	for _, contactUUID := range contactUUIDs {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO list_contacts (list_id, contact_id, created_at)
			SELECT $1, c.id, NOW()
			FROM contacts c
			WHERE c.uuid = $2 AND c.org_id = $3
			ON CONFLICT (list_id, contact_id) DO NOTHING
		`, listID, contactUUID, orgID)
		if err != nil {
			return fmt.Errorf("failed to add contact to list: %w", err)
		}
	}

	// Update contact count
	_, err = s.db.ExecContext(ctx, `
		UPDATE lists SET contact_count = (
			SELECT COUNT(*) FROM list_contacts WHERE list_id = $1
		), updated_at = NOW()
		WHERE id = $1
	`, listID)
	if err != nil {
		return fmt.Errorf("failed to update list count: %w", err)
	}

	return nil
}

// RemoveContactsFromList removes contacts from a list by their UUIDs
func (s *ListService) RemoveContactsFromList(ctx context.Context, orgID int64, listUUID string, contactUUIDs []string) error {
	// Get list ID
	var listID int
	err := s.db.QueryRowContext(ctx,
		"SELECT id FROM lists WHERE org_id = $1 AND uuid = $2",
		orgID, listUUID,
	).Scan(&listID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("list not found")
	}
	if err != nil {
		return fmt.Errorf("failed to get list: %w", err)
	}

	// Remove contacts by UUID
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM list_contacts
		WHERE list_id = $1 AND contact_id IN (
			SELECT id FROM contacts WHERE uuid = ANY($2) AND org_id = $3
		)
	`, listID, contactUUIDs, orgID)
	if err != nil {
		return fmt.Errorf("failed to remove contacts from list: %w", err)
	}

	// Update contact count
	_, err = s.db.ExecContext(ctx, `
		UPDATE lists SET contact_count = (
			SELECT COUNT(*) FROM list_contacts WHERE list_id = $1
		), updated_at = NOW()
		WHERE id = $1
	`, listID)
	if err != nil {
		return fmt.Errorf("failed to update list count: %w", err)
	}

	return nil
}

// GetListContacts retrieves contacts in a list
func (s *ListService) GetListContacts(ctx context.Context, orgID int64, listUUID string, page, pageSize int) (*model.ContactListResponse, error) {
	// Get list ID
	var listID int
	err := s.db.QueryRowContext(ctx,
		"SELECT id FROM lists WHERE org_id = $1 AND uuid = $2",
		orgID, listUUID,
	).Scan(&listID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("list not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get list: %w", err)
	}

	// Count total
	var total int
	err = s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM list_contacts WHERE list_id = $1",
		listID,
	).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("failed to count contacts: %w", err)
	}

	// Handle pagination
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize
	totalPages := (total + pageSize - 1) / pageSize

	// Query contacts
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.uuid, c.org_id, c.email, c.first_name, c.last_name, c.attributes,
			c.status, c.consent_source, c.consent_timestamp, c.last_engaged_at,
			c.engagement_score, c.created_at, c.updated_at
		FROM contacts c
		JOIN list_contacts lc ON lc.contact_id = c.id
		WHERE lc.list_id = $1
		ORDER BY lc.created_at DESC
		LIMIT $2 OFFSET $3
	`, listID, pageSize, offset)
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
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}

// ImportContactsToList imports contacts directly to a list. Addresses are
// normalized to lowercase, each row runs in its own SAVEPOINT, and suppressed
// or non-active contacts are never added (counted in Suppressed).
func (s *ListService) ImportContactsToList(ctx context.Context, orgID int64, listUUID string, req *model.ImportContactsToListRequest) (*model.ImportContactsToListResponse, error) {
	if len(req.Contacts) > MaxContactImportRows {
		return nil, invalidContact(fmt.Sprintf("cannot import more than %d contacts at once", MaxContactImportRows))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	var listID int
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM lists WHERE org_id = $1 AND uuid = $2",
		orgID, listUUID,
	).Scan(&listID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("list not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get list: %w", err)
	}

	result := &model.ImportContactsToListResponse{}
	consentSource := req.ConsentSource
	if consentSource == "" {
		consentSource = "list_import"
	}

	for i, row := range req.Contacts {
		n := i + 1
		if strings.TrimSpace(row.Email) == "" {
			result.Skipped++
			continue
		}
		email, err := normalizeContactEmail(row.Email)
		if err == nil {
			err = validateContactNames(row.FirstName, row.LastName)
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("row %d: %s", n, err.Error()))
			continue
		}
		if _, err = tx.ExecContext(ctx, `SAVEPOINT import_row`); err != nil {
			return nil, fmt.Errorf("failed to import contacts: %w", err)
		}
		outcome, err := importContactRow(ctx, tx, orgID, email, row, []int{listID}, req.UpdateExisting, consentSource)
		if err != nil {
			if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT import_row`); rbErr != nil {
				return nil, fmt.Errorf("failed to import contacts: %w", rbErr)
			}
			result.Errors = append(result.Errors, fmt.Sprintf("row %d: could not be saved", n))
			continue
		}
		if _, err = tx.ExecContext(ctx, `RELEASE SAVEPOINT import_row`); err != nil {
			return nil, fmt.Errorf("failed to import contacts: %w", err)
		}
		switch outcome {
		case importImported:
			result.Imported++
		case importUpdated:
			result.Updated++
		case importSkipped:
			result.Skipped++
		case importSuppressed:
			result.Suppressed++
		}
	}

	if err = recountLists(ctx, tx, orgID, []int{listID}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to import contacts: %w", err)
	}
	return result, nil
}

// ManualAddContactToList creates (or finds, case-insensitively) a contact and
// adds it to a list. Suppressed or non-active addresses are refused.
func (s *ListService) ManualAddContactToList(ctx context.Context, orgID int64, listUUID string, req *model.ManualAddContactToListRequest) (*model.Contact, error) {
	email, err := normalizeContactEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err = validateContactNames(req.FirstName, req.LastName); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	var listID int
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM lists WHERE org_id = $1 AND uuid = $2",
		orgID, listUUID,
	).Scan(&listID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("list not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get list: %w", err)
	}

	outcome, err := importContactRow(ctx, tx, orgID, email, model.ImportContactRow{
		Email: email, FirstName: req.FirstName, LastName: req.LastName, Attributes: req.Attributes,
	}, []int{listID}, false, "manual")
	if err != nil {
		return nil, fmt.Errorf("failed to add contact to list: %w", err)
	}
	if outcome == importSuppressed {
		return nil, ErrContactSuppressed
	}
	if err = recountLists(ctx, tx, orgID, []int{listID}); err != nil {
		return nil, err
	}

	var contact model.Contact
	var attributesJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, email, first_name, last_name, attributes, status, consent_source, consent_timestamp, last_engaged_at, engagement_score, created_at, updated_at
		FROM contacts WHERE org_id = $1 AND lower(email) = $2 ORDER BY id LIMIT 1
	`, orgID, email).Scan(
		&contact.ID, &contact.UUID, &contact.OrgID, &contact.Email,
		&contact.FirstName, &contact.LastName, &attributesJSON,
		&contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
		&contact.LastEngagedAt, &contact.EngagementScore,
		&contact.CreatedAt, &contact.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get contact: %w", err)
	}
	if len(attributesJSON) > 0 {
		json.Unmarshal(attributesJSON, &contact.Attributes)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to add contact to list: %w", err)
	}
	return &contact, nil
}
