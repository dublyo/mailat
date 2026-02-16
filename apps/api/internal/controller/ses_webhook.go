package controller

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// SESWebhookController handles incoming webhooks from AWS SES
type SESWebhookController struct {
	receivingService *service.ReceivingService
}

// NewSESWebhookController creates a new SES webhook controller
func NewSESWebhookController(receivingService *service.ReceivingService) *SESWebhookController {
	return &SESWebhookController{
		receivingService: receivingService,
	}
}

// HandleIncoming handles incoming email notifications from AWS SES via SNS
// POST /api/v1/webhooks/ses/incoming
func (c *SESWebhookController) HandleIncoming(r *ghttp.Request) {
	ctx := r.Context()

	// Get webhook secret from query
	secret := r.Get("secret").String()
	if secret == "" {
		g.Log().Warning(ctx, "Missing webhook secret")
		response.Unauthorized(r, "Missing webhook secret")
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Request.Body)
	if err != nil {
		g.Log().Errorf(ctx, "Failed to read request body: %v", err)
		response.BadRequest(r, "Failed to read request body")
		return
	}

	// Parse SNS notification
	var snsNotification model.SNSNotification
	if err := json.Unmarshal(body, &snsNotification); err != nil {
		g.Log().Errorf(ctx, "Failed to parse SNS notification: %v", err)
		response.BadRequest(r, "Invalid SNS notification format")
		return
	}

	g.Log().Infof(ctx, "Received SNS notification type: %s, MessageId: %s", snsNotification.Type, snsNotification.MessageId)

	// Verify SNS signature (optional but recommended)
	if err := verifySNSSignature(&snsNotification); err != nil {
		g.Log().Warningf(ctx, "SNS signature verification failed: %v", err)
		// Continue anyway for now, but log the warning
	}

	// Handle different notification types
	switch snsNotification.Type {
	case "SubscriptionConfirmation":
		// Confirm the subscription
		g.Log().Infof(ctx, "Confirming SNS subscription: %s", snsNotification.SubscribeURL)
		if err := confirmSNSSubscription(snsNotification.SubscribeURL); err != nil {
			g.Log().Errorf(ctx, "Failed to confirm subscription: %v", err)
			response.InternalError(r, "Failed to confirm subscription")
			return
		}
		response.Success(r, map[string]string{"status": "subscription_confirmed"})
		return

	case "Notification":
		// Parse the SES notification from the message
		var sesNotification model.SESNotification
		if err := json.Unmarshal([]byte(snsNotification.Message), &sesNotification); err != nil {
			g.Log().Errorf(ctx, "Failed to parse SES notification: %v", err)
			response.BadRequest(r, "Invalid SES notification format")
			return
		}

		// Process based on notification type
		switch sesNotification.NotificationType {
		case "Received":
			// Incoming email
			if err := c.receivingService.ProcessIncomingEmail(ctx, &sesNotification); err != nil {
				g.Log().Errorf(ctx, "Failed to process incoming email: %v", err)
				// Return 200 to prevent SNS from retrying
				response.Success(r, map[string]string{"status": "error", "message": err.Error()})
				return
			}
			response.Success(r, map[string]string{"status": "processed"})

		case "Bounce":
			// Handle bounce
			g.Log().Infof(ctx, "Received bounce notification")
			c.handleBounce(ctx, &sesNotification)
			response.Success(r, map[string]string{"status": "bounce_processed"})

		case "Complaint":
			// Handle complaint
			g.Log().Infof(ctx, "Received complaint notification")
			c.handleComplaint(ctx, &sesNotification)
			response.Success(r, map[string]string{"status": "complaint_processed"})

		case "Delivery":
			// Handle delivery confirmation
			g.Log().Infof(ctx, "Received delivery notification")
			c.handleDelivery(ctx, &sesNotification)
			response.Success(r, map[string]string{"status": "delivery_processed"})

		default:
			g.Log().Infof(ctx, "Unknown SES notification type: %s", sesNotification.NotificationType)
			response.Success(r, map[string]string{"status": "unknown_type"})
		}

	case "UnsubscribeConfirmation":
		g.Log().Infof(ctx, "Received unsubscribe confirmation")
		response.Success(r, map[string]string{"status": "unsubscribe_confirmed"})

	default:
		g.Log().Warningf(ctx, "Unknown SNS notification type: %s", snsNotification.Type)
		response.Success(r, map[string]string{"status": "unknown_type"})
	}
}

// handleBounce processes bounce notifications
func (c *SESWebhookController) handleBounce(ctx g.Ctx, notification *model.SESNotification) {
	if notification.Bounce == nil {
		return
	}

	bounce := notification.Bounce
	g.Log().Infof(ctx, "Processing bounce: type=%s, subType=%s", bounce.BounceType, bounce.BounceSubType)

	// Add bounced recipients to suppression list
	for _, recipient := range bounce.BouncedRecipients {
		g.Log().Infof(ctx, "Bounced recipient: %s, action=%s, status=%s",
			recipient.EmailAddress, recipient.Action, recipient.Status)

		// TODO: Add to suppression list and update transactional emails
	}
}

// handleComplaint processes complaint notifications
func (c *SESWebhookController) handleComplaint(ctx g.Ctx, notification *model.SESNotification) {
	if notification.Complaint == nil {
		return
	}

	complaint := notification.Complaint
	g.Log().Infof(ctx, "Processing complaint: type=%s", complaint.ComplaintFeedbackType)

	// Add complained recipients to suppression list
	for _, recipient := range complaint.ComplainedRecipients {
		g.Log().Infof(ctx, "Complained recipient: %s", recipient.EmailAddress)
		// TODO: Add to suppression list
	}
}

// handleDelivery processes delivery notifications
func (c *SESWebhookController) handleDelivery(ctx g.Ctx, notification *model.SESNotification) {
	if notification.Delivery == nil {
		return
	}

	delivery := notification.Delivery
	g.Log().Infof(ctx, "Processing delivery for %d recipients, took %dms",
		len(delivery.Recipients), delivery.ProcessingTimeMillis)

	// TODO: Update transactional email status to delivered
}

// confirmSNSSubscription confirms an SNS subscription by visiting the SubscribeURL
func confirmSNSSubscription(subscribeURL string) error {
	resp, err := http.Get(subscribeURL)
	if err != nil {
		return fmt.Errorf("failed to GET subscribe URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("subscribe URL returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// verifySNSSignature verifies the SNS message signature
func verifySNSSignature(notification *model.SNSNotification) error {
	// Validate the certificate URL
	certURL, err := url.Parse(notification.SigningCertURL)
	if err != nil {
		return fmt.Errorf("invalid signing cert URL: %w", err)
	}

	// Check that the cert URL is from AWS
	if !isAWSCertURL(certURL) {
		return fmt.Errorf("signing cert URL is not from AWS: %s", certURL.Host)
	}

	// Download the certificate
	resp, err := http.Get(notification.SigningCertURL)
	if err != nil {
		return fmt.Errorf("failed to download certificate: %w", err)
	}
	defer resp.Body.Close()

	certPEM, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read certificate: %w", err)
	}

	// Parse the certificate
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to decode PEM certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Build the string to sign
	stringToSign := buildStringToSign(notification)

	// Decode the signature
	signature, err := base64.StdEncoding.DecodeString(notification.Signature)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %w", err)
	}

	// Verify the signature
	err = cert.CheckSignature(x509.SHA1WithRSA, []byte(stringToSign), signature)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}

// isAWSCertURL checks if a URL is a valid AWS SNS certificate URL
func isAWSCertURL(u *url.URL) bool {
	// AWS SNS certificate URLs are from sns.<region>.amazonaws.com
	pattern := regexp.MustCompile(`^sns\.[a-z0-9-]+\.amazonaws\.com$`)
	return u.Scheme == "https" && pattern.MatchString(u.Host)
}

// buildStringToSign builds the string to sign for SNS signature verification
func buildStringToSign(notification *model.SNSNotification) string {
	var sb strings.Builder

	sb.WriteString("Message\n")
	sb.WriteString(notification.Message)
	sb.WriteString("\n")

	sb.WriteString("MessageId\n")
	sb.WriteString(notification.MessageId)
	sb.WriteString("\n")

	if notification.Subject != "" {
		sb.WriteString("Subject\n")
		sb.WriteString(notification.Subject)
		sb.WriteString("\n")
	}

	if notification.SubscribeURL != "" {
		sb.WriteString("SubscribeURL\n")
		sb.WriteString(notification.SubscribeURL)
		sb.WriteString("\n")
	}

	if notification.Token != "" {
		sb.WriteString("Token\n")
		sb.WriteString(notification.Token)
		sb.WriteString("\n")
	}

	sb.WriteString("Timestamp\n")
	sb.WriteString(notification.Timestamp)
	sb.WriteString("\n")

	sb.WriteString("TopicArn\n")
	sb.WriteString(notification.TopicArn)
	sb.WriteString("\n")

	sb.WriteString("Type\n")
	sb.WriteString(notification.Type)
	sb.WriteString("\n")

	return sb.String()
}
