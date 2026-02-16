package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// TenantBranding represents organization branding settings
type TenantBranding struct {
	ID                   int       `json:"id"`
	OrgID                int       `json:"orgId"`
	LogoURL              string    `json:"logoUrl,omitempty"`
	LogoLightURL         string    `json:"logoLightUrl,omitempty"`
	FaviconURL           string    `json:"faviconUrl,omitempty"`
	PrimaryColor         string    `json:"primaryColor,omitempty"`
	AccentColor          string    `json:"accentColor,omitempty"`
	CustomDomain         string    `json:"customDomain,omitempty"`
	CustomDomainVerified bool      `json:"customDomainVerified"`
	EmailFooterHTML      string    `json:"emailFooterHtml,omitempty"`
	EmailHeaderHTML      string    `json:"emailHeaderHtml,omitempty"`
	HidePoweredBy        bool      `json:"hidePoweredBy"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// UpdateBrandingInput is the input for updating branding
type UpdateBrandingInput struct {
	LogoURL         *string `json:"logoUrl,omitempty"`
	LogoLightURL    *string `json:"logoLightUrl,omitempty"`
	FaviconURL      *string `json:"faviconUrl,omitempty"`
	PrimaryColor    *string `json:"primaryColor,omitempty"`
	AccentColor     *string `json:"accentColor,omitempty"`
	CustomDomain    *string `json:"customDomain,omitempty"`
	EmailFooterHTML *string `json:"emailFooterHtml,omitempty"`
	EmailHeaderHTML *string `json:"emailHeaderHtml,omitempty"`
	HidePoweredBy   *bool   `json:"hidePoweredBy,omitempty"`
}

// BrandingService handles tenant branding operations
type BrandingService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewBrandingService creates a new branding service
func NewBrandingService(db *sql.DB, cfg *config.Config) *BrandingService {
	return &BrandingService{db: db, cfg: cfg}
}

// Get gets branding settings for an organization
func (s *BrandingService) Get(ctx context.Context, orgID int64) (*TenantBranding, error) {
	var branding TenantBranding
	err := s.db.QueryRowContext(ctx, `
		SELECT id, org_id, COALESCE(logo_url, ''), COALESCE(logo_light_url, ''), COALESCE(favicon_url, ''),
		       COALESCE(primary_color, ''), COALESCE(accent_color, ''), COALESCE(custom_domain, ''),
		       custom_domain_verified, COALESCE(email_footer_html, ''), COALESCE(email_header_html, ''),
		       hide_powered_by, created_at, updated_at
		FROM tenant_brandings
		WHERE org_id = $1
	`, orgID).Scan(&branding.ID, &branding.OrgID, &branding.LogoURL, &branding.LogoLightURL, &branding.FaviconURL,
		&branding.PrimaryColor, &branding.AccentColor, &branding.CustomDomain,
		&branding.CustomDomainVerified, &branding.EmailFooterHTML, &branding.EmailHeaderHTML,
		&branding.HidePoweredBy, &branding.CreatedAt, &branding.UpdatedAt)

	if err == sql.ErrNoRows {
		// Return default branding
		return &TenantBranding{
			OrgID:        int(orgID),
			PrimaryColor: "#4F46E5", // Default indigo
			AccentColor:  "#10B981", // Default emerald
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get branding: %w", err)
	}

	return &branding, nil
}

// Update updates branding settings for an organization
func (s *BrandingService) Update(ctx context.Context, orgID int64, input *UpdateBrandingInput) (*TenantBranding, error) {
	// Validate colors if provided
	if input.PrimaryColor != nil && !isValidHexColor(*input.PrimaryColor) {
		return nil, fmt.Errorf("invalid primary color format (use #RRGGBB)")
	}
	if input.AccentColor != nil && !isValidHexColor(*input.AccentColor) {
		return nil, fmt.Errorf("invalid accent color format (use #RRGGBB)")
	}

	// Validate custom domain if provided
	if input.CustomDomain != nil && *input.CustomDomain != "" && !isValidDomain(*input.CustomDomain) {
		return nil, fmt.Errorf("invalid custom domain format")
	}

	// Check if branding exists
	var exists bool
	s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_brandings WHERE org_id = $1)`, orgID).Scan(&exists)

	if !exists {
		// Create new branding record
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO tenant_brandings (org_id) VALUES ($1)
		`, orgID)
		if err != nil {
			return nil, fmt.Errorf("failed to create branding: %w", err)
		}
	}

	// Build update query
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if input.LogoURL != nil {
		updates = append(updates, fmt.Sprintf("logo_url = $%d", argNum))
		args = append(args, *input.LogoURL)
		argNum++
	}

	if input.LogoLightURL != nil {
		updates = append(updates, fmt.Sprintf("logo_light_url = $%d", argNum))
		args = append(args, *input.LogoLightURL)
		argNum++
	}

	if input.FaviconURL != nil {
		updates = append(updates, fmt.Sprintf("favicon_url = $%d", argNum))
		args = append(args, *input.FaviconURL)
		argNum++
	}

	if input.PrimaryColor != nil {
		updates = append(updates, fmt.Sprintf("primary_color = $%d", argNum))
		args = append(args, *input.PrimaryColor)
		argNum++
	}

	if input.AccentColor != nil {
		updates = append(updates, fmt.Sprintf("accent_color = $%d", argNum))
		args = append(args, *input.AccentColor)
		argNum++
	}

	if input.CustomDomain != nil {
		updates = append(updates, fmt.Sprintf("custom_domain = $%d", argNum))
		args = append(args, *input.CustomDomain)
		argNum++
		// Reset verification when domain changes
		updates = append(updates, "custom_domain_verified = false")
	}

	if input.EmailFooterHTML != nil {
		updates = append(updates, fmt.Sprintf("email_footer_html = $%d", argNum))
		args = append(args, *input.EmailFooterHTML)
		argNum++
	}

	if input.EmailHeaderHTML != nil {
		updates = append(updates, fmt.Sprintf("email_header_html = $%d", argNum))
		args = append(args, *input.EmailHeaderHTML)
		argNum++
	}

	if input.HidePoweredBy != nil {
		updates = append(updates, fmt.Sprintf("hide_powered_by = $%d", argNum))
		args = append(args, *input.HidePoweredBy)
		argNum++
	}

	if len(updates) == 0 {
		return s.Get(ctx, orgID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE tenant_brandings
		SET %s
		WHERE org_id = $%d
	`, strings.Join(updates, ", "), argNum)

	args = append(args, orgID)

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update branding: %w", err)
	}

	return s.Get(ctx, orgID)
}

// VerifyCustomDomain verifies a custom domain
func (s *BrandingService) VerifyCustomDomain(ctx context.Context, orgID int64) (bool, string, error) {
	branding, err := s.Get(ctx, orgID)
	if err != nil {
		return false, "", err
	}

	if branding.CustomDomain == "" {
		return false, "", fmt.Errorf("no custom domain configured")
	}

	// Generate verification token
	verificationToken := fmt.Sprintf("mailat-verify=%s", generateRandomToken(16))

	// In production, you would:
	// 1. Check DNS TXT record for verification token
	// 2. Check CNAME/A record points to our servers
	// For now, we'll just mark as verified

	// Store verification token for checking
	// The user needs to add this as a TXT record

	return false, verificationToken, nil
}

// ConfirmCustomDomainVerification confirms the custom domain is properly configured
func (s *BrandingService) ConfirmCustomDomainVerification(ctx context.Context, orgID int64) error {
	// In production, this would:
	// 1. Check DNS TXT record for verification token
	// 2. Check CNAME points to our CDN/load balancer
	// 3. Provision SSL certificate

	result, err := s.db.ExecContext(ctx, `
		UPDATE tenant_brandings
		SET custom_domain_verified = true, updated_at = NOW()
		WHERE org_id = $1 AND custom_domain IS NOT NULL AND custom_domain != ''
	`, orgID)
	if err != nil {
		return fmt.Errorf("failed to verify domain: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("no custom domain configured")
	}

	return nil
}

// GetByCustomDomain gets organization by custom domain
func (s *BrandingService) GetByCustomDomain(ctx context.Context, domain string) (*TenantBranding, error) {
	var branding TenantBranding
	err := s.db.QueryRowContext(ctx, `
		SELECT id, org_id, COALESCE(logo_url, ''), COALESCE(logo_light_url, ''), COALESCE(favicon_url, ''),
		       COALESCE(primary_color, ''), COALESCE(accent_color, ''), COALESCE(custom_domain, ''),
		       custom_domain_verified, COALESCE(email_footer_html, ''), COALESCE(email_header_html, ''),
		       hide_powered_by, created_at, updated_at
		FROM tenant_brandings
		WHERE custom_domain = $1 AND custom_domain_verified = true
	`, domain).Scan(&branding.ID, &branding.OrgID, &branding.LogoURL, &branding.LogoLightURL, &branding.FaviconURL,
		&branding.PrimaryColor, &branding.AccentColor, &branding.CustomDomain,
		&branding.CustomDomainVerified, &branding.EmailFooterHTML, &branding.EmailHeaderHTML,
		&branding.HidePoweredBy, &branding.CreatedAt, &branding.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("domain not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get branding: %w", err)
	}

	return &branding, nil
}

// ApplyBrandingToEmail applies branding to email HTML
func (s *BrandingService) ApplyBrandingToEmail(ctx context.Context, orgID int64, emailHTML string) (string, error) {
	branding, err := s.Get(ctx, orgID)
	if err != nil {
		return emailHTML, nil // Return original if branding fails
	}

	// Add header if configured
	if branding.EmailHeaderHTML != "" {
		// Insert after <body> tag
		bodyIndex := strings.Index(strings.ToLower(emailHTML), "<body")
		if bodyIndex != -1 {
			closeTag := strings.Index(emailHTML[bodyIndex:], ">")
			if closeTag != -1 {
				insertPos := bodyIndex + closeTag + 1
				emailHTML = emailHTML[:insertPos] + branding.EmailHeaderHTML + emailHTML[insertPos:]
			}
		}
	}

	// Add footer if configured
	if branding.EmailFooterHTML != "" {
		// Insert before </body> tag
		endBodyIndex := strings.LastIndex(strings.ToLower(emailHTML), "</body>")
		if endBodyIndex != -1 {
			emailHTML = emailHTML[:endBodyIndex] + branding.EmailFooterHTML + emailHTML[endBodyIndex:]
		}
	}

	// Add powered by unless disabled
	if !branding.HidePoweredBy {
		poweredByURL := fmt.Sprintf("https://%s", s.cfg.AppDomain)
		poweredBy := fmt.Sprintf(`<div style="text-align:center;font-size:12px;color:#666;margin-top:20px;padding:10px;">
			Powered by <a href="%s" style="color:#4F46E5;">%s</a>
		</div>`, poweredByURL, s.cfg.AppName)
		endBodyIndex := strings.LastIndex(strings.ToLower(emailHTML), "</body>")
		if endBodyIndex != -1 {
			emailHTML = emailHTML[:endBodyIndex] + poweredBy + emailHTML[endBodyIndex:]
		}
	}

	return emailHTML, nil
}

// GetBrandingCSS generates CSS variables for branding
func (s *BrandingService) GetBrandingCSS(ctx context.Context, orgID int64) (string, error) {
	branding, err := s.Get(ctx, orgID)
	if err != nil {
		return "", err
	}

	css := fmt.Sprintf(`
:root {
  --primary-color: %s;
  --accent-color: %s;
  --logo-url: url('%s');
  --logo-light-url: url('%s');
}
`,
		defaultIfEmpty(branding.PrimaryColor, "#4F46E5"),
		defaultIfEmpty(branding.AccentColor, "#10B981"),
		branding.LogoURL,
		defaultIfEmpty(branding.LogoLightURL, branding.LogoURL),
	)

	return css, nil
}

// Helper functions

func isValidHexColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := color[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func isValidDomain(domain string) bool {
	// Basic domain validation
	domainRegex := regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)
	return domainRegex.MatchString(domain)
}

func defaultIfEmpty(value, defaultVal string) string {
	if value == "" {
		return defaultVal
	}
	return value
}
