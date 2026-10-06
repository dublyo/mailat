package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

// CampaignActor is the caller of a campaign operation. API keys are bound to
// a user but never inherit that user's admin role for other users' campaigns.
type CampaignActor struct {
	UserID int64
	APIKey bool
}

var (
	ErrCampaignNotFound  = errors.New("campaign not found")
	ErrCampaignState     = errors.New("campaign is not in a state that allows this action")
	ErrCampaignForbidden = errors.New("only the campaign creator or a workspace admin can do this")
	// ErrCampaignTestRateLimited: more than 10 test sends per campaign per hour.
	ErrCampaignTestRateLimited = errors.New("test send limit reached for this campaign; try again later")
)

// CampaignStateError is a 409 with a specific message; errors.Is(err, ErrCampaignState) holds.
type CampaignStateError struct{ Message string }

func (e *CampaignStateError) Error() string        { return e.Message }
func (e *CampaignStateError) Is(target error) bool { return target == ErrCampaignState }

func campaignStateErr(msg string) error { return &CampaignStateError{Message: msg} }

const (
	maxCampaignSubjectBytes = 500
	maxPostalAddressRunes   = 500
	campaignTestMaxEmails   = 5
	campaignTestHourlyLimit = 10
)

type CampaignService struct {
	db       *sql.DB
	cfg      *config.Config
	provider provider.EmailProvider // nil unless EMAIL_PROVIDER=ses and SES was built

	// sandbox is the cached SES account sandbox flag for audience warnings.
	sandboxMu   sync.Mutex
	sandbox     *bool
	sandboxAt   time.Time
	sandboxFunc func(context.Context) (bool, error)
}

// NewCampaignService takes the SES provider built once at startup (see
// NewCampaignProvider); campaigns refuse to start without it.
func NewCampaignService(db *sql.DB, cfg *config.Config, emailProvider provider.EmailProvider) *CampaignService {
	s := &CampaignService{db: db, cfg: cfg, provider: emailProvider}
	if cfg.EmailProvider == "ses" && cfg.AWSAccessKeyID != "" && cfg.AWSSecretAccessKey != "" {
		health := NewHealthService(db, cfg)
		s.sandboxFunc = func(ctx context.Context) (bool, error) {
			limits, err := health.GetSESAccountLimits(ctx)
			if err != nil {
				return false, err
			}
			return limits.SandboxMode, nil
		}
	}
	return s
}

// NewCampaignProvider builds the SES provider used for campaign sends, or nil
// when EMAIL_PROVIDER is not ses or SES cannot be configured.
func NewCampaignProvider(ctx context.Context, cfg *config.Config) provider.EmailProvider {
	if cfg.EmailProvider != "ses" {
		return nil
	}
	p, err := provider.NewSESProvider(ctx, &provider.SESConfig{
		Region:           cfg.AWSRegion,
		AccessKeyID:      cfg.AWSAccessKeyID,
		SecretAccessKey:  cfg.AWSSecretAccessKey,
		ConfigurationSet: cfg.SESConfigurationSet,
	})
	if err != nil {
		fmt.Printf("Warning: campaign sending is unavailable: %v\n", err)
		return nil
	}
	return p
}

func (s *CampaignService) requireSES() error {
	if s.cfg.EmailProvider != "ses" || s.provider == nil {
		return &provider.MailValidationError{Message: "campaigns send only through Amazon SES; set EMAIL_PROVIDER=ses and configure AWS credentials"}
	}
	return nil
}

// canChangeSendState: API-key callers must be the creator; session callers may
// also be an active owner/admin of the org (read from the DB, not the token).
func canChangeSendState(ctx context.Context, q eventoutbox.DBTX, orgID int64, actor CampaignActor, createdBy int64) error {
	if actor.UserID > 0 && createdBy == actor.UserID {
		return nil
	}
	if actor.APIKey || actor.UserID <= 0 {
		return ErrCampaignForbidden
	}
	var admin bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND org_id=$2 AND status='active' AND role IN ('owner','admin'))`, actor.UserID, orgID).Scan(&admin); err != nil {
		return fmt.Errorf("failed to check campaign permission: %w", err)
	}
	if !admin {
		return ErrCampaignForbidden
	}
	return nil
}

const campaignColumns = `c.id, c.uuid, c.org_id, c.name, c.subject, COALESCE(c.html_content,''), COALESCE(c.text_content,''), c.template_id,
	c.from_name, c.from_email, COALESCE(c.reply_to,''), c.list_id, COALESCE(l.name,'Deleted List'), COALESCE(l.type,'static'),
	c.status, c.status_reason, c.scheduled_at, c.started_at, c.completed_at, c.prepared_at, c.throttled_until,
	c.total_recipients, c.sent_count, c.delivered_count, c.open_count, c.click_count,
	c.bounce_count, c.unsubscribe_count, c.complaint_count, c.failed_count, c.skipped_count, c.unknown_count,
	c.track_opens, c.track_clicks, c.created_by_user_id, c.is_ab_test, c.ab_test_settings, c.created_at, c.updated_at`

const campaignFrom = ` FROM campaigns c LEFT JOIN lists l ON l.id=c.list_id AND l.org_id=c.org_id`

func scanCampaign(row rowScanner) (*model.Campaign, error) {
	var c model.Campaign
	var listType string
	var createdBy sql.NullInt64
	var ab []byte
	err := row.Scan(&c.ID, &c.UUID, &c.OrgID, &c.Name, &c.Subject, &c.HTMLContent, &c.TextContent, &c.TemplateID,
		&c.FromName, &c.FromEmail, &c.ReplyTo, &c.ListID, &c.ListName, &listType,
		&c.Status, &c.StatusReason, &c.ScheduledAt, &c.StartedAt, &c.CompletedAt, &c.PreparedAt, &c.ThrottledUntil,
		&c.TotalRecipients, &c.SentCount, &c.DeliveredCount, &c.OpenCount, &c.ClickCount,
		&c.BounceCount, &c.UnsubscribeCount, &c.ComplaintCount, &c.FailedCount, &c.SkippedCount, &c.UnknownCount,
		&c.TrackOpens, &c.TrackClicks, &createdBy, &c.IsAbTest, &ab, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.ListType = "static"
	if listType == "dynamic" {
		c.ListType = "dynamic"
	}
	if createdBy.Valid {
		c.CreatedByUserID = &createdBy.Int64
	}
	if len(ab) > 0 {
		_ = json.Unmarshal(ab, &c.AbTestSettings)
	}
	return &c, nil
}

func (s *CampaignService) getCampaign(ctx context.Context, q eventoutbox.DBTX, orgID int64, campaignUUID string, lock bool) (*model.Campaign, error) {
	if _, err := uuid.Parse(campaignUUID); err != nil {
		return nil, ErrCampaignNotFound
	}
	query := `SELECT ` + campaignColumns + campaignFrom + ` WHERE c.org_id=$1 AND c.uuid=$2`
	if lock {
		query += ` FOR UPDATE OF c`
	}
	c, err := scanCampaign(q.QueryRowContext(ctx, query, orgID, campaignUUID))
	if err == sql.ErrNoRows {
		return nil, ErrCampaignNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get campaign: %w", err)
	}
	return c, nil
}

func createdBy(c *model.Campaign) int64 {
	if c.CreatedByUserID == nil {
		return 0
	}
	return *c.CreatedByUserID
}

// effectiveCreator is the user whose identities the campaign may send from:
// the creator, or the caller for legacy campaigns that have none.
func effectiveCreator(c *model.Campaign, actor CampaignActor) int64 {
	if id := createdBy(c); id > 0 {
		return id
	}
	return actor.UserID
}

// validateCampaignContent enforces the limits checked at create, update and send.
func validateCampaignContent(subject, htmlContent, textContent string) error {
	if strings.TrimSpace(subject) == "" {
		return &provider.MailValidationError{Message: "subject is required"}
	}
	if len(subject) > maxCampaignSubjectBytes || strings.ContainsAny(subject, "\r\n") {
		return &provider.MailValidationError{Message: "subject must be at most 500 bytes on a single line"}
	}
	if htmlContent == "" && textContent == "" {
		return &provider.MailValidationError{Message: "campaign content is required (HTML or text)"}
	}
	if len(htmlContent)+len(textContent) > maxComposeBodyBytes {
		return &provider.MailValidationError{Message: "campaign content exceeds the maximum size"}
	}
	if htmlContent != "" {
		if _, _, err := footerInsertion(htmlContent); err != nil {
			return err
		}
	}
	return nil
}

func validateCampaignHeaders(name, fromName, replyTo string) error {
	if strings.TrimSpace(name) == "" {
		return &provider.MailValidationError{Message: "name is required"}
	}
	if strings.ContainsAny(fromName, "\r\n") || strings.ContainsAny(replyTo, "\r\n") {
		return &provider.MailValidationError{Message: "sender fields must not contain line breaks"}
	}
	if replyTo != "" {
		if a, err := mail.ParseAddress(replyTo); err != nil || !strings.EqualFold(a.Address, strings.TrimSpace(replyTo)) {
			return &provider.MailValidationError{Message: "invalid Reply-To address"}
		}
	}
	return nil
}

// variableWarnings lists {{variables}} that are neither standard fields nor an
// attribute key on any contact of the org; they would render empty.
func (s *CampaignService) variableWarnings(ctx context.Context, orgID int64, parts ...string) []string {
	seen := map[string]bool{}
	var warnings []string
	for _, part := range parts {
		for _, m := range templateVarRe.FindAllStringSubmatch(part, -1) {
			key := m[1]
			switch key {
			case "email", "firstName", "first_name", "lastName", "last_name":
				continue
			}
			if seen[key] || len(seen) >= 20 {
				continue
			}
			seen[key] = true
			var exists bool
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE org_id=$1 AND attributes ? $2)`, orgID, key).Scan(&exists); err == nil && !exists {
				warnings = append(warnings, "{{"+key+"}} matches no contact field or attribute and will render empty")
			}
		}
	}
	return warnings
}

// CreateCampaign stores a draft. The From address must be one of the caller's
// sendable identities; feedback readiness is checked only when sending.
func (s *CampaignService) CreateCampaign(ctx context.Context, orgID int64, actor CampaignActor, req *model.CreateCampaignRequest) (*model.Campaign, error) {
	if err := validateCampaignHeaders(req.Name, req.FromName, req.ReplyTo); err != nil {
		return nil, err
	}
	if err := validateCampaignContent(req.Subject, req.HTMLContent, req.TextContent); err != nil {
		return nil, err
	}
	var listExists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lists WHERE id=$1 AND org_id=$2)`, req.ListID, orgID).Scan(&listExists); err != nil {
		return nil, fmt.Errorf("failed to verify list: %w", err)
	}
	if !listExists {
		return nil, &provider.MailValidationError{Message: "list not found"}
	}
	identityID, _, err := resolveCampaignSender(ctx, s.db, orgID, actor.UserID, req.FromEmail)
	if err != nil {
		return nil, err
	}
	trackOpens, trackClicks := true, true
	if req.TrackOpens != nil {
		trackOpens = *req.TrackOpens
	}
	if req.TrackClicks != nil {
		trackClicks = *req.TrackClicks
	}
	var id string
	err = s.db.QueryRowContext(ctx, `INSERT INTO campaigns (org_id, name, subject, html_content, text_content, template_id,
			from_name, from_email, reply_to, list_id, status, track_opens, track_clicks, created_by_user_id, identity_id, created_at, updated_at)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,NULLIF($9,''),$10,'draft',$11,$12,$13,$14,now(),now())
		RETURNING uuid`,
		orgID, strings.TrimSpace(req.Name), req.Subject, req.HTMLContent, req.TextContent, req.TemplateID,
		req.FromName, strings.TrimSpace(req.FromEmail), req.ReplyTo, req.ListID, trackOpens, trackClicks, actor.UserID, identityID,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to create campaign: %w", err)
	}
	c, err := s.getCampaign(ctx, s.db, orgID, id, false)
	if err != nil {
		return nil, err
	}
	c.Warnings = s.variableWarnings(ctx, orgID, c.Subject, c.HTMLContent, c.TextContent)
	return c, nil
}

// GetCampaign retrieves a campaign by UUID
func (s *CampaignService) GetCampaign(ctx context.Context, orgID int64, campaignUUID string) (*model.Campaign, error) {
	return s.getCampaign(ctx, s.db, orgID, campaignUUID, false)
}

// ListCampaigns retrieves campaigns with pagination
func (s *CampaignService) ListCampaigns(ctx context.Context, orgID int64, page, pageSize int, status string) (*model.CampaignListResponse, error) {
	where := ` WHERE c.org_id=$1`
	args := []any{orgID}
	if status != "" {
		where += ` AND c.status=$2`
		args = append(args, status)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM campaigns c`+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count campaigns: %w", err)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	n := len(args)
	query := `SELECT ` + campaignColumns + campaignFrom + where +
		fmt.Sprintf(` ORDER BY c.created_at DESC, c.id DESC LIMIT $%d OFFSET $%d`, n+1, n+2)
	rows, err := s.db.QueryContext(ctx, query, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, fmt.Errorf("failed to query campaigns: %w", err)
	}
	defer rows.Close()
	campaigns := []model.Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to read campaign: %w", err)
		}
		campaigns = append(campaigns, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read campaigns: %w", err)
	}
	return &model.CampaignListResponse{
		Campaigns:  campaigns,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: (total + pageSize - 1) / pageSize,
	}, nil
}

// UpdateCampaign applies the present fields. Full edits are allowed while the
// campaign is a draft or scheduled, or paused before its audience was
// snapshotted (except the list); once prepared only the name may change.
func (s *CampaignService) UpdateCampaign(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string, req *model.UpdateCampaignRequest) (*model.Campaign, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to update campaign: %w", err)
	}
	defer tx.Rollback()
	c, err := s.getCampaign(ctx, tx, orgID, campaignUUID, true)
	if err != nil {
		return nil, err
	}
	if err := canChangeSendState(ctx, tx, orgID, actor, createdBy(c)); err != nil {
		return nil, err
	}
	next := *c
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&next.Name, req.Name)
	set(&next.Subject, req.Subject)
	set(&next.HTMLContent, req.HTMLContent)
	set(&next.TextContent, req.TextContent)
	set(&next.FromName, req.FromName)
	set(&next.ReplyTo, req.ReplyTo)
	if req.TemplateID != nil {
		next.TemplateID = req.TemplateID
	}
	if req.ListID != nil {
		next.ListID = *req.ListID
	}
	if req.TrackOpens != nil {
		next.TrackOpens = *req.TrackOpens
	}
	if req.TrackClicks != nil {
		next.TrackClicks = *req.TrackClicks
	}
	fromChanged := req.FromEmail != nil && !strings.EqualFold(strings.TrimSpace(*req.FromEmail), c.FromEmail)
	contentChange := fromChanged || next.Subject != c.Subject || next.HTMLContent != c.HTMLContent || next.TextContent != c.TextContent ||
		!equalIntPtr(next.TemplateID, c.TemplateID) || next.FromName != c.FromName || next.ReplyTo != c.ReplyTo ||
		next.ListID != c.ListID || next.TrackOpens != c.TrackOpens || next.TrackClicks != c.TrackClicks
	switch c.Status {
	case "draft", "scheduled":
	case "paused":
		if c.PreparedAt == nil && next.ListID != c.ListID {
			return nil, campaignStateErr("the list cannot change after sending started")
		}
		if c.PreparedAt != nil && contentChange {
			return nil, campaignStateErr("campaign content is locked after sending started")
		}
	default:
		return nil, campaignStateErr("campaign content is locked after sending started")
	}
	if err := validateCampaignHeaders(next.Name, next.FromName, next.ReplyTo); err != nil {
		return nil, err
	}
	if contentChange {
		if err := validateCampaignContent(next.Subject, next.HTMLContent, next.TextContent); err != nil {
			return nil, err
		}
	}
	if next.ListID != c.ListID {
		var ok bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lists WHERE id=$1 AND org_id=$2)`, next.ListID, orgID).Scan(&ok); err != nil {
			return nil, fmt.Errorf("failed to verify list: %w", err)
		}
		if !ok {
			return nil, &provider.MailValidationError{Message: "list not found"}
		}
	}
	// A changed From re-resolves against the creator's identities, even when an
	// admin edits; legacy campaigns without a creator adopt the editor.
	owner := effectiveCreator(c, actor)
	var identityID sql.NullInt64
	if fromChanged {
		id, _, err := resolveCampaignSender(ctx, tx, orgID, owner, *req.FromEmail)
		if err != nil {
			return nil, err
		}
		next.FromEmail = strings.TrimSpace(*req.FromEmail)
		identityID = sql.NullInt64{Int64: id, Valid: true}
	}
	_, err = tx.ExecContext(ctx, `UPDATE campaigns SET name=$3, subject=$4, html_content=NULLIF($5,''), text_content=NULLIF($6,''),
			template_id=$7, from_name=$8, from_email=$9, reply_to=NULLIF($10,''), list_id=$11, track_opens=$12, track_clicks=$13,
			identity_id=COALESCE($14, identity_id),
			created_by_user_id=CASE WHEN $14::int IS NULL THEN created_by_user_id ELSE COALESCE(created_by_user_id, $15) END,
			updated_at=now()
		WHERE org_id=$1 AND id=$2`,
		orgID, c.ID, strings.TrimSpace(next.Name), next.Subject, next.HTMLContent, next.TextContent, next.TemplateID,
		next.FromName, next.FromEmail, next.ReplyTo, next.ListID, next.TrackOpens, next.TrackClicks, identityID, owner)
	if err != nil {
		return nil, fmt.Errorf("failed to update campaign: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to update campaign: %w", err)
	}
	out, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if contentChange {
		out.Warnings = s.variableWarnings(ctx, orgID, out.Subject, out.HTMLContent, out.TextContent)
	}
	return out, nil
}

// DeleteCampaign removes a draft, or a cancelled campaign that never prepared
// an audience (sent history is kept).
func (s *CampaignService) DeleteCampaign(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string) error {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return err
	}
	if err := canChangeSendState(ctx, s.db, orgID, actor, createdBy(c)); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM campaigns WHERE org_id=$1 AND id=$2
		AND (status='draft' OR (status='cancelled' AND prepared_at IS NULL))`, orgID, c.ID)
	if err != nil {
		return fmt.Errorf("failed to delete campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return campaignStateErr("only drafts and cancelled campaigns that never sent can be deleted")
	}
	return nil
}

// checkSendable runs the checks shared by send, schedule and resume. When
// resuming, the stored identity is revalidated instead of re-resolved.
func (s *CampaignService) checkSendable(ctx context.Context, q eventoutbox.DBTX, orgID int64, c *model.Campaign, actor CampaignActor, resume bool) (identityID int64, audience *campaignAudience, err error) {
	if err := s.requireSES(); err != nil {
		return 0, nil, err
	}
	if err := checkCampaignLinkBases(s.cfg.APIUrl, s.cfg.WebUrl); err != nil {
		return 0, nil, err
	}
	if err := canChangeSendState(ctx, q, orgID, actor, createdBy(c)); err != nil {
		return 0, nil, err
	}
	if err := validateCampaignContent(c.Subject, c.HTMLContent, c.TextContent); err != nil {
		return 0, nil, err
	}
	var domainID int64
	if resume {
		var stored sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT identity_id FROM campaigns WHERE id=$1 AND org_id=$2`, c.ID, orgID).Scan(&stored); err != nil {
			return 0, nil, fmt.Errorf("failed to load campaign sender: %w", err)
		}
		identityID = stored.Int64
		if domainID, err = revalidateCampaignSender(ctx, q, orgID, identityID, createdBy(c), c.FromEmail); err != nil {
			return 0, nil, err
		}
	} else if identityID, domainID, err = resolveCampaignSender(ctx, q, orgID, effectiveCreator(c, actor), c.FromEmail); err != nil {
		return 0, nil, err
	}
	if err := requireFeedbackReady(ctx, q, domainID); err != nil {
		return 0, nil, err
	}
	if err := requirePostalAddress(ctx, q, orgID); err != nil {
		return 0, nil, err
	}
	if audience, err = loadCampaignAudience(ctx, q, orgID, int64(c.ListID)); err != nil {
		return 0, nil, err
	}
	return identityID, audience, nil
}

func requirePostalAddress(ctx context.Context, q eventoutbox.DBTX, orgID int64) error {
	var postal string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(postal_address,'') FROM organizations WHERE id=$1`, orgID).Scan(&postal); err != nil {
		return fmt.Errorf("failed to load postal address: %w", err)
	}
	if strings.TrimSpace(postal) == "" {
		return &provider.MailValidationError{Message: "set the organization's postal address in campaign settings before sending campaigns"}
	}
	return nil
}

// ScheduleCampaign schedules (or reschedules) a draft or scheduled campaign.
// The audience estimate is informational here; the runner snapshots it later.
func (s *CampaignService) ScheduleCampaign(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string, req *model.ScheduleCampaignRequest) (*model.Campaign, error) {
	scheduledAt, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ScheduledAt))
	if err != nil {
		return nil, &provider.MailValidationError{Message: "invalid scheduledAt; use RFC 3339 with a time zone offset"}
	}
	now := time.Now()
	if scheduledAt.Before(now.Add(time.Minute)) || scheduledAt.After(now.Add(365*24*time.Hour)) {
		return nil, &provider.MailValidationError{Message: "scheduledAt must be between 1 minute and 365 days from now"}
	}
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if c.Status != "draft" && c.Status != "scheduled" {
		return nil, campaignStateErr("only draft or scheduled campaigns can be scheduled")
	}
	identityID, _, err := s.checkSendable(ctx, s.db, orgID, c, actor, false)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE campaigns SET status='scheduled', scheduled_at=$3, status_reason=NULL, throttled_until=NULL,
			identity_id=$4, created_by_user_id=COALESCE(created_by_user_id,$5), updated_at=now()
		WHERE org_id=$1 AND id=$2 AND status IN ('draft','scheduled')`, orgID, c.ID, scheduledAt.UTC(), identityID, actor.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to schedule campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrCampaignState
	}
	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// SendCampaignNow moves a draft or scheduled campaign to sending; the runner
// snapshots the audience and sends. campaign.started commits atomically.
func (s *CampaignService) SendCampaignNow(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string) (*model.Campaign, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if c.Status != "draft" && c.Status != "scheduled" {
		return nil, campaignStateErr("only draft or scheduled campaigns can be sent")
	}
	identityID, audience, err := s.checkSendable(ctx, s.db, orgID, c, actor, false)
	if err != nil {
		return nil, err
	}
	est, err := audience.estimate(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if est.Eligible == 0 {
		return nil, &provider.MailValidationError{Message: "the list has no eligible recipients"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start campaign: %w", err)
	}
	defer tx.Rollback()
	var id int64
	var startedAt time.Time
	err = tx.QueryRowContext(ctx, `UPDATE campaigns SET status='sending', started_at=now(), prepared_at=NULL, status_reason=NULL, throttled_until=NULL,
			identity_id=$3, created_by_user_id=COALESCE(created_by_user_id,$4), updated_at=now()
		WHERE org_id=$1 AND uuid=$2 AND status IN ('draft','scheduled') RETURNING id, started_at`,
		orgID, campaignUUID, identityID, actor.UserID).Scan(&id, &startedAt)
	if err == sql.ErrNoRows {
		return nil, ErrCampaignState
	}
	if err != nil {
		return nil, fmt.Errorf("failed to start campaign: %w", err)
	}
	if err := emitCampaignEvent(ctx, tx, orgID, id, "campaign.started", fmt.Sprintf(":%d", startedAt.Unix())); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to start campaign: %w", err)
	}
	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// PauseCampaign pauses a sending campaign; in-flight sends finish and the
// runner releases its claimed rows.
func (s *CampaignService) PauseCampaign(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string) (*model.Campaign, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if err := canChangeSendState(ctx, s.db, orgID, actor, createdBy(c)); err != nil {
		return nil, err
	}
	if err := pauseCampaignTx(ctx, s.db, orgID, int64(c.ID), "user_paused"); err != nil {
		return nil, err
	}
	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// pauseCampaignTx moves a sending campaign to paused with a reason and emits
// campaign.paused in the same transaction. It returns ErrCampaignState when
// the campaign was no longer sending.
func pauseCampaignTx(ctx context.Context, db *sql.DB, orgID, campaignID int64, reason string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to pause campaign: %w", err)
	}
	defer tx.Rollback()
	if err := pauseCampaign(ctx, tx, orgID, campaignID, reason); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to pause campaign: %w", err)
	}
	return nil
}

// pauseCampaign is the guarded sending→paused transition for an open transaction.
func pauseCampaign(ctx context.Context, q eventoutbox.DBTX, orgID, campaignID int64, reason string) error {
	var updatedAt time.Time
	err := q.QueryRowContext(ctx, `UPDATE campaigns SET status='paused', status_reason=$3, updated_at=clock_timestamp()
		WHERE org_id=$1 AND id=$2 AND status='sending' RETURNING updated_at`, orgID, campaignID, reason).Scan(&updatedAt)
	if err == sql.ErrNoRows {
		return campaignStateErr("only sending campaigns can be paused")
	}
	if err != nil {
		return fmt.Errorf("failed to pause campaign: %w", err)
	}
	return emitCampaignEvent(ctx, q, orgID, campaignID, "campaign.paused", fmt.Sprintf(":%d", updatedAt.UnixNano()))
}

// ResumeCampaign revalidates the stored sender, feedback and postal address and
// continues a paused campaign; already materialised rows are never duplicated.
// It moves the breaker baseline to the current counters, so a campaign paused
// for its bounce or complaint rate is judged afresh on what it sends next.
func (s *CampaignService) ResumeCampaign(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string) (*model.Campaign, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if c.Status != "paused" {
		return nil, campaignStateErr("only paused campaigns can be resumed")
	}
	if _, _, err := s.checkSendable(ctx, s.db, orgID, c, actor, true); err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE campaigns SET status='sending', status_reason=NULL, throttled_until=NULL,
			breaker_baseline_sent=sent_count, breaker_baseline_bounces=bounce_count, breaker_baseline_complaints=complaint_count, updated_at=now()
		WHERE org_id=$1 AND id=$2 AND status='paused'`, orgID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to resume campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrCampaignState
	}
	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// CancelCampaign stops a campaign for good. Unattempted rows become cancelled
// and their reserved monthly quota is refunded; rows already handed to SES
// finish normally. Like the runner, it locks the campaign row before
// recipient rows.
func (s *CampaignService) CancelCampaign(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string) (*model.Campaign, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if err := canChangeSendState(ctx, s.db, orgID, actor, createdBy(c)); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel campaign: %w", err)
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `UPDATE campaigns SET status='cancelled', throttled_until=NULL, updated_at=now()
		WHERE org_id=$1 AND id=$2 AND status IN ('draft','scheduled','sending','paused') RETURNING id`, orgID, c.ID).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, campaignStateErr("sent or cancelled campaigns cannot be cancelled")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to cancel campaign: %w", err)
	}
	var refund int64
	err = tx.QueryRowContext(ctx, `WITH u AS (
			UPDATE campaign_recipients SET status='cancelled', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE org_id=$1 AND campaign_id=$2 AND status IN ('pending','claimed') RETURNING quota_reserved)
		SELECT COUNT(*) FILTER (WHERE quota_reserved) FROM u`, orgID, id).Scan(&refund)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel recipients: %w", err)
	}
	if err := refundMonthlySendsTx(ctx, tx, orgID, refund); err != nil {
		return nil, err
	}
	if err := emitCampaignEvent(ctx, tx, orgID, id, "campaign.cancelled", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to cancel campaign: %w", err)
	}
	return s.GetCampaign(ctx, orgID, campaignUUID)
}

// emitCampaignEvent writes a campaign.* outbox event for the campaign's
// creator. The identity is attached only while the creator still owns it, so
// the ownership check in the outbox never fails the surrounding transaction.
// The dedupe key is "<kind>:<uuid><suffix>".
func emitCampaignEvent(ctx context.Context, q eventoutbox.DBTX, orgID, campaignID int64, kind, dedupeSuffix string) error {
	var (
		campaignUUID, name                    string
		total, sent, failed, unknown, skipped int64
		userID, identityID                    int64
		reason                                sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT c.uuid::text, c.name, c.total_recipients, c.sent_count, c.failed_count, c.unknown_count, c.skipped_count,
			c.status_reason, COALESCE(c.created_by_user_id,0),
			CASE WHEN EXISTS(SELECT 1 FROM identities i JOIN users u ON u.id=i.user_id
				WHERE i.id=c.identity_id AND i.user_id=c.created_by_user_id AND u.org_id=c.org_id) THEN c.identity_id ELSE 0 END
		FROM campaigns c WHERE c.id=$1 AND c.org_id=$2`, campaignID, orgID).
		Scan(&campaignUUID, &name, &total, &sent, &failed, &unknown, &skipped, &reason, &userID, &identityID)
	if err != nil {
		return fmt.Errorf("failed to load campaign event: %w", err)
	}
	data := map[string]any{
		"campaignUuid": campaignUUID, "name": name, "totalRecipients": total,
		"sent": sent, "failed": failed, "unknown": unknown, "skipped": skipped, "statusReason": nil,
	}
	if reason.Valid {
		data["statusReason"] = reason.String
	}
	if err := eventoutbox.Emit(ctx, q, eventoutbox.Event{
		Type: kind, OrgID: orgID, UserID: userID, IdentityID: identityID,
		DedupeKey: kind + ":" + campaignUUID + dedupeSuffix, Data: data,
	}); err != nil {
		return fmt.Errorf("failed to record %s event: %w", kind, err)
	}
	return nil
}

func equalIntPtr(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func percentOf(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / float64(d) * 100
}

// GetCampaignStats returns rates over sentCount (opens and clicks are unique),
// hourly opens since the start (at most a week) and the top 20 links.
func (s *CampaignService) GetCampaignStats(ctx context.Context, orgID int64, campaignUUID string) (*model.CampaignStatsResponse, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	stats := &model.CampaignStatsResponse{
		Campaign:        c,
		OpenRate:        percentOf(c.OpenCount, c.SentCount),
		ClickRate:       percentOf(c.ClickCount, c.SentCount),
		ClickToOpenRate: percentOf(c.ClickCount, c.OpenCount),
		BounceRate:      percentOf(c.BounceCount, c.SentCount),
		ComplaintRate:   percentOf(c.ComplaintCount, c.SentCount),
		UnsubscribeRate: percentOf(c.UnsubscribeCount, c.SentCount),
		DeliveredRate:   percentOf(c.DeliveredCount, c.SentCount),
		ClicksByLink:    []model.LinkClicks{},
		OpensByHour:     []model.HourCount{},
	}
	if c.StartedAt != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT date_trunc('hour', occurred_at), COUNT(*) FROM campaign_events
			WHERE campaign_id=$1 AND event_type='open' AND occurred_at>=$2 AND occurred_at<$2::timestamptz+interval '7 days'
			GROUP BY 1 ORDER BY 1 LIMIT 168`, c.ID, *c.StartedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to load opens: %w", err)
		}
		for rows.Next() {
			var h model.HourCount
			if err := rows.Scan(&h.Hour, &h.Opens); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to read opens: %w", err)
			}
			h.Hour = h.Hour.UTC()
			stats.OpensByHour = append(stats.OpensByHour, h)
		}
		rows.Close()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT url, COUNT(*), COUNT(DISTINCT recipient_id) FROM campaign_events
		WHERE campaign_id=$1 AND event_type='click' AND url IS NOT NULL
		GROUP BY url ORDER BY 2 DESC, 1 LIMIT 20`, c.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load clicks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l model.LinkClicks
		if err := rows.Scan(&l.URL, &l.Clicks, &l.UniqueClicks); err != nil {
			return nil, fmt.Errorf("failed to read clicks: %w", err)
		}
		stats.ClicksByLink = append(stats.ClicksByLink, l)
	}
	return stats, rows.Err()
}

// GetProgress summarises recipient rows by status in one query.
func (s *CampaignService) GetProgress(ctx context.Context, orgID int64, campaignUUID string) (*model.CampaignProgressResponse, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	p := &model.CampaignProgressResponse{
		Status: c.Status, StatusReason: c.StatusReason, ThrottledUntil: c.ThrottledUntil,
		Preparing: c.Status == "sending" && c.PreparedAt == nil,
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),
			COUNT(*) FILTER (WHERE status='pending'), COUNT(*) FILTER (WHERE status IN ('claimed','sending')),
			COUNT(*) FILTER (WHERE status='sent'), COUNT(*) FILTER (WHERE status='failed'),
			COUNT(*) FILTER (WHERE status='unknown'), COUNT(*) FILTER (WHERE status='skipped'),
			COUNT(*) FILTER (WHERE status='cancelled')
		FROM campaign_recipients WHERE org_id=$1 AND campaign_id=$2`, orgID, c.ID).
		Scan(&p.Total, &p.Pending, &p.InFlight, &p.Sent, &p.Failed, &p.Unknown, &p.Skipped, &p.Cancelled)
	if err != nil {
		return nil, fmt.Errorf("failed to load campaign progress: %w", err)
	}
	if p.Total > 0 {
		p.Percent = float64(p.Sent+p.Failed+p.Unknown+p.Skipped+p.Cancelled) / float64(p.Total) * 100
	} else if c.Status == "sent" {
		p.Percent = 100
	}
	return p, nil
}

// EstimateAudience estimates who would receive the campaign now and lists
// readiness warnings. The audience is snapshotted only when sending starts.
func (s *CampaignService) EstimateAudience(ctx context.Context, orgID int64, campaignUUID string) (*model.CampaignAudienceResponse, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	est, err := estimateAudience(ctx, s.db, orgID, int64(c.ListID))
	if err != nil {
		return nil, err
	}
	out := &model.CampaignAudienceResponse{
		ListType: est.ListType, Eligible: est.Eligible,
		ExcludedInactive: est.ExcludedInactive, ExcludedSuppressed: est.ExcludedSuppressed,
		Warnings: []string{},
	}
	var dmarc, feedback bool
	domain := ""
	if i := strings.LastIndexByte(c.FromEmail, '@'); i >= 0 {
		domain = c.FromEmail[i+1:]
	}
	err = s.db.QueryRowContext(ctx, `SELECT d.dmarc_verified, COALESCE(d.sending_feedback_ready,false) FROM domains d
		WHERE d.org_id=$1 AND (d.id=(SELECT i.domain_id FROM identities i JOIN campaigns c ON c.identity_id=i.id WHERE c.id=$2) OR lower(d.name)=lower($3))
		ORDER BY d.id LIMIT 1`, orgID, c.ID, domain).Scan(&dmarc, &feedback)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to load sending domain: %w", err)
	}
	if !dmarc {
		out.Warnings = append(out.Warnings, "dmarc_missing")
	}
	if s.sandboxMode(ctx) {
		out.Warnings = append(out.Warnings, "sandbox_mode")
	}
	if requirePostalAddress(ctx, s.db, orgID) != nil {
		out.Warnings = append(out.Warnings, "no_postal_address")
	}
	if !feedback {
		out.Warnings = append(out.Warnings, "feedback_not_ready")
	}
	return out, nil
}

// sandboxMode reports the SES sandbox flag, cached for 5 minutes; unknown is false.
func (s *CampaignService) sandboxMode(ctx context.Context) bool {
	if s.sandboxFunc == nil {
		return false
	}
	s.sandboxMu.Lock()
	defer s.sandboxMu.Unlock()
	if s.sandbox == nil || time.Since(s.sandboxAt) > 5*time.Minute {
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		v, err := s.sandboxFunc(lookupCtx)
		cancel()
		s.sandboxAt = time.Now()
		if err != nil {
			v = false // unknown; retried after the cache expires
		}
		s.sandbox = &v
	}
	return *s.sandbox
}

// ListRecipients pages through the campaign's materialised recipients.
func (s *CampaignService) ListRecipients(ctx context.Context, orgID int64, campaignUUID, status string, page, pageSize int) (*model.CampaignRecipientListResponse, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	out := &model.CampaignRecipientListResponse{Recipients: []model.CampaignRecipient{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM campaign_recipients WHERE org_id=$1 AND campaign_id=$2 AND ($3='' OR status=$3)`,
		orgID, c.ID, status).Scan(&out.Total); err != nil {
		return nil, fmt.Errorf("failed to count recipients: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT email, status, skip_reason, delivery_status, sent_at, open_count, click_count, unsubscribed_at, error
		FROM campaign_recipients WHERE org_id=$1 AND campaign_id=$2 AND ($3='' OR status=$3)
		ORDER BY id LIMIT $4 OFFSET $5`, orgID, c.ID, status, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list recipients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r model.CampaignRecipient
		if err := rows.Scan(&r.Email, &r.Status, &r.SkipReason, &r.DeliveryStatus, &r.SentAt, &r.OpenCount, &r.ClickCount, &r.UnsubscribedAt, &r.Error); err != nil {
			return nil, fmt.Errorf("failed to read recipient: %w", err)
		}
		out.Recipients = append(out.Recipients, r)
	}
	return out, rows.Err()
}

// loadCampaignSnapshot reads the content, sender and org footer data used to
// render one campaign's messages.
func loadCampaignSnapshot(ctx context.Context, q eventoutbox.DBTX, orgID, campaignID int64) (campaignSnapshot, error) {
	snap := campaignSnapshot{ID: campaignID, OrgID: orgID}
	err := q.QueryRowContext(ctx, `SELECT c.subject, COALESCE(c.html_content,''), COALESCE(c.text_content,''), c.from_name, c.from_email,
			COALESCE(c.reply_to,''), c.track_opens, c.track_clicks, o.name, COALESCE(o.postal_address,'')
		FROM campaigns c JOIN organizations o ON o.id=c.org_id WHERE c.id=$1 AND c.org_id=$2`, campaignID, orgID).
		Scan(&snap.Subject, &snap.HTMLContent, &snap.TextContent, &snap.FromName, &snap.FromEmail,
			&snap.ReplyTo, &snap.TrackOpens, &snap.TrackClicks, &snap.OrgName, &snap.PostalAddress)
	if err == sql.ErrNoRows {
		return snap, ErrCampaignNotFound
	}
	if err != nil {
		return snap, fmt.Errorf("failed to load campaign content: %w", err)
	}
	return snap, nil
}

// sampleContact loads an org contact for personalising previews and tests.
func sampleContact(ctx context.Context, q eventoutbox.DBTX, query string, args ...any) (eligibleRecipient, bool, error) {
	var r eligibleRecipient
	var attrs []byte
	err := q.QueryRowContext(ctx, query, args...).Scan(&r.ContactID, &r.Email, &r.FirstName, &r.LastName, &attrs)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("failed to load contact: %w", err)
	}
	if json.Unmarshal(attrs, &r.Attributes) != nil || r.Attributes == nil {
		r.Attributes = map[string]any{}
	}
	return r, true, nil
}

const sampleContactColumns = `SELECT id, email, COALESCE(first_name,''), COALESCE(last_name,''), COALESCE(attributes,'{}'::jsonb) FROM contacts`

// PreviewCampaign renders the campaign without tracking and with a "#"
// unsubscribe link, personalised for a contact of the org when one is given.
func (s *CampaignService) PreviewCampaign(ctx context.Context, orgID int64, campaignUUID, contactUUID string) (*model.CampaignPreviewResponse, error) {
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	snap, err := loadCampaignSnapshot(ctx, s.db, orgID, int64(c.ID))
	if err != nil {
		return nil, err
	}
	rcpt := eligibleRecipient{Email: "subscriber@example.com", Attributes: map[string]any{}}
	if contactUUID = strings.TrimSpace(contactUUID); contactUUID != "" {
		found := false
		if _, perr := uuid.Parse(contactUUID); perr == nil {
			if rcpt, found, err = sampleContact(ctx, s.db, sampleContactColumns+` WHERE org_id=$1 AND uuid=$2`, orgID, contactUUID); err != nil {
				return nil, err
			}
		}
		if !found {
			return nil, &provider.MailValidationError{Message: "contact not found"}
		}
	}
	msg, unknown, err := renderCampaignMessage(snap, rcpt, renderOptions{APIURL: s.cfg.APIUrl, WebURL: s.cfg.WebUrl, Mode: renderPreview})
	if err != nil {
		return nil, err
	}
	if unknown == nil {
		unknown = []string{}
	}
	return &model.CampaignPreviewResponse{Subject: msg.Subject, HTML: msg.HTMLBody, Text: msg.TextBody, UnknownVariables: unknown}, nil
}

// GetCampaignSettings returns the org's campaign footer settings.
func (s *CampaignService) GetCampaignSettings(ctx context.Context, orgID int64) (*model.CampaignSettings, error) {
	var out model.CampaignSettings
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(postal_address,'') FROM organizations WHERE id=$1`, orgID).Scan(&out.PostalAddress); err != nil {
		return nil, fmt.Errorf("failed to load campaign settings: %w", err)
	}
	return &out, nil
}

// UpdateCampaignSettings stores the postal address printed in every campaign
// footer. Only owner/admin sessions may change it; API keys never can.
func (s *CampaignService) UpdateCampaignSettings(ctx context.Context, orgID int64, actor CampaignActor, req *model.CampaignSettings) (*model.CampaignSettings, error) {
	if actor.APIKey {
		return nil, ErrCampaignForbidden
	}
	if err := canChangeSendState(ctx, s.db, orgID, actor, 0); err != nil {
		return nil, err
	}
	postal := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(req.PostalAddress, "\r\n", "\n"), "\r", "\n"))
	if utf8.RuneCountInString(postal) > maxPostalAddressRunes || !utf8.ValidString(postal) {
		return nil, &provider.MailValidationError{Message: "postal address must be at most 500 characters"}
	}
	for _, r := range postal {
		if r != '\n' && (r < 0x20 || r == 0x7f) {
			return nil, &provider.MailValidationError{Message: "postal address contains invalid characters"}
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE organizations SET postal_address=NULLIF($2,''), updated_at=now() WHERE id=$1`, orgID, postal); err != nil {
		return nil, fmt.Errorf("failed to save campaign settings: %w", err)
	}
	return &model.CampaignSettings{PostalAddress: postal}, nil
}
