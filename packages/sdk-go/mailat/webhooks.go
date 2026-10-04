package mailat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// WebhooksService handles webhook operations.
type WebhooksService struct {
	client *Client
}

// Create creates a new webhook endpoint.
func (s *WebhooksService) Create(ctx context.Context, req *CreateWebhookRequest) (*Webhook, error) {
	data, err := s.client.request(ctx, "POST", "/webhooks", req, nil)
	if err != nil {
		return nil, err
	}

	var resp Webhook
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// Get retrieves a webhook by UUID.
func (s *WebhooksService) Get(ctx context.Context, webhookID string) (*Webhook, error) {
	data, err := s.client.request(ctx, "GET", "/webhooks/"+url.PathEscape(webhookID), nil, nil)
	if err != nil {
		return nil, err
	}

	var resp Webhook
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// List retrieves all webhooks.
func (s *WebhooksService) List(ctx context.Context) ([]Webhook, error) {
	data, err := s.client.request(ctx, "GET", "/webhooks", nil, nil)
	if err != nil {
		return nil, err
	}

	resp := []Webhook{}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return resp, nil
}

// Update updates a webhook.
func (s *WebhooksService) Update(ctx context.Context, webhookID string, req *UpdateWebhookRequest) (*Webhook, error) {
	data, err := s.client.request(ctx, "PUT", "/webhooks/"+url.PathEscape(webhookID), req, nil)
	if err != nil {
		return nil, err
	}

	var resp Webhook
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// Delete deletes a webhook.
func (s *WebhooksService) Delete(ctx context.Context, webhookID string) error {
	_, err := s.client.request(ctx, "DELETE", "/webhooks/"+url.PathEscape(webhookID), nil, nil)
	return err
}

// RotateSecret generates a new secret for a webhook.
func (s *WebhooksService) RotateSecret(ctx context.Context, webhookID string) (string, error) {
	data, err := s.client.request(ctx, "POST", "/webhooks/"+url.PathEscape(webhookID)+"/rotate-secret", nil, nil)
	if err != nil {
		return "", err
	}

	var resp RotateSecretResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return resp.Secret, nil
}

// GetCalls retrieves recent webhook delivery attempts.
func (s *WebhooksService) GetCalls(ctx context.Context, webhookID string, limit int) ([]WebhookCall, error) {
	path := fmt.Sprintf("/webhooks/%s/calls?limit=%d", url.PathEscape(webhookID), limit)
	data, err := s.client.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}

	resp := []WebhookCall{}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return resp, nil
}

// Test sends a test event to a webhook.
func (s *WebhooksService) Test(ctx context.Context, webhookID string) error {
	_, err := s.client.request(ctx, "POST", "/webhooks/"+url.PathEscape(webhookID)+"/test", nil, nil)
	return err
}

// TestDelivery exposes receiver outcome; HTTP success alone does not mean delivery.
func (s *WebhooksService) TestDelivery(ctx context.Context, webhookID string) (*WebhookTestResult, error) {
	data, err := s.client.request(ctx, "POST", "/webhooks/"+url.PathEscape(webhookID)+"/test", nil, nil)
	if err != nil {
		return nil, err
	}
	var result WebhookTestResult
	err = json.Unmarshal(data, &result)
	return &result, err
}
