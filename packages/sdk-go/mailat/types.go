package mailat

import "time"

// SendEmailRequest represents a request to send an email.
type SendEmailRequest struct {
	From         string            `json:"from"`
	To           []string          `json:"to"`
	Subject      string            `json:"subject"`
	HTML         string            `json:"html,omitempty"`
	Text         string            `json:"text,omitempty"`
	CC           []string          `json:"cc,omitempty"`
	BCC          []string          `json:"bcc,omitempty"`
	ReplyTo      string            `json:"replyTo,omitempty"`
	TemplateID   string            `json:"templateId,omitempty"`
	Variables    map[string]string `json:"variables,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	ScheduledFor *time.Time        `json:"scheduledFor,omitempty"`
}

// SendEmailResponse is returned after sending an email.
type SendEmailResponse struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ScheduledAt string `json:"scheduledAt,omitempty"`
}

// BatchSendRequest contains multiple emails to send.
type BatchSendRequest struct {
	Emails []SendEmailRequest `json:"emails"`
}

// BatchEmailResult is the result for a single email in a batch.
type BatchEmailResult struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// BatchSendResponse is returned after sending a batch of emails.
type BatchSendResponse struct {
	Sent   int                `json:"sent"`
	Failed int                `json:"failed"`
	Results []BatchEmailResult `json:"results"`
}

// DeliveryEvent represents an email delivery event.
type DeliveryEvent struct {
	Event     string    `json:"event"`
	Timestamp time.Time `json:"timestamp"`
	Details   string    `json:"details,omitempty"`
}

// EmailStatusResponse contains the status and events for an email.
type EmailStatusResponse struct {
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
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Subject     string    `json:"subject"`
	HTML        string    `json:"html"`
	Text        string    `json:"text,omitempty"`
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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Secret    string    `json:"secret,omitempty"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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
	ID           string    `json:"id"`
	Event        string    `json:"event"`
	URL          string    `json:"url"`
	StatusCode   int       `json:"statusCode"`
	Success      bool      `json:"success"`
	Payload      string    `json:"payload"`
	Response     string    `json:"response,omitempty"`
	Attempts     int       `json:"attempts"`
	LastAttempt  time.Time `json:"lastAttempt"`
	NextRetry    *time.Time `json:"nextRetry,omitempty"`
}

// WebhookPayload is the payload received in webhook callbacks.
type WebhookPayload struct {
	ID        string                 `json:"id"`
	Event     string                 `json:"event"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}
