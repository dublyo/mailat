package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

// ErrCampaignTestConflict: the Idempotency-Key was used for different addresses.
var ErrCampaignTestConflict = errors.New("Idempotency-Key was already used for a different test send")

// SendTestEmail sends the campaign to 1-5 addresses synchronously, with
// tracking off, a "[Test] " subject and a test unsubscribe token. A retry with
// the same Idempotency-Key returns the stored outcome instead of sending again.
func (s *CampaignService) SendTestEmail(ctx context.Context, orgID int64, actor CampaignActor, campaignUUID string, emails []string, key string) (*model.CampaignTestResponse, error) {
	if err := validateSubmissionKey(key); err != nil {
		return nil, err
	}
	recipients, err := normalizeTestRecipients(emails)
	if err != nil {
		return nil, err
	}
	c, err := s.GetCampaign(ctx, orgID, campaignUUID)
	if err != nil {
		return nil, err
	}
	if err := canChangeSendState(ctx, s.db, orgID, actor, createdBy(c)); err != nil {
		return nil, err
	}
	if err := s.requireSES(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(strings.Join(recipients, "\n")))
	hash := hex.EncodeToString(sum[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start test send: %w", err)
	}
	defer tx.Rollback()
	// Serialises test sends per campaign so the hourly limit cannot be raced.
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM campaigns WHERE id=$1 AND org_id=$2 FOR UPDATE`, c.ID, orgID); err != nil {
		return nil, fmt.Errorf("failed to start test send: %w", err)
	}
	if stored, err := storedTestSend(ctx, tx, int64(c.ID), actor.UserID, key, hash); stored != nil || err != nil {
		return stored, err
	}
	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM campaign_test_sends WHERE campaign_id=$1 AND created_at>now()-interval '1 hour'`, c.ID).Scan(&recent); err != nil {
		return nil, fmt.Errorf("failed to check test send limit: %w", err)
	}
	if recent >= campaignTestHourlyLimit {
		return nil, ErrCampaignTestRateLimited
	}
	for _, r := range recipients {
		var suppressed bool
		if err := tx.QueryRowContext(ctx, `SELECT `+campaignSuppressedSQL("$1::int", "$2::text"), orgID, r).Scan(&suppressed); err != nil {
			return nil, fmt.Errorf("failed to check suppressions: %w", err)
		}
		if suppressed {
			return nil, &provider.MailValidationError{Message: r + " is suppressed and cannot receive campaign mail"}
		}
	}
	if _, _, err := resolveCampaignSender(ctx, tx, orgID, effectiveCreator(c, actor), c.FromEmail); err != nil {
		return nil, err
	}
	granted, err := reserveMonthlySendsTx(ctx, tx, s.cfg, orgID, int64(len(recipients)))
	if err != nil {
		return nil, err
	}
	if granted < int64(len(recipients)) {
		return nil, &provider.MailValidationError{Message: "monthly quota exceeded"}
	}
	var testID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO campaign_test_sends(campaign_id,user_id,idempotency_key,request_hash,recipients)
		VALUES($1,$2,$3,$4,$5) RETURNING id`, c.ID, actor.UserID, key, hash, pq.Array(recipients)).Scan(&testID); err != nil {
		return nil, fmt.Errorf("failed to record test send: %w", err)
	}
	snap, err := loadCampaignSnapshot(ctx, tx, orgID, int64(c.ID))
	if err != nil {
		return nil, err
	}
	var callerName string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM users WHERE id=$1 AND org_id=$2`, actor.UserID, orgID).Scan(&callerName); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to load caller: %w", err)
	}
	samples := make([]eligibleRecipient, len(recipients))
	for i, r := range recipients {
		sample, found, err := sampleContact(ctx, tx, sampleContactColumns+` WHERE org_id=$1 AND lower(email)=lower($2) ORDER BY id LIMIT 1`, orgID, r)
		if err != nil {
			return nil, err
		}
		if !found {
			sample = eligibleRecipient{FirstName: callerName, Attributes: map[string]any{}}
		}
		sample.ID, sample.ContactID, sample.Email = 0, 0, r
		samples[i] = sample
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to record test send: %w", err)
	}

	out := &model.CampaignTestResponse{Results: make([]model.CampaignTestResult, 0, len(recipients))}
	opts := renderOptions{APIURL: s.cfg.APIUrl, WebURL: s.cfg.WebUrl, Secret: s.cfg.JWTSecret, Mode: renderTest}
	for _, rcpt := range samples {
		out.Results = append(out.Results, s.sendTestMessage(ctx, snap, rcpt, opts))
	}
	out.Status = testSendStatus(out.Results)
	results, _ := json.Marshal(out.Results)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(finishCtx, `UPDATE campaign_test_sends SET status=$2, results=$3, updated_at=now() WHERE id=$1`, testID, out.Status, string(results)); err != nil {
		return nil, fmt.Errorf("failed to store test send outcome: %w", err)
	}
	return out, nil
}

func (s *CampaignService) sendTestMessage(ctx context.Context, snap campaignSnapshot, rcpt eligibleRecipient, opts renderOptions) model.CampaignTestResult {
	result := model.CampaignTestResult{Email: rcpt.Email}
	msg, _, err := renderCampaignMessage(snap, rcpt, opts)
	if err != nil {
		result.Status, result.Error = "failed", truncateRunes(err.Error(), 500)
		return result
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, err = s.provider.SendEmail(sendCtx, msg)
	switch classifySESSendError(err) {
	case sendOutcomeOK:
		result.Status = "sent"
	case sendOutcomeUncertain:
		result.Status, result.Error = "unknown", "the outcome is unknown; check the inbox before retrying"
	case sendOutcomeThrottle:
		result.Status, result.Error = "failed", "SES is throttling sends; try again shortly"
	case sendOutcomeProviderPaused:
		result.Status, result.Error = "failed", "SES sending is paused for this account"
	case sendOutcomeSender:
		result.Status, result.Error = "failed", "the From domain is not ready to send in SES"
	default:
		result.Status, result.Error = "failed", truncateRunes(err.Error(), 500)
	}
	return result
}

func testSendStatus(results []model.CampaignTestResult) string {
	status := ""
	for _, r := range results {
		if status == "" {
			status = r.Status
		} else if status != r.Status {
			return "partial"
		}
	}
	if status == "" {
		return "failed"
	}
	return status
}

// storedTestSend returns the outcome of an earlier request with the same key,
// ErrCampaignTestConflict when its addresses differ, or nil when the key is new.
// A row still "sending" after 2 minutes is reported as unknown.
func storedTestSend(ctx context.Context, q eventoutbox.DBTX, campaignID, userID int64, key, hash string) (*model.CampaignTestResponse, error) {
	var storedHash, status string
	var results []byte
	var stale bool
	err := q.QueryRowContext(ctx, `SELECT request_hash, status, results, created_at<now()-interval '2 minutes' FROM campaign_test_sends
		WHERE campaign_id=$1 AND user_id=$2 AND idempotency_key=$3`, campaignID, userID, key).Scan(&storedHash, &status, &results, &stale)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load test send: %w", err)
	}
	if storedHash != hash {
		return nil, ErrCampaignTestConflict
	}
	out := &model.CampaignTestResponse{Status: status, Results: []model.CampaignTestResult{}}
	_ = json.Unmarshal(results, &out.Results)
	if status == "sending" && stale {
		out.Status = "unknown"
	}
	return out, nil
}

// normalizeTestRecipients accepts 1-5 distinct plain addresses, lower-cased and sorted.
func normalizeTestRecipients(emails []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range emails {
		e = strings.TrimSpace(e)
		a, err := mail.ParseAddress(e)
		if strings.ContainsAny(e, "\r\n") || err != nil || !strings.EqualFold(a.Address, e) {
			return nil, &provider.MailValidationError{Message: "invalid test address"}
		}
		e = strings.ToLower(a.Address)
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 || len(out) > campaignTestMaxEmails {
		return nil, &provider.MailValidationError{Message: "send a test to between 1 and 5 addresses"}
	}
	sort.Strings(out)
	return out, nil
}
