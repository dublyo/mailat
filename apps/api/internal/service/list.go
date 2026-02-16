package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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
	// Handle nullable segment rules
	var segmentRulesJSON interface{}
	if req.SegmentRules != nil {
		jsonBytes, err := json.Marshal(req.SegmentRules)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal segment rules: %w", err)
		}
		segmentRulesJSON = jsonBytes
	}

	listType := req.Type
	if listType == "" {
		listType = "static"
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
		INSERT INTO lists (org_id, name, description, type, segment_rules, contact_count, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 0, NOW(), NOW())
		RETURNING id, uuid, org_id, name, description, type, segment_rules, contact_count, created_at, updated_at
	`, orgID, req.Name, description, listType, segmentRulesJSON,
	).Scan(
		&list.ID, &list.UUID, &list.OrgID, &list.Name, &descPtr,
		&list.Type, &rulesJSON, &list.ContactCount, &list.CreatedAt, &list.UpdatedAt,
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
		SELECT id, uuid, org_id, name, description, type, segment_rules, contact_count, created_at, updated_at
		FROM lists
		WHERE org_id = $1 AND uuid = $2
	`, orgID, listUUID).Scan(
		&list.ID, &list.UUID, &list.OrgID, &list.Name, &descPtr,
		&list.Type, &rulesJSON, &list.ContactCount, &list.CreatedAt, &list.UpdatedAt,
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
		SELECT id, uuid, org_id, name, description, type, segment_rules, contact_count, created_at, updated_at
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
			&list.Type, &rulesJSON, &list.ContactCount, &list.CreatedAt, &list.UpdatedAt,
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

	// Apply updates
	name := existing.Name
	if req.Name != "" {
		name = req.Name
	}
	description := existing.Description
	if req.Description != "" {
		description = req.Description
	}

	var segmentRulesJSON []byte
	if req.SegmentRules != nil {
		segmentRulesJSON, _ = json.Marshal(req.SegmentRules)
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE lists SET
			name = $1,
			description = $2,
			segment_rules = COALESCE($3::jsonb, segment_rules),
			updated_at = NOW()
		WHERE org_id = $4 AND uuid = $5
	`, name, description, segmentRulesJSON, orgID, listUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to update list: %w", err)
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
		WHERE l.org_id = $1 AND l.uuid = $2 AND c.status IN ('scheduled', 'sending')
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

// ImportContactsToList imports contacts from a CSV-like structure directly to a list
func (s *ListService) ImportContactsToList(ctx context.Context, orgID int64, listUUID string, req *model.ImportContactsToListRequest) (*model.ImportContactsToListResponse, error) {
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

	result := &model.ImportContactsToListResponse{}
	consentSource := req.ConsentSource
	if consentSource == "" {
		consentSource = "list_import"
	}

	for _, row := range req.Contacts {
		if row.Email == "" {
			result.Skipped++
			continue
		}

		// Check if contact exists
		var existingID int64
		var existingUUID string
		err := s.db.QueryRowContext(ctx,
			"SELECT id, uuid FROM contacts WHERE org_id = $1 AND email = $2",
			orgID, row.Email,
		).Scan(&existingID, &existingUUID)

		var contactID int64
		if err == sql.ErrNoRows {
			// Create new contact
			var attributesJSON []byte
			if row.Attributes != nil {
				attributesJSON, _ = json.Marshal(row.Attributes)
			}

			err = s.db.QueryRowContext(ctx, `
				INSERT INTO contacts (org_id, email, first_name, last_name, attributes, status, consent_source, consent_timestamp, engagement_score, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, 'active', $6, NOW(), 0, NOW(), NOW())
				RETURNING id
			`, orgID, row.Email, row.FirstName, row.LastName, attributesJSON, consentSource).Scan(&contactID)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("Failed to create contact %s: %v", row.Email, err))
				continue
			}
			result.Imported++
		} else if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("Failed to check contact %s: %v", row.Email, err))
			continue
		} else {
			contactID = existingID
			if req.UpdateExisting {
				// Update existing contact
				var attributesJSON []byte
				if row.Attributes != nil {
					attributesJSON, _ = json.Marshal(row.Attributes)
				}
				_, err = s.db.ExecContext(ctx, `
					UPDATE contacts SET
						first_name = COALESCE(NULLIF($1, ''), first_name),
						last_name = COALESCE(NULLIF($2, ''), last_name),
						attributes = COALESCE($3::jsonb, attributes),
						updated_at = NOW()
					WHERE id = $4
				`, row.FirstName, row.LastName, attributesJSON, contactID)
				if err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to update contact %s: %v", row.Email, err))
				} else {
					result.Updated++
				}
			} else {
				result.Skipped++
			}
		}

		// Add contact to list
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO list_contacts (list_id, contact_id, created_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (list_id, contact_id) DO NOTHING
		`, listID, contactID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("Failed to add contact %s to list: %v", row.Email, err))
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
		return nil, fmt.Errorf("failed to update list count: %w", err)
	}

	return result, nil
}

// ManualAddContactToList creates a new contact and adds it to a list in one operation
func (s *ListService) ManualAddContactToList(ctx context.Context, orgID int64, listUUID string, req *model.ManualAddContactToListRequest) (*model.Contact, error) {
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

	// Check if contact already exists
	var existingID int64
	err = s.db.QueryRowContext(ctx,
		"SELECT id FROM contacts WHERE org_id = $1 AND email = $2",
		orgID, req.Email,
	).Scan(&existingID)

	var contact model.Contact
	var attributesJSON []byte
	if req.Attributes != nil {
		attributesJSON, _ = json.Marshal(req.Attributes)
	}

	if err == sql.ErrNoRows {
		// Create new contact
		err = s.db.QueryRowContext(ctx, `
			INSERT INTO contacts (org_id, email, first_name, last_name, attributes, status, consent_source, consent_timestamp, engagement_score, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'active', 'manual', NOW(), 0, NOW(), NOW())
			RETURNING id, uuid, org_id, email, first_name, last_name, attributes, status, consent_source, consent_timestamp, last_engaged_at, engagement_score, created_at, updated_at
		`, orgID, req.Email, req.FirstName, req.LastName, attributesJSON).Scan(
			&contact.ID, &contact.UUID, &contact.OrgID, &contact.Email,
			&contact.FirstName, &contact.LastName, &attributesJSON,
			&contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
			&contact.LastEngagedAt, &contact.EngagementScore,
			&contact.CreatedAt, &contact.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create contact: %w", err)
		}
		if len(attributesJSON) > 0 {
			json.Unmarshal(attributesJSON, &contact.Attributes)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to check existing contact: %w", err)
	} else {
		// Contact exists, just get it
		err = s.db.QueryRowContext(ctx, `
			SELECT id, uuid, org_id, email, first_name, last_name, attributes, status, consent_source, consent_timestamp, last_engaged_at, engagement_score, created_at, updated_at
			FROM contacts WHERE id = $1
		`, existingID).Scan(
			&contact.ID, &contact.UUID, &contact.OrgID, &contact.Email,
			&contact.FirstName, &contact.LastName, &attributesJSON,
			&contact.Status, &contact.ConsentSource, &contact.ConsentTimestamp,
			&contact.LastEngagedAt, &contact.EngagementScore,
			&contact.CreatedAt, &contact.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing contact: %w", err)
		}
		if len(attributesJSON) > 0 {
			json.Unmarshal(attributesJSON, &contact.Attributes)
		}
	}

	// Add contact to list
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO list_contacts (list_id, contact_id, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (list_id, contact_id) DO NOTHING
	`, listID, contact.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to add contact to list: %w", err)
	}

	// Update contact count
	_, err = s.db.ExecContext(ctx, `
		UPDATE lists SET contact_count = (
			SELECT COUNT(*) FROM list_contacts WHERE list_id = $1
		), updated_at = NOW()
		WHERE id = $1
	`, listID)
	if err != nil {
		return nil, fmt.Errorf("failed to update list count: %w", err)
	}

	return &contact, nil
}
