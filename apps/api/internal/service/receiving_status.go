package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MXResolver looks up public MX records. net.DefaultResolver satisfies it;
// tests inject a fake so no real DNS query is made.
type MXResolver interface {
	LookupMX(context.Context, string) ([]*net.MX, error)
}

const (
	mxLookupTimeout = 3 * time.Second
	mxCacheTTL      = 60 * time.Second
)

// mxRefreshMinAge is how old a cached answer must be before a forced refresh
// queries DNS again, so a Re-check loop cannot hammer the resolver.
var mxRefreshMinAge = 2 * time.Second

// Receiving MX states. Published is only reported after a lookup found the
// SES inbound host as the preferred exchanger; a failed lookup is unknown.
const (
	MXStatusNotEnabled = "not_enabled"
	MXStatusMissing    = "missing"
	MXStatusPublished  = "published"
	MXStatusConflict   = "conflict"
	MXStatusUnknown    = "unknown"
)

// ReceivingMXRecord is the root MX a domain needs before SES accepts its mail.
type ReceivingMXRecord struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Name     string `json:"name"`  // "@" in most DNS dashboards.
	Value    string `json:"value"` // Priority and target, as stored with the domain.
	Priority int    `json:"priority"`
	Target   string `json:"target"`
}

// DomainReceivingStatus says whether a domain can receive mail. Sending works
// without it; mailboxes and inbound routing need receiving and a published MX.
type DomainReceivingStatus struct {
	DomainUUID string            `json:"domainUuid"`
	Domain     string            `json:"domain"`
	Enabled    bool              `json:"enabled"`
	MXRecord   ReceivingMXRecord `json:"mxRecord"`
	MXStatus   string            `json:"mxStatus"`   // not_enabled, missing, published, conflict or unknown.
	ExistingMX []string          `json:"existingMx"` // Public MX targets found at the domain root.
	Reason     string            `json:"reason"`
	CheckedAt  time.Time         `json:"checkedAt"`
}

type mxLookupResult struct {
	records []*net.MX
	err     error
	at      time.Time
}

type mxLookupCache struct {
	mu       sync.Mutex
	entries  map[string]mxLookupResult
	inflight map[string]*mxLookupCall
}

// mxLookupCall lets concurrent lookups for one name share a single query.
type mxLookupCall struct {
	done    chan struct{}
	records []*net.MX
	err     error
}

func (c *mxLookupCache) put(name string, r mxLookupResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]mxLookupResult{}
	}
	if len(c.entries) > 1000 {
		c.entries = map[string]mxLookupResult{}
	}
	c.entries[name] = r
}

func (c *mxLookupCache) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, name)
}

var (
	sesInboundMXPattern = regexp.MustCompile(`^(\d{1,5})\s+(inbound-smtp\.[a-z0-9-]+\.amazonaws\.com)\.?$`)
	awsRegionPattern    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
)

// receivingMXRecord returns the SES inbound MX for a domain: the value saved
// when receiving was enabled, else the organization's receiving region, else
// the configured AWS region.
func (s *DomainService) receivingMXRecord(ctx context.Context, orgID, domainID int64, domainName string) (ReceivingMXRecord, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domainName)), ".")
	rec := ReceivingMXRecord{Type: "MX", Host: host, Name: "@", Priority: 10}
	rows, err := s.db.QueryContext(ctx, `SELECT hostname, expected_value FROM domain_dns_records WHERE domain_id=$1 AND record_type='MX'`, domainID)
	if err != nil {
		return rec, err
	}
	defer rows.Close()
	for rows.Next() {
		var hostname, value string
		if err := rows.Scan(&hostname, &value); err != nil {
			return rec, err
		}
		if !isDomainRootHostname(hostname, host) {
			continue
		}
		if m := sesInboundMXPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(value))); m != nil {
			rec.Priority, _ = strconv.Atoi(m[1])
			rec.Target = m[2]
		}
	}
	if err := rows.Err(); err != nil {
		return rec, err
	}
	if rec.Target == "" {
		region := ""
		var stored sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT s3_region FROM receiving_configs WHERE org_id=$1`, orgID).Scan(&stored)
		if err != nil && err != sql.ErrNoRows {
			return rec, err
		}
		if stored.Valid && awsRegionPattern.MatchString(stored.String) {
			region = stored.String
		} else if s.cfg != nil && awsRegionPattern.MatchString(s.cfg.AWSRegion) {
			region = s.cfg.AWSRegion
		} else {
			region = "us-east-1"
		}
		rec.Target = "inbound-smtp." + region + ".amazonaws.com"
	}
	rec.Value = fmt.Sprintf("%d %s", rec.Priority, rec.Target)
	return rec, nil
}

func (s *DomainService) lookupMX(ctx context.Context, name string, refresh bool) ([]*net.MX, error) {
	now := time.Now()
	c := &s.mxCache
	c.mu.Lock()
	if r, ok := c.entries[name]; ok && now.Sub(r.at) <= mxCacheTTL && (!refresh || now.Sub(r.at) < mxRefreshMinAge) {
		c.mu.Unlock()
		return r.records, r.err
	}
	if call, ok := c.inflight[name]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.records, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &mxLookupCall{done: make(chan struct{})}
	if c.inflight == nil {
		c.inflight = map[string]*mxLookupCall{}
	}
	c.inflight[name] = call
	c.mu.Unlock()

	var resolver MXResolver = net.DefaultResolver
	if s.mxResolver != nil {
		resolver = s.mxResolver
	}
	// The shared query outlives one caller's cancellation, bounded by the timeout.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mxLookupTimeout)
	defer cancel()
	// Waiters are always released, even if the resolver panics.
	defer func() {
		c.mu.Lock()
		delete(c.inflight, name)
		c.mu.Unlock()
		close(call.done)
	}()
	// Fully qualified, so resolv.conf search domains never apply.
	call.records, call.err = resolver.LookupMX(lookupCtx, strings.TrimSuffix(name, ".")+".")
	// A definite answer (records or "no such record") is cached; transient
	// failures are not, so a Re-check retries them.
	var dnsErr *net.DNSError
	if call.err == nil || (errors.As(call.err, &dnsErr) && dnsErr.IsNotFound) {
		c.put(name, mxLookupResult{records: call.records, err: call.err, at: time.Now()})
	} else {
		c.forget(name)
	}
	return call.records, call.err
}

// classifyReceivingMX compares the public root MX set with the SES inbound host.
func classifyReceivingMX(records []*net.MX, lookupErr error, target string) (string, []string, string) {
	existing := make([]string, 0, len(records))
	if lookupErr != nil {
		var dnsErr *net.DNSError
		if errors.As(lookupErr, &dnsErr) && dnsErr.IsNotFound {
			return MXStatusMissing, existing, "No MX record is published at the domain root."
		}
		return MXStatusUnknown, existing, "The public MX lookup failed; try Re-check."
	}
	sorted := append([]*net.MX(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Pref < sorted[j].Pref })
	sesPref, otherPref, nullMX := -1, -1, true
	for _, mx := range sorted {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mx.Host)), ".")
		if host == "" {
			host = "." // RFC 7505 null MX: the domain accepts no mail.
		}
		existing = append(existing, host)
		nullMX = nullMX && host == "."
		if strings.EqualFold(host, target) {
			if sesPref < 0 || int(mx.Pref) < sesPref {
				sesPref = int(mx.Pref)
			}
		} else if otherPref < 0 || int(mx.Pref) < otherPref {
			otherPref = int(mx.Pref)
		}
	}
	switch {
	case len(existing) == 0:
		return MXStatusMissing, existing, "No MX record is published at the domain root."
	case nullMX:
		return MXStatusConflict, existing, "The domain publishes a null MX (it accepts no mail). Replace it with the Mailat MX to receive here."
	case sesPref >= 0 && (otherPref < 0 || sesPref < otherPref):
		return MXStatusPublished, existing, ""
	case sesPref >= 0:
		return MXStatusConflict, existing, "Another MX has the same or a better priority, so some mail goes elsewhere."
	default:
		return MXStatusConflict, existing, "The domain root MX points to another mail provider."
	}
}

// GetReceivingStatus reports a domain's receiving switch and its live root MX.
// It is read-only: nothing is published or changed.
func (s *DomainService) GetReceivingStatus(ctx context.Context, orgID int64, domainUUID string, refresh bool) (*DomainReceivingStatus, error) {
	if _, err := uuid.Parse(domainUUID); err != nil {
		return nil, ErrDomainNotFound
	}
	var id int64
	var name string
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT id, name, receiving_enabled FROM domains WHERE uuid=$1 AND org_id=$2`, domainUUID, orgID).Scan(&id, &name, &enabled)
	if err == sql.ErrNoRows {
		return nil, ErrDomainNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load domain: %w", err)
	}
	rec, err := s.receivingMXRecord(ctx, orgID, id, name)
	if err != nil {
		return nil, fmt.Errorf("failed to load receiving record: %w", err)
	}
	out := &DomainReceivingStatus{DomainUUID: domainUUID, Domain: rec.Host, Enabled: enabled, MXRecord: rec, CheckedAt: time.Now().UTC()}
	records, lookupErr := s.lookupMX(ctx, rec.Host, refresh)
	out.MXStatus, out.ExistingMX, out.Reason = classifyReceivingMX(records, lookupErr, rec.Target)
	if !enabled {
		out.MXStatus = MXStatusNotEnabled
		out.Reason = "Receiving is off. Sending works without it; mailboxes need receiving and this MX record."
	}
	return out, nil
}
