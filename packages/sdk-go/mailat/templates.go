package mailat

import (
	"context"
	"encoding/json"
	"fmt"
)

// TemplatesService handles template operations.
type TemplatesService struct {
	client *Client
}

// Create creates a new email template.
func (s *TemplatesService) Create(ctx context.Context, req *CreateTemplateRequest) (*Template, error) {
	data, err := s.client.request(ctx, "POST", "/templates", req, nil)
	if err != nil {
		return nil, err
	}

	var resp Template
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// Get retrieves a template by UUID.
func (s *TemplatesService) Get(ctx context.Context, templateID string) (*Template, error) {
	data, err := s.client.request(ctx, "GET", "/templates/"+templateID, nil, nil)
	if err != nil {
		return nil, err
	}

	var resp Template
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// List retrieves all templates.
func (s *TemplatesService) List(ctx context.Context) ([]Template, error) {
	data, err := s.client.request(ctx, "GET", "/templates", nil, nil)
	if err != nil {
		return nil, err
	}

	var resp []Template
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return resp, nil
}

// Update updates a template.
func (s *TemplatesService) Update(ctx context.Context, templateID string, req *UpdateTemplateRequest) (*Template, error) {
	data, err := s.client.request(ctx, "PUT", "/templates/"+templateID, req, nil)
	if err != nil {
		return nil, err
	}

	var resp Template
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// Delete deletes a template.
func (s *TemplatesService) Delete(ctx context.Context, templateID string) error {
	_, err := s.client.request(ctx, "DELETE", "/templates/"+templateID, nil, nil)
	return err
}

// Preview renders a template with variables.
func (s *TemplatesService) Preview(ctx context.Context, templateID string, variables map[string]string) (*PreviewTemplateResponse, error) {
	req := PreviewTemplateRequest{Variables: variables}
	data, err := s.client.request(ctx, "POST", "/templates/"+templateID+"/preview", req, nil)
	if err != nil {
		return nil, err
	}

	var resp PreviewTemplateResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}
