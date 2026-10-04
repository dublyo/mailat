package mailat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// EmailsService handles email operations.
type EmailsService struct {
	client *Client
}

// SendOptions are additional options for sending emails.
type SendOptions struct {
	IdempotencyKey string
}

// Send sends a single transactional email.
func (s *EmailsService) Send(ctx context.Context, req *SendEmailRequest, opts *SendOptions) (*SendEmailResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("email request required")
	}
	key := req.IdempotencyKey
	if opts != nil && opts.IdempotencyKey != "" {
		if key != "" && key != opts.IdempotencyKey {
			return nil, fmt.Errorf("idempotency keys must match")
		}
		key = opts.IdempotencyKey
	}
	headers, err := sendHeaders(key)
	if err != nil {
		return nil, err
	}

	data, err := s.client.request(ctx, "POST", "/emails", req, headers)
	if err != nil {
		return nil, err
	}

	var resp SendEmailResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// SendBatch sends multiple emails in a batch (up to 100).
func (s *EmailsService) SendBatch(ctx context.Context, emails []SendEmailRequest, options ...*SendOptions) (*BatchSendResponse, error) {
	if len(emails) == 0 || len(emails) > 100 {
		return nil, fmt.Errorf("batch size cannot exceed 100 emails")
	}

	key := ""
	if len(options) > 0 && options[0] != nil {
		key = options[0].IdempotencyKey
	}
	headers, err := sendHeaders(key)
	if err != nil {
		return nil, err
	}
	req := BatchSendRequest{Emails: emails}
	data, err := s.client.request(ctx, "POST", "/emails/batch", req, headers)
	if err != nil {
		return nil, err
	}

	var resp BatchSendResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// Get retrieves the status and events for an email.
func (s *EmailsService) Get(ctx context.Context, emailID string) (*EmailStatusResponse, error) {
	data, err := s.client.request(ctx, "GET", "/emails/"+url.PathEscape(emailID), nil, nil)
	if err != nil {
		return nil, err
	}

	var resp EmailStatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &resp, nil
}

// Cancel cancels a scheduled email.
func (s *EmailsService) Cancel(ctx context.Context, emailID string) error {
	_, err := s.client.request(ctx, "DELETE", "/emails/"+url.PathEscape(emailID), nil, nil)
	return err
}
