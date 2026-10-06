package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/time/rate"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
)

// The campaign sender is a Postgres state machine. One leader per schema
// (advisory lock) promotes, prepares, claims and sends; leases, the guarded
// start and quota_reserved make crashes and restarts safe. A row that may
// have reached SES is never sent again.
const (
	campaignSenderLockKey   = 20261006
	campaignTickInterval    = 2 * time.Second
	campaignLockRetry       = 15 * time.Second
	campaignLease           = "5 minutes"
	campaignStaleSending    = "10 minutes"
	campaignSendTimeout     = 30 * time.Second
	campaignFinishTimeout   = 10 * time.Second
	campaignQuotaTTL        = 5 * time.Minute
	campaignSenderTTL       = time.Minute
	campaignRejectStreakMax = 5
	campaignDailyQuotaWait  = 30 * time.Minute
)

// RunCampaignSender sends campaigns until ctx ends. It runs regardless of
// WORKER_ENABLED; without an SES provider it idles (sending is refused upstream).
func RunCampaignSender(ctx context.Context, db *sql.DB, cfg *config.Config) {
	p := NewCampaignProvider(ctx, cfg)
	if p == nil {
		log.Printf("Campaign sender idle: campaigns send only with EMAIL_PROVIDER=ses and SES configured")
		<-ctx.Done()
		return
	}
	defer p.Close()
	r := newCampaignRunner(db, cfg, p)
	for ctx.Err() == nil {
		conn, ok, err := r.tryLock(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Campaign sender lock unavailable: %v", err)
		}
		if !ok {
			select {
			case <-ctx.Done():
			case <-time.After(campaignLockRetry):
			}
			continue
		}
		r.lead(ctx, conn)
		r.unlock(conn)
	}
}

type campaignRunner struct {
	db          *sql.DB
	cfg         *config.Config
	provider    provider.EmailProvider
	now         func() time.Time
	run         string
	concurrency int
	limiter     *rate.Limiter

	quota   *provider.SendQuota // last known SES quota; nil until fetched
	quotaAt time.Time

	mu           sync.Mutex
	senderOKAt   map[int64]time.Time // campaign id -> last successful sender check
	rejectStreak map[int64]int       // consecutive rejected sends per campaign
}

func newCampaignRunner(db *sql.DB, cfg *config.Config, p provider.EmailProvider) *campaignRunner {
	return &campaignRunner{
		db: db, cfg: cfg, provider: p, now: time.Now, run: uuid.NewString(),
		concurrency:  min(max(cfg.CampaignSendConcurrency, 1), 16),
		limiter:      rate.NewLimiter(1, 1),
		senderOKAt:   map[int64]time.Time{},
		rejectStreak: map[int64]int{},
	}
}

// tryLock takes the per-schema leader lock on a dedicated connection. The key
// includes current_schema() so isolated test schemas never collide.
func (r *campaignRunner) tryLock(ctx context.Context) (*sql.Conn, bool, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext(current_schema()), $1)`, campaignSenderLockKey).Scan(&ok); err != nil || !ok {
		conn.Close()
		return nil, false, err
	}
	return conn, true, nil
}

func (r *campaignRunner) unlock(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()), $1)`, campaignSenderLockKey)
	conn.Close()
}

// lead runs ticks while this process holds the lock. A batch that did work is
// followed immediately by the next one; idle ticks wait. The lock connection
// is checked before every batch so a leader that lost it stops claiming.
func (r *campaignRunner) lead(ctx context.Context, conn *sql.Conn) {
	defer r.releaseAll()
	ticker := time.NewTicker(campaignTickInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := conn.PingContext(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Campaign sender lost its lock connection: %v", err)
			}
			return
		}
		worked, err := r.runOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Campaign sender will retry: %v", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// releaseAll returns every row this runner still holds as claimed, on any exit.
func (r *campaignRunner) releaseAll() {
	ctx, cancel := context.WithTimeout(context.Background(), campaignFinishTimeout)
	defer cancel()
	if _, err := r.db.ExecContext(ctx, `UPDATE campaign_recipients SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		WHERE status='claimed' AND lease_owner=$1::uuid`, r.run); err != nil {
		log.Printf("Campaign sender could not release claimed rows (their lease expires): %v", err)
	}
}

// runOnce performs one tick: promote, recover, prepare, then at most one
// batch of one campaign. worked reports whether a batch was attempted.
func (r *campaignRunner) runOnce(ctx context.Context) (worked bool, err error) {
	if err := r.promoteDue(ctx); err != nil {
		return false, err
	}
	if err := r.recoverStale(ctx); err != nil {
		return false, err
	}
	if err := r.prepareDue(ctx); err != nil {
		return false, err
	}
	c, err := r.pickCampaign(ctx)
	if err != nil || c == nil {
		return false, err
	}
	return r.processBatch(ctx, c)
}

// promoteDue moves due scheduled campaigns to sending with campaign.started.
func (r *campaignRunner) promoteDue(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("promote scheduled campaigns: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `UPDATE campaigns SET status='sending', started_at=now(), prepared_at=NULL, updated_at=now()
		WHERE id IN (SELECT id FROM campaigns WHERE status='scheduled' AND scheduled_at<=now() ORDER BY scheduled_at, id LIMIT 50 FOR UPDATE SKIP LOCKED)
		RETURNING id, org_id, started_at`)
	if err != nil {
		return fmt.Errorf("promote scheduled campaigns: %w", err)
	}
	type promoted struct {
		id, org int64
		started time.Time
	}
	var due []promoted
	for rows.Next() {
		var p promoted
		if err := rows.Scan(&p.id, &p.org, &p.started); err != nil {
			rows.Close()
			return fmt.Errorf("promote scheduled campaigns: %w", err)
		}
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("promote scheduled campaigns: %w", err)
	}
	if len(due) == 0 {
		return nil
	}
	for _, p := range due {
		if err := emitCampaignEvent(ctx, tx, p.org, p.id, "campaign.started", fmt.Sprintf(":%d", p.started.Unix())); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// recoverStale returns expired claims to pending and resolves sends that never
// finished: unknown (never resent), or sent when SNS already proved delivery.
func (r *campaignRunner) recoverStale(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE campaign_recipients SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		WHERE status='claimed' AND lease_expires_at<now()`); err != nil {
		return fmt.Errorf("recover expired claims: %w", err)
	}
	_, err := r.db.ExecContext(ctx, `WITH s AS (
			UPDATE campaign_recipients SET
				status=CASE WHEN delivery_status IS NOT NULL THEN 'sent' ELSE 'unknown' END,
				sent_at=CASE WHEN delivery_status IS NOT NULL THEN COALESCE(sent_at,now()) ELSE sent_at END,
				error=CASE WHEN delivery_status IS NULL THEN 'send was interrupted; outcome unknown' ELSE error END,
				lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE status='sending' AND attempt_started_at<now()-interval '`+campaignStaleSending+`'
			RETURNING campaign_id, org_id, status)
		UPDATE campaigns c SET unknown_count=c.unknown_count+x.unknown, sent_count=c.sent_count+x.sent, updated_at=now()
		FROM (SELECT campaign_id, org_id, COUNT(*) FILTER (WHERE status='unknown') AS unknown, COUNT(*) FILTER (WHERE status='sent') AS sent
			FROM s GROUP BY campaign_id, org_id) x
		WHERE c.id=x.campaign_id AND c.org_id=x.org_id`)
	if err != nil {
		return fmt.Errorf("recover interrupted sends: %w", err)
	}
	return nil
}

// prepareDue snapshots the audience of sending campaigns not yet prepared.
func (r *campaignRunner) prepareDue(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT id, org_id FROM campaigns WHERE status='sending' AND prepared_at IS NULL ORDER BY id LIMIT 5`)
	if err != nil {
		return fmt.Errorf("find campaigns to prepare: %w", err)
	}
	var ids [][2]int64
	for rows.Next() {
		var id, org int64
		if err := rows.Scan(&id, &org); err != nil {
			rows.Close()
			return fmt.Errorf("find campaigns to prepare: %w", err)
		}
		ids = append(ids, [2]int64{id, org})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find campaigns to prepare: %w", err)
	}
	for _, c := range ids {
		if err := r.prepareCampaign(ctx, c[1], c[0]); err != nil {
			return err
		}
	}
	return nil
}

// prepareCampaign holds the campaign row while it revalidates and
// materialises, so a concurrent cancel or pause either waits or wins.
func (r *campaignRunner) prepareCampaign(ctx context.Context, orgID, campaignID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("prepare campaign: %w", err)
	}
	defer tx.Rollback()
	var listID, identityID, userID int64
	var fromEmail string
	err = tx.QueryRowContext(ctx, `SELECT list_id, COALESCE(identity_id,0), COALESCE(created_by_user_id,0), from_email FROM campaigns
		WHERE id=$1 AND org_id=$2 AND status='sending' AND prepared_at IS NULL FOR UPDATE SKIP LOCKED`, campaignID, orgID).
		Scan(&listID, &identityID, &userID, &fromEmail)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("prepare campaign: %w", err)
	}
	audience, reason, err := r.validateForSend(ctx, tx, orgID, listID, identityID, userID, fromEmail)
	if err != nil {
		return err
	}
	if reason != "" {
		if err := pauseCampaign(ctx, tx, orgID, campaignID, reason); err != nil {
			return err
		}
		return tx.Commit()
	}
	n, err := audience.materialise(ctx, tx, campaignID)
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET total_recipients=0, prepared_at=now(), status='sent', status_reason='no_eligible_recipients',
				completed_at=now(), updated_at=now() WHERE id=$1 AND org_id=$2`, campaignID, orgID); err != nil {
			return fmt.Errorf("prepare campaign: %w", err)
		}
		if err := emitCampaignEvent(ctx, tx, orgID, campaignID, "campaign.sent", ""); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET total_recipients=$3, prepared_at=now(), updated_at=now() WHERE id=$1 AND org_id=$2`,
		campaignID, orgID, n); err != nil {
		return fmt.Errorf("prepare campaign: %w", err)
	}
	return tx.Commit()
}

// validateForSend rechecks what SendCampaignNow checked. A validation failure
// comes back as a pause reason; only infrastructure errors are returned.
func (r *campaignRunner) validateForSend(ctx context.Context, q eventoutbox.DBTX, orgID, listID, identityID, userID int64, fromEmail string) (*campaignAudience, string, error) {
	if reason, err := r.senderReason(ctx, q, orgID, identityID, userID, fromEmail); reason != "" || err != nil {
		return nil, reason, err
	}
	audience, err := loadCampaignAudience(ctx, q, orgID, listID)
	if IsSegmentError(err) {
		return nil, "invalid_segment", nil
	}
	var invalid *provider.MailValidationError
	if errors.As(err, &invalid) {
		return nil, "list_unavailable", nil
	}
	return audience, "", err
}

// senderReason revalidates the stored sender, feedback readiness and postal
// address, returning a pause reason when one no longer holds.
func (r *campaignRunner) senderReason(ctx context.Context, q eventoutbox.DBTX, orgID, identityID, userID int64, fromEmail string) (string, error) {
	var invalid *provider.MailValidationError
	domainID, err := revalidateCampaignSender(ctx, q, orgID, identityID, userID, fromEmail)
	if err == nil {
		err = requireFeedbackReady(ctx, q, domainID)
	}
	if errors.As(err, &invalid) {
		return "sender_unavailable", nil
	}
	if err != nil {
		return "", err
	}
	if err := requirePostalAddress(ctx, q, orgID); errors.As(err, &invalid) {
		return "no_postal_address", nil
	} else if err != nil {
		return "", err
	}
	if checkCampaignLinkBases(r.cfg.APIUrl, r.cfg.WebUrl) != nil {
		return "unsubscribe_url_invalid", nil
	}
	return "", nil
}

type senderCampaign struct {
	ID, OrgID, ListID, IdentityID, UserID int64
	FromEmail                             string
	Throttled                             bool // throttled_until was set and has passed
}

// pickCampaign round-robins between prepared, unthrottled sending campaigns.
func (r *campaignRunner) pickCampaign(ctx context.Context) (*senderCampaign, error) {
	c := &senderCampaign{}
	err := r.db.QueryRowContext(ctx, `SELECT id, org_id, list_id, COALESCE(identity_id,0), COALESCE(created_by_user_id,0), from_email, throttled_until IS NOT NULL
		FROM campaigns WHERE status='sending' AND prepared_at IS NOT NULL AND (throttled_until IS NULL OR throttled_until<=now())
		ORDER BY last_batch_at NULLS FIRST, id LIMIT 1`).Scan(&c.ID, &c.OrgID, &c.ListID, &c.IdentityID, &c.UserID, &c.FromEmail, &c.Throttled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pick campaign: %w", err)
	}
	return c, nil
}

// campaignAction is a campaign-level consequence of a batch, applied after
// in-flight sends have finished.
type campaignAction struct {
	pause    string // pause reason
	throttle time.Duration
	reason   string // status_reason for a throttle
}

func (a *campaignAction) set(next campaignAction) {
	if a.pause == "" && (next.pause != "" || next.throttle > a.throttle) {
		*a = next
	}
}

// processBatch sends one batch of one campaign. It returns worked=false when
// the campaign had nothing to do, so the loop waits for the next tick.
func (r *campaignRunner) processBatch(ctx context.Context, c *senderCampaign) (bool, error) {
	defer r.touch(c)
	if reason, err := r.cachedSenderReason(ctx, c); err != nil || reason != "" {
		if reason != "" {
			r.pause(ctx, c, reason)
		}
		return false, err
	}
	quota := r.sendQuota(ctx)
	if quota != nil && quota.Max24HourSend >= 0 && quota.SentLast24Hours >= quota.Max24HourSend {
		return false, r.throttle(ctx, c, campaignDailyQuotaWait, "ses_daily_quota")
	}
	if c.Throttled {
		if _, err := r.db.ExecContext(ctx, `UPDATE campaigns SET throttled_until=NULL, status_reason=NULL, updated_at=now()
			WHERE id=$1 AND org_id=$2 AND status='sending' AND throttled_until<=now()`, c.ID, c.OrgID); err != nil {
			return false, fmt.Errorf("clear campaign throttle: %w", err)
		}
	}
	limit := r.applyRate(quota)
	if ctx.Err() != nil {
		return false, nil
	}
	ids, err := r.claimBatch(ctx, c, campaignBatchSize(limit))
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, r.maybeComplete(ctx, c)
	}
	defer r.releaseOwned(c)

	batch, action, err := r.recheckAndReserve(ctx, c, ids)
	if err != nil {
		return true, err
	}
	sendAction, err := r.sendBatch(ctx, c, batch)
	if err != nil {
		return false, err
	}
	action.set(sendAction)
	r.releaseOwned(c)
	if action.pause != "" {
		r.pause(ctx, c, action.pause)
	} else if action.throttle > 0 {
		if err := r.throttle(ctx, c, action.throttle, action.reason); err != nil {
			return true, err
		}
	}
	if reason, err := r.breaker(ctx, c); err != nil {
		return true, err
	} else if reason != "" {
		r.pause(ctx, c, reason)
	}
	return true, r.maybeComplete(ctx, c)
}

// cachedSenderReason revalidates the sender at most once a minute per campaign.
func (r *campaignRunner) cachedSenderReason(ctx context.Context, c *senderCampaign) (string, error) {
	r.mu.Lock()
	at, ok := r.senderOKAt[c.ID]
	r.mu.Unlock()
	if ok && r.now().Sub(at) < campaignSenderTTL {
		return "", nil
	}
	reason, err := r.senderReason(ctx, r.db, c.OrgID, c.IdentityID, c.UserID, c.FromEmail)
	if err == nil && reason == "" {
		r.mu.Lock()
		r.senderOKAt[c.ID] = r.now()
		r.mu.Unlock()
	}
	return reason, err
}

// claimBatch leases up to n pending rows to this runner.
func (r *campaignRunner) claimBatch(ctx context.Context, c *senderCampaign, n int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `UPDATE campaign_recipients SET status='claimed', lease_owner=$4::uuid, lease_expires_at=now()+interval '`+campaignLease+`', updated_at=now()
		WHERE id IN (SELECT id FROM campaign_recipients WHERE org_id=$1 AND campaign_id=$2 AND status='pending' ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING id`, c.OrgID, c.ID, n, r.run)
	if err != nil {
		return nil, fmt.Errorf("claim recipients: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("claim recipients: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// recheckAndReserve skips rows that are no longer eligible and reserves
// monthly quota for the rest in one transaction. Rows already reserved by an
// earlier attempt are not charged again; rows the quota cannot cover go back
// to pending and the campaign pauses once the granted rows are sent.
func (r *campaignRunner) recheckAndReserve(ctx context.Context, c *senderCampaign, ids []int64) ([]eligibleRecipient, campaignAction, error) {
	var action campaignAction
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, action, fmt.Errorf("recheck recipients: %w", err)
	}
	defer tx.Rollback()
	// The campaign row is locked first, as CancelCampaign does, so the two
	// never deadlock and a cancel either waits for this or has already won.
	var sending bool
	if err := tx.QueryRowContext(ctx, `SELECT status='sending' FROM campaigns WHERE id=$1 AND org_id=$2 FOR NO KEY UPDATE`, c.ID, c.OrgID).Scan(&sending); err != nil && err != sql.ErrNoRows {
		return nil, action, fmt.Errorf("recheck recipients: %w", err)
	}
	if !sending {
		return nil, action, nil // paused or cancelled meanwhile; the caller releases the claim
	}
	audience, reason, err := r.validateForSend(ctx, tx, c.OrgID, c.ListID, c.IdentityID, c.UserID, c.FromEmail)
	if err != nil {
		return nil, action, err
	}
	if reason != "" {
		action.pause = reason
		return nil, action, nil
	}
	eligible, refund, err := audience.recheck(ctx, tx, c.ID, r.run, ids)
	if err != nil {
		return nil, action, err
	}
	// Skipped rows that a released earlier attempt had reserved were never sent.
	if err := refundMonthlySendsTx(ctx, tx, c.OrgID, refund); err != nil {
		return nil, action, err
	}
	var unreserved []int64
	rows, err := tx.QueryContext(ctx, `SELECT id FROM campaign_recipients WHERE org_id=$1 AND campaign_id=$2 AND id=ANY($3::bigint[])
		AND status='claimed' AND lease_owner=$4::uuid AND NOT quota_reserved ORDER BY id FOR UPDATE`, c.OrgID, c.ID, pq.Array(eligibleIDs(eligible)), r.run)
	if err != nil {
		return nil, action, fmt.Errorf("reserve recipients: %w", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, action, fmt.Errorf("reserve recipients: %w", err)
		}
		unreserved = append(unreserved, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, action, fmt.Errorf("reserve recipients: %w", err)
	}
	granted, err := reserveMonthlySendsTx(ctx, tx, r.cfg, c.OrgID, int64(len(unreserved)))
	if err != nil {
		return nil, action, err
	}
	if granted > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE campaign_recipients SET quota_reserved=true, updated_at=now()
			WHERE org_id=$1 AND id=ANY($2::bigint[]) AND status='claimed' AND lease_owner=$3::uuid`, c.OrgID, pq.Array(unreserved[:granted]), r.run)
		if err != nil {
			return nil, action, fmt.Errorf("reserve recipients: %w", err)
		}
		if n, _ := res.RowsAffected(); n < granted {
			if err := refundMonthlySendsTx(ctx, tx, c.OrgID, granted-n); err != nil {
				return nil, action, err
			}
		}
	}
	if denied := unreserved[granted:]; len(denied) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE campaign_recipients SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE org_id=$1 AND id=ANY($2::bigint[]) AND status='claimed' AND lease_owner=$3::uuid`, c.OrgID, pq.Array(denied), r.run); err != nil {
			return nil, action, fmt.Errorf("release unreserved recipients: %w", err)
		}
		action.pause = "monthly_quota_exceeded"
		skip := map[int64]bool{}
		for _, id := range denied {
			skip[id] = true
		}
		kept := eligible[:0]
		for _, e := range eligible {
			if !skip[e.ID] {
				kept = append(kept, e)
			}
		}
		eligible = kept
	}
	if err := tx.Commit(); err != nil {
		return nil, action, fmt.Errorf("recheck recipients: %w", err)
	}
	return eligible, action, nil
}

// sendBatch sends rows through a small worker pool sharing the rate limiter.
// It stops starting sends when ctx ends, the guarded start fails (pause,
// cancel or lost lease) or a send result acts on the campaign.
func (r *campaignRunner) sendBatch(ctx context.Context, c *senderCampaign, batch []eligibleRecipient) (campaignAction, error) {
	if len(batch) == 0 {
		return campaignAction{}, nil
	}
	snap, err := loadCampaignSnapshot(ctx, r.db, c.OrgID, c.ID)
	if err != nil {
		return campaignAction{}, err
	}
	opts := renderOptions{APIURL: r.cfg.APIUrl, WebURL: r.cfg.WebUrl, Secret: r.cfg.JWTSecret, Mode: renderSend}
	var (
		mu      sync.Mutex
		action  campaignAction
		stopped bool
		wg      sync.WaitGroup
	)
	stop := func(next campaignAction) {
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		action.set(next)
	}
	isStopped := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return stopped
	}
	work := make(chan eligibleRecipient)
	for i := 0; i < min(r.concurrency, len(batch)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rcpt := range work {
				if isStopped() || r.limiter.Wait(ctx) != nil {
					continue
				}
				next, cont := r.sendOne(ctx, c, snap, rcpt, opts)
				if !cont {
					stop(next)
				}
			}
		}()
	}
	for _, rcpt := range batch {
		if isStopped() || ctx.Err() != nil {
			break
		}
		work <- rcpt
	}
	close(work)
	wg.Wait()
	return action, nil
}

// sendOne performs the guarded start, the SES call and the finish for one
// row. cont=false stops the batch, with an optional campaign action.
func (r *campaignRunner) sendOne(ctx context.Context, c *senderCampaign, snap campaignSnapshot, rcpt eligibleRecipient, opts renderOptions) (campaignAction, bool) {
	// Checked before, not during, the guarded start: a cancel racing a committed
	// start would leave a row that never reached SES to become unknown.
	if ctx.Err() != nil {
		return campaignAction{}, false
	}
	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), campaignFinishTimeout)
	defer cancelStart()
	// The start also rechecks what can change between the batch recheck and
	// this send (unsubscribe, suppression, erasure, a changed address), and
	// mails the address stored now, not the one read for the batch.
	var started, email string
	err := r.db.QueryRowContext(startCtx, `UPDATE campaign_recipients r SET status='sending', attempt_started_at=now(), updated_at=now()
		WHERE r.id=$1 AND r.org_id=$2 AND r.campaign_id=$3 AND r.status='claimed' AND r.lease_owner=$4::uuid AND r.lease_expires_at>now()
			AND EXISTS(SELECT 1 FROM campaigns WHERE id=$3 AND org_id=$2 AND status='sending')
			AND `+recipientSendableSQL+`
		RETURNING r.message_uuid::text, r.email`, rcpt.ID, c.OrgID, c.ID, r.run).Scan(&started, &email)
	if err == sql.ErrNoRows {
		skipped, serr := r.skipUnsendable(startCtx, c, rcpt.ID)
		if serr != nil {
			log.Printf("Campaign %d: cannot skip recipient: %v", c.ID, serr)
		}
		return campaignAction{}, skipped
	}
	if err != nil {
		log.Printf("Campaign %d: guarded start failed: %v", c.ID, err)
		return campaignAction{}, false
	}
	rcpt.MessageUUID, rcpt.Email = started, email

	var result *provider.SendResult
	msg, _, err := renderCampaignMessage(snap, rcpt, opts)
	if err == nil {
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), campaignSendTimeout)
		result, err = r.provider.SendEmail(sendCtx, msg)
		cancel()
	}
	outcome := classifySESSendError(err)
	messageID := ""
	if outcome == sendOutcomeOK && result != nil {
		messageID = result.MessageID
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), campaignFinishTimeout)
	defer cancel()
	if ferr := r.finishRecipient(finishCtx, c, rcpt.ID, rcpt.Email, outcome, messageID, err); ferr != nil {
		// The row stays sending and recovery turns it into unknown: never resent.
		log.Printf("Campaign %d: cannot record send outcome: %v", c.ID, ferr)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	switch outcome {
	case sendOutcomeOK:
		r.rejectStreak[c.ID] = 0
	case sendOutcomeRejected:
		r.rejectStreak[c.ID]++
		if r.rejectStreak[c.ID] >= campaignRejectStreakMax {
			r.rejectStreak[c.ID] = 0
			return campaignAction{pause: "provider_rejected"}, false
		}
	case sendOutcomeThrottle:
		reason := "ses_throttled"
		delay := campaignThrottleDelay(err)
		if delay >= campaignDailyQuotaWait {
			reason = "ses_daily_quota"
		}
		return campaignAction{throttle: delay, reason: reason}, false
	case sendOutcomeProviderPaused:
		return campaignAction{pause: "provider_paused"}, false
	case sendOutcomeSender:
		delete(r.senderOKAt, c.ID)
		return campaignAction{pause: "sender_unavailable"}, false
	}
	return campaignAction{}, true
}

// recipientSendableSQL holds for a campaign_recipients row r whose contact is
// still active at the same address, not unsubscribed and not suppressed.
var recipientSendableSQL = `r.unsubscribed_at IS NULL
	AND EXISTS(SELECT 1 FROM contacts c WHERE c.id=r.contact_id AND c.org_id=r.org_id AND c.status='active' AND lower(c.email)=lower(r.email))
	AND NOT ` + campaignSuppressedSQL("r.org_id", "r.email")

// skipUnsendable runs after a failed guarded start. A row still claimed by
// this runner in a sending campaign failed only the eligibility part: it
// becomes skipped (refunding its reservation) and the batch continues. Any
// other failure (pause, cancel, lost lease) returns false to stop the batch.
func (r *campaignRunner) skipUnsendable(ctx context.Context, c *senderCampaign, id int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var sending bool
	err = tx.QueryRowContext(ctx, `SELECT status='sending' FROM campaigns WHERE id=$1 AND org_id=$2 FOR NO KEY UPDATE`, c.ID, c.OrgID).Scan(&sending)
	if err == sql.ErrNoRows || (err == nil && !sending) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var reserved bool
	err = tx.QueryRowContext(ctx, `UPDATE campaign_recipients r SET status='skipped', quota_reserved=false,
			skip_reason=CASE
				WHEN c.id IS NULL THEN 'contact_deleted'
				WHEN lower(c.email)<>lower(r.email) THEN 'email_changed'
				WHEN c.status<>'active' OR r.unsubscribed_at IS NOT NULL THEN 'inactive'
				ELSE 'suppressed' END,
			lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		FROM campaign_recipients old LEFT JOIN contacts c ON c.id=old.contact_id AND c.org_id=old.org_id
		WHERE old.id=r.id AND r.id=$1 AND r.org_id=$2 AND r.campaign_id=$3 AND r.status='claimed' AND r.lease_owner=$4::uuid AND r.lease_expires_at>now()
			AND NOT (`+recipientSendableSQL+`)
		RETURNING old.quota_reserved`, id, c.OrgID, c.ID, r.run).Scan(&reserved)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if reserved {
		if err := refundMonthlySendsTx(ctx, tx, c.OrgID, 1); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET skipped_count=skipped_count+1, updated_at=now() WHERE id=$1 AND org_id=$2`, c.ID, c.OrgID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// finishRecipient records a send outcome and the campaign counters in one
// transaction. A row SES never accepted goes back to pending uncounted. SNS
// may already have promoted the row (sending/unknown -> sent); that is kept.
func (r *campaignRunner) finishRecipient(ctx context.Context, c *senderCampaign, id int64, email, outcome, messageID string, sendErr error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM campaign_recipients WHERE id=$1 AND org_id=$2 AND campaign_id=$3 FOR UPDATE`, id, c.OrgID, c.ID).Scan(&status)
	if err != nil {
		return err
	}
	errText := ""
	if sendErr != nil {
		errText = sendErrorText(sendErr, email)
	}
	var sent, failed, unknown int
	switch {
	case outcome == sendOutcomeOK && (status == "sending" || status == "unknown"):
		_, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET status='sent', provider_message_id=COALESCE(provider_message_id,NULLIF($3,'')),
			sent_at=COALESCE(sent_at,now()), error=NULL, lease_owner=NULL, lease_expires_at=NULL, updated_at=now() WHERE id=$1 AND org_id=$2`, id, c.OrgID, messageID)
		sent = 1
		if status == "unknown" {
			unknown = -1
		}
	case outcome == sendOutcomeOK:
		_, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET provider_message_id=COALESCE(provider_message_id,NULLIF($3,'')), updated_at=now()
			WHERE id=$1 AND org_id=$2`, id, c.OrgID, messageID)
	case status != "sending":
		return tx.Commit()
	case outcome == sendOutcomeRejected:
		_, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET status='failed', error=$3, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND org_id=$2`, id, c.OrgID, errText)
		failed = 1
	case outcome == sendOutcomeUncertain:
		_, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET status='unknown', error=$3, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND org_id=$2`, id, c.OrgID, errText)
		unknown = 1
	default: // throttle, provider_paused, sender: SES did not accept the message
		// A cancel that committed during the send left this row alone (it was
		// sending); it becomes cancelled and refunded instead of pending. The
		// share lock waits for a cancel in progress (campaign after recipient,
		// as everywhere this row is finished).
		var campaignStatus string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM campaigns WHERE id=$1 AND org_id=$2 FOR SHARE`, c.ID, c.OrgID).Scan(&campaignStatus); err != nil {
			return err
		}
		if campaignStatus == "cancelled" {
			var reserved bool
			if err = tx.QueryRowContext(ctx, `UPDATE campaign_recipients r SET status='cancelled', quota_reserved=false, attempt_started_at=NULL,
					lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
				FROM campaign_recipients old WHERE old.id=r.id AND r.id=$1 AND r.org_id=$2 RETURNING old.quota_reserved`, id, c.OrgID).Scan(&reserved); err != nil {
				return err
			}
			if reserved {
				err = refundMonthlySendsTx(ctx, tx, c.OrgID, 1)
			}
			break
		}
		_, err = tx.ExecContext(ctx, `UPDATE campaign_recipients SET status='pending', attempt_started_at=NULL, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND org_id=$2`, id, c.OrgID)
	}
	if err != nil {
		return err
	}
	if sent != 0 || failed != 0 || unknown != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET sent_count=sent_count+$3, failed_count=failed_count+$4, unknown_count=unknown_count+$5, updated_at=now()
			WHERE id=$1 AND org_id=$2`, c.ID, c.OrgID, sent, failed, unknown); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// releaseOwned returns this runner's claimed rows of one campaign to pending.
func (r *campaignRunner) releaseOwned(c *senderCampaign) {
	ctx, cancel := context.WithTimeout(context.Background(), campaignFinishTimeout)
	defer cancel()
	if _, err := r.db.ExecContext(ctx, `UPDATE campaign_recipients SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		WHERE org_id=$1 AND campaign_id=$2 AND status='claimed' AND lease_owner=$3::uuid`, c.OrgID, c.ID, r.run); err != nil {
		log.Printf("Campaign %d: cannot release claimed rows (their lease expires): %v", c.ID, err)
	}
}

// touch moves the campaign to the back of the round-robin.
func (r *campaignRunner) touch(c *senderCampaign) {
	ctx, cancel := context.WithTimeout(context.Background(), campaignFinishTimeout)
	defer cancel()
	_, _ = r.db.ExecContext(ctx, `UPDATE campaigns SET last_batch_at=now() WHERE id=$1 AND org_id=$2`, c.ID, c.OrgID)
}

// pause applies a runner pause; a campaign that already left sending is fine.
func (r *campaignRunner) pause(ctx context.Context, c *senderCampaign, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), campaignFinishTimeout)
	defer cancel()
	r.mu.Lock()
	delete(r.senderOKAt, c.ID)
	delete(r.rejectStreak, c.ID)
	r.mu.Unlock()
	if err := pauseCampaignTx(ctx, r.db, c.OrgID, c.ID, reason); err != nil && !errors.Is(err, ErrCampaignState) {
		log.Printf("Campaign %d: cannot pause (%s): %v", c.ID, reason, err)
	}
}

func (r *campaignRunner) throttle(ctx context.Context, c *senderCampaign, d time.Duration, reason string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), campaignFinishTimeout)
	defer cancel()
	if _, err := r.db.ExecContext(ctx, `UPDATE campaigns SET throttled_until=now()+$3*interval '1 second', status_reason=$4, updated_at=now()
		WHERE id=$1 AND org_id=$2 AND status='sending'`, c.ID, c.OrgID, int64(d/time.Second), reason); err != nil {
		return fmt.Errorf("throttle campaign: %w", err)
	}
	return nil
}

// breaker pauses campaigns whose bounce or complaint rate endangers the
// account. Rates use the campaign counters fed by SNS, counted since the last
// resume (ResumeCampaign moves the baseline), so a resumed campaign is judged
// on what it sends next instead of pausing again before sending anything.
func (r *campaignRunner) breaker(ctx context.Context, c *senderCampaign) (string, error) {
	var sent, bounces, complaints int64
	if err := r.db.QueryRowContext(ctx, `SELECT sent_count-breaker_baseline_sent, bounce_count-breaker_baseline_bounces,
			complaint_count-breaker_baseline_complaints FROM campaigns WHERE id=$1 AND org_id=$2`, c.ID, c.OrgID).
		Scan(&sent, &bounces, &complaints); err != nil {
		return "", fmt.Errorf("check campaign rates: %w", err)
	}
	return breakerReason(sent, bounces, complaints), nil
}

func breakerReason(sent, bounces, complaints int64) string {
	switch {
	case sent >= 100 && float64(bounces)/float64(sent) >= 0.05:
		return "bounce_rate_high"
	case sent >= 200 && float64(complaints)/float64(sent) >= 0.003:
		return "complaint_rate_high"
	}
	return ""
}

// maybeComplete marks a prepared campaign sent once no row can still be sent.
func (r *campaignRunner) maybeComplete(ctx context.Context, c *senderCampaign) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complete campaign: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE campaigns SET status='sent', status_reason=NULL, throttled_until=NULL, completed_at=now(), updated_at=now()
		WHERE id=$1 AND org_id=$2 AND status='sending' AND prepared_at IS NOT NULL
			AND NOT EXISTS(SELECT 1 FROM campaign_recipients WHERE campaign_id=$1 AND org_id=$2 AND status IN ('pending','claimed','sending'))`, c.ID, c.OrgID)
	if err != nil {
		return fmt.Errorf("complete campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := emitCampaignEvent(ctx, tx, c.OrgID, c.ID, "campaign.sent", ""); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.senderOKAt, c.ID)
	delete(r.rejectStreak, c.ID)
	r.mu.Unlock()
	return tx.Commit()
}

// sendQuota returns the SES quota, refreshed every five minutes. A failed
// refresh keeps the last known value (nil when none was ever read).
func (r *campaignRunner) sendQuota(ctx context.Context) *provider.SendQuota {
	if !r.quotaAt.IsZero() && r.now().Sub(r.quotaAt) < campaignQuotaTTL {
		return r.quota
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q, err := r.provider.GetSendQuota(qctx)
	r.quotaAt = r.now()
	if err != nil || q == nil {
		if ctx.Err() == nil {
			log.Printf("Campaign sender: SES quota unavailable, keeping the last known rate: %v", err)
		}
		return r.quota
	}
	r.quota = q
	return q
}

// applyRate updates the shared limiter and returns the rate in use.
func (r *campaignRunner) applyRate(q *provider.SendQuota) float64 {
	perSecond := campaignSendRate(r.cfg.CampaignMaxSendRate, q)
	if float64(r.limiter.Limit()) != perSecond {
		r.limiter.SetLimit(rate.Limit(perSecond))
	}
	return perSecond
}

// campaignSendRate leaves 20% of the SES MaxSendRate for compose and
// transactional mail; an operator cap may lower it. Never below 1/s.
func campaignSendRate(override float64, q *provider.SendQuota) float64 {
	perSecond := 1.0
	if q != nil && q.MaxSendRate > 0 {
		perSecond = math.Floor(0.8 * q.MaxSendRate)
		if override > 0 && override < perSecond {
			perSecond = override
		}
	}
	return max(perSecond, 1)
}

// campaignBatchSize keeps a batch well inside the five-minute lease.
func campaignBatchSize(perSecond float64) int {
	return min(max(int(math.Floor(perSecond*20)), 1), 50)
}

func eligibleIDs(rows []eligibleRecipient) []int64 {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}
