package handler

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// SNSMessage represents an SNS notification message
type SNSMessage struct {
	Type             string `json:"Type"`
	MessageId        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	UnsubscribeURL   string `json:"UnsubscribeURL"`
	SubscribeURL     string `json:"SubscribeURL"`
	Token            string `json:"Token"`
}

// SESNotification represents the SES notification payload within SNS message
type SESNotification struct {
	NotificationType string          `json:"notificationType"`
	Mail             SESMail         `json:"mail"`
	Bounce           *SESBounce      `json:"bounce,omitempty"`
	Complaint        *SESComplaint   `json:"complaint,omitempty"`
	Delivery         *SESDelivery    `json:"delivery,omitempty"`
	Send             *SESSend        `json:"send,omitempty"`
	Reject           *SESReject      `json:"reject,omitempty"`
	Open             *SESOpen        `json:"open,omitempty"`
	Click            *SESClick       `json:"click,omitempty"`
	RenderingFailure *SESRendering   `json:"renderingFailure,omitempty"`
	DeliveryDelay    *SESDelay       `json:"deliveryDelay,omitempty"`
}

// SESMail contains the email metadata from SES
type SESMail struct {
	Timestamp        string            `json:"timestamp"`
	MessageId        string            `json:"messageId"`
	Source           string            `json:"source"`
	SourceArn        string            `json:"sourceArn"`
	SourceIp         string            `json:"sourceIp"`
	CallerIdentity   string            `json:"callerIdentity"`
	SendingAccountId string            `json:"sendingAccountId"`
	Destination      []string          `json:"destination"`
	HeadersTruncated bool              `json:"headersTruncated"`
	Headers          []SESHeader       `json:"headers"`
	CommonHeaders    SESCommonHeaders  `json:"commonHeaders"`
	Tags             map[string]string `json:"tags"`
}

// SESHeader represents an email header
type SESHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SESCommonHeaders contains common email headers
type SESCommonHeaders struct {
	From      []string `json:"from"`
	To        []string `json:"to"`
	MessageId string   `json:"messageId"`
	Subject   string   `json:"subject"`
}

// SESBounce contains bounce notification details
type SESBounce struct {
	BounceType        string               `json:"bounceType"`
	BounceSubType     string               `json:"bounceSubType"`
	BouncedRecipients []SESBouncedRecipient `json:"bouncedRecipients"`
	Timestamp         string               `json:"timestamp"`
	FeedbackId        string               `json:"feedbackId"`
	ReportingMTA      string               `json:"reportingMTA"`
}

// SESBouncedRecipient contains recipient-specific bounce info
type SESBouncedRecipient struct {
	EmailAddress   string `json:"emailAddress"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	DiagnosticCode string `json:"diagnosticCode"`
}

// SESComplaint contains complaint notification details
type SESComplaint struct {
	ComplaintSubType      string                  `json:"complaintSubType"`
	ComplainedRecipients  []SESComplainedRecipient `json:"complainedRecipients"`
	Timestamp             string                  `json:"timestamp"`
	FeedbackId            string                  `json:"feedbackId"`
	UserAgent             string                  `json:"userAgent"`
	ComplaintFeedbackType string                  `json:"complaintFeedbackType"`
	ArrivalDate           string                  `json:"arrivalDate"`
}

// SESComplainedRecipient contains recipient-specific complaint info
type SESComplainedRecipient struct {
	EmailAddress string `json:"emailAddress"`
}

// SESDelivery contains delivery notification details
type SESDelivery struct {
	Timestamp            string   `json:"timestamp"`
	ProcessingTimeMillis int64    `json:"processingTimeMillis"`
	Recipients           []string `json:"recipients"`
	SmtpResponse         string   `json:"smtpResponse"`
	ReportingMTA         string   `json:"reportingMTA"`
}

// SESSend contains send notification details
type SESSend struct {
	// Send events have no additional fields
}

// SESReject contains rejection notification details
type SESReject struct {
	Reason string `json:"reason"`
}

// SESOpen contains open tracking details
type SESOpen struct {
	Timestamp string `json:"timestamp"`
	UserAgent string `json:"userAgent"`
	IpAddress string `json:"ipAddress"`
}

// SESClick contains click tracking details
type SESClick struct {
	Timestamp string `json:"timestamp"`
	UserAgent string `json:"userAgent"`
	IpAddress string `json:"ipAddress"`
	Link      string `json:"link"`
}

// SESRendering contains rendering failure details
type SESRendering struct {
	TemplateName string `json:"templateName"`
	ErrorMessage string `json:"errorMessage"`
}

// SESDelay contains delivery delay details
type SESDelay struct {
	DelayType         string               `json:"delayType"`
	ExpirationTime    string               `json:"expirationTime"`
	DelayedRecipients []SESBouncedRecipient `json:"delayedRecipients"`
	Timestamp         string               `json:"timestamp"`
}

// SNSWebhookHandler handles SNS notifications from SES
type SNSWebhookHandler struct {
	db  *sql.DB
	cfg *config.Config
}

// NewSNSWebhookHandler creates a new SNS webhook handler
func NewSNSWebhookHandler(db *sql.DB, cfg *config.Config) *SNSWebhookHandler {
	return &SNSWebhookHandler{
		db:  db,
		cfg: cfg,
	}
}

// HandleSNSWebhook processes SNS notifications from SES
func (h *SNSWebhookHandler) HandleSNSWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Parse SNS message
	var snsMsg SNSMessage
	if err := json.Unmarshal(body, &snsMsg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Verify the message is from AWS SNS (basic check)
	if !strings.HasPrefix(snsMsg.SigningCertURL, "https://sns.") ||
		!strings.Contains(snsMsg.SigningCertURL, ".amazonaws.com/") {
		http.Error(w, "Invalid signing cert URL", http.StatusForbidden)
		return
	}

	// Handle different SNS message types
	switch snsMsg.Type {
	case "SubscriptionConfirmation":
		// Automatically confirm SNS subscription
		h.confirmSubscription(snsMsg.SubscribeURL)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Subscription confirmed"))
		return

	case "Notification":
		// Process SES notification
		if err := h.processSESNotification(r.Context(), snsMsg.Message); err != nil {
			fmt.Printf("Error processing SES notification: %v\n", err)
			// Still return 200 to prevent SNS from retrying
		}
		w.WriteHeader(http.StatusOK)
		return

	case "UnsubscribeConfirmation":
		fmt.Println("SNS subscription unsubscribed")
		w.WriteHeader(http.StatusOK)
		return

	default:
		http.Error(w, "Unknown message type", http.StatusBadRequest)
		return
	}
}

// confirmSubscription confirms an SNS subscription by visiting the SubscribeURL
func (h *SNSWebhookHandler) confirmSubscription(subscribeURL string) {
	if subscribeURL == "" {
		return
	}

	// Validate URL is from AWS
	if !strings.HasPrefix(subscribeURL, "https://sns.") {
		fmt.Println("Invalid SNS subscribe URL")
		return
	}

	resp, err := http.Get(subscribeURL)
	if err != nil {
		fmt.Printf("Failed to confirm SNS subscription: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		fmt.Println("SNS subscription confirmed successfully")
	} else {
		fmt.Printf("SNS subscription confirmation returned status: %d\n", resp.StatusCode)
	}
}

// processSESNotification processes a SES notification from the SNS message
func (h *SNSWebhookHandler) processSESNotification(ctx context.Context, message string) error {
	var notification SESNotification
	if err := json.Unmarshal([]byte(message), &notification); err != nil {
		return fmt.Errorf("failed to parse SES notification: %w", err)
	}

	// Get the SES message ID
	sesMessageId := notification.Mail.MessageId

	// Find the email by provider_message_id
	var emailID int64
	var orgID int64
	err := h.db.QueryRowContext(ctx, `
		SELECT id, org_id FROM transactional_emails WHERE provider_message_id = $1
	`, sesMessageId).Scan(&emailID, &orgID)
	if err != nil {
		// Try the emails table
		err = h.db.QueryRowContext(ctx, `
			SELECT id, org_id FROM emails WHERE provider_message_id = $1
		`, sesMessageId).Scan(&emailID, &orgID)
		if err != nil {
			// Email not found, this might be from a different system
			return nil
		}
	}

	// Process based on notification type
	switch notification.NotificationType {
	case "Bounce":
		return h.handleBounce(ctx, emailID, orgID, notification.Bounce)

	case "Complaint":
		return h.handleComplaint(ctx, emailID, orgID, notification.Complaint)

	case "Delivery":
		return h.handleDelivery(ctx, emailID, notification.Delivery)

	case "Send":
		return h.handleSend(ctx, emailID)

	case "Reject":
		return h.handleReject(ctx, emailID, notification.Reject)

	case "Open":
		return h.handleOpen(ctx, emailID, notification.Open)

	case "Click":
		return h.handleClick(ctx, emailID, notification.Click)

	case "DeliveryDelay":
		return h.handleDeliveryDelay(ctx, emailID, notification.DeliveryDelay)

	default:
		fmt.Printf("Unknown SES notification type: %s\n", notification.NotificationType)
		return nil
	}
}

// handleBounce processes bounce notifications
func (h *SNSWebhookHandler) handleBounce(ctx context.Context, emailID, orgID int64, bounce *SESBounce) error {
	if bounce == nil {
		return nil
	}

	// Update email status
	bounceType := "soft"
	if bounce.BounceType == "Permanent" {
		bounceType = "hard"
	}

	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = 'bounced', bounced_at = NOW(), bounce_type = $2, bounce_reason = $3, updated_at = NOW()
		WHERE id = $1
	`, emailID, bounceType, bounce.BounceSubType)
	if err != nil {
		return err
	}

	// Record delivery event
	details := fmt.Sprintf("Bounce: %s/%s", bounce.BounceType, bounce.BounceSubType)
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, 'bounced', $2)
	`, emailID, details)
	if err != nil {
		fmt.Printf("Warning: failed to record bounce event: %v\n", err)
	}

	// Add bounced recipients to suppression list for hard bounces
	if bounce.BounceType == "Permanent" {
		for _, recipient := range bounce.BouncedRecipients {
			_, err = h.db.ExecContext(ctx, `
				INSERT INTO suppression_list (org_id, email, reason, source)
				VALUES ($1, $2, $3, 'bounce')
				ON CONFLICT (org_id, email) DO NOTHING
			`, orgID, strings.ToLower(recipient.EmailAddress), recipient.DiagnosticCode)
			if err != nil {
				fmt.Printf("Warning: failed to add to suppression list: %v\n", err)
			}
		}
	}

	return nil
}

// handleComplaint processes complaint notifications
func (h *SNSWebhookHandler) handleComplaint(ctx context.Context, emailID, orgID int64, complaint *SESComplaint) error {
	if complaint == nil {
		return nil
	}

	// Update email status
	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = 'complained', updated_at = NOW()
		WHERE id = $1
	`, emailID)
	if err != nil {
		return err
	}

	// Record delivery event
	details := fmt.Sprintf("Complaint: %s", complaint.ComplaintFeedbackType)
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, 'complained', $2)
	`, emailID, details)
	if err != nil {
		fmt.Printf("Warning: failed to record complaint event: %v\n", err)
	}

	// Add complained recipients to suppression list
	for _, recipient := range complaint.ComplainedRecipients {
		_, err = h.db.ExecContext(ctx, `
			INSERT INTO suppression_list (org_id, email, reason, source)
			VALUES ($1, $2, $3, 'complaint')
			ON CONFLICT (org_id, email) DO NOTHING
		`, orgID, strings.ToLower(recipient.EmailAddress), complaint.ComplaintFeedbackType)
		if err != nil {
			fmt.Printf("Warning: failed to add to suppression list: %v\n", err)
		}
	}

	return nil
}

// handleDelivery processes delivery notifications
func (h *SNSWebhookHandler) handleDelivery(ctx context.Context, emailID int64, delivery *SESDelivery) error {
	if delivery == nil {
		return nil
	}

	// Update email status
	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = 'delivered', delivered_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, emailID)
	if err != nil {
		return err
	}

	// Record delivery event
	details := fmt.Sprintf("Delivered to %s in %dms", strings.Join(delivery.Recipients, ", "), delivery.ProcessingTimeMillis)
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, 'delivered', $2)
	`, emailID, details)
	if err != nil {
		fmt.Printf("Warning: failed to record delivery event: %v\n", err)
	}

	return nil
}

// handleSend processes send notifications
func (h *SNSWebhookHandler) handleSend(ctx context.Context, emailID int64) error {
	// Email was sent - update if not already in sent state
	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = 'sent', sent_at = COALESCE(sent_at, NOW()), updated_at = NOW()
		WHERE id = $1 AND status IN ('queued', 'sending')
	`, emailID)
	return err
}

// handleReject processes rejection notifications
func (h *SNSWebhookHandler) handleReject(ctx context.Context, emailID int64, reject *SESReject) error {
	if reject == nil {
		return nil
	}

	// Update email status
	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = 'failed', updated_at = NOW()
		WHERE id = $1
	`, emailID)
	if err != nil {
		return err
	}

	// Record delivery event
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, 'rejected', $2)
	`, emailID, reject.Reason)
	if err != nil {
		fmt.Printf("Warning: failed to record reject event: %v\n", err)
	}

	return nil
}

// handleOpen processes open tracking notifications
func (h *SNSWebhookHandler) handleOpen(ctx context.Context, emailID int64, open *SESOpen) error {
	if open == nil {
		return nil
	}

	// Update email open timestamp
	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET opened_at = COALESCE(opened_at, NOW()), updated_at = NOW()
		WHERE id = $1
	`, emailID)
	if err != nil {
		return err
	}

	// Record delivery event
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details, ip_address, user_agent)
		VALUES ($1, 'opened', 'Email opened', $2, $3)
	`, emailID, open.IpAddress, open.UserAgent)
	if err != nil {
		fmt.Printf("Warning: failed to record open event: %v\n", err)
	}

	return nil
}

// handleClick processes click tracking notifications
func (h *SNSWebhookHandler) handleClick(ctx context.Context, emailID int64, click *SESClick) error {
	if click == nil {
		return nil
	}

	// Update email click timestamp
	_, err := h.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET clicked_at = COALESCE(clicked_at, NOW()), updated_at = NOW()
		WHERE id = $1
	`, emailID)
	if err != nil {
		return err
	}

	// Record delivery event
	details := fmt.Sprintf("Link clicked: %s", click.Link)
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details, ip_address, user_agent)
		VALUES ($1, 'clicked', $2, $3, $4)
	`, emailID, details, click.IpAddress, click.UserAgent)
	if err != nil {
		fmt.Printf("Warning: failed to record click event: %v\n", err)
	}

	return nil
}

// handleDeliveryDelay processes delivery delay notifications
func (h *SNSWebhookHandler) handleDeliveryDelay(ctx context.Context, emailID int64, delay *SESDelay) error {
	if delay == nil {
		return nil
	}

	// Record delivery event
	details := fmt.Sprintf("Delivery delayed: %s (expires: %s)", delay.DelayType, delay.ExpirationTime)
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, 'deferred', $2)
	`, emailID, details)
	if err != nil {
		fmt.Printf("Warning: failed to record delay event: %v\n", err)
	}

	return nil
}

// verifySNSSignature verifies the SNS message signature (optional, for production use)
func verifySNSSignature(msg *SNSMessage) bool {
	// For production, implement proper signature verification
	// https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html

	if msg.SigningCertURL == "" || msg.Signature == "" {
		return false
	}

	// Fetch the certificate
	resp, err := http.Get(msg.SigningCertURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	certBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}

	// Parse the certificate
	block, _ := pem.Decode(certBody)
	if block == nil {
		return false
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	// Build the string to sign based on message type
	var stringToSign string
	switch msg.Type {
	case "Notification":
		stringToSign = fmt.Sprintf("Message\n%s\nMessageId\n%s\nSubject\n%s\nTimestamp\n%s\nTopicArn\n%s\nType\n%s\n",
			msg.Message, msg.MessageId, msg.Subject, msg.Timestamp, msg.TopicArn, msg.Type)
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		stringToSign = fmt.Sprintf("Message\n%s\nMessageId\n%s\nSubscribeURL\n%s\nTimestamp\n%s\nToken\n%s\nTopicArn\n%s\nType\n%s\n",
			msg.Message, msg.MessageId, msg.SubscribeURL, msg.Timestamp, msg.Token, msg.TopicArn, msg.Type)
	default:
		return false
	}

	// Decode signature
	signature, err := base64.StdEncoding.DecodeString(msg.Signature)
	if err != nil {
		return false
	}

	// Verify signature
	err = cert.CheckSignature(x509.SHA1WithRSA, []byte(stringToSign), signature)
	return err == nil
}

// Unused import placeholder
var _ = time.Now
