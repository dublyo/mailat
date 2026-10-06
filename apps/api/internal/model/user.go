package model

import (
	"time"
)

// User represents a user in the system
type User struct {
	ID            int64      `json:"id"`
	UUID          string     `json:"uuid"`
	OrgID         int64      `json:"orgId"`
	Email         string     `json:"email"`
	PasswordHash  string     `json:"-"`
	Name          string     `json:"name"`
	Role          string     `json:"role"` // owner, admin, member
	Status        string     `json:"status"`
	EmailVerified bool       `json:"emailVerified"`
	LastLoginAt   *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Organization represents an organization/tenant
type Organization struct {
	ID           int64     `json:"id"`
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	PlanType     string    `json:"planType"`
	MonthlyQuota int       `json:"monthlyQuota"`
	DailyQuota   int       `json:"dailyQuota"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ApiKey represents an API key for programmatic access
type ApiKey struct {
	ID          int64      `json:"id"`
	UUID        string     `json:"uuid"`
	OrgID       int64      `json:"orgId"`
	Name        string     `json:"name"`
	KeyPrefix   string     `json:"keyPrefix"`
	KeyHash     string     `json:"-"`
	Permissions []string   `json:"permissions"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// Domain represents an email domain
type Domain struct {
	ID                int64  `json:"id"`
	UUID              string `json:"uuid"`
	OrgID             int64  `json:"orgId"`
	Name              string `json:"name"`
	Status            string `json:"status"` // pending, active, suspended
	VerificationToken string `json:"verificationToken,omitempty"`
	DKIMSelector      string `json:"dkimSelector"`
	DKIMPublicKey     string `json:"dkimPublicKey,omitempty"`
	DKIMPrivateKey    string `json:"-"`
	// SES Integration fields
	EmailProvider  string   `json:"emailProvider"`            // ses, smtp
	SESVerified    bool     `json:"sesVerified"`              // SES domain verification status
	SESDKIMTokens  []string `json:"sesDkimTokens,omitempty"`  // SES Easy DKIM tokens
	SESIdentityArn string   `json:"sesIdentityArn,omitempty"` // SES identity ARN
	// Verification flags
	MXVerified    bool `json:"mxVerified"`
	SPFVerified   bool `json:"spfVerified"`
	DKIMVerified  bool `json:"dkimVerified"`
	DMARCVerified bool `json:"dmarcVerified"`
	// Email receiving configuration
	ReceivingEnabled     bool       `json:"receivingEnabled"`
	ReceivingS3Bucket    string     `json:"receivingS3Bucket,omitempty"`
	ReceivingSnsTopicArn string     `json:"receivingSnsTopicArn,omitempty"`
	ReceivingRuleSetName string     `json:"receivingRuleSetName,omitempty"`
	ReceivingRuleName    string     `json:"receivingRuleName,omitempty"`
	ReceivingSetupAt     *time.Time `json:"receivingSetupAt,omitempty"`
	// Timestamps
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// DomainDNSRecord represents a DNS record for a domain
type DomainDNSRecord struct {
	ID         int64      `json:"id"`
	DomainID   int64      `json:"domainId"`
	RecordType string     `json:"recordType"` // MX, TXT, CNAME
	Hostname   string     `json:"hostname"`
	Value      string     `json:"value"`
	Priority   *int       `json:"priority,omitempty"`
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// Identity represents an email identity/mailbox
type Identity struct {
	ID                int64     `json:"id"`
	UUID              string    `json:"uuid"`
	UserID            int64     `json:"userId"`
	DomainID          int64     `json:"domainId"`
	Email             string    `json:"email"`
	DisplayName       string    `json:"displayName"`
	IsDefault         bool      `json:"isDefault"`
	IsCatchAll        bool      `json:"isCatchAll"`
	Color             string    `json:"color"` // Hex color for UI display
	CanSend           bool      `json:"canSend"`
	CanReceive        bool      `json:"canReceive"`
	StalwartAcctID    string    `json:"stalwartAcctId,omitempty"`
	PasswordHash      string    `json:"-"`
	EncryptedPassword string    `json:"-"` // AES-encrypted password for JMAP auth
	QuotaBytes        int64     `json:"quotaBytes"`
	UsedBytes         int64     `json:"usedBytes"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// JWT Claims
type JWTClaims struct {
	UserID int64  `json:"userId"`
	OrgID  int64  `json:"orgId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// Request/Response DTOs

type RegisterRequest struct {
	Email    string `json:"email" v:"required|email"`
	Password string `json:"password" v:"required|min-length:8"`
	Name     string `json:"name" v:"required|min-length:2"`
	OrgName  string `json:"orgName"`
}

type LoginRequest struct {
	Email    string `json:"email" v:"required|email"`
	Password string `json:"password" v:"required"`
}

type AuthResponse struct {
	Token string `json:"token"`
	User  *User  `json:"user"`
}

type CreateDomainRequest struct {
	Name string `json:"name" v:"required|domain"`
}

type CreateIdentityRequest struct {
	DomainId    string `json:"domainId"` // UUID of the domain
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
	QuotaBytes  int64  `json:"quotaBytes"`
	IsDefault   bool   `json:"isDefault"`
	IsCatchAll  bool   `json:"isCatchAll"` // Only one catch-all per domain allowed
}

type UpdateIdentityRequest struct {
	DisplayName *string `json:"displayName"`
	IsDefault   *bool   `json:"isDefault"`
	IsCatchAll  *bool   `json:"isCatchAll"`
	Color       *string `json:"color"`
}

type CreateApiKeyRequest struct {
	Name        string   `json:"name" v:"required|min-length:2"`
	Permissions []string `json:"permissions"`
	RateLimit   int      `json:"rateLimit"` // Requests per minute (default: 100)
	ExpiresAt   *string  `json:"expiresAt"` // RFC3339 format or null for no expiry
}

type ApiKeyResponse struct {
	ID          int64      `json:"id"`
	UUID        string     `json:"uuid"`
	Name        string     `json:"name"`
	Key         string     `json:"key,omitempty"` // Only shown on creation
	KeyPrefix   string     `json:"keyPrefix"`
	Permissions []string   `json:"permissions"`
	RateLimit   int        `json:"rateLimit"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// Unified Inbox Request/Response DTOs

type UnifiedInboxRequest struct {
	Page       int    `json:"page" d:"1"`
	PageSize   int    `json:"pageSize" d:"50"`
	MailboxID  string `json:"mailboxId"`
	IdentityID int64  `json:"identityId"`
	Search     string `json:"search"`
	Unread     bool   `json:"unread"`
	Flagged    bool   `json:"flagged"`
}

type EmailActionRequest struct {
	EmailIDs []string `json:"emailIds" v:"required"`
}

type MarkReadRequest struct {
	EmailIDs []string `json:"emailIds" v:"required"`
	Read     bool     `json:"read"`
}

type FlagEmailRequest struct {
	EmailIDs []string `json:"emailIds" v:"required"`
	Flagged  bool     `json:"flagged"`
}

type MoveEmailRequest struct {
	EmailIDs        []string `json:"emailIds" v:"required"`
	TargetMailboxID string   `json:"targetMailboxId" v:"required"`
}

type DeleteEmailRequest struct {
	EmailIDs  []string `json:"emailIds" v:"required"`
	Permanent bool     `json:"permanent"`
}

// Compose Request/Response DTOs

type ComposeEmailRequest struct {
	FromEmail    string            `json:"fromEmail"`
	DraftID      string            `json:"draftId"`
	DraftVersion int               `json:"draftVersion"`
	IdentityID   int64             `json:"identityId" v:"required"`
	To           []EmailAddressDTO `json:"to"`
	Cc           []EmailAddressDTO `json:"cc"`
	Bcc          []EmailAddressDTO `json:"bcc"`
	ReplyTo      []EmailAddressDTO `json:"replyTo"`
	Subject      string            `json:"subject" v:"required"`
	TextBody     string            `json:"textBody"`
	HTMLBody     string            `json:"htmlBody"`
	InReplyTo    string            `json:"inReplyTo"`
	References   []string          `json:"references"`
	Attachments  []AttachmentDTO   `json:"attachments"`
}

type EmailAddressDTO struct {
	Name  string `json:"name"`
	Email string `json:"email" v:"required|email"`
}

type AttachmentDTO struct {
	BlobID      string `json:"blobId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Content     string `json:"content"`
	Size        int    `json:"size"`
	Disposition string `json:"disposition"`
	CID         string `json:"cid"`
}

type SaveDraftRequest struct {
	ReplyTo     []EmailAddressDTO `json:"replyTo"`
	FromEmail   string            `json:"fromEmail"`
	Version     int               `json:"version"`
	IdentityID  int64             `json:"identityId" v:"required"`
	To          []EmailAddressDTO `json:"to"`
	Cc          []EmailAddressDTO `json:"cc"`
	Bcc         []EmailAddressDTO `json:"bcc"`
	Subject     string            `json:"subject"`
	TextBody    string            `json:"textBody"`
	HTMLBody    string            `json:"htmlBody"`
	InReplyTo   string            `json:"inReplyTo"`
	References  []string          `json:"references"`
	Attachments []AttachmentDTO   `json:"attachments"`
}

type ReplyContextRequest struct {
	EmailID  string `json:"emailId" v:"required"`
	ReplyAll bool   `json:"replyAll"`
}

type ForwardContextRequest struct {
	EmailID string `json:"emailId" v:"required"`
}

// Transactional Email API Models (Phase 2)

// TransactionalEmail represents an outbound transactional email
type TransactionalEmail struct {
	ID             int64             `json:"id"`
	UUID           string            `json:"uuid"`
	OrgID          int64             `json:"orgId"`
	IdentityID     int64             `json:"identityId"`
	MessageID      string            `json:"messageId"` // RFC 5322 Message-ID
	From           string            `json:"from"`
	To             []string          `json:"to"`
	Cc             []string          `json:"cc,omitempty"`
	Bcc            []string          `json:"bcc,omitempty"`
	ReplyTo        string            `json:"replyTo,omitempty"`
	Subject        string            `json:"subject"`
	HTMLBody       string            `json:"htmlBody,omitempty"`
	TextBody       string            `json:"textBody,omitempty"`
	TemplateID     *int64            `json:"templateId,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Status         string            `json:"status"` // queued, sending, sent, delivered, bounced, failed
	ScheduledFor   *time.Time        `json:"scheduledFor,omitempty"`
	SentAt         *time.Time        `json:"sentAt,omitempty"`
	DeliveredAt    *time.Time        `json:"deliveredAt,omitempty"`
	OpenedAt       *time.Time        `json:"openedAt,omitempty"`
	ClickedAt      *time.Time        `json:"clickedAt,omitempty"`
	BouncedAt      *time.Time        `json:"bouncedAt,omitempty"`
	BounceType     string            `json:"bounceType,omitempty"` // hard, soft
	BounceReason   string            `json:"bounceReason,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// EmailTemplate represents a reusable email template
type EmailTemplate struct {
	ID          int64     `json:"id"`
	UUID        string    `json:"uuid"`
	OrgID       int64     `json:"orgId"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Subject     string    `json:"subject"`
	HTMLBody    string    `json:"htmlBody"`
	TextBody    string    `json:"textBody,omitempty"`
	Variables   []string  `json:"variables"` // List of variable names
	IsActive    bool      `json:"isActive"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// DeliveryEvent represents a delivery event for tracking
type DeliveryEvent struct {
	ID        int64     `json:"id"`
	EmailID   int64     `json:"emailId"`
	EventType string    `json:"eventType"` // queued, sent, delivered, opened, clicked, bounced, complained
	Timestamp time.Time `json:"timestamp"`
	Details   string    `json:"details,omitempty"`
	IPAddress string    `json:"ipAddress,omitempty"`
	UserAgent string    `json:"userAgent,omitempty"`
}

// Webhook represents a webhook endpoint
type Webhook struct {
	ID        int64     `json:"id"`
	UUID      string    `json:"uuid"`
	OrgID     int64     `json:"orgId"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"` // sent, delivered, bounced, complained, opened, clicked
	Secret    string    `json:"-"`
	IsActive  bool      `json:"isActive"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Transactional API Request DTOs

type SendEmailRequest struct {
	From           string            `json:"from" v:"required|email"`
	To             []string          `json:"to"`
	Cc             []string          `json:"cc"`
	Bcc            []string          `json:"bcc"`
	ReplyTo        string            `json:"replyTo"`
	Subject        string            `json:"subject" v:"required"`
	HTML           string            `json:"html"`
	Text           string            `json:"text"`
	TemplateID     string            `json:"templateId"`
	Variables      map[string]string `json:"variables"`
	Attachments    []AttachmentDTO   `json:"attachments"`
	Tags           []string          `json:"tags"`
	Metadata       map[string]string `json:"metadata"`
	ScheduledFor   *string           `json:"scheduledFor"`             // RFC3339 timestamp
	IdempotencyKey string            `json:"idempotencyKey,omitempty"` // Header or per-item batch key
}

type SendEmailResponse struct {
	ID         string    `json:"id"`
	MessageID  string    `json:"messageId"`
	Status     string    `json:"status"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

type BatchSendRequest struct {
	IdempotencyKey string             `json:"-"` // Whole-batch key from the Idempotency-Key header.
	Emails         []SendEmailRequest `json:"emails" v:"required"`
}

type BatchSendResponse struct {
	Results []BatchEmailResult `json:"results"`
}

type BatchEmailResult struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	MessageID string `json:"messageId,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type GetEmailStatusResponse struct {
	ID          string          `json:"id"`
	MessageID   string          `json:"messageId"`
	From        string          `json:"from"`
	To          []string        `json:"to"`
	Subject     string          `json:"subject"`
	Status      string          `json:"status"`
	Events      []DeliveryEvent `json:"events"`
	CreatedAt   time.Time       `json:"createdAt"`
	SentAt      *time.Time      `json:"sentAt,omitempty"`
	DeliveredAt *time.Time      `json:"deliveredAt,omitempty"`
}

// Template API Request DTOs

type CreateTemplateRequest struct {
	Name        string `json:"name" v:"required|min-length:2"`
	Description string `json:"description"`
	Subject     string `json:"subject" v:"required"`
	HTML        string `json:"html" v:"required"`
	Text        string `json:"text"`
}

type UpdateTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Subject     string `json:"subject"`
	HTML        string `json:"html"`
	Text        string `json:"text"`
	IsActive    *bool  `json:"isActive"`
}

type PreviewTemplateRequest struct {
	Variables map[string]string `json:"variables"`
}

type PreviewTemplateResponse struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
}

// Webhook API Request/Response DTOs

type CreateWebhookRequest struct {
	Name   string   `json:"name" v:"required|min-length:2"`
	URL    string   `json:"url" v:"required|url"`
	Events []string `json:"events" v:"required"`
}

type UpdateWebhookRequest struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active *bool    `json:"active"`
}

type WebhookResponse struct {
	ID              int64      `json:"id"`
	UUID            string     `json:"uuid"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	Events          []string   `json:"events"`
	Active          bool       `json:"active"`
	Secret          string     `json:"secret,omitempty"` // Only returned on creation
	SuccessCount    int        `json:"successCount"`
	FailureCount    int        `json:"failureCount"`
	LastTriggeredAt *time.Time `json:"lastTriggeredAt,omitempty"`
	LastSuccessAt   *time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt   *time.Time `json:"lastFailureAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type WebhookCallResponse struct {
	ID             int64                  `json:"id"`
	EventType      string                 `json:"eventType"`
	Payload        map[string]interface{} `json:"payload"`
	ResponseStatus int                    `json:"responseStatus,omitempty"`
	ResponseBody   string                 `json:"responseBody,omitempty"`
	ResponseTimeMs int                    `json:"responseTimeMs,omitempty"`
	Status         string                 `json:"status"`
	Attempts       int                    `json:"attempts"`
	Error          string                 `json:"error,omitempty"`
	CreatedAt      time.Time              `json:"createdAt"`
	CompletedAt    *time.Time             `json:"completedAt,omitempty"`
}

type RotateSecretResponse struct {
	Secret string `json:"secret"`
}

// ===================
// PHASE 3: MARKETING - CONTACTS & LISTS
// ===================

// Contact represents a marketing contact
type Contact struct {
	ID               int64            `json:"id"`
	UUID             string           `json:"uuid"`
	OrgID            int64            `json:"orgId"`
	Email            string           `json:"email"`
	FirstName        string           `json:"firstName,omitempty"`
	LastName         string           `json:"lastName,omitempty"`
	Attributes       map[string]any   `json:"attributes,omitempty"`
	Status           string           `json:"status"` // active, unsubscribed, bounced, complained
	ConsentSource    string           `json:"consentSource,omitempty"`
	ConsentTimestamp *time.Time       `json:"consentTimestamp,omitempty"`
	ConsentIP        string           `json:"consentIp,omitempty"`
	LastEngagedAt    *time.Time       `json:"lastEngagedAt,omitempty"`
	EngagementScore  float64          `json:"engagementScore"`
	CreatedAt        time.Time        `json:"createdAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
	Lists            []ListMembership `json:"lists,omitempty"`
}

// ListMembership represents a contact's membership in a list
type ListMembership struct {
	ListID   int       `json:"listId"`
	ListName string    `json:"listName"`
	JoinedAt time.Time `json:"joinedAt"`
}

// List represents a contact list
type List struct {
	ConfirmationMode string    `json:"confirmationMode"`
	ID               int       `json:"id"`
	UUID             string    `json:"uuid"`
	OrgID            int64     `json:"orgId"`
	Name             string    `json:"name"`
	Description      string    `json:"description,omitempty"`
	Type             string    `json:"type"` // static, dynamic
	SegmentRules     any       `json:"segmentRules,omitempty"`
	ContactCount     int       `json:"contactCount"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Contact API Request DTOs

type CreateContactRequest struct {
	Email            string         `json:"email" v:"required|email"`
	FirstName        string         `json:"firstName"`
	LastName         string         `json:"lastName"`
	Attributes       map[string]any `json:"attributes"`
	ListIDs          []int          `json:"listIds"`
	ConsentSource    string         `json:"consentSource"`
	SkipConfirmation bool           `json:"skipConfirmation"` // For double opt-in
}

type UpdateContactRequest struct {
	Email      string         `json:"email"`
	FirstName  string         `json:"firstName"`
	LastName   string         `json:"lastName"`
	Attributes map[string]any `json:"attributes"`
	Status     string         `json:"status"`
}

type ContactListResponse struct {
	Contacts   []Contact `json:"contacts"`
	Total      int       `json:"total"`
	Page       int       `json:"page"`
	PageSize   int       `json:"pageSize"`
	TotalPages int       `json:"totalPages"`
}

type ImportContactsRequest struct {
	Contacts         []ImportContactRow `json:"contacts" v:"required"`
	ListIDs          []int              `json:"listIds"`
	UpdateExisting   bool               `json:"updateExisting"`
	ConsentSource    string             `json:"consentSource"`
	SkipConfirmation bool               `json:"skipConfirmation"`
}

// ImportContactRow is validated per row by the service (not by request
// validation) so one bad row is reported instead of rejecting the whole batch.
type ImportContactRow struct {
	Email      string         `json:"email"`
	FirstName  string         `json:"firstName"`
	LastName   string         `json:"lastName"`
	Attributes map[string]any `json:"attributes"`
}

type ImportContactsResponse struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
	// Suppressed counts rows not added to lists because the address is
	// suppressed or the existing contact is no longer active.
	Suppressed int      `json:"suppressed"`
	Errors     []string `json:"errors,omitempty"`
}

type ExportContactsRequest struct {
	ListIDs []int    `json:"listIds"`
	Status  []string `json:"status"` // active, unsubscribed, etc.
	Format  string   `json:"format"` // csv, json
}

type ContactSearchRequest struct {
	Query     string   `json:"query"`
	Status    []string `json:"status"`
	ListIDs   []int    `json:"listIds"`
	Page      int      `json:"page" d:"1"`
	PageSize  int      `json:"pageSize" d:"50"`
	SortBy    string   `json:"sortBy" d:"createdAt"`
	SortOrder string   `json:"sortOrder" d:"desc"`
}

// List API Request DTOs

type CreateListRequest struct {
	ConfirmationMode string `json:"confirmationMode"`
	Name             string `json:"name" v:"required|min-length:2"`
	Description      string `json:"description"`
	Type             string `json:"type" d:"static"` // static, dynamic
	SegmentRules     any    `json:"segmentRules"`
}

type UpdateListRequest struct {
	ConfirmationMode string `json:"confirmationMode"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	SegmentRules     any    `json:"segmentRules"`
}

type AddContactsToListRequest struct {
	ContactUUIDs []string `json:"contactIds" v:"required"`
}

type RemoveContactsFromListRequest struct {
	ContactUUIDs []string `json:"contactIds" v:"required"`
}

// ImportContactsToListRequest for bulk importing contacts directly to a list
type ImportContactsToListRequest struct {
	Contacts       []ImportContactRow `json:"contacts" v:"required"`
	UpdateExisting bool               `json:"updateExisting"`
	ConsentSource  string             `json:"consentSource"`
}

// ImportContactsToListResponse returns the result of importing contacts to a list
type ImportContactsToListResponse struct {
	Imported   int      `json:"imported"`
	Updated    int      `json:"updated"`
	Skipped    int      `json:"skipped"`
	Suppressed int      `json:"suppressed"`
	Errors     []string `json:"errors,omitempty"`
}

// ManualAddContactToListRequest for adding a single contact manually to a list
type ManualAddContactToListRequest struct {
	Email      string                 `json:"email" v:"required|email"`
	FirstName  string                 `json:"firstName"`
	LastName   string                 `json:"lastName"`
	Attributes map[string]interface{} `json:"attributes"`
}

// ===================
// PHASE 3: CAMPAIGNS
// ===================

// Campaign represents an email campaign
type Campaign struct {
	ID               int        `json:"id"`
	UUID             string     `json:"uuid"`
	OrgID            int64      `json:"orgId"`
	Name             string     `json:"name"`
	Subject          string     `json:"subject"`
	HTMLContent      string     `json:"htmlContent,omitempty"`
	TextContent      string     `json:"textContent,omitempty"`
	TemplateID       *int       `json:"templateId,omitempty"`
	FromName         string     `json:"fromName"`
	FromEmail        string     `json:"fromEmail"`
	ReplyTo          string     `json:"replyTo,omitempty"`
	ListID           int        `json:"listId"`
	ListName         string     `json:"listName,omitempty"`
	ListType         string     `json:"listType"`     // static | dynamic
	Status           string     `json:"status"`       // draft, scheduled, sending, paused, sent, cancelled
	StatusReason     *string    `json:"statusReason"` // why it is paused, throttled or was reset
	ScheduledAt      *time.Time `json:"scheduledAt,omitempty"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
	PreparedAt       *time.Time `json:"preparedAt"`     // null while sending = audience not yet snapshotted
	ThrottledUntil   *time.Time `json:"throttledUntil"` // set while SES quota/throttling defers sending
	TotalRecipients  int        `json:"totalRecipients"`
	SentCount        int        `json:"sentCount"`
	DeliveredCount   int        `json:"deliveredCount"`
	OpenCount        int        `json:"openCount"`  // unique opens
	ClickCount       int        `json:"clickCount"` // unique clicks
	BounceCount      int        `json:"bounceCount"`
	UnsubscribeCount int        `json:"unsubscribeCount"`
	ComplaintCount   int        `json:"complaintCount"`
	FailedCount      int        `json:"failedCount"`
	SkippedCount     int        `json:"skippedCount"`
	UnknownCount     int        `json:"unknownCount"`
	TrackOpens       bool       `json:"trackOpens"`
	TrackClicks      bool       `json:"trackClicks"`
	CreatedByUserID  *int64     `json:"createdByUserId,omitempty"`
	IsAbTest         bool       `json:"isAbTest"`
	AbTestSettings   any        `json:"abTestSettings,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	Warnings         []string   `json:"warnings,omitempty"` // non-blocking notes on create/update
}

// Campaign API Request DTOs

type CreateCampaignRequest struct {
	Name        string `json:"name" v:"required|min-length:2"`
	Subject     string `json:"subject" v:"required"`
	HTMLContent string `json:"htmlContent"`
	TextContent string `json:"textContent"`
	TemplateID  *int   `json:"templateId"`
	FromName    string `json:"fromName" v:"required"`
	FromEmail   string `json:"fromEmail" v:"required|email"`
	ReplyTo     string `json:"replyTo"`
	ListID      int    `json:"listId" v:"required"`
	TrackOpens  *bool  `json:"trackOpens"`  // default true
	TrackClicks *bool  `json:"trackClicks"` // default true
}

// UpdateCampaignRequest changes only the fields that are present. An empty
// replyTo or textContent (or JSON null) clears it.
type UpdateCampaignRequest struct {
	Name        *string `json:"name"`
	Subject     *string `json:"subject"`
	HTMLContent *string `json:"htmlContent"`
	TextContent *string `json:"textContent"`
	TemplateID  *int    `json:"templateId"`
	FromName    *string `json:"fromName"`
	FromEmail   *string `json:"fromEmail"`
	ReplyTo     *string `json:"replyTo"`
	ListID      *int    `json:"listId"`
	TrackOpens  *bool   `json:"trackOpens"`
	TrackClicks *bool   `json:"trackClicks"`
}

type ScheduleCampaignRequest struct {
	ScheduledAt string `json:"scheduledAt" v:"required"` // RFC3339 timestamp; the offset sets the time zone
	Timezone    string `json:"timezone" d:"UTC"`         // ignored
}

type LinkClicks struct {
	URL          string `json:"url"`
	Clicks       int    `json:"clicks"`
	UniqueClicks int    `json:"uniqueClicks"`
}

type HourCount struct {
	Hour  time.Time `json:"hour"`
	Opens int       `json:"opens"`
}

// CampaignStatsResponse rates are percentages of sentCount; opens and clicks are unique.
type CampaignStatsResponse struct {
	Campaign        *Campaign    `json:"campaign"`
	OpenRate        float64      `json:"openRate"`
	ClickRate       float64      `json:"clickRate"`
	ClickToOpenRate float64      `json:"clickToOpenRate"`
	BounceRate      float64      `json:"bounceRate"`
	UnsubscribeRate float64      `json:"unsubscribeRate"`
	ComplaintRate   float64      `json:"complaintRate"`
	DeliveredRate   float64      `json:"deliveredRate"`
	ClicksByLink    []LinkClicks `json:"clicksByLink"`
	OpensByHour     []HourCount  `json:"opensByHour"`
}

type CampaignListResponse struct {
	Campaigns  []Campaign `json:"campaigns"`
	Total      int        `json:"total"`
	Page       int        `json:"page"`
	PageSize   int        `json:"pageSize"`
	TotalPages int        `json:"totalPages"`
}

type CampaignProgressResponse struct {
	Status         string     `json:"status"`
	StatusReason   *string    `json:"statusReason"`
	Preparing      bool       `json:"preparing"`
	Total          int        `json:"total"`
	Pending        int        `json:"pending"`
	InFlight       int        `json:"inFlight"` // claimed or sending
	Sent           int        `json:"sent"`
	Failed         int        `json:"failed"`
	Unknown        int        `json:"unknown"`
	Skipped        int        `json:"skipped"`
	Cancelled      int        `json:"cancelled"`
	ThrottledUntil *time.Time `json:"throttledUntil"`
	Percent        float64    `json:"percent"`
}

// CampaignAudienceResponse is an estimate; the audience is snapshotted when sending starts.
type CampaignAudienceResponse struct {
	ListType           string   `json:"listType"`
	Eligible           int64    `json:"eligible"`
	ExcludedInactive   int64    `json:"excludedInactive"`
	ExcludedSuppressed int64    `json:"excludedSuppressed"`
	Warnings           []string `json:"warnings"` // dmarc_missing, sandbox_mode, no_postal_address, feedback_not_ready
}

type CampaignPreviewRequest struct {
	ContactUUID string `json:"contactUuid"`
}

type CampaignPreviewResponse struct {
	Subject          string   `json:"subject"`
	HTML             string   `json:"html"`
	Text             string   `json:"text"`
	UnknownVariables []string `json:"unknownVariables"`
}

type CampaignTestRequest struct {
	Emails []string `json:"emails"`
}

type CampaignTestResult struct {
	Email  string `json:"email"`
	Status string `json:"status"` // sent, failed, unknown
	Error  string `json:"error,omitempty"`
}

type CampaignTestResponse struct {
	Status  string               `json:"status"` // sending, sent, failed, unknown, partial
	Results []CampaignTestResult `json:"results"`
}

type CampaignRecipient struct {
	Email          string     `json:"email"`
	Status         string     `json:"status"`
	SkipReason     *string    `json:"skipReason"`
	DeliveryStatus *string    `json:"deliveryStatus"`
	SentAt         *time.Time `json:"sentAt"`
	OpenCount      int        `json:"openCount"`
	ClickCount     int        `json:"clickCount"`
	UnsubscribedAt *time.Time `json:"unsubscribedAt"`
	Error          *string    `json:"error"`
}

type CampaignRecipientListResponse struct {
	Recipients []CampaignRecipient `json:"recipients"`
	Total      int                 `json:"total"`
}

type CampaignSettings struct {
	PostalAddress string `json:"postalAddress"`
}

// A/B Test DTOs

type CreateAbTestRequest struct {
	Name      string          `json:"name" v:"required"`
	ListID    int             `json:"listId" v:"required"`
	FromName  string          `json:"fromName" v:"required"`
	FromEmail string          `json:"fromEmail" v:"required|email"`
	ReplyTo   string          `json:"replyTo"`
	Variants  []AbTestVariant `json:"variants" v:"required|min-length:2"`
	TestSize  int             `json:"testSize" d:"20"`       // Percentage
	WinnerBy  string          `json:"winnerBy" d:"openRate"` // openRate, clickRate
	WaitHours int             `json:"waitHours" d:"4"`
}

type AbTestVariant struct {
	Name        string `json:"name" v:"required"`
	Subject     string `json:"subject" v:"required"`
	HTMLContent string `json:"htmlContent"`
	TextContent string `json:"textContent"`
}

// ===================
// AUTOMATIONS / WORKFLOWS
// ===================

// Automation is an automation's draft plus its published-version state.
// TriggerType, TriggerConfig, Workflow and ReentryPolicy are the draft; running
// enrollments use the immutable published version.
type Automation struct {
	ID                    int              `json:"id"`
	UUID                  string           `json:"uuid"`
	OrgID                 int              `json:"orgId"`
	Name                  string           `json:"name"`
	Description           string           `json:"description,omitempty"`
	TriggerType           string           `json:"triggerType"` // contact.subscribed, contact.created, manual
	TriggerConfig         map[string]any   `json:"triggerConfig,omitempty"`
	Workflow              *Workflow        `json:"workflow"`
	Status                string           `json:"status"`        // draft, active, paused, archived
	ReentryPolicy         string           `json:"reentryPolicy"` // never, after_exit
	PublishedVersion      *int             `json:"publishedVersion"`
	HasUnpublishedChanges bool             `json:"hasUnpublishedChanges"`
	ActivatedAt           *time.Time       `json:"activatedAt"`
	ArchivedAt            *time.Time       `json:"archivedAt"`
	Stats                 AutomationCounts `json:"stats"`
	EnrolledCount         int              `json:"enrolledCount"`
	CompletedCount        int              `json:"completedCount"`
	InProgressCount       int              `json:"inProgressCount"` // equals Stats.Active
	CreatedAt             time.Time        `json:"createdAt"`
	UpdatedAt             time.Time        `json:"updatedAt"`
}

// AutomationCounts are live enrollment counts; Waiting is the subset of
// Active whose next step is in the future.
type AutomationCounts struct {
	Enrolled  int `json:"enrolled"`
	Active    int `json:"active"`
	Waiting   int `json:"waiting"`
	Completed int `json:"completed"`
	Exited    int `json:"exited"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// AutomationSummary is a lighter version for list views
type AutomationSummary struct {
	ID                    int              `json:"id"`
	UUID                  string           `json:"uuid"`
	OrgID                 int              `json:"orgId"`
	Name                  string           `json:"name"`
	Description           string           `json:"description,omitempty"`
	TriggerType           string           `json:"triggerType"`
	TriggerConfig         map[string]any   `json:"triggerConfig,omitempty"`
	Status                string           `json:"status"`
	ReentryPolicy         string           `json:"reentryPolicy"`
	PublishedVersion      *int             `json:"publishedVersion"`
	HasUnpublishedChanges bool             `json:"hasUnpublishedChanges"`
	ActivatedAt           *time.Time       `json:"activatedAt"`
	ArchivedAt            *time.Time       `json:"archivedAt"`
	Stats                 AutomationCounts `json:"stats"`
	EnrolledCount         int              `json:"enrolledCount"`
	CompletedCount        int              `json:"completedCount"`
	InProgressCount       int              `json:"inProgressCount"`
	CreatedAt             time.Time        `json:"createdAt"`
	UpdatedAt             time.Time        `json:"updatedAt"`
}

// WorkflowSchemaVersion is the current canonical graph schema.
const WorkflowSchemaVersion = 1

// Workflow represents the visual workflow graph
type Workflow struct {
	SchemaVersion int            `json:"schemaVersion"`
	Nodes         []WorkflowNode `json:"nodes"`
	Edges         []WorkflowEdge `json:"edges"`
}

// WorkflowNode represents a node in the workflow
type WorkflowNode struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // renderer key; always 'workflow'; kind is Data.Type
	Position WorkflowPosition `json:"position"`
	Data     WorkflowNodeData `json:"data"`
}

// WorkflowPosition represents a node's position
type WorkflowPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// WorkflowNodeData represents the data for a workflow node
type WorkflowNodeData struct {
	Label  string         `json:"label"`
	Type   string         `json:"type"` // trigger, email, delay, condition, action, webhook
	Config map[string]any `json:"config,omitempty"`
}

// WorkflowEdge connects two nodes; a condition's edges use sourceHandle yes/no.
type WorkflowEdge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"sourceHandle,omitempty"`
	TargetHandle string `json:"targetHandle,omitempty"`
	Type         string `json:"type,omitempty"`
	Animated     bool   `json:"animated,omitempty"`
}

// AutomationListResult represents paginated automation list
type AutomationListResult struct {
	Automations []AutomationSummary `json:"automations"`
	Total       int                 `json:"total"`
	Page        int                 `json:"page"`
	PageSize    int                 `json:"pageSize"`
}

// AutomationNodeStats counts step runs of one node in one version.
type AutomationNodeStats struct {
	Entered   int `json:"entered"`
	Waiting   int `json:"waiting"`
	Succeeded int `json:"succeeded"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Yes       int `json:"yes"`
	No        int `json:"no"`
	Sent      int `json:"sent"`
	Opened    int `json:"opened"`
	Clicked   int `json:"clicked"`
}

// AutomationStats are live counts plus per-node counts for one version.
type AutomationStats struct {
	AutomationUUID string `json:"automationUuid"`
	AutomationCounts
	InProgress     int                            `json:"inProgress"` // equals Active
	Errors         int                            `json:"errors"`     // equals Failed
	CompletionRate float64                        `json:"completionRate"`
	Version        int                            `json:"version"`
	Nodes          map[string]AutomationNodeStats `json:"nodes"`
}

// CreateAutomationRequest for creating an automation. The trigger node's
// config.event is the trigger; a differing triggerType is rejected.
type CreateAutomationRequest struct {
	Name          string         `json:"name" v:"required|min-length:2"`
	Description   string         `json:"description"`
	TriggerType   string         `json:"triggerType"`
	TriggerConfig map[string]any `json:"triggerConfig"`
	Workflow      *Workflow      `json:"workflow"`
	ReentryPolicy *string        `json:"reentryPolicy"` // never (default), after_exit
}

// UpdateAutomationRequest changes the draft only.
type UpdateAutomationRequest struct {
	Name          *string        `json:"name"`
	Description   *string        `json:"description"`
	TriggerType   *string        `json:"triggerType"`
	TriggerConfig map[string]any `json:"triggerConfig"`
	Workflow      *Workflow      `json:"workflow"`
	ReentryPolicy *string        `json:"reentryPolicy"`
}

// AutomationValidationError locates one problem; NodeID is empty for graph-wide errors.
type AutomationValidationError struct {
	NodeID  string `json:"nodeId"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// AutomationValidationResult is the outcome of POST /automations/:uuid/validate.
type AutomationValidationResult struct {
	Valid  bool                        `json:"valid"`
	Errors []AutomationValidationError `json:"errors"`
}

// ActivateAutomationRequest: PublishDraft defaults to true; false resumes the
// current published version without touching the draft.
type ActivateAutomationRequest struct {
	PublishDraft *bool `json:"publishDraft"`
}

// EnrollRequest names exactly one of a contact or a list.
type EnrollRequest struct {
	ContactUUID string `json:"contactUuid"`
	ListUUID    string `json:"listUuid"`
}

type EnrollResult struct {
	Enrolled int `json:"enrolled"`
	Skipped  int `json:"skipped"`
}

// AutomationEnrollmentView is one contact's run through a pinned version.
type AutomationEnrollmentView struct {
	UUID          string                  `json:"uuid"`
	ContactUUID   string                  `json:"contactUuid"`
	ContactEmail  string                  `json:"contactEmail"`
	Status        string                  `json:"status"` // active, completed, exited, failed, cancelled
	ExitReason    *string                 `json:"exitReason"`
	Version       *int                    `json:"version"`
	CurrentNodeID *string                 `json:"currentNodeId"`
	NextRunAt     *time.Time              `json:"nextRunAt"`
	RetryCount    int                     `json:"retryCount"`
	Error         *string                 `json:"error"`
	EnrolledAt    time.Time               `json:"enrolledAt"`
	CompletedAt   *time.Time              `json:"completedAt"`
	Steps         []AutomationStepRunView `json:"steps,omitempty"`
}

// AutomationStepRunView is one node's run; MessageUUID is the queued email.
type AutomationStepRunView struct {
	NodeID      string     `json:"nodeId"`
	NodeType    string     `json:"nodeType"`
	Status      string     `json:"status"` // waiting, succeeded, skipped, failed
	Outcome     *string    `json:"outcome"`
	MessageUUID *string    `json:"messageUuid,omitempty"`
	Error       *string    `json:"error"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	ResumeAt    *time.Time `json:"resumeAt"`
}

type AutomationEnrollmentListResult struct {
	Enrollments []AutomationEnrollmentView `json:"enrollments"`
	Total       int                        `json:"total"`
	Page        int                        `json:"page"`
	PageSize    int                        `json:"pageSize"`
}

// ===================
// EMAIL RECEIVING MODELS
// ===================

// ReceivedEmail represents an incoming email
type ReceivedEmail struct {
	EnvelopeRecipients []string   `json:"envelopeRecipients,omitempty"`
	Direction          string     `json:"direction"`
	SendStatus         string     `json:"sendStatus"`
	SendError          string     `json:"sendError,omitempty"`
	DraftVersion       int        `json:"draftVersion"`
	RemoteImages       string     `json:"remoteImages,omitempty"` // blocked|allowed; single-message reads only
	TrustedSender      bool       `json:"trustedSender"`
	SentAt             *time.Time `json:"sentAt,omitempty"`
	ID                 int64      `json:"id"`
	UUID               string     `json:"uuid"`
	OrgID              int64      `json:"orgId"`
	DomainID           int64      `json:"domainId"`
	IdentityID         int64      `json:"identityId"`
	MessageID          string     `json:"messageId"`
	InReplyTo          string     `json:"inReplyTo,omitempty"`
	References         []string   `json:"references,omitempty"`
	ThreadID           string     `json:"threadId,omitempty"`
	FromEmail          string     `json:"fromEmail"`
	FromName           string     `json:"fromName,omitempty"`
	ToEmails           []string   `json:"toEmails"`
	CcEmails           []string   `json:"ccEmails,omitempty"`
	BccEmails          []string   `json:"bccEmails,omitempty"`
	ReplyTo            string     `json:"replyTo,omitempty"`
	Subject            string     `json:"subject"`
	TextBody           string     `json:"textBody,omitempty"`
	HTMLBody           string     `json:"htmlBody,omitempty"`
	Snippet            string     `json:"snippet,omitempty"`
	RawS3Key           string     `json:"rawS3Key,omitempty"`
	RawS3Bucket        string     `json:"rawS3Bucket,omitempty"`
	SizeBytes          int        `json:"sizeBytes"`
	HasAttachments     bool       `json:"hasAttachments"`
	// Mailbox state
	Folder     string   `json:"folder"`
	IsRead     bool     `json:"isRead"`
	IsStarred  bool     `json:"isStarred"`
	IsArchived bool     `json:"isArchived"`
	IsTrashed  bool     `json:"isTrashed"`
	IsSpam     bool     `json:"isSpam"`
	Labels     []string `json:"labels,omitempty"`
	// Spam detection
	SpamScore    *float64 `json:"spamScore,omitempty"`
	SpamVerdict  string   `json:"spamVerdict,omitempty"`
	VirusVerdict string   `json:"virusVerdict,omitempty"`
	SPFVerdict   string   `json:"spfVerdict,omitempty"`
	DKIMVerdict  string   `json:"dkimVerdict,omitempty"`
	DMARCVerdict string   `json:"dmarcVerdict,omitempty"`
	// AWS metadata
	SESMessageID      string `json:"sesMessageId,omitempty"`
	SNSNotificationID string `json:"snsNotificationId,omitempty"`
	// Timestamps
	ReceivedAt time.Time  `json:"receivedAt"`
	ReadAt     *time.Time `json:"readAt,omitempty"`
	TrashedAt  *time.Time `json:"trashedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	// Identity info (for unified inbox display)
	IdentityEmail       string `json:"identityEmail,omitempty"`
	IdentityDisplayName string `json:"identityDisplayName,omitempty"`
	IdentityColor       string `json:"identityColor,omitempty"`
	// Relations (loaded on demand)
	Attachments []EmailAttachment `json:"attachments,omitempty"`
}

// EmailAttachment represents a file attached to an email
type EmailAttachment struct {
	ID              int64     `json:"id"`
	UUID            string    `json:"uuid"`
	ReceivedEmailID int64     `json:"receivedEmailId"`
	Filename        string    `json:"filename"`
	ContentType     string    `json:"contentType"`
	SizeBytes       int       `json:"sizeBytes"`
	S3Key           string    `json:"s3Key"`
	S3Bucket        string    `json:"s3Bucket"`
	ContentID       string    `json:"contentId,omitempty"`
	IsInline        bool      `json:"isInline"`
	Checksum        string    `json:"checksum,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	// Presigned URL (generated on demand)
	DownloadURL string `json:"downloadUrl,omitempty"`
}

// EmailLabel represents a user-defined label for organizing emails
type EmailLabel struct {
	ID        int       `json:"id"`
	UUID      string    `json:"uuid"`
	OrgID     int64     `json:"orgId"`
	UserID    int64     `json:"userId"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// InboxFilter represents a filter rule for incoming emails
type InboxFilter struct {
	ID             int               `json:"id"`
	UUID           string            `json:"uuid"`
	OrgID          int64             `json:"orgId"`
	UserID         int64             `json:"userId"`
	IdentityID     *int64            `json:"identityId,omitempty"`
	Name           string            `json:"name"`
	Kind           string            `json:"kind"` // filter, blocked_sender (applied last)
	Priority       int               `json:"priority"`
	Active         bool              `json:"active"`
	Conditions     []FilterCondition `json:"conditions"`
	ConditionLogic string            `json:"conditionLogic"` // all, any
	// Actions
	ActionLabels   []string `json:"actionLabels,omitempty"`
	ActionFolder   string   `json:"actionFolder,omitempty"`
	ActionStar     bool     `json:"actionStar"`
	ActionMarkRead bool     `json:"actionMarkRead"`
	ActionArchive  bool     `json:"actionArchive"`
	ActionTrash    bool     `json:"actionTrash"`
	ActionForward  string   `json:"actionForward,omitempty"`
	// Stats
	MatchCount    int        `json:"matchCount"`
	LastMatchedAt *time.Time `json:"lastMatchedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// FilterCondition represents a single condition in a filter
type FilterCondition struct {
	Field    string `json:"field"`    // from, to, subject, body, hasAttachment
	Operator string `json:"operator"` // contains, equals, startsWith, endsWith, regex
	Value    string `json:"value"`
}

// ReceivingConfig represents the organization's email receiving configuration
type ReceivingConfig struct {
	ID               int        `json:"id"`
	UUID             string     `json:"uuid"`
	OrgID            int64      `json:"orgId"`
	S3Bucket         string     `json:"s3Bucket"`
	S3Region         string     `json:"s3Region"`
	SNSTopicArn      string     `json:"snsTopicArn"`
	SESRuleSetName   string     `json:"sesRuleSetName"`
	WebhookSecret    string     `json:"-"`
	Status           string     `json:"status"` // pending, active, error
	LastHealthCheck  *time.Time `json:"lastHealthCheck,omitempty"`
	SetupCompletedAt *time.Time `json:"setupCompletedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// ===================
// EMAIL RECEIVING REQUEST/RESPONSE DTOs
// ===================

// InboxListRequest for listing emails in inbox
type InboxListRequest struct {
	DomainID       int64    `json:"domainId"`
	HasAttachments *bool    `json:"hasAttachments"`
	Sender         string   `json:"sender"`
	DateFrom       string   `json:"dateFrom"`
	DateTo         string   `json:"dateTo"`
	IdentityID     int64    `json:"identityId"`
	Folder         string   `json:"folder" d:"inbox"`
	Labels         []string `json:"labels"`
	IsRead         *bool    `json:"isRead"`
	IsStarred      *bool    `json:"isStarred"`
	Search         string   `json:"search"`
	Page           int      `json:"page" d:"1"`
	PageSize       int      `json:"pageSize" d:"50"`
	SortBy         string   `json:"sortBy" d:"receivedAt"`
	SortOrder      string   `json:"sortOrder" d:"desc"`
}

// InboxListResponse for paginated email list
type InboxListResponse struct {
	Emails     []ReceivedEmail `json:"emails"`
	Total      int             `json:"total"`
	Unread     int             `json:"unread"`
	Page       int             `json:"page"`
	PageSize   int             `json:"pageSize"`
	TotalPages int             `json:"totalPages"`
}

// InboxCountsResponse for folder/label counts
type InboxCountsResponse struct {
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

// MarkEmailsRequest for marking emails as read/unread
type MarkEmailsRequest struct {
	EmailUUIDs []string `json:"emailUuids" v:"required"`
	IsRead     bool     `json:"isRead"`
}

// StarEmailsRequest for starring/unstarring emails
type StarEmailsRequest struct {
	EmailUUIDs []string `json:"emailUuids" v:"required"`
	IsStarred  bool     `json:"isStarred"`
}

// MoveEmailsRequest for moving emails to folder
type MoveEmailsRequest struct {
	EmailUUIDs []string `json:"emailUuids" v:"required"`
	Folder     string   `json:"folder" v:"required"`
}

// LabelEmailsRequest for adding/removing labels
type LabelEmailsRequest struct {
	EmailUUIDs   []string `json:"emailUuids" v:"required"`
	AddLabels    []string `json:"addLabels"`
	RemoveLabels []string `json:"removeLabels"`
}

// TrashEmailsRequest for moving emails to trash
type TrashEmailsRequest struct {
	EmailUUIDs []string `json:"emailUuids" v:"required"`
	Permanent  bool     `json:"permanent"` // If true, delete permanently
}

// CreateLabelRequest for creating a new label
type CreateLabelRequest struct {
	Name  string `json:"name" v:"required|min-length:1|max-length:100"`
	Color string `json:"color" d:"#6366f1"`
}

// UpdateLabelRequest for updating a label
type UpdateLabelRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// CreateFilterRequest for creating a new inbox filter
type CreateFilterRequest struct {
	Name           string            `json:"name" v:"required"`
	IdentityID     *int64            `json:"identityId"`
	Priority       int               `json:"priority" d:"0"`
	Conditions     []FilterCondition `json:"conditions" v:"required"`
	ConditionLogic string            `json:"conditionLogic" d:"all"`
	ActionLabels   []string          `json:"actionLabels"`
	ActionFolder   string            `json:"actionFolder"`
	ActionStar     bool              `json:"actionStar"`
	ActionMarkRead bool              `json:"actionMarkRead"`
	ActionArchive  bool              `json:"actionArchive"`
	ActionTrash    bool              `json:"actionTrash"`
	ActionForward  string            `json:"actionForward"`
}

// UpdateFilterRequest for updating an inbox filter
type UpdateFilterRequest struct {
	Name           string            `json:"name"`
	Priority       *int              `json:"priority"`
	Active         *bool             `json:"active"`
	Conditions     []FilterCondition `json:"conditions"`
	ConditionLogic string            `json:"conditionLogic"`
	ActionLabels   []string          `json:"actionLabels"`
	ActionFolder   string            `json:"actionFolder"`
	ActionStar     *bool             `json:"actionStar"`
	ActionMarkRead *bool             `json:"actionMarkRead"`
	ActionArchive  *bool             `json:"actionArchive"`
	ActionTrash    *bool             `json:"actionTrash"`
	ActionForward  string            `json:"actionForward"`
}

// SetupReceivingRequest for setting up email receiving
type SetupReceivingRequest struct {
	DomainUUID string `json:"domainUuid" v:"required"`
}

// SetupReceivingResponse contains the setup result
type SetupReceivingResponse struct {
	Success     bool              `json:"success"`
	S3Bucket    string            `json:"s3Bucket"`
	SNSTopicArn string            `json:"snsTopicArn"`
	RuleSetName string            `json:"ruleSetName"`
	RuleName    string            `json:"ruleName"`
	WebhookURL  string            `json:"webhookUrl"`
	RequiredDNS []DomainDNSRecord `json:"requiredDns,omitempty"`
}

// SNSNotification represents an incoming SNS notification
type SNSNotification struct {
	Type             string `json:"Type"`
	MessageId        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject,omitempty"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL,omitempty"`
	UnsubscribeURL   string `json:"UnsubscribeURL,omitempty"`
	Token            string `json:"Token,omitempty"`
}

// SESNotification represents the SES notification inside SNS message
type SESNotification struct {
	NotificationType string        `json:"notificationType"`
	Mail             SESMail       `json:"mail"`
	Receipt          *SESReceipt   `json:"receipt,omitempty"`
	Bounce           *SESBounce    `json:"bounce,omitempty"`
	Complaint        *SESComplaint `json:"complaint,omitempty"`
	Delivery         *SESDelivery  `json:"delivery,omitempty"`
}

// SESMail contains common mail information
type SESMail struct {
	Timestamp        string           `json:"timestamp"`
	Source           string           `json:"source"`
	MessageId        string           `json:"messageId"`
	Destination      []string         `json:"destination"`
	HeadersTruncated bool             `json:"headersTruncated"`
	Headers          []SESHeader      `json:"headers,omitempty"`
	CommonHeaders    SESCommonHeaders `json:"commonHeaders,omitempty"`
}

// SESHeader represents an email header
type SESHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SESCommonHeaders contains common email headers
type SESCommonHeaders struct {
	ReturnPath string   `json:"returnPath,omitempty"`
	From       []string `json:"from,omitempty"`
	Date       string   `json:"date,omitempty"`
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	MessageId  string   `json:"messageId,omitempty"`
	Subject    string   `json:"subject,omitempty"`
}

// SESReceipt contains receipt information for incoming emails
type SESReceipt struct {
	Timestamp            string     `json:"timestamp"`
	ProcessingTimeMillis int        `json:"processingTimeMillis"`
	Recipients           []string   `json:"recipients"`
	SpamVerdict          SESVerdict `json:"spamVerdict"`
	VirusVerdict         SESVerdict `json:"virusVerdict"`
	SPFVerdict           SESVerdict `json:"spfVerdict"`
	DKIMVerdict          SESVerdict `json:"dkimVerdict"`
	DMARCVerdict         SESVerdict `json:"dmarcVerdict"`
	Action               SESAction  `json:"action"`
}

// SESVerdict represents a spam/virus verdict
type SESVerdict struct {
	Status string `json:"status"` // PASS, FAIL, GRAY, PROCESSING_FAILED
}

// SESAction represents the action taken on the email
type SESAction struct {
	Type            string `json:"type"` // S3, SNS, Lambda, etc.
	TopicArn        string `json:"topicArn,omitempty"`
	BucketName      string `json:"bucketName,omitempty"`
	ObjectKeyPrefix string `json:"objectKeyPrefix,omitempty"`
	ObjectKey       string `json:"objectKey,omitempty"`
}

// SESBounce contains bounce information
type SESBounce struct {
	BounceType        string                `json:"bounceType"`
	BounceSubType     string                `json:"bounceSubType"`
	BouncedRecipients []SESBouncedRecipient `json:"bouncedRecipients"`
	Timestamp         string                `json:"timestamp"`
	FeedbackId        string                `json:"feedbackId"`
}

// SESBouncedRecipient contains bounced recipient info
type SESBouncedRecipient struct {
	EmailAddress   string `json:"emailAddress"`
	Action         string `json:"action,omitempty"`
	Status         string `json:"status,omitempty"`
	DiagnosticCode string `json:"diagnosticCode,omitempty"`
}

// SESComplaint contains complaint information
type SESComplaint struct {
	ComplainedRecipients  []SESComplainedRecipient `json:"complainedRecipients"`
	Timestamp             string                   `json:"timestamp"`
	FeedbackId            string                   `json:"feedbackId"`
	ComplaintFeedbackType string                   `json:"complaintFeedbackType,omitempty"`
}

// SESComplainedRecipient contains complained recipient info
type SESComplainedRecipient struct {
	EmailAddress string `json:"emailAddress"`
}

// SESDelivery contains delivery information
type SESDelivery struct {
	Timestamp            string   `json:"timestamp"`
	ProcessingTimeMillis int      `json:"processingTimeMillis"`
	Recipients           []string `json:"recipients"`
	SMTPResponse         string   `json:"smtpResponse,omitempty"`
	ReportingMTA         string   `json:"reportingMTA,omitempty"`
}
