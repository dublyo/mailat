package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/provider"
)

// Readiness item states. Receiving is optional and reports off until enabled.
const (
	ReadinessOK        = "ok"
	ReadinessMissing   = "missing"
	ReadinessPending   = "pending"
	ReadinessAttention = "attention"
	ReadinessUnknown   = "unknown"
	ReadinessOff       = "off"
)

// DomainReadinessItem is one line of the API sending checklist. Fix names the
// action that resolves it: verify, dmarc, setup_sending, create_identity or
// receiving; empty when nothing is needed or nothing can be done from Mailat.
type DomainReadinessItem struct {
	Key      string `json:"key"` // verified, dmarc, sending_resources, sending_identity or receiving.
	Label    string `json:"label"`
	Status   string `json:"status"` // ok, missing, pending, attention, unknown or off.
	State    string `json:"state"`  // Item-specific detail, e.g. published, inherited or missing for DMARC.
	Optional bool   `json:"optional"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
	Value    string `json:"value"` // A copyable value, such as the DMARC record or the caller's sending address.
}

// DomainReadiness says whether the caller can send through the API from this
// domain. Ready needs every required item ok; receiving never blocks it.
type DomainReadiness struct {
	DomainUUID        string                `json:"domainUuid"`
	Domain            string                `json:"domain"`
	Ready             bool                  `json:"ready"`
	Items             []DomainReadinessItem `json:"items"`
	SuggestedIdentity string                `json:"suggestedIdentity"` // noreply@<domain> while that address is free, else empty.
	AutomaticSetup    bool                  `json:"automaticSetup"`    // The one-time setup after verification is still running; re-read shortly.
	CheckedAt         time.Time             `json:"checkedAt"`
}

// ReadinessOptions describe the reader: Admin owners and admins (or keys they
// own) get the fix wording for themselves; Refresh re-checks DNS now.
type ReadinessOptions struct {
	Admin   bool
	Refresh bool
}

// readyAutomationWindow is how long after verification the checklist reports
// the one-time setup as running instead of offering manual fixes.
const readyAutomationWindow = 2 * time.Minute

// dmarcCacheTTL keeps the checklist from running a live DMARC lookup for every
// card on every render. A Re-check (refresh) bypasses it.
const dmarcCacheTTL = 60 * time.Second

type dmarcInspectionCache struct {
	mu      sync.Mutex
	entries map[string]dmarcCacheEntry
}

type dmarcCacheEntry struct {
	inspection provider.DMARCInspection
	at         time.Time
}

func (c *dmarcInspectionCache) put(name string, inspection provider.DMARCInspection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) > 1000 {
		c.entries = map[string]dmarcCacheEntry{}
	}
	c.entries[name] = dmarcCacheEntry{inspection: inspection, at: time.Now()}
}

func (c *dmarcInspectionCache) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, name)
}

// inspectDMARC returns a recent definite DMARC answer from the cache, or looks
// it up. Failed (unknown) lookups are not cached so the next read retries.
func (s *DomainService) inspectDMARC(ctx context.Context, name string, refresh bool) provider.DMARCInspection {
	c := &s.dmarcCache
	c.mu.Lock()
	if e, ok := c.entries[name]; ok {
		age := time.Since(e.at)
		if age <= dmarcCacheTTL && (!refresh || age < mxRefreshMinAge) {
			c.mu.Unlock()
			return e.inspection
		}
	}
	c.mu.Unlock()
	return s.rememberDMARC(name, provider.InspectDMARC(ctx, name, s.dmarcResolver))
}

func (s *DomainService) rememberDMARC(name string, inspection provider.DMARCInspection) provider.DMARCInspection {
	if inspection.Status == "unknown" {
		s.dmarcCache.forget(name)
	} else {
		s.dmarcCache.put(name, inspection)
	}
	return inspection
}

// GetDomainReadiness builds the checklist for userID (the caller, or an API
// key's owner) on one of the organization's domains. It only reads state.
func (s *DomainService) GetDomainReadiness(ctx context.Context, orgID, userID int64, domainUUID string, opts ReadinessOptions) (*DomainReadiness, error) {
	if _, err := uuid.Parse(domainUUID); err != nil {
		return nil, ErrDomainNotFound
	}
	var domainID int64
	var name, status, setupError string
	var sesVerified, automationRecent bool
	var createdBy sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,name,status,COALESCE(ses_verified,false),sending_setup_error,created_by,
		COALESCE(email_provider,'ses')='ses' AND COALESCE(ready_automation_at > now() - $3::int * interval '1 second', ready_automation_at IS NULL AND verified_at > now() - $3::int * interval '1 second', false)
		FROM domains WHERE uuid=$1 AND org_id=$2`, domainUUID, orgID, int(readyAutomationWindow/time.Second)).
		Scan(&domainID, &name, &status, &sesVerified, &setupError, &createdBy, &automationRecent)
	if err == sql.ErrNoRows {
		return nil, ErrDomainNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load domain: %w", err)
	}
	out := &DomainReadiness{DomainUUID: domainUUID, Domain: name, CheckedAt: time.Now().UTC()}
	verified := status == "active" && sesVerified

	item := DomainReadinessItem{Key: "verified", Label: "Domain verified", Status: ReadinessOK, State: "verified", Detail: "The domain is active and verified with SES."}
	if !verified {
		item.Status, item.State, item.Fix = ReadinessMissing, "unverified", "verify"
		item.Detail = "Publish the sending DNS records, then choose Verify."
	}
	out.Items = append(out.Items, item)

	dmarc := s.inspectDMARC(ctx, name, opts.Refresh)
	item = DomainReadinessItem{Key: "dmarc", Label: "DMARC policy", Detail: dmarc.Reason}
	switch dmarc.Status {
	case "existing":
		item.Status, item.State, item.Value = ReadinessOK, "published", dmarc.Value
		item.Detail = "A DMARC policy is published."
	case "inherited":
		item.Status, item.State, item.Value = ReadinessOK, "inherited", dmarc.Value
		item.Detail = "Inherited from " + strings.TrimPrefix(dmarc.PolicyHostname, "_dmarc.") + "."
	case "absent":
		item.Status, item.State, item.Fix = ReadinessMissing, "missing", "dmarc"
		item.Value = dmarc.SuggestedValue
		item.Detail = "Publish TXT _dmarc." + name + " with the value shown, or add it with Cloudflare."
	case "conflict":
		item.Status, item.State, item.Fix = ReadinessAttention, "conflict", "dmarc"
	default:
		item.Status, item.State = ReadinessUnknown, "unknown"
		if item.Detail == "" {
			item.Detail = "The DMARC lookup did not finish; choose Re-check."
		}
	}
	out.Items = append(out.Items, item)

	sending, err := s.GetSendingStatus(ctx, orgID, domainUUID)
	if err != nil {
		return nil, err
	}
	// The one-time setup after verification is still running: it has been
	// claimed (or is about to be) and has neither configured feedback nor
	// recorded a failure yet.
	automating := verified && automationRecent && s.cfg != nil && s.cfg.EmailProvider == "ses" &&
		!sending.FeedbackConfigured && !sending.FeedbackReady && setupError == ""
	out.AutomaticSetup = automating
	item = DomainReadinessItem{Key: "sending_resources", Label: "Sending resources", Status: ReadinessOK, State: "ready", Detail: "Attachment storage and delivery feedback are ready."}
	switch {
	case sending.FeedbackReady:
	case automating:
		item.Status, item.State = ReadinessPending, "automatic_setup"
		item.Detail = "Setting up automatically…"
	case sending.FeedbackConfigured && sending.SubscriptionStatus == "pending":
		// Nothing to fix: SES confirms the subscription on its own. Re-check shows it.
		item.Status, item.State = ReadinessPending, "awaiting_confirmation"
		item.Detail = "Waiting for SES to confirm the delivery feedback subscription."
	case setupError != "":
		item.Status, item.State, item.Detail, item.Fix = ReadinessAttention, "needs_attention", setupError, "setup_sending"
	case !verified:
		item.Status, item.State = ReadinessMissing, "not_set_up"
		item.Detail = "Attachment storage and delivery feedback are set up after the domain is verified."
	default:
		item.Status, item.State, item.Fix = ReadinessMissing, "not_set_up", "setup_sending"
		item.Detail = "Set up attachment storage and delivery feedback."
	}
	if !verified {
		item.Fix = ""
	}
	out.Items = append(out.Items, item)

	var own string
	err = s.db.QueryRowContext(ctx, `SELECT email FROM identities WHERE domain_id=$1 AND user_id=$2 AND kind='personal' AND can_send ORDER BY is_default DESC,id LIMIT 1`, domainID, userID).Scan(&own)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	free, err := addressFree(ctx, s.db, orgID, "noreply@"+name, userID)
	if err != nil {
		return nil, err
	}
	if free {
		out.SuggestedIdentity = "noreply@" + name
	}
	item = DomainReadinessItem{Key: "sending_identity", Label: "Your sending identity", Status: ReadinessOK, State: "present", Value: own, Detail: "You send from " + name + " as " + own + "."}
	if own == "" {
		item.Status, item.State, item.Value = ReadinessMissing, "missing", out.SuggestedIdentity
		target := int64(0)
		if automating && free {
			if target, err = readyAutomationUser(ctx, s.db, orgID, createdBy); err != nil {
				return nil, err
			}
		}
		switch {
		case target != 0 && target == userID:
			item.Status, item.State = ReadinessPending, "automatic_setup"
			item.Detail = "Creating " + out.SuggestedIdentity + " for you automatically…"
		case !verified:
			item.Detail = "You have no sending identity on " + name + " yet. You can add one after the domain is verified."
		case !opts.Admin:
			item.Fix = "create_identity"
			item.Detail = "You have no sending identity on " + name + ". Ask an organization owner or admin to add one for you."
		case free:
			item.Fix = "create_identity"
			item.Detail = "You have no sending identity on " + name + ". Create " + out.SuggestedIdentity + " or use another address."
		default:
			item.Fix = "create_identity"
			item.Detail = "You have no sending identity on " + name + ". Add one with Add Identity."
		}
	}
	out.Items = append(out.Items, item)

	receiving, err := s.GetReceivingStatus(ctx, orgID, domainUUID, opts.Refresh)
	if err != nil {
		return nil, err
	}
	item = DomainReadinessItem{Key: "receiving", Label: "Receiving (optional)", Optional: true, State: receiving.MXStatus, Detail: receiving.Reason, Value: receiving.MXRecord.Value, Fix: "receiving"}
	switch receiving.MXStatus {
	case MXStatusPublished:
		item.Status, item.Fix = ReadinessOK, ""
		item.Detail = "Receiving is on and the MX record is published."
	case MXStatusNotEnabled:
		// No copyable root MX while receiving is off: publishing one would
		// move the domain's mail away from its current inbox provider.
		item.Status, item.Value = ReadinessOff, ""
		item.Detail = "Receiving is off. Sending works without it."
	case MXStatusMissing:
		item.Status = ReadinessMissing
	case MXStatusConflict:
		item.Status = ReadinessAttention
	default:
		item.Status = ReadinessUnknown
	}
	out.Items = append(out.Items, item)

	out.Ready = true
	for _, it := range out.Items {
		if !it.Optional && it.Status != ReadinessOK {
			out.Ready = false
		}
	}
	return out, nil
}

// noSendingIdentityMessage tells an API caller exactly how to fix a send that
// has no identity of theirs on the From domain. Only owners and admins can add
// identities, so members are told whom to ask.
func noSendingIdentityMessage(domain string, admin bool) string {
	if !admin {
		return fmt.Sprintf("you have no sending identity on %[1]s; ask an organization owner or admin to add one for you (e.g. noreply@%[1]s) under Domains → %[1]s → Add identity", domain)
	}
	return fmt.Sprintf("you have no sending identity on %[1]s; add one (e.g. noreply@%[1]s) under Domains → %[1]s → Add identity", domain)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// addressFree reports whether addr is not an identity, a send-as alias, the
// address of an open invite or mailbox setup link, or another person's Mailat
// login (in any organization). Mailboxes are identities. forUser's own login
// address does not count as taken.
func addressFree(ctx context.Context, q queryRower, orgID int64, addr string, forUser int64) (bool, error) {
	var taken bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1)
		OR EXISTS(SELECT 1 FROM identity_send_aliases WHERE address=$1)
		OR EXISTS(SELECT 1 FROM org_invites WHERE org_id=$2 AND accepted_at IS NULL AND revoked_at IS NULL AND (email=$1 OR delivery_email=$1))
		OR EXISTS(SELECT 1 FROM users WHERE lower(email)=$1 AND id<>$3)`, addr, orgID, forUser).Scan(&taken)
	return !taken, err
}

// readyAutomationUser is who gets the noreply identity: the person who added
// the domain while they are still an active staff member, otherwise the
// organization owner, never another admin or member. 0 means nobody.
func readyAutomationUser(ctx context.Context, q queryRower, orgID int64, createdBy sql.NullInt64) (int64, error) {
	var userID int64
	err := q.QueryRowContext(ctx, `SELECT id FROM users WHERE org_id=$1 AND status='active' AND removed_at IS NULL
		AND ((id=$2 AND role IN ('owner','admin','member')) OR role='owner') ORDER BY (id=$2) DESC,id LIMIT 1`, orgID, createdBy.Int64).Scan(&userID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return userID, err
}

// afterDomainCheck runs the one-time ready automation when a check has just
// left the domain active and SES verified. It returns at once; the work runs
// after the caller's writes have committed.
func (s *DomainService) afterDomainCheck(domainID int64) {
	if s.cfg == nil || s.cfg.EmailProvider != "ses" {
		return
	}
	run := s.async
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("domain %d ready automation panicked: %v", domainID, v)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.runReadyAutomation(ctx, domainID)
	})
}

// runReadyAutomation claims the domain's single automation run, gives the
// person who added it a noreply identity (a quick database step, so the
// checklist shows it at once), then sets up sending resources.
// Failures are recorded for the sending status and never retried here; the
// manual Set up action still works. DNS (root MX, SPF) is never touched.
func (s *DomainService) runReadyAutomation(ctx context.Context, domainID int64) {
	var orgID int64
	var name string
	var createdBy sql.NullInt64
	err := s.db.QueryRowContext(ctx, `UPDATE domains SET ready_automation_at=now() WHERE id=$1 AND status='active' AND COALESCE(ses_verified,false)
		AND COALESCE(email_provider,'ses')='ses' AND ready_automation_at IS NULL RETURNING org_id,name,created_by`, domainID).Scan(&orgID, &name, &createdBy)
	if err == sql.ErrNoRows {
		return
	}
	if err != nil {
		log.Printf("domain %d ready automation: claim failed: %v", domainID, err)
		return
	}
	if err = s.ensureOwnerIdentity(ctx, orgID, domainID, name, createdBy); err != nil {
		log.Printf("domain %d ready automation: owner identity skipped: %v", domainID, err)
	}
	if _, err = s.EnsureDomainSendingResources(ctx, orgID, domainID); err != nil {
		reason := "Automatic sending setup did not finish; choose Retry sending setup to try again"
		var validation *provider.MailValidationError
		if errors.As(err, &validation) {
			reason = "Automatic sending setup did not finish: " + validation.Error()
		}
		if _, e := s.db.ExecContext(ctx, `UPDATE domains SET sending_feedback_ready=false,sending_setup_error=$1 WHERE id=$2 AND org_id=$3`, reason, domainID, orgID); e != nil {
			log.Printf("domain %d ready automation: record setup error: %v", domainID, e)
		}
	}
}

// ensureOwnerIdentity creates noreply@<domain> for the user who added the
// domain (or the organization owner) when they have no personal identity on
// it, the address is free and the identity cap allows it. Otherwise it does
// nothing; the readiness checklist shows the missing identity.
func (s *DomainService) ensureOwnerIdentity(ctx context.Context, orgID, domainID int64, domainName string, createdBy sql.NullInt64) error {
	userID, err := readyAutomationUser(ctx, s.db, orgID, createdBy)
	if err != nil || userID == 0 {
		return err
	}
	return s.createOwnerIdentity(ctx, orgID, domainID, domainName, userID)
}

// createOwnerIdentity gives userID noreply@<domain> unless, under the locks,
// the user is no longer an active staff member, already has an identity on
// the domain, the address is taken or the identity cap is reached.
func (s *DomainService) createOwnerIdentity(ctx context.Context, orgID, domainID int64, domainName string, userID int64) error {
	addr := "noreply@" + strings.ToLower(domainName)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Same lock order as CreateIdentity: user, domain, then organization. The
	// user is re-checked under the lock: removed, disabled or demoted to a
	// mailbox since they were chosen means no identity.
	var role string
	err = tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id=$1 AND org_id=$2 AND status='active' AND removed_at IS NULL FOR UPDATE`, userID, orgID).Scan(&role)
	if err == sql.ErrNoRows || (err == nil && role != "owner" && role != "admin" && role != "member") {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT id FROM domains WHERE id=$1 FOR UPDATE`, domainID); err != nil {
		return err
	}
	var hasOwn, hasDefault bool
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE user_id=$1 AND domain_id=$2 AND kind='personal'),
		EXISTS(SELECT 1 FROM identities WHERE user_id=$1 AND kind='personal' AND is_default),
		(SELECT count(*) FROM identities WHERE user_id=$1 AND kind='personal')`, userID, domainID).Scan(&hasOwn, &hasDefault, &count); err != nil {
		return err
	}
	if hasOwn {
		return nil
	}
	free, err := addressFree(ctx, tx, orgID, addr, userID)
	if err != nil || !free {
		return err
	}
	if s.cfg == nil || !s.cfg.DisableAppLimits {
		var limit, total int
		if err = tx.QueryRowContext(ctx, `SELECT max_identities FROM organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&limit); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM identities i JOIN users u ON u.id=i.user_id WHERE u.org_id=$1`, orgID).Scan(&total); err != nil {
			return err
		}
		if limit > 0 && total >= limit {
			return nil
		}
	}
	var orgName string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(name,''),$2) FROM organizations WHERE id=$1`, orgID, domainName).Scan(&orgName); err != nil {
		return err
	}
	colors := []string{"#3B82F6", "#10B981", "#8B5CF6", "#F59E0B", "#EF4444", "#EC4899", "#06B6D4", "#84CC16"}
	_, err = tx.ExecContext(ctx, `INSERT INTO identities(uuid,user_id,domain_id,email,display_name,is_default,is_catch_all,color,password_hash,encrypted_password,quota_bytes,can_send,can_receive,kind,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,false,$7,'','',$8,true,true,'personal',now())`,
		uuid.New().String(), userID, domainID, addr, orgName, !hasDefault, colors[count%len(colors)], int64(1024*1024*1024))
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return nil // Taken concurrently: skip silently.
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
