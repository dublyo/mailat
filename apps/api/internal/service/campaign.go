package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
)

type CampaignService struct {
	db                    *sql.DB
	cfg                   *config.Config
	redis                 *redis.Client
	webhookTriggerService *WebhookTriggerService
}

func NewCampaignService(db *sql.DB, cfg *config.Config, redis *redis.Client) *CampaignService {
	return &CampaignService{db: db, cfg: cfg, redis: redis}
}

// SetWebhookTriggerService sets the webhook trigger service for firing trigger events
func (s *CampaignService) SetWebhookTriggerService(svc *WebhookTriggerService) {
	s.webhookTriggerService = svc
}

// CreateCampaign creates a new email campaign
func (s *CampaignService) CreateCampaign(ctx context.Context, orgID int64, req *model.CreateCampaignRequest) (*model.Campaign, error) {
	// Verify list exists and belongs to org
	var listName string
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM lists WHERE id = $1 AND org_id = $2",
		req.ListID, orgID,
	).Scan(&listName)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("list not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to verify list: %w", err)
	}

	// Insert campaign
	var campaign model.Campaign
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO campaigns (
			org_id, name, subject, html_content, text_content, template_id,
			from_name, from_email, reply_to, list_id, status,
			total_recipients, sent_count, delivered_count, open_count, click_count,
			bounce_count, unsubscribe_count, complaint_count,
			is_ab_test, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'draft',
			0, 0, 0, 0, 0, 0, 0, 0, false, NOW(), NOW())
		RETURNING id, uuid, org_id, name, subject, html_content, text_content, template_id,
			from_name, from_email, reply_to, list_id, status, scheduled_at, started_at, completed_at,
			total_recipients, sent_count, delivered_count, open_count, click_count,
			bounce_count, unsubscribe_count, complaint_count, is_ab_test, created_at, updated_at
	`,
		orgID, req.Name, req.Subject, req.HTMLContent, req.TextContent, req.TemplateID,
		req.FromName, req.FromEmail, req.ReplyTo, req.ListID,
	).Scan(
		&campaign.ID, &campaign.UUID, &campaign.OrgID, &campaign.Name, &campaign.Subject,
		&campaign.HTMLContent, &campaign.TextContent, &campaign.TemplateID,
		&campaign.FromName, &campaign.FromEmail, &campaign.ReplyTo, &campaign.ListID,
		&campaign.Status, &campaign.ScheduledAt, &campaign.StartedAt, &campaign.CompletedAt,
		&campaign.TotalRecipients, &campaign.SentCount, &campaign.DeliveredCount,
		&campaign.OpenCount, &campaign.ClickCount, &campaign.BounceCount,
		&campaign.UnsubscribeCount, &campaign.ComplaintCount, &campaign.IsAbTest,
		&campaign.CreatedAt, &campaign.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create campaign: %w", err)
	}

	campaign.ListName = listName
	return &campaign, nil
}

// GetCampaign retrieves a campaign by UUID
func (s *CampaignService) GetCampaign(ctx context.Context, orgID int64, campaignUUID string) (*model.Campaign, error) {
	var campaign model.Campaign
	var abTestSettingsJSON []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.uuid, c.org_id, c.name, c.subject, c.html_content, c.text_content, c.template_id,
			c.from_name, c.from_email, c.reply_to, c.list_id, COALESCE(l.name, 'Deleted List'),
			c.status, c.scheduled_at, c.started_at, c.completed_at,
			c.total_recipients, c.sent_count, c.delivered_count, c.open_count, c.click_count,
			c.bounce_count, c.unsubscribe_count, c.complaint_count, c.is_ab_test, c.ab_test_settings,
			c.created_at, c.updated_at
		FROM campaigns c
		LEFT JOIN lists l ON l.id = c.list_id
		WHERE c.org_id = $1 AND c.uuid = $2
	`, orgID, campaignUUID).Scan(
		&campaign.ID, &campaign.UUID, &campaign.OrgID, &campaign.Name, &campaign.Subject,
		&campaign.HTMLContent, &campaign.TextContent, &campaign.TemplateID,
		&campaign.FromName, &campaign.FromEmail, &campaign.ReplyTo, &campaign.ListID, &campaign.ListName,
		&campaign.Status, &campaign.ScheduledAt, &campaign.StartedAt, &campaign.CompletedAt,
		&campaign.TotalRecipients, &campaign.SentCount, &campaign.DeliveredCount,
		&campaign.OpenCount, &campaign.ClickCount, &campaign.BounceCount,
		&campaign.UnsubscribeCount, &campaign.ComplaintCount, &campaign.IsAbTest, &abTestSettingsJSON,
		&campaign.CreatedAt, &campaign.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("campaign not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get campaign: %w", err)
	}

	if len(abTestSettingsJSON) > 0 {
		json.Unmarshal(abTestSettingsJSON, &campaign.AbTestSettings)
	}

	return &campaign, nil
}

// ListCampaigns retrieves campaigns with pagination
func (s *CampaignService) ListCampaigns(ctx context.Context, orgID int64, page, pageSize int, status string) (*model.CampaignListResponse, error) {
	// Build query - use LEFT JOIN to include campaigns even if their list was deleted
	baseQuery := "FROM campaigns c LEFT JOIN lists l ON l.id = c.list_id WHERE c.org_id = $1"
	args := []interface{}{orgID}
	argIndex := 2

	if status != "" {
		baseQuery += fmt.Sprintf(" AND c.status = $%d", argIndex)
		args = append(args, status)
		argIndex++
	}

	// Count total
	var total int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) "+baseQuery, args...).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("failed to count campaigns: %w", err)
	}

	// Handle pagination
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize
	totalPages := (total + pageSize - 1) / pageSize

	// Query campaigns
	query := fmt.Sprintf(`
		SELECT c.id, c.uuid, c.org_id, c.name, c.subject, c.html_content, c.text_content, c.template_id,
			c.from_name, c.from_email, c.reply_to, c.list_id, COALESCE(l.name, 'Deleted List'),
			c.status, c.scheduled_at, c.started_at, c.completed_at,
			c.total_recipients, c.sent_count, c.delivered_count, c.open_count, c.click_count,
			c.bounce_count, c.unsubscribe_count, c.complaint_count, c.is_ab_test,
			c.created_at, c.updated_at
		%s
		ORDER BY c.created_at DESC
		LIMIT $%d OFFSET $%d
	`, baseQuery, argIndex, argIndex+1)

	args = append(args, pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query campaigns: %w", err)
	}
	defer rows.Close()

	var campaigns []model.Campaign
	for rows.Next() {
		var c model.Campaign
		if err := rows.Scan(
			&c.ID, &c.UUID, &c.OrgID, &c.Name, &c.Subject, &c.HTMLContent, &c.TextContent, &c.TemplateID,
			&c.FromName, &c.FromEmail, &c.ReplyTo, &c.ListID, &c.ListName,
			&c.Status, &c.ScheduledAt, &c.StartedAt, &c.CompletedAt,
			&c.TotalRecipients, &c.SentCount, &c.DeliveredCount,
			&c.OpenCount, &c.ClickCount, &c.BounceCount,
			&c.UnsubscribeCount, &c.ComplaintCount, &c.IsAbTest,
			&c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			continue
		}
		campaigns = append(campaigns, c)
	}

	return &model.CampaignListResponse{
		Campaigns:  campaigns,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}

// UpdateCampaign updates a campaign
func (s *CampaignService) UpdateCampaign(ctx context.Context, orgID int64, campaignUUID string, req *model.UpdateCampaignRequest) (*model.Campaign, error) {
	// Verify campaign exists and is in draft status
	var currentStatus string
	err := s.db.QueryRowContext(ctx,
		"SELECT status FROM campaigns WHERE org_id = $1 AND uuid = $2",
		orgID, campaignUUID,
	).Scan(&currentStatus)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("campaign not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get campaign: %w", err)
	}
	if currentStatus != "draft" && currentStatus != "paused" {
		return nil, fmt.Errorf("can only update campaigns in draft or paused status")
	}

	// Build update query
	_, err = s.db.ExecContext(ctx, `
		UPDATE campaigns SET
			name = COALESCE(NULLIF($1, ''), name),
			subject = COALESCE(NULLIF($2, ''), subject),
			html_content = COALESCE(NULLIF($3, ''), html_content),
			text_content = COALESCE(NULLIF($4, ''), text_content),
			from_name = COALESCE(NULLIF($5, ''), from_name),
			from_email = COALESCE(NULLIF($6, ''), from_email),
			reply_to = COALESCE(NULLIF($7, ''), reply_to),
			updated_at = NOW()
		WHERE org_id = $8 AND uuid = $9
	`, req.Name, req.Subject, req.HTMLContent, req.TextContent,
		req.FromName, req.FromEmail, req.ReplyTo, orgID, campaignUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to update campaign: %w", err)
	}

	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// DeleteCampaign deletes a campaign
func (s *CampaignService) DeleteCampaign(ctx context.Context, orgID int64, campaignUUID string) error {
	// Verify campaign is in draft status
	var status string
	err := s.db.QueryRowContext(ctx,
		"SELECT status FROM campaigns WHERE org_id = $1 AND uuid = $2",
		orgID, campaignUUID,
	).Scan(&status)
	if err == sql.ErrNoRows {
		return fmt.Errorf("campaign not found")
	}
	if err != nil {
		return fmt.Errorf("failed to get campaign: %w", err)
	}
	if status != "draft" {
		return fmt.Errorf("can only delete campaigns in draft status")
	}

	_, err = s.db.ExecContext(ctx,
		"DELETE FROM campaigns WHERE org_id = $1 AND uuid = $2",
		orgID, campaignUUID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete campaign: %w", err)
	}

	return nil
}

// ScheduleCampaign schedules a campaign for future sending
func (s *CampaignService) ScheduleCampaign(ctx context.Context, orgID int64, campaignUUID string, req *model.ScheduleCampaignRequest) (*model.Campaign, error) {
	// Parse scheduled time
	scheduledAt, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil {
		return nil, fmt.Errorf("invalid scheduledAt format, use RFC3339")
	}

	if scheduledAt.Before(time.Now()) {
		return nil, fmt.Errorf("scheduledAt must be in the future")
	}

	// Get campaign and verify status
	campaign, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if campaign.Status != "draft" && campaign.Status != "paused" {
		return nil, fmt.Errorf("can only schedule campaigns in draft or paused status")
	}

	// Count recipients
	var recipientCount int
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT c.id)
		FROM contacts c
		JOIN list_contacts lc ON lc.contact_id = c.id
		WHERE lc.list_id = $1 AND c.status = 'active'
		AND c.email NOT IN (SELECT email FROM suppressions WHERE org_id = $2)
	`, campaign.ListID, orgID).Scan(&recipientCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count recipients: %w", err)
	}

	if recipientCount == 0 {
		return nil, fmt.Errorf("no active recipients in list")
	}

	// Update campaign
	_, err = s.db.ExecContext(ctx, `
		UPDATE campaigns SET
			status = 'scheduled',
			scheduled_at = $1,
			total_recipients = $2,
			updated_at = NOW()
		WHERE org_id = $3 AND uuid = $4
	`, scheduledAt, recipientCount, orgID, campaignUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to schedule campaign: %w", err)
	}

	// Queue the campaign for processing at scheduled time
	s.queueCampaignJob(ctx, campaign.ID, scheduledAt)

	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// SendCampaignNow starts sending a campaign immediately
func (s *CampaignService) SendCampaignNow(ctx context.Context, orgID int64, campaignUUID string) (*model.Campaign, error) {
	campaign, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if campaign.Status != "draft" && campaign.Status != "scheduled" && campaign.Status != "paused" {
		return nil, fmt.Errorf("campaign is not in a sendable status")
	}

	// Count recipients
	var recipientCount int
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT c.id)
		FROM contacts c
		JOIN list_contacts lc ON lc.contact_id = c.id
		WHERE lc.list_id = $1 AND c.status = 'active'
		AND c.email NOT IN (SELECT email FROM suppressions WHERE org_id = $2)
	`, campaign.ListID, orgID).Scan(&recipientCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count recipients: %w", err)
	}

	if recipientCount == 0 {
		return nil, fmt.Errorf("no active recipients in list")
	}

	// Update campaign status
	now := time.Now()
	_, err = s.db.ExecContext(ctx, `
		UPDATE campaigns SET
			status = 'sending',
			started_at = $1,
			total_recipients = $2,
			updated_at = NOW()
		WHERE org_id = $3 AND uuid = $4
	`, now, recipientCount, orgID, campaignUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to start campaign: %w", err)
	}

	// Queue the campaign for immediate processing
	s.queueCampaignJob(ctx, campaign.ID, now)

	// Fire webhook trigger
	if s.webhookTriggerService != nil {
		go s.webhookTriggerService.Fire(context.Background(), orgID, TriggerCampaignSent, map[string]interface{}{
			"campaign_id":     campaignUUID,
			"campaign_name":   campaign.Name,
			"recipient_count": recipientCount,
			"list_id":         campaign.ListID,
		})
	}

	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// PauseCampaign pauses a sending campaign
func (s *CampaignService) PauseCampaign(ctx context.Context, orgID int64, campaignUUID string) (*model.Campaign, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE campaigns SET
			status = 'paused',
			updated_at = NOW()
		WHERE org_id = $1 AND uuid = $2 AND status = 'sending'
	`, orgID, campaignUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to pause campaign: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("campaign not found or not in sending status")
	}

	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// ResumeCampaign resumes a paused campaign
func (s *CampaignService) ResumeCampaign(ctx context.Context, orgID int64, campaignUUID string) (*model.Campaign, error) {
	campaign, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if campaign.Status != "paused" {
		return nil, fmt.Errorf("campaign is not paused")
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE campaigns SET
			status = 'sending',
			updated_at = NOW()
		WHERE org_id = $1 AND uuid = $2
	`, orgID, campaignUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to resume campaign: %w", err)
	}

	// Re-queue for processing
	s.queueCampaignJob(ctx, campaign.ID, time.Now())

	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// CancelCampaign cancels a scheduled or sending campaign
func (s *CampaignService) CancelCampaign(ctx context.Context, orgID int64, campaignUUID string) (*model.Campaign, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE campaigns SET
			status = 'cancelled',
			updated_at = NOW()
		WHERE org_id = $1 AND uuid = $2 AND status IN ('scheduled', 'sending', 'paused')
	`, orgID, campaignUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel campaign: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("campaign not found or cannot be cancelled")
	}

	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// GetCampaignStats retrieves detailed campaign statistics
func (s *CampaignService) GetCampaignStats(ctx context.Context, orgID int64, campaignUUID string) (*model.CampaignStatsResponse, error) {
	campaign, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}

	stats := &model.CampaignStatsResponse{
		Campaign: campaign,
	}

	// Calculate rates
	if campaign.SentCount > 0 {
		stats.OpenRate = float64(campaign.OpenCount) / float64(campaign.SentCount) * 100
		stats.ClickRate = float64(campaign.ClickCount) / float64(campaign.SentCount) * 100
		stats.BounceRate = float64(campaign.BounceCount) / float64(campaign.SentCount) * 100
		stats.UnsubscribeRate = float64(campaign.UnsubscribeCount) / float64(campaign.SentCount) * 100
		stats.ComplaintRate = float64(campaign.ComplaintCount) / float64(campaign.SentCount) * 100
	}

	return stats, nil
}

// PreviewCampaign renders a campaign preview
func (s *CampaignService) PreviewCampaign(ctx context.Context, orgID int64, campaignUUID string) (map[string]string, error) {
	campaign, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}

	// TODO: Process template variables with sample data
	return map[string]string{
		"subject": campaign.Subject,
		"html":    campaign.HTMLContent,
		"text":    campaign.TextContent,
	}, nil
}

// SendTestEmail sends a test email for a campaign
func (s *CampaignService) SendTestEmail(ctx context.Context, orgID int64, campaignUUID string, testEmail string) error {
	campaign, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return err
	}

	// Queue a test email job
	job := map[string]interface{}{
		"type":       "campaign_test",
		"campaignId": campaign.ID,
		"testEmail":  testEmail,
		"orgId":      orgID,
	}
	jobData, _ := json.Marshal(job)

	s.redis.LPush(ctx, "email:test", jobData)

	return nil
}

// queueCampaignJob queues a campaign for processing
func (s *CampaignService) queueCampaignJob(ctx context.Context, campaignID int, scheduledAt time.Time) {
	job := map[string]interface{}{
		"type":        "campaign_send",
		"campaignId":  campaignID,
		"scheduledAt": scheduledAt.Unix(),
	}
	jobData, _ := json.Marshal(job)

	// Use Redis sorted set for scheduled jobs
	if scheduledAt.After(time.Now()) {
		s.redis.ZAdd(ctx, "campaign:scheduled", redis.Z{
			Score:  float64(scheduledAt.Unix()),
			Member: jobData,
		})
	} else {
		// Immediate processing
		s.redis.LPush(ctx, "email:campaign", jobData)
	}
}
