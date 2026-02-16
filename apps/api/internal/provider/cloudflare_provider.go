package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const cloudflareBaseURL = "https://api.cloudflare.com/client/v4"

// CloudflareZone represents a Cloudflare zone
type CloudflareZone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CloudflareListZonesResponse represents the API response for listing zones
type CloudflareListZonesResponse struct {
	Success bool             `json:"success"`
	Errors  []CloudflareError `json:"errors"`
	Result  []CloudflareZone `json:"result"`
}

// CloudflareError represents an error from the Cloudflare API
type CloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// CloudflareCreateRecordResponse represents the API response for creating a record
type CloudflareCreateRecordResponse struct {
	Success bool              `json:"success"`
	Errors  []CloudflareError `json:"errors"`
	Result  struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"result"`
}

// CloudflareListZones lists all zones available for the API token
func CloudflareListZones(ctx context.Context, apiToken string) ([]CloudflareZone, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", cloudflareBaseURL+"/zones?per_page=50", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result CloudflareListZonesResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.Success {
		if len(result.Errors) > 0 {
			return nil, fmt.Errorf("cloudflare API error: %s", result.Errors[0].Message)
		}
		return nil, fmt.Errorf("cloudflare API returned unsuccessful response")
	}

	return result.Result, nil
}

// CloudflareCreateDNSRecord creates a DNS record in Cloudflare
func CloudflareCreateDNSRecord(ctx context.Context, apiToken, zoneID, recordType, hostname, value string) error {
	// Prepare the record data
	recordData := map[string]interface{}{
		"type":    recordType,
		"name":    hostname,
		"content": value,
		"ttl":     1, // Auto TTL
	}

	// For MX records, extract priority and value
	if recordType == "MX" {
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			var priority int
			fmt.Sscanf(parts[0], "%d", &priority)
			recordData["priority"] = priority
			recordData["content"] = parts[1]
		}
	}

	// For CNAME records, don't proxy
	if recordType == "CNAME" {
		recordData["proxied"] = false
	}

	jsonData, err := json.Marshal(recordData)
	if err != nil {
		return fmt.Errorf("failed to marshal record data: %w", err)
	}

	url := fmt.Sprintf("%s/zones/%s/dns_records", cloudflareBaseURL, zoneID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result CloudflareCreateRecordResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.Success {
		if len(result.Errors) > 0 {
			// Check if record already exists
			if result.Errors[0].Code == 81057 {
				fmt.Printf("DNS record already exists: %s %s\n", recordType, hostname)
				return nil // Not an error, record already exists
			}
			return fmt.Errorf("cloudflare API error: %s (code: %d)", result.Errors[0].Message, result.Errors[0].Code)
		}
		return fmt.Errorf("cloudflare API returned unsuccessful response")
	}

	fmt.Printf("Created Cloudflare DNS record: %s %s -> %s\n", recordType, hostname, value)
	return nil
}
