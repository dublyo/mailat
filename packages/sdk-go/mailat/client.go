// Package mailat provides a Go client for the mailat.co API.
package mailat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.mailat.co/api/v1"
	DefaultTimeout = 30 * time.Second
	Version        = "0.1.0"
)

// Client is the mailat.co API client.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client

	// Resource namespaces
	Emails     *EmailsService
	Templates  *TemplatesService
	Webhooks   *WebhooksService
	Inbox      *InboxService
	Compose    *ComposeService
	Domains    *DomainsService
	Identities *CRUDResource
	Triggers   *TriggersService
	Deliveries *DeliveriesService
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithBaseURL sets a custom base URL.
func WithBaseURL(url string) ClientOption {
	return func(c *Client) {
		c.baseURL = strings.TrimSuffix(url, "/")
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithTimeout sets the request timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = timeout
	}
}

// NewClient creates a new mailat.co API client.
func NewClient(apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: DefaultBaseURL,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	// Initialize services
	c.Emails = &EmailsService{client: c}
	c.Templates = &TemplatesService{client: c}
	c.Webhooks = &WebhooksService{client: c}
	c.Inbox = newInbox(c)
	c.Compose = &ComposeService{c}
	c.Domains = &DomainsService{ReadResource{c, "/domains"}}
	c.Identities = &CRUDResource{ReadResource{c, "/identities"}}
	c.Triggers = &TriggersService{CRUDResource{ReadResource{c, "/webhook-triggers"}}}
	c.Deliveries = &DeliveriesService{ReadResource{c, "/webhook-deliveries"}}

	return c
}

// APIError represents an API error response.
type APIError struct {
	StatusCode int         `json:"-"`
	RetryAfter string      `json:"-"`
	Message    string      `json:"message"`
	Code       interface{} `json:"code,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code != nil {
		return fmt.Sprintf("%s (code: %v, status: %d)", e.Message, e.Code, e.StatusCode)
	}
	return fmt.Sprintf("%s (status: %d)", e.Message, e.StatusCode)
}

// apiResponse wraps all API responses.
type apiResponse struct {
	Code    interface{}     `json:"code"`
	Success bool            `json:"success"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) request(ctx context.Context, method, path string, body interface{}, headers map[string]string, binary ...bool) (json.RawMessage, error) {
	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mailat-go/"+Version)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 400 && len(binary) > 0 && binary[0] {
		return respBody, nil
	}
	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    apiResp.Message,
			Code:       apiResp.Code,
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}

	return apiResp.Data, nil
}

// VerifyWebhookSignature verifies a webhook signature.
// Returns true if the signature is valid.
func VerifyWebhookSignature(payload []byte, signature, secret string, tolerance time.Duration) bool {
	if secret == "" || tolerance < 0 {
		return false
	}
	// A zero tolerance uses the safe default; it never disables replay checks.
	if tolerance == 0 {
		tolerance = 5 * time.Minute
	}
	parts := make(map[string]string)
	for _, part := range strings.Split(signature, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 || (kv[0] != "t" && kv[0] != "v1") {
			return false
		}
		if _, exists := parts[kv[0]]; exists {
			return false
		}
		parts[kv[0]] = kv[1]
	}
	timestampStr, v1Sig := parts["t"], parts["v1"]
	if !regexp.MustCompile(`^[0-9]{1,13}$`).MatchString(timestampStr) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(v1Sig) {
		return false
	}
	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil || timestamp <= 0 {
		return false
	}
	diff := time.Since(time.Unix(timestamp, 0))
	if diff < -tolerance || diff > tolerance {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestampStr + "."))
	h.Write(payload)
	expectedSig := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(v1Sig), []byte(expectedSig))
}

// ParseWebhookPayload verifies and parses a webhook payload.
func ParseWebhookPayload(payload []byte, signature, secret string, claimEvent ...func(string) (bool, error)) (*WebhookPayload, error) {
	if !VerifyWebhookSignature(payload, signature, secret, 5*time.Minute) {
		return nil, &APIError{
			StatusCode: 401,
			Message:    "Invalid webhook signature",
		}
	}

	var wp WebhookPayload
	if err := json.Unmarshal(payload, &wp); err != nil {
		return nil, fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	if wp.Version != "1" || wp.ID == "" || wp.Type == "" || wp.CreatedAt.IsZero() || wp.Data == nil {
		return nil, &APIError{StatusCode: 400, Message: "Invalid webhook event envelope"}
	}
	// The optional callback must atomically claim IDs in durable application storage.
	if len(claimEvent) > 0 && claimEvent[0] != nil {
		claimed, err := claimEvent[0](wp.ID)
		if err != nil {
			return nil, err
		}
		if !claimed {
			return nil, &APIError{StatusCode: 409, Message: "Webhook event already processed"}
		}
	}
	return &wp, nil
}
