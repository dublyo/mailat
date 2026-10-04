package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	Success bool              `json:"success"`
	Errors  []CloudflareError `json:"errors"`
	Result  []CloudflareZone  `json:"result"`
}

// CloudflareError represents an error from the Cloudflare API
type CloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// CloudflareDNSConflictError means automatic setup preserved existing DNS.
type CloudflareDNSConflictError struct{ Hostname string }

func (e *CloudflareDNSConflictError) Error() string {
	return fmt.Sprintf("Existing DNS at %s conflicts with this record and was preserved; review it with your current mail provider.", e.Hostname)
}

type cloudflareDNSRecord struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	Priority int    `json:"priority"`
	Proxied  bool   `json:"proxied"`
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
func CloudflareCreateDNSRecord(ctx context.Context, apiToken, zoneID, recordType, hostname, value string, companions ...DNSRecord) (string, error) {
	recordType = strings.ToUpper(recordType)
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

	// Cloudflare accepts competing MX and duplicate SPF TXT records. Read before
	// adding so connecting a sender never silently changes another provider's DNS.
	client := &http.Client{Timeout: 30 * time.Second}
	identical := false
	for page := 1; ; page++ {
		query := url.Values{"name": {hostname}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/zones/%s/dns_records?%s", cloudflareBaseURL, zoneID, query.Encode()), nil)
		if err != nil {
			return "", fmt.Errorf("failed to prepare DNS preflight: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to inspect existing DNS: %w", err)
		}
		var existing struct {
			Success bool                  `json:"success"`
			Result  []cloudflareDNSRecord `json:"result"`
			Info    struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		err = json.NewDecoder(resp.Body).Decode(&existing)
		resp.Body.Close()
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || !existing.Success {
			return "", fmt.Errorf("could not inspect existing DNS; no record was added")
		}
		for _, record := range existing.Result {
			// Only records at the requested owner name can conflict. Do not trust
			// an unexpectedly broad API response to claim a different record exists.
			if !strings.EqualFold(strings.TrimSuffix(record.Name, "."), strings.TrimSuffix(hostname, ".")) {
				continue
			}
			existingType := strings.ToUpper(record.Type)
			content := recordData["content"].(string)
			priority, _ := recordData["priority"].(int)
			if existingType != recordType {
				if existingType == "CNAME" || recordType == "CNAME" {
					return "conflict", &CloudflareDNSConflictError{Hostname: hostname}
				}
				// MAIL FROM MX/SPF share one namespace. A conflict in either must
				// prevent creating the other record for someone else's bounce host.
				paired := false
				for _, companion := range companions {
					if strings.EqualFold(companion.Type, existingType) && strings.EqualFold(strings.TrimSuffix(companion.Name, "."), strings.TrimSuffix(hostname, ".")) {
						content, priority = companion.Value, companion.Priority
						paired = true
						break
					}
				}
				if !paired {
					continue
				}
			}
			same := record.Content == content
			if existingType == "MX" || existingType == "CNAME" {
				same = strings.EqualFold(strings.TrimSuffix(record.Content, "."), strings.TrimSuffix(content, "."))
			}
			if existingType == "MX" {
				same = same && record.Priority == priority
			}
			if existingType == "CNAME" && record.Proxied {
				same = false
			}
			if same {
				identical = identical || existingType == recordType
				continue
			}
			if existingType == "TXT" {
				// Unrelated TXT verification records may coexist with a policy.
				want := strings.ToLower(strings.Trim(content, "\" "))
				have := strings.ToLower(strings.Trim(record.Content, "\" "))
				if (strings.HasPrefix(want, "v=spf1") && !strings.HasPrefix(have, "v=spf1")) ||
					(strings.HasPrefix(want, "v=dmarc1") && !strings.HasPrefix(have, "v=dmarc1")) {
					continue
				}
			}
			return "conflict", &CloudflareDNSConflictError{Hostname: hostname}
		}
		if page >= existing.Info.TotalPages {
			break
		}
	}
	if identical {
		return "preserved", nil
	}

	jsonData, err := json.Marshal(recordData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal record data: %w", err)
	}

	url := fmt.Sprintf("%s/zones/%s/dns_records", cloudflareBaseURL, zoneID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result CloudflareCreateRecordResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.Success {
		if len(result.Errors) > 0 {
			// Check if record already exists
			if result.Errors[0].Code == 81057 {
				fmt.Printf("DNS record already exists: %s %s\n", recordType, hostname)
				return "preserved", nil
			}
			return "", fmt.Errorf("cloudflare API error: %s (code: %d)", result.Errors[0].Message, result.Errors[0].Code)
		}
		return "", fmt.Errorf("cloudflare API returned unsuccessful response")
	}

	fmt.Printf("Created Cloudflare DNS record: %s %s -> %s\n", recordType, hostname, value)
	return "created", nil
}
