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
	Emails    *EmailsService
	Templates *TemplatesService
	Webhooks  *WebhooksService
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

	return c
}

// APIError represents an API error response.
type APIError struct {
	StatusCode int    `json:"-"`
	Message    string `json:"message"`
	Code       string `json:"code,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (code: %s, status: %d)", e.Message, e.Code, e.StatusCode)
	}
	return fmt.Sprintf("%s (status: %d)", e.Message, e.StatusCode)
}

// apiResponse wraps all API responses.
type apiResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) request(ctx context.Context, method, path string, body interface{}, headers map[string]string) (json.RawMessage, error) {
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

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    apiResp.Message,
		}
	}

	return apiResp.Data, nil
}

// VerifyWebhookSignature verifies a webhook signature.
// Returns true if the signature is valid.
func VerifyWebhookSignature(payload []byte, signature, secret string, tolerance time.Duration) bool {
	// Parse signature: t=timestamp,v1=signature
	parts := make(map[string]string)
	for _, part := range strings.Split(signature, ",") {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			parts[kv[0]] = kv[1]
		}
	}

	timestampStr, ok := parts["t"]
	if !ok {
		return false
	}
	v1Sig, ok := parts["v1"]
	if !ok {
		return false
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false
	}

	// Check timestamp tolerance
	now := time.Now().Unix()
	if tolerance > 0 {
		diff := now - timestamp
		if diff < 0 {
			diff = -diff
		}
		if diff > int64(tolerance.Seconds()) {
			return false
		}
	}

	// Compute expected signature
	signedPayload := fmt.Sprintf("%d.%s", timestamp, string(payload))
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(signedPayload))
	expectedSig := hex.EncodeToString(h.Sum(nil))

	// Timing-safe comparison
	return hmac.Equal([]byte(v1Sig), []byte(expectedSig))
}

// ParseWebhookPayload verifies and parses a webhook payload.
func ParseWebhookPayload(payload []byte, signature, secret string) (*WebhookPayload, error) {
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

	return &wp, nil
}
