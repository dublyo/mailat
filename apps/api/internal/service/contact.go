package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
)

type ContactService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewContactService(db *sql.DB, cfg *config.Config) *ContactService {
	return &ContactService{db: db, cfg: cfg}
}

// CreateContact creates a new contact
func (s *ContactService) CreateContact(ctx context.Context, orgID int64, req *model.CreateContactRequest) (*model.Contact, error) {
	// Check if contact already exists
	var existingID int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id FROM contacts WHERE org_id = $1 AND email = $2",
		orgID, strings.ToLower(req.Email),
	).Scan(&existingID)

	if err == nil {
		return nil, fmt.Errorf("contact with this email already exists")
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to check existing contact: %w", err)
	}

	// Marshal attributes
	attributesJSON, err := json.Marshal(req.Attributes)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal attributes: %w", err)
	}

	// Determine consent timestamp
	var consentTimestamp *time.Time
	if req.ConsentSource != "" {
		now := time.Now()
		consentTimestamp = &now
	}

	// Insert contact
	var contact model.Contact
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO contacts (
			org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		RETURNING id, uuid, org_id, email, first_name, last_name, attributes,
			status, consent_source, consent_timestamp, engagement_score, created_at, updated_at
	`,
		orgID, strings.ToLower(req.Email), req.FirstName, req.LastName, attributesJSON,
		"active", req.ConsentSource, consentTimestamp,
	).Scan(
		&contact.ID, &contact.UUID, &contact.OrgID, &contact.Email,
		&contact.FirstName, &contact.LastName, &attributesJSON,
		&contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
		&contact.EngagementScore, &contact.CreatedAt, &contact.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create contact: %w", err)
	}

	// Parse attributes back
	if len(attributesJSON) > 0 {
		json.Unmarshal(attributesJSON, &contact.Attributes)
	}

	// Add to lists if specified
	if len(req.ListIDs) > 0 {
		for _, listID := range req.ListIDs {
			_, err = s.db.ExecContext(ctx, `
				INSERT INTO list_contacts (list_id, contact_id, created_at)
				VALUES ($1, $2, NOW())
				ON CONFLICT (list_id, contact_id) DO NOTHING
			`, listID, contact.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to add contact to list: %w", err)
			}
		}

		// Update list counts
		_, err = s.db.ExecContext(ctx, `
			UPDATE lists SET contact_count = (
				SELECT COUNT(*) FROM list_contacts WHERE list_id = lists.id
			) WHERE id = ANY($1)
		`, req.ListIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to update list counts: %w", err)
		}
	}

	return &contact, nil
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

// ListContacts retrieves contacts with pagination and filtering
func (s *ContactService) ListContacts(ctx context.Context, orgID int64, req *model.ContactSearchRequest) (*model.ContactListResponse, error) {
	// Build query
	baseQuery := "FROM contacts WHERE org_id = $1"
	args := []interface{}{orgID}
	argIndex := 2

	// Add filters
	if req.Query != "" {
		baseQuery += fmt.Sprintf(" AND (email ILIKE $%d OR first_name ILIKE $%d OR last_name ILIKE $%d)",
			argIndex, argIndex, argIndex)
		args = append(args, "%"+req.Query+"%")
		argIndex++
	}

	if len(req.Status) > 0 {
		baseQuery += fmt.Sprintf(" AND status = ANY($%d)", argIndex)
		args = append(args, req.Status)
		argIndex++
	}

	if len(req.ListIDs) > 0 {
		baseQuery += fmt.Sprintf(" AND id IN (SELECT contact_id FROM list_contacts WHERE list_id = ANY($%d))", argIndex)
		args = append(args, req.ListIDs)
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
	sortColumn := "created_at"
	if req.SortBy == "email" || req.SortBy == "firstName" || req.SortBy == "lastName" || req.SortBy == "updatedAt" {
		sortColumn = req.SortBy
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
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, baseQuery, sortColumn, sortOrder, argIndex, argIndex+1)

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

// UpdateContact updates a contact
func (s *ContactService) UpdateContact(ctx context.Context, orgID int64, contactUUID string, req *model.UpdateContactRequest) (*model.Contact, error) {
	// Build update query dynamically
	updates := []string{}
	args := []interface{}{}
	argIndex := 1

	if req.Email != "" {
		updates = append(updates, fmt.Sprintf("email = $%d", argIndex))
		args = append(args, strings.ToLower(req.Email))
		argIndex++
	}
	if req.FirstName != "" {
		updates = append(updates, fmt.Sprintf("first_name = $%d", argIndex))
		args = append(args, req.FirstName)
		argIndex++
	}
	if req.LastName != "" {
		updates = append(updates, fmt.Sprintf("last_name = $%d", argIndex))
		args = append(args, req.LastName)
		argIndex++
	}
	if req.Status != "" {
		updates = append(updates, fmt.Sprintf("status = $%d", argIndex))
		args = append(args, req.Status)
		argIndex++
	}
	if req.Attributes != nil {
		attributesJSON, _ := json.Marshal(req.Attributes)
		updates = append(updates, fmt.Sprintf("attributes = $%d", argIndex))
		args = append(args, attributesJSON)
		argIndex++
	}

	if len(updates) == 0 {
		return s.GetContact(ctx, orgID, contactUUID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE contacts SET %s
		WHERE org_id = $%d AND uuid = $%d
		RETURNING id
	`, strings.Join(updates, ", "), argIndex, argIndex+1)

	args = append(args, orgID, contactUUID)

	var id int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("contact not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update contact: %w", err)
	}

	return s.GetContact(ctx, orgID, contactUUID)
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

	return nil
}

// ImportContacts bulk imports contacts
func (s *ContactService) ImportContacts(ctx context.Context, orgID int64, req *model.ImportContactsRequest) (*model.ImportContactsResponse, error) {
	response := &model.ImportContactsResponse{}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	for i, row := range req.Contacts {
		email := strings.ToLower(row.Email)

		// Check if exists
		var existingID int64
		err := tx.QueryRowContext(ctx,
			"SELECT id FROM contacts WHERE org_id = $1 AND email = $2",
			orgID, email,
		).Scan(&existingID)

		if err == nil {
			// Contact exists
			if req.UpdateExisting {
				// Update existing contact
				attributesJSON, _ := json.Marshal(row.Attributes)
				_, err = tx.ExecContext(ctx, `
					UPDATE contacts SET
						first_name = COALESCE(NULLIF($1, ''), first_name),
						last_name = COALESCE(NULLIF($2, ''), last_name),
						attributes = COALESCE($3::jsonb, attributes),
						updated_at = NOW()
					WHERE id = $4
				`, row.FirstName, row.LastName, attributesJSON, existingID)
				if err != nil {
					response.Errors = append(response.Errors, fmt.Sprintf("row %d: failed to update", i))
					continue
				}
				response.Updated++
			} else {
				response.Skipped++
			}

			// Add to lists
			for _, listID := range req.ListIDs {
				tx.ExecContext(ctx, `
					INSERT INTO list_contacts (list_id, contact_id, created_at)
					VALUES ($1, $2, NOW())
					ON CONFLICT DO NOTHING
				`, listID, existingID)
			}
			continue
		}

		if err != sql.ErrNoRows {
			response.Errors = append(response.Errors, fmt.Sprintf("row %d: %v", i, err))
			continue
		}

		// Insert new contact
		attributesJSON, _ := json.Marshal(row.Attributes)
		var consentTimestamp *time.Time
		if req.ConsentSource != "" {
			now := time.Now()
			consentTimestamp = &now
		}

		var newID int64
		err = tx.QueryRowContext(ctx, `
			INSERT INTO contacts (
				org_id, email, first_name, last_name, attributes,
				status, consent_source, consent_timestamp, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, NOW(), NOW())
			RETURNING id
		`, orgID, email, row.FirstName, row.LastName, attributesJSON,
			req.ConsentSource, consentTimestamp,
		).Scan(&newID)
		if err != nil {
			response.Errors = append(response.Errors, fmt.Sprintf("row %d: %v", i, err))
			continue
		}

		// Add to lists
		for _, listID := range req.ListIDs {
			tx.ExecContext(ctx, `
				INSERT INTO list_contacts (list_id, contact_id, created_at)
				VALUES ($1, $2, NOW())
				ON CONFLICT DO NOTHING
			`, listID, newID)
		}

		response.Imported++
	}

	// Update list counts
	if len(req.ListIDs) > 0 {
		tx.ExecContext(ctx, `
			UPDATE lists SET contact_count = (
				SELECT COUNT(*) FROM list_contacts WHERE list_id = lists.id
			) WHERE id = ANY($1)
		`, req.ListIDs)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return response, nil
}

// Unsubscribe marks a contact as unsubscribed
func (s *ContactService) Unsubscribe(ctx context.Context, orgID int64, email string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE contacts SET status = 'unsubscribed', updated_at = NOW()
		WHERE org_id = $1 AND email = $2
	`, orgID, strings.ToLower(email))
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
	`, orgID, strings.ToLower(email))

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
		args = append(args, req.Status)
		argIndex++
	}

	// Filter by list membership
	if len(req.ListIDs) > 0 {
		baseQuery += fmt.Sprintf(" AND id IN (SELECT contact_id FROM list_contacts WHERE list_id = ANY($%d))", argIndex)
		args = append(args, req.ListIDs)
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
