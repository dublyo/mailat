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
	TypeCampaignProcess  = "campaign:process"
	TypeCampaignBatch    = "campaign:batch"
)

// EmailSendPayload contains the data needed to send an email
type EmailSendPayload struct {
	EmailID        int64             `json:"emailId"`
	OrgID          int64             `json:"orgId"`
	From           string            `json:"from"`
	To             []string          `json:"to"`
	Cc             []string          `json:"cc,omitempty"`
	Bcc            []string          `json:"bcc,omitempty"`
	ReplyTo        string            `json:"replyTo,omitempty"`
	Subject        string            `json:"subject"`
	HTMLBody       string            `json:"htmlBody,omitempty"`
	TextBody       string            `json:"textBody,omitempty"`
	MessageID      string            `json:"messageId"`
	Attachments    []AttachmentInfo  `json:"attachments,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	RetryCount     int               `json:"retryCount"`
	MaxRetries     int               `json:"maxRetries"`
	ScheduledFor   *time.Time        `json:"scheduledFor,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
}

// AttachmentInfo contains attachment metadata for sending
type AttachmentInfo struct {
	BlobID      string `json:"blobId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int    `json:"size"`
	Disposition string `json:"disposition"`
	CID         string `json:"cid,omitempty"`
}

// EmailBatchPayload contains data for batch email sending
type EmailBatchPayload struct {
	OrgID   int64              `json:"orgId"`
	Emails  []EmailSendPayload `json:"emails"`
	BatchID string             `json:"batchId"`
}

// WebhookDeliverPayload contains data for webhook delivery
type WebhookDeliverPayload struct {
	WebhookID   int64             `json:"webhookId"`
	OrgID       int64             `json:"orgId"`
	URL         string            `json:"url"`
	Secret      string            `json:"secret"`
	EventType   string            `json:"eventType"`
	EmailID     int64             `json:"emailId"`
	Payload     map[string]any    `json:"payload"`
	RetryCount  int               `json:"retryCount"`
	MaxRetries  int               `json:"maxRetries"`
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
	OrgID     int64  `json:"orgId"`
	Email     string `json:"email"`
	Reason    string `json:"reason"`
	Source    string `json:"source"` // bounce, complaint, manual
}

// CampaignProcessPayload contains data for campaign processing
type CampaignProcessPayload struct {
	CampaignID int   `json:"campaignId"`
	OrgID      int64 `json:"orgId"`
}

// CampaignBatchPayload contains data for a batch of campaign emails
type CampaignBatchPayload struct {
	CampaignID  int      `json:"campaignId"`
	OrgID       int64    `json:"orgId"`
	ContactIDs  []int64  `json:"contactIds"`
	BatchNumber int      `json:"batchNumber"`
	TotalBatches int     `json:"totalBatches"`
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

// Marshal serializes the payload to JSON
func (p *CampaignProcessPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalCampaignProcessPayload deserializes JSON to CampaignProcessPayload
func UnmarshalCampaignProcessPayload(data []byte) (*CampaignProcessPayload, error) {
	var p CampaignProcessPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Marshal serializes the payload to JSON
func (p *CampaignBatchPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalCampaignBatchPayload deserializes JSON to CampaignBatchPayload
func UnmarshalCampaignBatchPayload(data []byte) (*CampaignBatchPayload, error) {
	var p CampaignBatchPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
