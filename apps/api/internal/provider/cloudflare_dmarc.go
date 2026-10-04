package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Validate the selected zone before any record writes, including when the UI
// supplied an ID directly. Never let a stale selection provision another zone.
func CloudflareValidateZone(ctx context.Context, token, zoneID, domain string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cloudflareBaseURL+"/zones/"+url.PathEscape(zoneID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("could not verify the selected Cloudflare zone")
	}
	defer resp.Body.Close()
	var result struct {
		Success bool           `json:"success"`
		Result  CloudflareZone `json:"result"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil || resp.StatusCode != 200 || !result.Success {
		return fmt.Errorf("could not verify the selected Cloudflare zone")
	}
	zone := dnsName(result.Result.Name)
	domain = dnsName(domain)
	if zone == "" || (domain != zone && !strings.HasSuffix(domain, "."+zone)) {
		return fmt.Errorf("selected Cloudflare zone does not contain this domain")
	}
	return nil
}

func cloudflareDMARCRecords(ctx context.Context, token, zoneID, hostname string) ([]cloudflareDNSRecord, error) {
	var records []cloudflareDNSRecord
	for page := 1; page <= 10; page++ {
		query := url.Values{"name": {hostname}, "page": {fmt.Sprint(page)}, "per_page": {"100"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cloudflareBaseURL+"/zones/"+url.PathEscape(zoneID)+"/dns_records?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return nil, fmt.Errorf("could not inspect Cloudflare DMARC records; no policy was added")
		}
		var result struct {
			Success bool                  `json:"success"`
			Result  []cloudflareDNSRecord `json:"result"`
			Info    struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || !result.Success {
			return nil, fmt.Errorf("could not inspect Cloudflare DMARC records; no policy was added")
		}
		for _, rec := range result.Result {
			if dnsName(rec.Name) == hostname {
				records = append(records, rec)
			}
		}
		if page >= result.Info.TotalPages {
			return records, nil
		}
	}
	return nil, fmt.Errorf("Cloudflare DMARC inspection exceeded its page limit; review records manually")
}

func cloudflareDMARCPolicy(records []cloudflareDNSRecord) (*dmarcPolicy, error) {
	var txt []string
	for _, rec := range records {
		if strings.EqualFold(rec.Type, "CNAME") {
			return nil, fmt.Errorf("DMARC uses CNAME delegation; preserve it and review the delegated policy")
		}
		if strings.EqualFold(rec.Type, "TXT") {
			txt = append(txt, rec.Content)
		}
	}
	return dmarcRecordSet(txt)
}

// CloudflareEnsureDMARC creates only after both public discovery and provider
// inspection establish absence. It never updates/deletes existing DNS. The
// service serializes Mailat callers; postflight detects external DNS races.
func CloudflareEnsureDMARC(ctx context.Context, token, zoneID, domain string, resolver DMARCResolver) (string, DMARCInspection, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	domain = dnsName(domain)
	out := DMARCInspection{Status: "unknown", Hostname: "_dmarc." + domain, CheckedAt: time.Now().UTC(), SuggestedValue: DefaultDMARCValue}
	fail := func(status string, err error) (string, DMARCInspection, error) {
		out.Status = "unknown"
		if status == "conflict" {
			out.Status = "conflict"
		}
		out.CanCreate = false
		out.Verified = false
		out.Reason = err.Error()
		return status, out, err
	}
	if err := CloudflareValidateZone(ctx, token, zoneID, domain); err != nil {
		return fail("failed", err)
	}
	records, err := cloudflareDMARCRecords(ctx, token, zoneID, out.Hostname)
	if err != nil {
		return fail("failed", err)
	}
	existing, err := cloudflareDMARCPolicy(records)
	if err != nil {
		return fail("conflict", err)
	}
	if existing != nil {
		out.Status = "existing"
		out.PolicyHostname = out.Hostname
		out.Value = existing.value
		out.Policy = strings.ToLower(existing.tags["p"])
		if strings.EqualFold(existing.tags["t"], "y") {
			switch out.Policy {
			case "reject":
				out.Policy = "quarantine"
			case "quarantine":
				out.Policy = "none"
			}
		}
		out.Reason = "Existing Cloudflare DMARC policy was preserved unchanged; use Check DMARC to confirm public DNS."
		return "preserved", out, nil
	}
	out = InspectDMARC(ctx, domain, resolver)
	if out.Verified {
		return "preserved", out, nil
	}
	if !out.CanCreate {
		status := "conflict"
		if out.Status == "unknown" {
			status = "failed"
		}
		return status, out, fmt.Errorf("%s", out.Reason)
	}
	// The ordinary create helper performs a second complete provider preflight
	// immediately before POST and understands alternate valid DMARC policies.
	status, err := CloudflareCreateDNSRecord(ctx, token, zoneID, "TXT", out.Hostname, DefaultDMARCValue)
	if err != nil {
		if status == "conflict" {
			return fail("conflict", err)
		}
		return fail("failed", fmt.Errorf("DMARC setup could not be completed; recheck before retrying: %w", err))
	}
	records, err = cloudflareDMARCRecords(ctx, token, zoneID, out.Hostname)
	if err != nil {
		return fail("failed", fmt.Errorf("DMARC write completed but its final state could not be confirmed; recheck before retrying"))
	}
	existing, err = cloudflareDMARCPolicy(records)
	if err != nil {
		return fail("conflict", fmt.Errorf("DMARC changed during setup: %w; no records were removed", err))
	}
	if existing == nil {
		return fail("failed", fmt.Errorf("DMARC write completed but no policy was returned; recheck before retrying"))
	}
	out.Status = "existing"
	out.CanCreate = false
	out.Verified = false
	out.PolicyHostname = out.Hostname
	out.Value = existing.value
	out.Policy = strings.ToLower(existing.tags["p"])
	if strings.EqualFold(existing.tags["t"], "y") {
		switch out.Policy {
		case "reject":
			out.Policy = "quarantine"
		case "quarantine":
			out.Policy = "none"
		}
	}
	out.Reason = "DMARC is configured in Cloudflare; use Check DMARC after DNS propagation."
	return status, out, nil
}
