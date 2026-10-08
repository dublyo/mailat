package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
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
	CheckedAt         time.Time             `json:"checkedAt"`
}

// GetDomainReadiness builds the checklist for userID (the caller, or an API
// key's owner) on one of the organization's domains. It only reads state.
func (s *DomainService) GetDomainReadiness(ctx context.Context, orgID, userID int64, domainUUID string) (*DomainReadiness, error) {
	if _, err := uuid.Parse(domainUUID); err != nil {
		return nil, ErrDomainNotFound
	}
	var domainID int64
	var name, status, setupError string
	var sesVerified bool
	err := s.db.QueryRowContext(ctx, `SELECT id,name,status,COALESCE(ses_verified,false),sending_setup_error FROM domains WHERE uuid=$1 AND org_id=$2`, domainUUID, orgID).
		Scan(&domainID, &name, &status, &sesVerified, &setupError)
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

	dmarc := provider.InspectDMARC(ctx, name, s.dmarcResolver)
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
	}
	out.Items = append(out.Items, item)

	sending, err := s.GetSendingStatus(ctx, orgID, domainUUID)
	if err != nil {
		return nil, err
	}
	item = DomainReadinessItem{Key: "sending_resources", Label: "Sending resources", Status: ReadinessOK, State: "ready", Detail: "Attachment storage and delivery feedback are ready."}
	switch {
	case sending.FeedbackReady:
	case sending.FeedbackConfigured && sending.SubscriptionStatus == "pending":
		item.Status, item.State = ReadinessPending, "awaiting_confirmation"
		item.Detail = "Waiting for SES to confirm the delivery feedback subscription."
	case setupError != "":
		item.Status, item.State, item.Detail = ReadinessAttention, "needs_attention", setupError
	default:
		item.Status, item.State = ReadinessMissing, "not_set_up"
		item.Detail = "Set up attachment storage and delivery feedback."
	}
	if item.Status != ReadinessOK && verified {
		item.Fix = "setup_sending"
	}
	out.Items = append(out.Items, item)

	var own string
	err = s.db.QueryRowContext(ctx, `SELECT email FROM identities WHERE domain_id=$1 AND user_id=$2 AND kind='personal' AND can_send ORDER BY is_default DESC,id LIMIT 1`, domainID, userID).Scan(&own)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	free, err := addressFree(ctx, s.db, orgID, "noreply@"+name)
	if err != nil {
		return nil, err
	}
	if free {
		out.SuggestedIdentity = "noreply@" + name
	}
	item = DomainReadinessItem{Key: "sending_identity", Label: "Your sending identity", Status: ReadinessOK, State: "present", Value: own, Detail: "You send from " + name + " as " + own + "."}
	if own == "" {
		item.Status, item.State, item.Value = ReadinessMissing, "missing", out.SuggestedIdentity
		item.Detail = noSendingIdentityMessage(name)
		if verified {
			item.Fix = "create_identity"
		}
	}
	out.Items = append(out.Items, item)

	receiving, err := s.GetReceivingStatus(ctx, orgID, domainUUID, false)
	if err != nil {
		return nil, err
	}
	item = DomainReadinessItem{Key: "receiving", Label: "Receiving (optional)", Optional: true, State: receiving.MXStatus, Detail: receiving.Reason, Value: receiving.MXRecord.Value, Fix: "receiving"}
	switch receiving.MXStatus {
	case MXStatusPublished:
		item.Status, item.Fix = ReadinessOK, ""
		item.Detail = "Receiving is on and the MX record is published."
	case MXStatusNotEnabled:
		item.Status = ReadinessOff
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
// has no identity of theirs on the From domain.
func noSendingIdentityMessage(domain string) string {
	return fmt.Sprintf("you have no sending identity on %[1]s; add one (e.g. noreply@%[1]s) under Domains → %[1]s → Add identity", domain)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// addressFree reports whether addr is not an identity, a send-as alias or the
// address of an open invite or mailbox setup link. Mailboxes are identities.
func addressFree(ctx context.Context, q queryRower, orgID int64, addr string) (bool, error) {
	var taken bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1)
		OR EXISTS(SELECT 1 FROM identity_send_aliases WHERE address=$1)
		OR EXISTS(SELECT 1 FROM org_invites WHERE org_id=$2 AND accepted_at IS NULL AND revoked_at IS NULL AND (email=$1 OR delivery_email=$1))`, addr, orgID).Scan(&taken)
	return !taken, err
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

// runReadyAutomation claims the domain's single automation run, then sets up
// sending resources and gives the person who added it a noreply identity.
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
	if _, err = s.EnsureDomainSendingResources(ctx, orgID, domainID); err != nil {
		reason := "Automatic sending setup did not finish; choose Set up sending resources to retry"
		var validation *provider.MailValidationError
		if errors.As(err, &validation) {
			reason = "Automatic sending setup did not finish: " + validation.Error()
		}
		if _, e := s.db.ExecContext(ctx, `UPDATE domains SET sending_feedback_ready=false,sending_setup_error=$1 WHERE id=$2 AND org_id=$3`, reason, domainID, orgID); e != nil {
			log.Printf("domain %d ready automation: record setup error: %v", domainID, e)
		}
	}
	if err = s.ensureOwnerIdentity(ctx, orgID, domainID, name, createdBy); err != nil {
		log.Printf("domain %d ready automation: owner identity skipped: %v", domainID, err)
	}
}

// ensureOwnerIdentity creates noreply@<domain> for the user who added the
// domain (or the organization owner) when they have no personal identity on
// it, the address is free and the identity cap allows it. Otherwise it does
// nothing; the readiness checklist shows the missing identity.
func (s *DomainService) ensureOwnerIdentity(ctx context.Context, orgID, domainID int64, domainName string, createdBy sql.NullInt64) error {
	// The person who added the domain while they are still an active staff
	// member; otherwise the organization owner, never another admin or member.
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE org_id=$1 AND status='active' AND removed_at IS NULL
		AND ((id=$2 AND role IN ('owner','admin','member')) OR role='owner') ORDER BY (id=$2) DESC,id LIMIT 1`, orgID, createdBy.Int64).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	addr := "noreply@" + strings.ToLower(domainName)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Same lock order as CreateIdentity: user, domain, then organization.
	if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
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
	free, err := addressFree(ctx, tx, orgID, addr)
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
