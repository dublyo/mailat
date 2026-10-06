package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/lib/pq"
)

// campaignAudience is the single eligibility definition shared by the
// estimate, materialisation and the per-batch recheck.
type campaignAudience struct {
	orgID    int64
	listID   int64
	listType string   // static | dynamic
	segment  *segment // dynamic lists only
}

// audienceEstimate counts distinct addresses (case-insensitive), matching the
// de-duplication applied when recipients are materialised.
type audienceEstimate struct {
	ListType           string
	Eligible           int64
	ExcludedInactive   int64
	ExcludedSuppressed int64
}

// eligibleRecipient is a claimed recipient that passed the recheck, joined to
// fresh contact data for personalisation.
type eligibleRecipient struct {
	ID          int64
	ContactID   int64
	Email       string
	MessageUUID string
	FirstName   string
	LastName    string
	Attributes  map[string]any
}

// campaignSuppressedSQL is true when the address is on the org's marketing
// suppressions (hash match, survives GDPR erasure; see suppressedSQL) or the
// transactional suppression_list (bounces/complaints), in any letter case.
func campaignSuppressedSQL(orgExpr, emailExpr string) string {
	return "(" + suppressedSQL(orgExpr, emailExpr) +
		" OR EXISTS(SELECT 1 FROM suppression_list sl WHERE sl.org_id=" + orgExpr + " AND lower(sl.email)=lower(" + emailExpr + ")))"
}

// loadCampaignAudience reads the campaign list in the org. Stored dynamic rules
// that no longer validate return a SegmentError (400 / invalid_segment).
func loadCampaignAudience(ctx context.Context, q eventoutbox.DBTX, orgID, listID int64) (*campaignAudience, error) {
	a := &campaignAudience{orgID: orgID, listID: listID}
	var rules []byte
	err := q.QueryRowContext(ctx, `SELECT type, segment_rules FROM lists WHERE id=$1 AND org_id=$2`, listID, orgID).Scan(&a.listType, &rules)
	if err == sql.ErrNoRows {
		return nil, &provider.MailValidationError{Message: "campaign list not found"}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load campaign list: %w", err)
	}
	if a.listType != "dynamic" {
		a.listType = "static"
		return a, nil
	}
	if a.segment, err = parseSegment(ctx, q, orgID, rules); err != nil {
		return nil, err
	}
	return a, nil
}

// membershipSQL is true when contact c is in the static list or matches the segment.
func (a *campaignAudience) membershipSQL(args *sqlArgs) string {
	if a.segment != nil {
		return a.segment.predicate(args)
	}
	return "EXISTS(SELECT 1 FROM list_contacts lc JOIN lists l ON l.id=lc.list_id WHERE lc.contact_id=c.id AND lc.list_id=" +
		args.add(a.listID) + " AND l.org_id=c.org_id)"
}

// eligibilitySQL is the full predicate over contacts c: in the org, active,
// not suppressed in either table, and a list member / segment match.
func (a *campaignAudience) eligibilitySQL(args *sqlArgs) string {
	org := args.add(a.orgID)
	return "c.org_id=" + org + " AND c.status='active' AND NOT " + campaignSuppressedSQL("c.org_id", "c.email") +
		" AND " + a.membershipSQL(args)
}

func (a *campaignAudience) estimate(ctx context.Context, q eventoutbox.DBTX) (*audienceEstimate, error) {
	args := sqlArgs{}
	org := args.add(a.orgID)
	supp := campaignSuppressedSQL("c.org_id", "c.email")
	query := `SELECT
		COUNT(DISTINCT lower(c.email)) FILTER (WHERE c.status='active' AND NOT ` + supp + `),
		COUNT(DISTINCT lower(c.email)) FILTER (WHERE c.status<>'active'),
		COUNT(DISTINCT lower(c.email)) FILTER (WHERE c.status='active' AND ` + supp + `)
		FROM contacts c WHERE c.org_id=` + org + ` AND ` + a.membershipSQL(&args)
	est := &audienceEstimate{ListType: a.listType}
	if err := q.QueryRowContext(ctx, query, args...).Scan(&est.Eligible, &est.ExcludedInactive, &est.ExcludedSuppressed); err != nil {
		return nil, fmt.Errorf("failed to estimate audience: %w", err)
	}
	return est, nil
}

// estimateAudience loads the list and estimates its eligible audience.
func estimateAudience(ctx context.Context, q eventoutbox.DBTX, orgID, listID int64) (*audienceEstimate, error) {
	a, err := loadCampaignAudience(ctx, q, orgID, listID)
	if err != nil {
		return nil, err
	}
	return a.estimate(ctx, q)
}

// materialise snapshots the eligible audience into campaign_recipients and
// returns the rows inserted. ON CONFLICT without a target de-duplicates on both
// (campaign_id, contact_id) and (campaign_id, lower(email)); ORDER BY c.id makes
// the surviving contact deterministic. Run it in the prepare transaction.
func (a *campaignAudience) materialise(ctx context.Context, q eventoutbox.DBTX, campaignID int64) (int64, error) {
	args := sqlArgs{}
	cid := args.add(campaignID)
	query := `INSERT INTO campaign_recipients(campaign_id, org_id, contact_id, email)
		SELECT ` + cid + `::int, c.org_id, c.id, c.email FROM contacts c
		WHERE ` + a.eligibilitySQL(&args) + `
		ORDER BY c.id
		ON CONFLICT DO NOTHING`
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to materialise recipients: %w", err)
	}
	return res.RowsAffected()
}

// recheck re-evaluates claimed rows owned by runID just before sending. In one
// statement, rows that are no longer eligible become skipped (reason from the
// first failing check, quota_reserved cleared) and campaigns.skipped_count
// grows by the same amount; the eligible rows come back with fresh contact
// data. refund counts skipped rows that had monthly quota reserved; the caller
// refunds them in the same transaction. Callers lock the campaign row first.
func (a *campaignAudience) recheck(ctx context.Context, q eventoutbox.DBTX, campaignID int64, runID string, ids []int64) (out []eligibleRecipient, refund int64, err error) {
	if len(ids) == 0 {
		return nil, 0, nil
	}
	args := sqlArgs{}
	org := args.add(a.orgID)
	cid := args.add(campaignID)
	run := args.add(runID)
	idList := args.add(pq.Array(ids))
	query := `WITH chk AS (
		SELECT r.id, r.contact_id, r.email, r.message_uuid, r.quota_reserved, c.first_name, c.last_name, c.attributes,
			CASE
				WHEN c.id IS NULL THEN 'contact_deleted'
				WHEN lower(c.email)<>lower(r.email) THEN 'email_changed'
				WHEN c.status<>'active' THEN 'inactive'
				WHEN ` + campaignSuppressedSQL("c.org_id", "c.email") + ` THEN 'suppressed'
				WHEN NOT ` + a.membershipSQL(&args) + ` THEN 'not_member'
			END AS reason
		FROM campaign_recipients r
		LEFT JOIN contacts c ON c.id=r.contact_id AND c.org_id=r.org_id
		WHERE r.org_id=` + org + ` AND r.campaign_id=` + cid + `::int AND r.id=ANY(` + idList + `::bigint[])
			AND r.status='claimed' AND r.lease_owner=` + run + `::uuid
	), skipped AS (
		UPDATE campaign_recipients r SET status='skipped', skip_reason=chk.reason, quota_reserved=false,
			lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		FROM chk WHERE r.id=chk.id AND chk.reason IS NOT NULL AND r.status='claimed' AND r.lease_owner=` + run + `::uuid
		RETURNING r.id, chk.quota_reserved AS was_reserved
	), bump AS (
		UPDATE campaigns SET skipped_count=skipped_count+(SELECT COUNT(*) FROM skipped), updated_at=now()
		WHERE id=` + cid + `::int AND org_id=` + org + ` AND EXISTS(SELECT 1 FROM skipped)
	)
	-- One summary row (the refund) left-joined to the eligible rows, so the
	-- refund comes back even when no row is eligible.
	SELECT e.id, e.contact_id, e.email, e.message_uuid::text, COALESCE(e.first_name,''), COALESCE(e.last_name,''),
		COALESCE(e.attributes,'{}'::jsonb), s.refund
	FROM (SELECT COUNT(*) FILTER (WHERE was_reserved) AS refund FROM skipped) s
	LEFT JOIN chk e ON e.reason IS NULL ORDER BY e.id`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to recheck recipients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, contactID sql.NullInt64
		var email, messageUUID sql.NullString
		var r eligibleRecipient
		var attrs []byte
		if err := rows.Scan(&id, &contactID, &email, &messageUUID, &r.FirstName, &r.LastName, &attrs, &refund); err != nil {
			return nil, 0, fmt.Errorf("failed to read rechecked recipient: %w", err)
		}
		if !id.Valid {
			continue // the summary row alone: nothing eligible
		}
		r.ID, r.ContactID, r.Email, r.MessageUUID = id.Int64, contactID.Int64, email.String, messageUUID.String
		if err := json.Unmarshal(attrs, &r.Attributes); err != nil || r.Attributes == nil {
			r.Attributes = map[string]any{}
		}
		out = append(out, r)
	}
	return out, refund, rows.Err()
}
