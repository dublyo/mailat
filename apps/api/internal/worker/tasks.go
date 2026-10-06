package worker

import (
	"encoding/json"
	"time"
)

// Task type constants
const (
	TypeEmailSend        = "email:send"
	TypeEmailBatch       = "email:batch"
	TypeWebhookDeliver   = "webhook:deliver"
	TypeBounceProcess    = "bounce:process"
	TypeSuppressionCheck = "suppression:check"
)

// EmailSendPayload contains the data needed to send an email
type EmailSendPayload struct {
	MessageUUID string            `json:"messageUuid,omitempty"`
	UserID      int64             `json:"userId,omitempty"`
	IdentityID  int64             `json:"identityId,omitempty"`
	EmailID     int64             `json:"emailId"`
	OrgID       int64             `json:"orgId"`
	From        string            `json:"from"`
	To          []string          `json:"to"`
	Cc          []string          `json:"cc,omitempty"`
	Bcc         []string          `json:"bcc,omitempty"`
	ReplyTo     string            `json:"replyTo,omitempty"`
	Subject     string            `json:"subject"`
	HTMLBody    string            `json:"htmlBody,omitempty"`
	TextBody    string            `json:"textBody,omitempty"`
	MessageID   string            `json:"messageId"`
	Attachments []AttachmentInfo  `json:"attachments,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Headers pass through provider.AllowedMailHeader; others are dropped.
	Headers        map[string]string `json:"headers,omitempty"`
	RetryCount     int               `json:"retryCount"`
	MaxRetries     int               `json:"maxRetries"`
	ScheduledFor   *time.Time        `json:"scheduledFor,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
}

// AttachmentInfo contains attachment metadata for sending
type AttachmentInfo struct {
	Data        []byte `json:"data,omitempty"`
	BlobID      string `json:"blobId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int    `json:"size"`
	Disposition string `json:"disposition"`
	CID         string `json:"cid,omitempty"`
	// S3Bucket/S3Key reference stored bytes when Data is empty; the worker
	// loads them at send time so durable payloads stay small.
	S3Bucket string `json:"s3Bucket,omitempty"`
	S3Key    string `json:"s3Key,omitempty"`
}

// EmailBatchPayload contains data for batch email sending
type EmailBatchPayload struct {
	OrgID   int64              `json:"orgId"`
	Emails  []EmailSendPayload `json:"emails"`
	BatchID string             `json:"batchId"`
}

// WebhookDeliverPayload contains data for webhook delivery
type WebhookDeliverPayload struct {
	WebhookID  int64          `json:"webhookId"`
	OrgID      int64          `json:"orgId"`
	URL        string         `json:"url"`
	Secret     string         `json:"secret"`
	EventType  string         `json:"eventType"`
	EmailID    int64          `json:"emailId"`
	Payload    map[string]any `json:"payload"`
	RetryCount int            `json:"retryCount"`
	MaxRetries int            `json:"maxRetries"`
}

// BounceProcessPayload contains data for bounce processing
type BounceProcessPayload struct {
	EmailID      int64  `json:"emailId"`
	OrgID        int64  `json:"orgId"`
	BounceType   string `json:"bounceType"` // hard, soft
	BounceReason string `json:"bounceReason"`
	Recipient    string `json:"recipient"`
}

// SuppressionCheckPayload contains data for suppression list updates
type SuppressionCheckPayload struct {
	OrgID  int64  `json:"orgId"`
	Email  string `json:"email"`
	Reason string `json:"reason"`
	Source string `json:"source"` // bounce, complaint, manual
}

// NewEmailSendPayload creates a new email send task payload
func NewEmailSendPayload(emailID, orgID int64, from string, to []string, subject, htmlBody, textBody, messageID string) *EmailSendPayload {
	return &EmailSendPayload{
		EmailID:    emailID,
		OrgID:      orgID,
		From:       from,
		To:         to,
		Subject:    subject,
		HTMLBody:   htmlBody,
		TextBody:   textBody,
		MessageID:  messageID,
		MaxRetries: 3,
	}
}

// Marshal serializes the payload to JSON
func (p *EmailSendPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalEmailSendPayload deserializes JSON to EmailSendPayload
func UnmarshalEmailSendPayload(data []byte) (*EmailSendPayload, error) {
	var p EmailSendPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Marshal serializes the payload to JSON
func (p *WebhookDeliverPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalWebhookDeliverPayload deserializes JSON to WebhookDeliverPayload
func UnmarshalWebhookDeliverPayload(data []byte) (*WebhookDeliverPayload, error) {
	var p WebhookDeliverPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Marshal serializes the payload to JSON
func (p *BounceProcessPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalBounceProcessPayload deserializes JSON to BounceProcessPayload
func UnmarshalBounceProcessPayload(data []byte) (*BounceProcessPayload, error) {
	var p BounceProcessPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
