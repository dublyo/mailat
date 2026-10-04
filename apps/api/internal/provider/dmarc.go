package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultDMARCValue = "v=DMARC1; p=quarantine;"

// DMARCInspection describes published policy, never merely our suggested value.
type DMARCInspection struct {
	Status         string    `json:"status"`
	Hostname       string    `json:"hostname"`
	PolicyHostname string    `json:"policyHostname"`
	Value          string    `json:"value"`
	Policy         string    `json:"policy"`
	Reason         string    `json:"reason"`
	CheckedAt      time.Time `json:"checkedAt"`
	CanCreate      bool      `json:"canCreate"`
	Verified       bool      `json:"verified"`
	SuggestedValue string    `json:"suggestedValue"`
}

type DMARCResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
	LookupCNAME(context.Context, string) (string, error)
}

type dmarcPolicy struct {
	value string
	tags  map[string]string
}

// Provider APIs may represent one TXT RR as several quoted character strings.
// Concatenate chunks within that RR, never separate records in the RRset.
func normalizeDMARCTXT(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, `"`) {
		return value, nil
	}
	var joined strings.Builder
	for value != "" {
		if value[0] != '"' {
			return "", fmt.Errorf("invalid quoted TXT value")
		}
		end := 1
		for end < len(value) {
			if value[end] == '\\' {
				end += 2
				continue
			}
			if value[end] == '"' {
				break
			}
			end++
		}
		if end >= len(value) {
			return "", fmt.Errorf("unterminated TXT string")
		}
		chunk, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", err
		}
		joined.WriteString(chunk)
		value = strings.TrimSpace(value[end+1:])
	}
	return joined.String(), nil
}

func parseDMARCRecord(value string) (*dmarcPolicy, error) {
	normalized, err := normalizeDMARCTXT(value)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(normalized, ";")
	tags := map[string]string{}
	known := "|v|p|sp|np|adkim|aspf|rua|ruf|fo|psd|t|"
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" && i == len(parts)-1 {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if !ok || key == "" || val == "" {
			return nil, fmt.Errorf("invalid DMARC tag")
		}
		for _, ch := range key {
			if ch < 'a' || ch > 'z' {
				return nil, fmt.Errorf("invalid DMARC tag name")
			}
		}
		for _, ch := range val {
			if ch < 32 || ch > 126 {
				return nil, fmt.Errorf("invalid DMARC tag value")
			}
		}
		if i == 0 && (key != "v" || val != "DMARC1") {
			return nil, fmt.Errorf("DMARC must begin with v=DMARC1")
		}
		if strings.Contains(known, "|"+key+"|") {
			if _, exists := tags[key]; exists {
				return nil, fmt.Errorf("duplicate DMARC %s tag", key)
			}
			tags[key] = val
		}
	}
	if tags["v"] != "DMARC1" {
		return nil, fmt.Errorf("missing DMARC version")
	}
	// RFC 9989 makes p optional; an otherwise valid record defaults to none.
	if _, exists := tags["p"]; !exists {
		tags["p"] = "none"
	}
	for _, key := range []string{"p", "sp", "np"} {
		val, exists := tags[key]
		if exists && !strings.EqualFold(val, "none") && !strings.EqualFold(val, "quarantine") && !strings.EqualFold(val, "reject") {
			return nil, fmt.Errorf("invalid DMARC %s policy", key)
		}
	}
	for key, allowed := range map[string]string{"adkim": "|r|s|", "aspf": "|r|s|", "psd": "|y|n|u|", "t": "|y|n|"} {
		if val, ok := tags[key]; ok && !strings.Contains(allowed, "|"+strings.ToLower(val)+"|") {
			return nil, fmt.Errorf("invalid DMARC %s tag", key)
		}
	}
	if val, ok := tags["fo"]; ok {
		seen := map[string]bool{}
		for _, option := range strings.Split(strings.ToLower(val), ":") {
			if !strings.Contains("|0|1|d|s|", "|"+option+"|") || option == "" || seen[option] {
				return nil, fmt.Errorf("invalid DMARC fo tag")
			}
			seen[option] = true
		}
		if seen["0"] && seen["1"] {
			return nil, fmt.Errorf("conflicting DMARC fo options")
		}
	}
	for _, key := range []string{"rua", "ruf"} {
		if val, ok := tags[key]; ok {
			for _, destination := range strings.Split(val, ",") {
				destination = strings.TrimSpace(destination)
				if base, suffix, sized := strings.Cut(destination, "!"); sized {
					if suffix == "" {
						return nil, fmt.Errorf("invalid DMARC reporting size")
					}
					digits := strings.TrimRight(strings.ToLower(suffix), "kmgt")
					if _, err := strconv.ParseUint(digits, 10, 64); err != nil || len(suffix)-len(digits) > 1 {
						return nil, fmt.Errorf("invalid DMARC reporting size")
					}
					destination = base
				}
				u, err := url.Parse(destination)
				if err != nil || u.Scheme == "" || (u.Opaque == "" && u.Host == "") || strings.ContainsAny(destination, " \t\r\n") {
					return nil, fmt.Errorf("invalid DMARC reporting URI")
				}
			}
		}
	}
	return &dmarcPolicy{value: value, tags: tags}, nil
}

// Invalid DMARC-looking records block provisioning rather than being mistaken
// for absence. Unrelated verification TXT records can coexist safely.
func dmarcRecordSet(records []string) (*dmarcPolicy, error) {
	var found *dmarcPolicy
	for _, raw := range records {
		normalized, err := normalizeDMARCTXT(raw)
		if err != nil {
			return nil, fmt.Errorf("could not parse existing TXT record")
		}
		looksLikePolicy := false
		for _, field := range strings.Split(normalized, ";") {
			key, val, ok := strings.Cut(field, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			if ok && ((key == "v" && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(val)), "DMARC")) || key == "p" || key == "sp" || key == "np") {
				looksLikePolicy = true
			}
		}
		if !looksLikePolicy {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple DMARC records exist; retain only one policy with your DNS administrator")
		}
		found, err = parseDMARCRecord(raw)
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

func dnsAbsent(err error) bool {
	var e *net.DNSError
	return errors.As(err, &e) && e.IsNotFound && !e.IsTimeout && !e.IsTemporary
}
func dnsName(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

// InspectDMARC follows the RFC 9989 bounded tree walk. A direct policy wins;
// otherwise psd markers select the boundary, or the highest discovered policy.
// Ambiguous DNS is deliberately a provisioning stop, not permission to create.
func InspectDMARC(ctx context.Context, domain string, resolver DMARCResolver) DMARCInspection {
	domain = dnsName(domain)
	out := DMARCInspection{Status: "unknown", Hostname: "_dmarc." + domain, CheckedAt: time.Now().UTC(), SuggestedValue: DefaultDMARCValue}
	if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, " /\\\t\r\n:@") {
		out.Reason = "Invalid domain name."
		return out
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	type candidate struct {
		hostname  string
		policy    *dmarcPolicy
		delegated bool
	}
	var selected *candidate
	found := map[string]*candidate{}
	names := []string{domain}
	labels := strings.Split(domain, ".")
	if len(labels) > 8 {
		labels = labels[len(labels)-7:]
	} else if len(labels) > 1 {
		labels = labels[1:]
	} else {
		labels = nil
	}
	for len(labels) > 0 {
		names = append(names, strings.Join(labels, "."))
		labels = labels[1:]
	}
	apply := func(c *candidate, inherited bool) DMARCInspection {
		out.Status = "existing"
		if inherited {
			out.Status = "inherited"
		}
		out.PolicyHostname = c.hostname
		out.Value = c.policy.value
		out.Policy = strings.ToLower(c.policy.tags["p"])
		out.Verified = true
		if inherited {
			if sp := c.policy.tags["sp"]; sp != "" {
				out.Policy = strings.ToLower(sp)
			}
			// Domain existence rules vary between receivers. Never create a new policy
			// when np changes the inherited result and existence is not established.
			if np := c.policy.tags["np"]; np != "" && !strings.EqualFold(np, out.Policy) {
				out.Status = "conflict"
				out.Verified = false
				out.Reason = "Inherited np policy differs for nonexistent domains; review domain existence with your DNS administrator."
				return out
			}
		}
		if strings.EqualFold(c.policy.tags["t"], "y") {
			switch out.Policy {
			case "reject":
				out.Policy = "quarantine"
			case "quarantine":
				out.Policy = "none"
			}
		}
		out.Reason = "Existing DMARC policy is preserved."
		if inherited {
			out.Reason = "Inherited DMARC policy is preserved; no overriding record will be added."
		}
		if c.delegated {
			out.Reason += " DNS delegates this policy through a CNAME; the delegation will not be changed."
		}
		return out
	}
	for i, name := range names {
		hostname := "_dmarc." + name
		cname, cnameErr := resolver.LookupCNAME(ctx, hostname)
		if cnameErr != nil && !dnsAbsent(cnameErr) {
			out.Reason = "DNS delegation lookup failed; retry before adding DMARC."
			return out
		}
		delegated := cnameErr == nil && dnsName(cname) != "" && dnsName(cname) != hostname
		records, err := resolver.LookupTXT(ctx, hostname)
		if err != nil && !dnsAbsent(err) {
			out.Reason = "DMARC DNS lookup failed; retry before adding a record."
			return out
		}
		p, err := dmarcRecordSet(records)
		if err != nil {
			out.Status = "conflict"
			out.PolicyHostname = hostname
			out.Reason = err.Error()
			return out
		}
		if p == nil {
			if delegated {
				out.Status = "conflict"
				out.PolicyHostname = hostname
				out.Reason = "DMARC is delegated through CNAME but no valid policy could be resolved; preserve the delegation and review it."
				return out
			}
			continue
		}
		current := &candidate{hostname: hostname, policy: p, delegated: delegated}
		found[name] = current
		if i == 0 {
			return apply(current, false)
		}
		selected = current
		switch strings.ToLower(p.tags["psd"]) {
		case "n":
			return apply(current, true)
		case "y":
			// The organizational domain is one label below this PSD. Its own record
			// takes precedence over the PSD fallback, if the walk found one there.
			domainLabels := strings.Split(domain, ".")
			suffixLabels := strings.Split(name, ".")
			below := strings.Join(domainLabels[len(domainLabels)-len(suffixLabels)-1:], ".")
			if own := found[below]; own != nil {
				selected = own
			}
			return apply(selected, true)
		}
	}
	if selected != nil {
		return apply(selected, true)
	}
	out.Status = "absent"
	out.CanCreate = true
	out.Reason = "No applicable DMARC policy was found. Quarantine applies to all senders using this domain."
	return out
}
