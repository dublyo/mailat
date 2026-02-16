package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// UserSettings represents user preferences
type UserSettings struct {
	ID                    int64     `json:"id"`
	UserID                int64     `json:"userId"`
	DisplayName           string    `json:"displayName"`
	ShowSnippets          bool      `json:"showSnippets"`
	ConversationView      bool      `json:"conversationView"`
	AutoAdvance           bool      `json:"autoAdvance"`
	NewEmailNotifications bool      `json:"newEmailNotifications"`
	CampaignReports       bool      `json:"campaignReports"`
	WeeklyDigest          bool      `json:"weeklyDigest"`
	BlacklistAlerts       bool      `json:"blacklistAlerts"`
	BounceRateWarnings    bool      `json:"bounceRateWarnings"`
	QuotaWarnings         bool      `json:"quotaWarnings"`
	BrowserNotifications  bool      `json:"browserNotifications"`
	Theme                 string    `json:"theme"`
	Density               string    `json:"density"`
	InboxLayout           string    `json:"inboxLayout"`
	TwoFactorEnabled      bool      `json:"twoFactorEnabled"`
	TwoFactorMethod       *string   `json:"twoFactorMethod"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// UpdateSettingsRequest for updating settings
type UpdateSettingsRequest struct {
	DisplayName           *string `json:"displayName"`
	ShowSnippets          *bool   `json:"showSnippets"`
	ConversationView      *bool   `json:"conversationView"`
	AutoAdvance           *bool   `json:"autoAdvance"`
	NewEmailNotifications *bool   `json:"newEmailNotifications"`
	CampaignReports       *bool   `json:"campaignReports"`
	WeeklyDigest          *bool   `json:"weeklyDigest"`
	BlacklistAlerts       *bool   `json:"blacklistAlerts"`
	BounceRateWarnings    *bool   `json:"bounceRateWarnings"`
	QuotaWarnings         *bool   `json:"quotaWarnings"`
	BrowserNotifications  *bool   `json:"browserNotifications"`
	Theme                 *string `json:"theme"`
	Density               *string `json:"density"`
	InboxLayout           *string `json:"inboxLayout"`
}

// ChangePasswordRequest for password changes
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" v:"required"`
	NewPassword     string `json:"newPassword" v:"required|min-length:8"`
}

type SettingsService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewSettingsService(db *sql.DB, cfg *config.Config) *SettingsService {
	return &SettingsService{db: db, cfg: cfg}
}

// GetSettings retrieves user settings
func (s *SettingsService) GetSettings(ctx context.Context, userID int64) (*UserSettings, error) {
	settings := &UserSettings{}

	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, COALESCE(display_name, ''), show_snippets, conversation_view, auto_advance,
			   new_email_notifications, campaign_reports, weekly_digest, blacklist_alerts,
			   bounce_rate_warnings, quota_warnings, browser_notifications, theme, density,
			   inbox_layout, two_factor_enabled, two_factor_method, created_at, updated_at
		FROM user_settings
		WHERE user_id = $1
	`, userID).Scan(
		&settings.ID, &settings.UserID, &settings.DisplayName, &settings.ShowSnippets,
		&settings.ConversationView, &settings.AutoAdvance, &settings.NewEmailNotifications,
		&settings.CampaignReports, &settings.WeeklyDigest, &settings.BlacklistAlerts,
		&settings.BounceRateWarnings, &settings.QuotaWarnings, &settings.BrowserNotifications,
		&settings.Theme, &settings.Density, &settings.InboxLayout, &settings.TwoFactorEnabled,
		&settings.TwoFactorMethod, &settings.CreatedAt, &settings.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		// Create default settings
		return s.createDefaultSettings(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	return settings, nil
}

// createDefaultSettings creates default settings for a new user
func (s *SettingsService) createDefaultSettings(ctx context.Context, userID int64) (*UserSettings, error) {
	// Get orgID from user
	var orgID int64
	err := s.db.QueryRowContext(ctx, `SELECT org_id FROM users WHERE id = $1`, userID).Scan(&orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user org: %w", err)
	}

	settings := &UserSettings{
		UserID:                userID,
		DisplayName:           "",
		ShowSnippets:          true,
		ConversationView:      true,
		AutoAdvance:           false,
		NewEmailNotifications: true,
		CampaignReports:       true,
		WeeklyDigest:          false,
		BlacklistAlerts:       true,
		BounceRateWarnings:    true,
		QuotaWarnings:         true,
		BrowserNotifications:  false,
		Theme:                 "light",
		Density:               "comfortable",
		InboxLayout:           "default",
		TwoFactorEnabled:      false,
		TwoFactorMethod:       nil,
	}

	err = s.db.QueryRowContext(ctx, `
		INSERT INTO user_settings (user_id, org_id, display_name, show_snippets, conversation_view, auto_advance,
			new_email_notifications, campaign_reports, weekly_digest, blacklist_alerts,
			bounce_rate_warnings, quota_warnings, browser_notifications, theme, density,
			inbox_layout, two_factor_enabled, two_factor_method, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, NOW())
		RETURNING id, created_at, updated_at
	`, userID, orgID, settings.DisplayName, settings.ShowSnippets, settings.ConversationView,
		settings.AutoAdvance, settings.NewEmailNotifications, settings.CampaignReports,
		settings.WeeklyDigest, settings.BlacklistAlerts, settings.BounceRateWarnings,
		settings.QuotaWarnings, settings.BrowserNotifications, settings.Theme,
		settings.Density, settings.InboxLayout, settings.TwoFactorEnabled, settings.TwoFactorMethod,
	).Scan(&settings.ID, &settings.CreatedAt, &settings.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create settings: %w", err)
	}

	return settings, nil
}

// UpdateSettings updates user settings
func (s *SettingsService) UpdateSettings(ctx context.Context, userID int64, req *UpdateSettingsRequest) (*UserSettings, error) {
	// First ensure settings exist
	_, err := s.GetSettings(ctx, userID)
	if err != nil {
		return nil, err
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE user_settings SET
			display_name = COALESCE($2, display_name),
			show_snippets = COALESCE($3, show_snippets),
			conversation_view = COALESCE($4, conversation_view),
			auto_advance = COALESCE($5, auto_advance),
			new_email_notifications = COALESCE($6, new_email_notifications),
			campaign_reports = COALESCE($7, campaign_reports),
			weekly_digest = COALESCE($8, weekly_digest),
			blacklist_alerts = COALESCE($9, blacklist_alerts),
			bounce_rate_warnings = COALESCE($10, bounce_rate_warnings),
			quota_warnings = COALESCE($11, quota_warnings),
			browser_notifications = COALESCE($12, browser_notifications),
			theme = COALESCE($13, theme),
			density = COALESCE($14, density),
			inbox_layout = COALESCE($15, inbox_layout),
			updated_at = NOW()
		WHERE user_id = $1
	`, userID, req.DisplayName, req.ShowSnippets, req.ConversationView, req.AutoAdvance,
		req.NewEmailNotifications, req.CampaignReports, req.WeeklyDigest, req.BlacklistAlerts,
		req.BounceRateWarnings, req.QuotaWarnings, req.BrowserNotifications, req.Theme,
		req.Density, req.InboxLayout)

	if err != nil {
		return nil, fmt.Errorf("failed to update settings: %w", err)
	}

	return s.GetSettings(ctx, userID)
}
