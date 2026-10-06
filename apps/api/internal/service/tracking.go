package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dublyo/mailat/api/internal/config"
)

const (
	trackingMACLen = 16
	// maxStoredTrackingEvents bounds campaign_events rows per recipient and
	// type, so bots and prefetchers cannot inflate storage.
	maxStoredTrackingEvents = 50
	maxTrackedURLLen        = 2048
)

var errInvalidTrackingToken = errors.New("invalid tracking token")

// TrackingService records campaign opens and clicks from signed tokens.
type TrackingService struct {
	db  *sql.DB
	cfg *config.Config
}

// TrackingData is the signed payload of an open (no U) or click token.
// Tokens never contain email addresses.
type TrackingData struct {
	R int64  `json:"r"`           // campaign_recipients.id
	C int64  `json:"c"`           // campaigns.id
	O int64  `json:"o"`           // organizations.id
	L int    `json:"l,omitempty"` // link index in document order
	U string `json:"u,omitempty"` // click target
	// K is the token kind: empty for campaigns, trackingKindAutomation for
	// automation messages (R is then automation_messages.id, C the automation).
	K string `json:"k,omitempty"`
}

const trackingKindAutomation = "a"

func NewTrackingService(db *sql.DB, cfg *config.Config) *TrackingService {
	return &TrackingService{db: db, cfg: cfg}
}

// HomeURL is where invalid click links land.
func (s *TrackingService) HomeURL() string {
	if s.cfg.WebUrl != "" {
		return s.cfg.WebUrl
	}
	return "/"
}

func trackingKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("mailat/tracking/v1"))
	return mac.Sum(nil)
}

// encodeTrackingToken returns base64url(JSON || first 16 bytes of HMAC-SHA256).
func encodeTrackingToken(secret string, d TrackingData) string {
	payload, _ := json.Marshal(d)
	mac := hmac.New(sha256.New, trackingKey(secret))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)[:trackingMACLen]...))
}

func decodeTrackingToken(secret, token string) (*TrackingData, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) <= trackingMACLen {
		return nil, errInvalidTrackingToken
	}
	payload, sig := raw[:len(raw)-trackingMACLen], raw[len(raw)-trackingMACLen:]
	mac := hmac.New(sha256.New, trackingKey(secret))
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)[:trackingMACLen]) {
		return nil, errInvalidTrackingToken
	}
	var d TrackingData
	if err := json.Unmarshal(payload, &d); err != nil || d.R <= 0 || d.C <= 0 || d.O <= 0 || (d.K != "" && d.K != trackingKindAutomation) {
		return nil, errInvalidTrackingToken
	}
	return &d, nil
}

// trackableURL reports whether u is an absolute http(s) URL short enough to
// be signed into a click token (and later redirected to).
func trackableURL(u string) bool {
	if u == "" || len(u) > maxTrackedURLLen || strings.ContainsAny(u, "\r\n\t ") {
		return false
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return false
	}
	scheme := strings.ToLower(p.Scheme)
	return scheme == "http" || scheme == "https"
}

// trackableTemplate is trackableURL for a link that may still hold
// {{variables}}; they are filled in when the click redirects.
func trackableTemplate(u string) bool {
	return len(u) <= maxTrackedURLLen && trackableURL(templateVarRe.ReplaceAllString(u, "x"))
}

// OpenToken signs an open-pixel token for a campaign recipient.
func OpenToken(secret string, recipientID, campaignID, orgID int64) string {
	return encodeTrackingToken(secret, TrackingData{R: recipientID, C: campaignID, O: orgID})
}

// ClickToken signs a click token carrying the redirect target.
func ClickToken(secret string, recipientID, campaignID, orgID int64, linkIndex int, target string) string {
	return encodeTrackingToken(secret, TrackingData{R: recipientID, C: campaignID, O: orgID, L: linkIndex, U: target})
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

type trackedRecipient struct {
	contactID   sql.NullInt64
	openCount   int
	clickCount  int
	opened      bool
	clicked     bool
	trackOpens  bool
	trackClicks bool
}

// lockTrackedRecipient locks the token's recipient; a token whose recipient,
// campaign and org do not all match returns sql.ErrNoRows.
func lockTrackedRecipient(ctx context.Context, tx *sql.Tx, d *TrackingData) (*trackedRecipient, error) {
	var t trackedRecipient
	if d.K == trackingKindAutomation {
		err := tx.QueryRowContext(ctx, `
			SELECT contact_id, open_count, click_count, first_opened_at IS NOT NULL, first_clicked_at IS NOT NULL, track_opens, track_clicks
			FROM automation_messages WHERE id=$1 AND org_id=$2 AND automation_id=$3 FOR UPDATE`, d.R, d.O, d.C).
			Scan(&t.contactID, &t.openCount, &t.clickCount, &t.opened, &t.clicked, &t.trackOpens, &t.trackClicks)
		return &t, err
	}
	err := tx.QueryRowContext(ctx, `
		SELECT r.contact_id, r.open_count, r.click_count, r.first_opened_at IS NOT NULL, r.first_clicked_at IS NOT NULL,
			c.track_opens, c.track_clicks
		FROM campaign_recipients r JOIN campaigns c ON c.id=r.campaign_id AND c.org_id=r.org_id
		WHERE r.id=$1 AND r.org_id=$2 AND r.campaign_id=$3
		FOR UPDATE OF r`, d.R, d.O, d.C).Scan(&t.contactID, &t.openCount, &t.clickCount, &t.opened, &t.clicked, &t.trackOpens, &t.trackClicks)
	return &t, err
}

// recordOpen counts an open on a locked recipient. Campaign open_count is
// unique per recipient; the recipient's own count grows on every open.
func recordOpen(ctx context.Context, tx *sql.Tx, d *TrackingData, t *trackedRecipient, ip, ua string) error {
	if d.K == trackingKindAutomation {
		return recordAutomationEvent(ctx, tx, d, t, "open", ip, ua)
	}
	if t.openCount < maxStoredTrackingEvents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO campaign_events(campaign_id,recipient_id,event_type,user_agent,ip_address) VALUES($1,$2,'open',$3,$4)`,
			d.C, d.R, truncateRunes(ua, 512), truncateRunes(ip, 45)); err != nil {
			return fmt.Errorf("failed to record open: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE campaign_recipients SET open_count=open_count+1, first_opened_at=COALESCE(first_opened_at,now()), updated_at=now() WHERE id=$1 AND org_id=$2`, d.R, d.O); err != nil {
		return fmt.Errorf("failed to update recipient: %w", err)
	}
	if !t.opened {
		if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET open_count=open_count+1, updated_at=now() WHERE id=$1 AND org_id=$2`, d.C, d.O); err != nil {
			return fmt.Errorf("failed to update campaign: %w", err)
		}
		t.opened = true
	}
	t.openCount++
	return nil
}

// recordAutomationEvent counts an open or click on a locked automation
// message; events are stored up to maxStoredTrackingEvents per type.
func recordAutomationEvent(ctx context.Context, tx *sql.Tx, d *TrackingData, t *trackedRecipient, kind, ip, ua string) error {
	n, column := t.openCount, "open"
	var target, link any
	if kind == "click" {
		n, column, target, link = t.clickCount, "click", d.U, d.L
	}
	if n < maxStoredTrackingEvents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO automation_message_events(message_id,automation_id,event_type,url,link_index,user_agent,ip_address)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, d.R, d.C, kind, target, link, truncateRunes(ua, 512), truncateRunes(ip, 45)); err != nil {
			return fmt.Errorf("failed to record %s: %w", kind, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automation_messages SET `+column+`_count=`+column+`_count+1,
		first_`+column+`ed_at=COALESCE(first_`+column+`ed_at,now()), updated_at=now() WHERE id=$1 AND org_id=$2`, d.R, d.O); err != nil {
		return fmt.Errorf("failed to update automation message: %w", err)
	}
	if kind == "click" {
		t.clicked = true
		t.clickCount++
	} else {
		t.opened = true
		t.openCount++
	}
	return nil
}

func bumpEngagement(ctx context.Context, tx *sql.Tx, t *trackedRecipient, orgID int64, points int) error {
	if !t.contactID.Valid {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contacts SET last_engaged_at=now(), engagement_score=engagement_score+$3, updated_at=now() WHERE id=$1 AND org_id=$2`,
		t.contactID.Int64, orgID, points); err != nil {
		return fmt.Errorf("failed to update engagement: %w", err)
	}
	return nil
}

// ProcessOpenEvent records an open when the campaign tracks opens. Unknown or
// foreign recipients are ignored.
func (s *TrackingService) ProcessOpenEvent(ctx context.Context, token string, ipAddress string, userAgent string) error {
	d, err := decodeTrackingToken(s.cfg.JWTSecret, token)
	if err != nil || d.U != "" {
		return errInvalidTrackingToken
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to record open: %w", err)
	}
	defer tx.Rollback()
	t, err := lockTrackedRecipient(ctx, tx, d)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to load recipient: %w", err)
	}
	if !t.trackOpens {
		return nil
	}
	stored := t.openCount < maxStoredTrackingEvents
	if err = recordOpen(ctx, tx, d, t, ipAddress, userAgent); err != nil {
		return err
	}
	if stored {
		if err = bumpEngagement(ctx, tx, t, d.O, 1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// clickTarget fills the {{variables}} of a signed template link from the
// recipient, as the renderer would have. Without a contact (deleted, erased,
// or the lookup failed) they render empty, so the reader still reaches the
// link. Stored click events keep the template, never the result.
func (s *TrackingService) clickTarget(ctx context.Context, d *TrackingData) string {
	if !templateVarRe.MatchString(d.U) {
		return d.U
	}
	var rcpt eligibleRecipient
	var attrs []byte
	query := `SELECT r.email, COALESCE(c.first_name,''), COALESCE(c.last_name,''), COALESCE(c.attributes,'{}'::jsonb)
		FROM campaign_recipients r JOIN contacts c ON c.id=r.contact_id AND c.org_id=r.org_id
		WHERE r.id=$1 AND r.org_id=$2 AND r.campaign_id=$3`
	if d.K == trackingKindAutomation {
		query = `SELECT m.email, COALESCE(c.first_name,''), COALESCE(c.last_name,''), COALESCE(c.attributes,'{}'::jsonb)
			FROM automation_messages m JOIN contacts c ON c.id=m.contact_id AND c.org_id=m.org_id
			WHERE m.id=$1 AND m.org_id=$2 AND m.automation_id=$3`
	}
	if err := s.db.QueryRowContext(ctx, query, d.R, d.O, d.C).
		Scan(&rcpt.Email, &rcpt.FirstName, &rcpt.LastName, &attrs); err == nil {
		_ = json.Unmarshal(attrs, &rcpt.Attributes)
	} else {
		rcpt = eligibleRecipient{}
	}
	if target := (&personaliser{rcpt: rcpt}).apply(d.U, false); trackableURL(target) {
		return target
	}
	// A value made the link invalid (a space, too long): drop the values.
	return (&personaliser{}).apply(d.U, false)
}

// ProcessClickEvent returns the signed redirect target and records the click
// when the campaign tracks clicks. The target is returned even when recording
// is off or fails, so the reader always reaches the link. A click with no
// recorded open also counts as the first open (when opens are tracked).
func (s *TrackingService) ProcessClickEvent(ctx context.Context, token string, ipAddress string, userAgent string) (string, error) {
	d, err := decodeTrackingToken(s.cfg.JWTSecret, token)
	if err != nil || !trackableTemplate(d.U) {
		return "", errInvalidTrackingToken
	}
	target := s.clickTarget(ctx, d)
	if !trackableURL(target) {
		return "", errInvalidTrackingToken
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return target, fmt.Errorf("failed to record click: %w", err)
	}
	defer tx.Rollback()
	t, err := lockTrackedRecipient(ctx, tx, d)
	if err == sql.ErrNoRows {
		return target, nil
	}
	if err != nil {
		return target, fmt.Errorf("failed to load recipient: %w", err)
	}
	if !t.trackClicks {
		return target, nil
	}
	stored := t.clickCount < maxStoredTrackingEvents
	if d.K == trackingKindAutomation {
		if err = recordAutomationEvent(ctx, tx, d, t, "click", ipAddress, userAgent); err != nil {
			return target, err
		}
	} else {
		if stored {
			if _, err = tx.ExecContext(ctx, `INSERT INTO campaign_events(campaign_id,recipient_id,event_type,url,link_index,user_agent,ip_address) VALUES($1,$2,'click',$3,$4,$5,$6)`,
				d.C, d.R, d.U, d.L, truncateRunes(userAgent, 512), truncateRunes(ipAddress, 45)); err != nil {
				return target, fmt.Errorf("failed to record click: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET click_count=click_count+1, first_clicked_at=COALESCE(first_clicked_at,now()), updated_at=now() WHERE id=$1 AND org_id=$2`, d.R, d.O); err != nil {
			return target, fmt.Errorf("failed to update recipient: %w", err)
		}
		if !t.clicked {
			if _, err = tx.ExecContext(ctx, `UPDATE campaigns SET click_count=click_count+1, updated_at=now() WHERE id=$1 AND org_id=$2`, d.C, d.O); err != nil {
				return target, fmt.Errorf("failed to update campaign: %w", err)
			}
		}
	}
	if !t.opened && t.trackOpens {
		if err = recordOpen(ctx, tx, d, t, ipAddress, userAgent); err != nil {
			return target, err
		}
	}
	if stored {
		if err = bumpEngagement(ctx, tx, t, d.O, 2); err != nil {
			return target, err
		}
	}
	if err = tx.Commit(); err != nil {
		return target, fmt.Errorf("failed to record click: %w", err)
	}
	return target, nil
}
