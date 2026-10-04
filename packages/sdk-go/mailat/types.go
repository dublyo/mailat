package mailat

import "time"

// SendEmailRequest represents a request to send an email.
type Attachment struct {
	Name        string `json:"name"`
	Content     string `json:"content"` // Base64 encoded bytes.
	Type        string `json:"type"`
	Disposition string `json:"disposition,omitempty"`
	CID         string `json:"cid,omitempty"`
}

type SendEmailRequest struct {
	Attachments    []Attachment      `json:"attachments,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	From           string            `json:"from"`
	To             []string          `json:"to"`
	Subject        string            `json:"subject"`
	HTML           string            `json:"html,omitempty"`
	Text           string            `json:"text,omitempty"`
	CC             []string          `json:"cc,omitempty"`
	BCC            []string          `json:"bcc,omitempty"`
	ReplyTo        string            `json:"replyTo,omitempty"`
	TemplateID     string            `json:"templateId,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ScheduledFor   *time.Time        `json:"scheduledFor,omitempty"`
}

// SendEmailResponse is returned after sending an email.
type SendEmailResponse struct {
	MessageID   string    `json:"messageId"`
	AcceptedAt  time.Time `json:"acceptedAt"`
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	ScheduledAt string    `json:"scheduledAt,omitempty"`
}

// BatchSendRequest contains multiple emails to send.
type BatchSendRequest struct {
	Emails []SendEmailRequest `json:"emails"`
}

// BatchEmailResult is the result for a single email in a batch.
type BatchEmailResult struct {
	MessageID string `json:"messageId,omitempty"`
	Error     string `json:"error,omitempty"`
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

// BatchSendResponse is returned after sending a batch of emails.
type BatchSendResponse struct {
	Sent    int                `json:"sent"`
	Failed  int                `json:"failed"`
	Results []BatchEmailResult `json:"results"`
}

// DeliveryEvent represents an email delivery event.
type DeliveryEvent struct {
	ID        int       `json:"id"`
	EmailID   int       `json:"emailId"`
	EventType string    `json:"eventType"`
	Event     string    `json:"event"`
	Timestamp time.Time `json:"timestamp"`
	Details   string    `json:"details,omitempty"`
}

// EmailStatusResponse contains the status and events for an email.
type EmailStatusResponse struct {
	MessageID   string          `json:"messageId"`
	CreatedAt   time.Time       `json:"createdAt"`
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	From        string          `json:"from"`
	To          []string        `json:"to"`
	Subject     string          `json:"subject"`
	SentAt      *time.Time      `json:"sentAt,omitempty"`
	DeliveredAt *time.Time      `json:"deliveredAt,omitempty"`
	OpenedAt    *time.Time      `json:"openedAt,omitempty"`
	Events      []DeliveryEvent `json:"events"`
}

// Template represents an email template.
type Template struct {
	UUID        string    `json:"uuid"`
	Variables   []string  `json:"variables"`
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Subject     string    `json:"subject"`
	HTML        string    `json:"htmlBody"`
	Text        string    `json:"textBody,omitempty"`
	Description string    `json:"description,omitempty"`
	IsActive    bool      `json:"isActive"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateTemplateRequest is the request to create a template.
type CreateTemplateRequest struct {
	Name        string `json:"name"`
	Subject     string `json:"subject"`
	HTML        string `json:"html"`
	Text        string `json:"text,omitempty"`
	Description string `json:"description,omitempty"`
}

// UpdateTemplateRequest is the request to update a template.
type UpdateTemplateRequest struct {
	Name        *string `json:"name,omitempty"`
	Subject     *string `json:"subject,omitempty"`
	HTML        *string `json:"html,omitempty"`
	Text        *string `json:"text,omitempty"`
	Description *string `json:"description,omitempty"`
	IsActive    *bool   `json:"isActive,omitempty"`
}

// PreviewTemplateRequest is the request to preview a template.
type PreviewTemplateRequest struct {
	Variables map[string]string `json:"variables,omitempty"`
}

// PreviewTemplateResponse contains rendered template content.
type PreviewTemplateResponse struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text,omitempty"`
}

// Webhook represents a webhook endpoint.
type Webhook struct {
	UUID         string    `json:"uuid"`
	SuccessCount int       `json:"successCount"`
	FailureCount int       `json:"failureCount"`
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	Events       []string  `json:"events"`
	Secret       string    `json:"secret,omitempty"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CreateWebhookRequest is the request to create a webhook.
type CreateWebhookRequest struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// UpdateWebhookRequest is the request to update a webhook.
type UpdateWebhookRequest struct {
	Name   *string   `json:"name,omitempty"`
	URL    *string   `json:"url,omitempty"`
	Events *[]string `json:"events,omitempty"`
	Active *bool     `json:"active,omitempty"`
}

// RotateSecretResponse contains the new webhook secret.
type RotateSecretResponse struct {
	Secret string `json:"secret"`
}

// WebhookCall represents a webhook delivery attempt.
type WebhookCall struct {
	ID             int                    `json:"id"`
	EventType      string                 `json:"eventType"`
	Payload        map[string]interface{} `json:"payload"`
	ResponseStatus int                    `json:"responseStatus"`
	ResponseBody   string                 `json:"responseBody"`
	ResponseTimeMS int                    `json:"responseTimeMs"`
	Status         string                 `json:"status"`
	Attempts       int                    `json:"attempts"`
	Error          string                 `json:"error,omitempty"`
	CreatedAt      time.Time              `json:"createdAt"`
	CompletedAt    *time.Time             `json:"completedAt"`
}

type WebhookPayload struct {
	Version   string                 `json:"version"`
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	CreatedAt time.Time              `json:"createdAt"`
	Data      map[string]interface{} `json:"data"`
}

type WebhookTestResult struct {
	EventID    string `json:"eventId"`
	DeliveryID string `json:"deliveryId"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus"`
	Error      string `json:"error"`
}

// FolderDMARCReports is a built-in mailbox folder, independent of user labels.
const FolderDMARCReports = "dmarc-reports"

// InboxCounts keeps Inbox and report unread badges separate from global Unread.
type InboxCounts struct {
	Inbox              int            `json:"inbox"`
	InboxUnread        int            `json:"inboxUnread"`
	DMARCReports       int            `json:"dmarcReports"`
	DMARCReportsUnread int            `json:"dmarcReportsUnread"`
	Unread             int            `json:"unread"`
	Starred            int            `json:"starred"`
	Sent               int            `json:"sent"`
	Drafts             int            `json:"drafts"`
	Spam               int            `json:"spam"`
	Trash              int            `json:"trash"`
	Labels             map[string]int `json:"labels,omitempty"`
}

// DMARCReportsSettings describes the human-session-only account preference.
type DMARCReportsSettings struct {
	AutoOrganizeDMARCReports bool `json:"autoOrganizeDmarcReports"`
}

// A nil preference is omitted; a pointer to false explicitly opts out.
type UpdateDMARCReportsSettings struct {
	AutoOrganizeDMARCReports *bool `json:"autoOrganizeDmarcReports,omitempty"`
}
