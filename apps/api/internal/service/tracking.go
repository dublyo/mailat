package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// TrackingService handles email open and click tracking
type TrackingService struct {
	db  *sql.DB
	cfg *config.Config
}

// TrackingData contains encoded tracking information
type TrackingData struct {
	EmailID    int64  `json:"e"`
	CampaignID int    `json:"c,omitempty"`
	ContactID  int64  `json:"ct,omitempty"`
	LinkID     string `json:"l,omitempty"`
	TargetURL  string `json:"u,omitempty"`
}

// OpenEvent represents an email open event
type OpenEvent struct {
	EmailID    int64
	CampaignID int
	ContactID  int64
	Timestamp  time.Time
	IPAddress  string
	UserAgent  string
}

// ClickEvent represents a link click event
type ClickEvent struct {
	EmailID    int64
	CampaignID int
	ContactID  int64
	LinkID     string
	TargetURL  string
	Timestamp  time.Time
	IPAddress  string
	UserAgent  string
}

func NewTrackingService(db *sql.DB, cfg *config.Config) *TrackingService {
	return &TrackingService{db: db, cfg: cfg}
}

// GenerateTrackingPixelURL generates a tracking pixel URL for an email
func (s *TrackingService) GenerateTrackingPixelURL(emailID int64, campaignID int, contactID int64) string {
	data := TrackingData{
		EmailID:    emailID,
		CampaignID: campaignID,
		ContactID:  contactID,
	}

	token := s.encodeTrackingData(data)
	baseURL := s.cfg.APIUrl
	if baseURL == "" {
		baseURL = "http://localhost:3001"
	}

	return fmt.Sprintf("%s/api/v1/tracking/open/%s.gif", baseURL, token)
}

// GenerateClickTrackingURL wraps a URL with click tracking
func (s *TrackingService) GenerateClickTrackingURL(emailID int64, campaignID int, contactID int64, targetURL string, linkID string) string {
	data := TrackingData{
		EmailID:    emailID,
		CampaignID: campaignID,
		ContactID:  contactID,
		LinkID:     linkID,
		TargetURL:  targetURL,
	}

	token := s.encodeTrackingData(data)
	baseURL := s.cfg.APIUrl
	if baseURL == "" {
		baseURL = "http://localhost:3001"
	}

	return fmt.Sprintf("%s/api/v1/tracking/click/%s", baseURL, token)
}

// ProcessOpenEvent processes an email open event
func (s *TrackingService) ProcessOpenEvent(ctx context.Context, token string, ipAddress string, userAgent string) error {
	data, err := s.decodeTrackingData(token)
	if err != nil {
		return fmt.Errorf("invalid tracking token")
	}

	// Record the open event
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO delivery_events (email_id, event_type, data, occurred_at)
		VALUES ($1, 'opened', $2, NOW())
	`, data.EmailID, fmt.Sprintf(`{"ip":"%s","ua":"%s"}`, ipAddress, userAgent))
	if err != nil {
		return fmt.Errorf("failed to record open event: %w", err)
	}

	// Update email open count
	_, err = s.db.ExecContext(ctx, `
		UPDATE emails SET
			open_count = open_count + 1,
			updated_at = NOW()
		WHERE id = $1
	`, data.EmailID)
	if err != nil {
		return fmt.Errorf("failed to update email: %w", err)
	}

	// Update campaign stats if applicable
	if data.CampaignID > 0 {
		s.db.ExecContext(ctx, `
			UPDATE campaigns SET
				open_count = open_count + 1,
				updated_at = NOW()
			WHERE id = $1
		`, data.CampaignID)
	}

	// Update contact engagement
	if data.ContactID > 0 {
		s.db.ExecContext(ctx, `
			UPDATE contacts SET
				last_engaged_at = NOW(),
				engagement_score = engagement_score + 1,
				updated_at = NOW()
			WHERE id = $1
		`, data.ContactID)
	}

	return nil
}

// ProcessClickEvent processes a link click event
func (s *TrackingService) ProcessClickEvent(ctx context.Context, token string, ipAddress string, userAgent string) (string, error) {
	data, err := s.decodeTrackingData(token)
	if err != nil {
		return "", fmt.Errorf("invalid tracking token")
	}

	// Record the click event
	eventData := map[string]string{
		"ip":     ipAddress,
		"ua":     userAgent,
		"linkId": data.LinkID,
		"url":    data.TargetURL,
	}
	eventDataJSON, _ := json.Marshal(eventData)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO delivery_events (email_id, event_type, data, occurred_at)
		VALUES ($1, 'clicked', $2, NOW())
	`, data.EmailID, eventDataJSON)
	if err != nil {
		return data.TargetURL, fmt.Errorf("failed to record click event: %w", err)
	}

	// Update email click count
	_, err = s.db.ExecContext(ctx, `
		UPDATE emails SET
			click_count = click_count + 1,
			updated_at = NOW()
		WHERE id = $1
	`, data.EmailID)
	if err != nil {
		return data.TargetURL, fmt.Errorf("failed to update email: %w", err)
	}

	// Update campaign stats if applicable
	if data.CampaignID > 0 {
		s.db.ExecContext(ctx, `
			UPDATE campaigns SET
				click_count = click_count + 1,
				updated_at = NOW()
			WHERE id = $1
		`, data.CampaignID)
	}

	// Update contact engagement
	if data.ContactID > 0 {
		s.db.ExecContext(ctx, `
			UPDATE contacts SET
				last_engaged_at = NOW(),
				engagement_score = engagement_score + 2,
				updated_at = NOW()
			WHERE id = $1
		`, data.ContactID)
	}

	return data.TargetURL, nil
}

// ProcessEmailContent adds tracking to email HTML content
func (s *TrackingService) ProcessEmailContent(emailID int64, campaignID int, contactID int64, htmlContent string) string {
	if htmlContent == "" {
		return htmlContent
	}

	// Add tracking pixel before closing body tag
	trackingPixel := fmt.Sprintf(`<img src="%s" width="1" height="1" style="display:none" alt="" />`,
		s.GenerateTrackingPixelURL(emailID, campaignID, contactID))

	if strings.Contains(htmlContent, "</body>") {
		htmlContent = strings.Replace(htmlContent, "</body>", trackingPixel+"</body>", 1)
	} else {
		htmlContent = htmlContent + trackingPixel
	}

	// Wrap all links with click tracking
	// This is a simplified version - a proper implementation would use HTML parsing
	htmlContent = s.wrapLinksWithTracking(htmlContent, emailID, campaignID, contactID)

	return htmlContent
}

// wrapLinksWithTracking wraps href links with click tracking
func (s *TrackingService) wrapLinksWithTracking(html string, emailID int64, campaignID int, contactID int64) string {
	// Find and replace href attributes
	// Note: This is a simplified approach. Production code should use proper HTML parsing.
	result := html
	linkIndex := 0

	// Find href="..." patterns
	for {
		hrefStart := strings.Index(result, `href="`)
		if hrefStart == -1 {
			break
		}

		hrefStart += 6 // Move past href="
		hrefEnd := strings.Index(result[hrefStart:], `"`)
		if hrefEnd == -1 {
			break
		}

		originalURL := result[hrefStart : hrefStart+hrefEnd]

		// Skip mailto:, tel:, and anchor links
		if strings.HasPrefix(originalURL, "mailto:") ||
			strings.HasPrefix(originalURL, "tel:") ||
			strings.HasPrefix(originalURL, "#") ||
			strings.HasPrefix(originalURL, "{{") {
			result = result[:hrefStart] + "SKIP:" + result[hrefStart:]
			continue
		}

		// Generate tracked URL
		linkID := fmt.Sprintf("link_%d", linkIndex)
		trackedURL := s.GenerateClickTrackingURL(emailID, campaignID, contactID, originalURL, linkID)

		// Replace the URL
		result = result[:hrefStart] + trackedURL + result[hrefStart+hrefEnd:]
		linkIndex++
	}

	// Remove SKIP: markers
	result = strings.ReplaceAll(result, "SKIP:", "")

	return result
}

// encodeTrackingData encodes tracking data to a URL-safe token
func (s *TrackingService) encodeTrackingData(data TrackingData) string {
	jsonData, _ := json.Marshal(data)

	// Sign the data
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write(jsonData)
	signature := mac.Sum(nil)

	// Combine data + signature
	combined := append(jsonData, signature[:8]...) // Use first 8 bytes of signature

	return base64.URLEncoding.EncodeToString(combined)
}

// decodeTrackingData decodes and verifies a tracking token
func (s *TrackingService) decodeTrackingData(token string) (*TrackingData, error) {
	combined, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding")
	}

	if len(combined) < 9 {
		return nil, fmt.Errorf("token too short")
	}

	// Split data and signature
	jsonData := combined[:len(combined)-8]
	providedSig := combined[len(combined)-8:]

	// Verify signature
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write(jsonData)
	expectedSig := mac.Sum(nil)[:8]

	if !hmac.Equal(providedSig, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	var data TrackingData
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("invalid token data")
	}

	return &data, nil
}

// GetEmailAnalytics retrieves analytics for an email
func (s *TrackingService) GetEmailAnalytics(ctx context.Context, emailID int64) (map[string]interface{}, error) {
	var openCount, clickCount int
	var sentAt, deliveredAt, firstOpenedAt *time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT open_count, click_count, sent_at, delivered_at
		FROM emails WHERE id = $1
	`, emailID).Scan(&openCount, &clickCount, &sentAt, &deliveredAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get email: %w", err)
	}

	// Get first open time
	s.db.QueryRowContext(ctx, `
		SELECT MIN(occurred_at) FROM delivery_events
		WHERE email_id = $1 AND event_type = 'opened'
	`, emailID).Scan(&firstOpenedAt)

	// Get click details
	rows, _ := s.db.QueryContext(ctx, `
		SELECT data, occurred_at FROM delivery_events
		WHERE email_id = $1 AND event_type = 'clicked'
		ORDER BY occurred_at DESC
		LIMIT 10
	`, emailID)
	defer rows.Close()

	var clicks []map[string]interface{}
	for rows.Next() {
		var dataJSON []byte
		var occurredAt time.Time
		rows.Scan(&dataJSON, &occurredAt)

		var data map[string]interface{}
		json.Unmarshal(dataJSON, &data)
		data["occurredAt"] = occurredAt
		clicks = append(clicks, data)
	}

	return map[string]interface{}{
		"openCount":     openCount,
		"clickCount":    clickCount,
		"sentAt":        sentAt,
		"deliveredAt":   deliveredAt,
		"firstOpenedAt": firstOpenedAt,
		"recentClicks":  clicks,
	}, nil
}

// GetCampaignAnalytics retrieves detailed analytics for a campaign
func (s *TrackingService) GetCampaignAnalytics(ctx context.Context, campaignID int) (map[string]interface{}, error) {
	var sentCount, deliveredCount, openCount, clickCount, bounceCount, unsubscribeCount, complaintCount int
	var startedAt, completedAt *time.Time

	err := s.db.QueryRowContext(ctx, `
		SELECT sent_count, delivered_count, open_count, click_count,
			bounce_count, unsubscribe_count, complaint_count,
			started_at, completed_at
		FROM campaigns WHERE id = $1
	`, campaignID).Scan(
		&sentCount, &deliveredCount, &openCount, &clickCount,
		&bounceCount, &unsubscribeCount, &complaintCount,
		&startedAt, &completedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get campaign: %w", err)
	}

	// Calculate rates
	var openRate, clickRate, bounceRate, unsubscribeRate, complaintRate float64
	if sentCount > 0 {
		openRate = float64(openCount) / float64(sentCount) * 100
		clickRate = float64(clickCount) / float64(sentCount) * 100
		bounceRate = float64(bounceCount) / float64(sentCount) * 100
		unsubscribeRate = float64(unsubscribeCount) / float64(sentCount) * 100
		complaintRate = float64(complaintCount) / float64(sentCount) * 100
	}

	// Get click-to-open rate
	var clickToOpenRate float64
	if openCount > 0 {
		clickToOpenRate = float64(clickCount) / float64(openCount) * 100
	}

	// Get opens by hour
	opensByHour := make(map[int]int)
	rows, _ := s.db.QueryContext(ctx, `
		SELECT EXTRACT(HOUR FROM de.occurred_at)::int as hour, COUNT(*)
		FROM delivery_events de
		JOIN emails e ON e.id = de.email_id
		WHERE e.campaign_id = $1 AND de.event_type = 'opened'
		GROUP BY hour
		ORDER BY hour
	`, campaignID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var hour, count int
			rows.Scan(&hour, &count)
			opensByHour[hour] = count
		}
	}

	// Get top clicked links
	topLinks := []map[string]interface{}{}
	linkRows, _ := s.db.QueryContext(ctx, `
		SELECT de.data->>'url' as url, COUNT(*) as clicks
		FROM delivery_events de
		JOIN emails e ON e.id = de.email_id
		WHERE e.campaign_id = $1 AND de.event_type = 'clicked'
		GROUP BY url
		ORDER BY clicks DESC
		LIMIT 10
	`, campaignID)
	if linkRows != nil {
		defer linkRows.Close()
		for linkRows.Next() {
			var urlStr string
			var clicks int
			linkRows.Scan(&urlStr, &clicks)
			// Parse and display friendly URL
			parsedURL, _ := url.Parse(urlStr)
			displayURL := urlStr
			if parsedURL != nil {
				displayURL = parsedURL.Host + parsedURL.Path
			}
			topLinks = append(topLinks, map[string]interface{}{
				"url":    urlStr,
				"display": displayURL,
				"clicks": clicks,
			})
		}
	}

	return map[string]interface{}{
		"sentCount":        sentCount,
		"deliveredCount":   deliveredCount,
		"openCount":        openCount,
		"clickCount":       clickCount,
		"bounceCount":      bounceCount,
		"unsubscribeCount": unsubscribeCount,
		"complaintCount":   complaintCount,
		"openRate":         openRate,
		"clickRate":        clickRate,
		"bounceRate":       bounceRate,
		"unsubscribeRate":  unsubscribeRate,
		"complaintRate":    complaintRate,
		"clickToOpenRate":  clickToOpenRate,
		"startedAt":        startedAt,
		"completedAt":      completedAt,
		"opensByHour":      opensByHour,
		"topLinks":         topLinks,
	}, nil
}
