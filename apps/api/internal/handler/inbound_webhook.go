package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/config"
)

// InboundWebhookHandler handles incoming emails from SES Lambda
type InboundWebhookHandler struct {
	db  *sql.DB
	cfg *config.Config
}

// NewInboundWebhookHandler creates a new inbound webhook handler
func NewInboundWebhookHandler(db *sql.DB, cfg *config.Config) *InboundWebhookHandler {
	return &InboundWebhookHandler{db: db, cfg: cfg}
}

// InboundEmailPayload represents the email data from SES Lambda
type InboundEmailPayload struct {
	OrgUUID      string   `json:"orgUUID"`
	MessageID    string   `json:"messageId"`
	Source       string   `json:"source"`
	Destination  []string `json:"destination"`
	Subject      string   `json:"subject"`
	From         []string `json:"from"`
	To           []string `json:"to"`
	CC           []string `json:"cc"`
	Date         string   `json:"date"`
	Timestamp    string   `json:"timestamp"`
	SpamVerdict  string   `json:"spamVerdict"`
	VirusVerdict string   `json:"virusVerdict"`
	SPFVerdict   string   `json:"spfVerdict"`
	DKIMVerdict  string   `json:"dkimVerdict"`
	DMARCVerdict string   `json:"dmarcVerdict"`
	TextBody     string   `json:"textBody"`
	HTMLBody     string   `json:"htmlBody"`
	Attachments  []struct {
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		Size        int64  `json:"size"`
	} `json:"attachments"`
}

// HandleInboundEmail processes incoming emails from SES Lambda
func (h *InboundWebhookHandler) HandleInboundEmail(r *ghttp.Request) {
	ctx := r.GetCtx()

	// Verify source header
	source := r.Header.Get("X-Mailat-Source")
	if source != "ses-lambda" {
		r.Response.WriteStatus(http.StatusUnauthorized)
		r.Response.WriteJson(map[string]string{"error": "unauthorized"})
		return
	}

	var payload InboundEmailPayload
	if err := json.NewDecoder(r.Request.Body).Decode(&payload); err != nil {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]string{"error": "invalid payload"})
		return
	}

	// Get org ID from UUID
	var orgID int64
	err := h.db.QueryRowContext(ctx, `
		SELECT id FROM organizations WHERE uuid = $1
	`, payload.OrgUUID).Scan(&orgID)
	if err != nil {
		r.Response.WriteStatus(http.StatusNotFound)
		r.Response.WriteJson(map[string]string{"error": "organization not found"})
		return
	}

	// Check spam/virus verdicts
	if payload.SpamVerdict == "FAIL" || payload.VirusVerdict == "FAIL" {
		// Log but don't store spam/virus emails
		fmt.Printf("Rejected email: spam=%s virus=%s from=%s\n",
			payload.SpamVerdict, payload.VirusVerdict, payload.Source)
		r.Response.WriteJson(map[string]string{"status": "rejected", "reason": "spam_or_virus"})
		return
	}

	// Find the identity this email was sent to
	var identityID int64
	var identityUUID string
	var domainID int64
	for _, recipient := range payload.Destination {
		// Extract email address
		email := extractEmail(recipient)
		if email == "" {
			continue
		}

		err = h.db.QueryRowContext(ctx, `
			SELECT i.id, i.uuid, i.domain_id
			FROM identities i
			JOIN domains d ON i.domain_id = d.id
			WHERE i.email = $1 AND d.org_id = $2
		`, email, orgID).Scan(&identityID, &identityUUID, &domainID)
		if err == nil {
			break
		}

		// Try matching just the domain (catch-all)
		parts := strings.Split(email, "@")
		if len(parts) == 2 {
			domain := parts[1]
			err = h.db.QueryRowContext(ctx, `
				SELECT i.id, i.uuid, i.domain_id
				FROM identities i
				JOIN domains d ON i.domain_id = d.id
				WHERE d.name = $1 AND d.org_id = $2 AND i.is_default = true
			`, domain, orgID).Scan(&identityID, &identityUUID, &domainID)
			if err == nil {
				break
			}
		}
	}

	if identityID == 0 {
		// No matching identity found, try to find a catch-all
		err = h.db.QueryRowContext(ctx, `
			SELECT i.id, i.uuid, i.domain_id
			FROM identities i
			JOIN domains d ON i.domain_id = d.id
			WHERE d.org_id = $1 AND i.is_default = true
			LIMIT 1
		`, orgID).Scan(&identityID, &identityUUID, &domainID)
		if err != nil {
			r.Response.WriteStatus(http.StatusNotFound)
			r.Response.WriteJson(map[string]string{"error": "no matching identity"})
			return
		}
	}

	// Parse from address
	fromEmail := extractEmail(payload.Source)
	fromName := extractName(payload.Source)

	// Generate snippet
	snippet := payload.TextBody
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}

	// Store email in database
	emailUUID := uuid.New().String()
	hasAttachments := len(payload.Attachments) > 0

	_, err = h.db.ExecContext(ctx, `
		INSERT INTO emails (
			uuid, org_id, message_id, identity_id,
			from_email, from_name, to_emails,
			subject, text_body, html_body, snippet,
			folder, is_read, is_starred, has_attachments,
			email_provider, provider_message_id,
			received_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10, $11,
			'inbox', false, false, $12,
			'ses', $13,
			$14, NOW(), NOW()
		)
	`,
		emailUUID, orgID, payload.MessageID, identityID,
		fromEmail, fromName, strings.Join(payload.To, ","),
		payload.Subject, payload.TextBody, payload.HTMLBody, snippet,
		hasAttachments, payload.MessageID,
		parseTime(payload.Timestamp),
	)
	if err != nil {
		fmt.Printf("Failed to store email: %v\n", err)
		r.Response.WriteStatus(http.StatusInternalServerError)
		r.Response.WriteJson(map[string]string{"error": "failed to store email"})
		return
	}

	// Store attachments metadata
	for _, att := range payload.Attachments {
		attUUID := uuid.New().String()
		_, _ = h.db.ExecContext(ctx, `
			INSERT INTO email_attachments (
				uuid, email_id, filename, content_type, size, created_at
			)
			SELECT $1, id, $2, $3, $4, NOW()
			FROM emails WHERE uuid = $5
		`, attUUID, att.Filename, att.ContentType, att.Size, emailUUID)
	}

	fmt.Printf("Stored inbound email: %s from %s to %s\n",
		payload.Subject, fromEmail, payload.Destination)

	r.Response.WriteJson(map[string]interface{}{
		"status":    "received",
		"emailUUID": emailUUID,
	})
}

// extractEmail extracts email address from "Name <email>" format
func extractEmail(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "<"); idx != -1 {
		end := strings.Index(s, ">")
		if end > idx {
			return strings.TrimSpace(s[idx+1 : end])
		}
	}
	// Check if it's just an email
	if strings.Contains(s, "@") {
		return s
	}
	return ""
}

// extractName extracts name from "Name <email>" format
func extractName(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "<"); idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	return ""
}

// parseTime parses timestamp string
func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", s); err == nil {
		return t
	}
	return time.Now()
}
