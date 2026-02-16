package mailat

import (
	"context"
	"encoding/json"
	"fmt"
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
	data, err := s.client.request(ctx, "GET", "/webhooks/"+webhookID, nil, nil)
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

	var resp []Webhook
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return resp, nil
}

// Update updates a webhook.
func (s *WebhooksService) Update(ctx context.Context, webhookID string, req *UpdateWebhookRequest) (*Webhook, error) {
	data, err := s.client.request(ctx, "PUT", "/webhooks/"+webhookID, req, nil)
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
	_, err := s.client.request(ctx, "DELETE", "/webhooks/"+webhookID, nil, nil)
	return err
}

// RotateSecret generates a new secret for a webhook.
func (s *WebhooksService) RotateSecret(ctx context.Context, webhookID string) (string, error) {
	data, err := s.client.request(ctx, "POST", "/webhooks/"+webhookID+"/rotate-secret", nil, nil)
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
	path := fmt.Sprintf("/webhooks/%s/calls?limit=%d", webhookID, limit)
	data, err := s.client.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}

	var resp []WebhookCall
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return resp, nil
}

// Test sends a test event to a webhook.
func (s *WebhooksService) Test(ctx context.Context, webhookID string) error {
	_, err := s.client.request(ctx, "POST", "/webhooks/"+webhookID+"/test", nil, nil)
	return err
}
