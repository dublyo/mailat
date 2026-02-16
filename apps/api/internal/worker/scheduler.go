package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
)

// Task types for scheduled jobs
const (
	TypeScheduledBlacklistCheck = "scheduled:blacklist-check"
	TypeScheduledWarmupAdvance  = "scheduled:warmup-advance"
	TypeScheduledBounceCheck    = "scheduled:bounce-check"
	TypeScheduledAlertDigest    = "scheduled:alert-digest"
)

// Scheduler handles scheduled/periodic tasks
type Scheduler struct {
	scheduler *asynq.Scheduler
	db        *sql.DB
	cfg       *config.Config
}

// NewScheduler creates a new scheduler
func NewScheduler(db *sql.DB, cfg *config.Config) (*Scheduler, error) {
	redisOpt := parseRedisURL(cfg.RedisURL, cfg.RedisPassword)

	scheduler := asynq.NewScheduler(redisOpt, nil)

	return &Scheduler{
		scheduler: scheduler,
		db:        db,
		cfg:       cfg,
	}, nil
}

// RegisterScheduledTasks registers all periodic tasks
func (s *Scheduler) RegisterScheduledTasks() error {
	// Blacklist check every 6 hours
	_, err := s.scheduler.Register("0 */6 * * *", asynq.NewTask(TypeScheduledBlacklistCheck, nil))
	if err != nil {
		return fmt.Errorf("failed to register blacklist check: %w", err)
	}

	// Warmup day advance at midnight
	_, err = s.scheduler.Register("0 0 * * *", asynq.NewTask(TypeScheduledWarmupAdvance, nil))
	if err != nil {
		return fmt.Errorf("failed to register warmup advance: %w", err)
	}

	// Bounce rate check every hour
	_, err = s.scheduler.Register("0 * * * *", asynq.NewTask(TypeScheduledBounceCheck, nil))
	if err != nil {
		return fmt.Errorf("failed to register bounce check: %w", err)
	}

	// Daily alert digest at 9am
	_, err = s.scheduler.Register("0 9 * * *", asynq.NewTask(TypeScheduledAlertDigest, nil))
	if err != nil {
		return fmt.Errorf("failed to register alert digest: %w", err)
	}

	fmt.Println("Registered scheduled tasks:")
	fmt.Println("  - Blacklist check (every 6 hours)")
	fmt.Println("  - Warmup day advance (midnight)")
	fmt.Println("  - Bounce rate check (hourly)")
	fmt.Println("  - Alert digest (9am daily)")

	return nil
}

// Start starts the scheduler
func (s *Scheduler) Start() error {
	return s.scheduler.Start()
}

// Shutdown stops the scheduler
func (s *Scheduler) Shutdown() {
	s.scheduler.Shutdown()
}

// ScheduledTaskHandler handles scheduled task execution
type ScheduledTaskHandler struct {
	db  *sql.DB
	cfg *config.Config
}

// NewScheduledTaskHandler creates a new scheduled task handler
func NewScheduledTaskHandler(db *sql.DB, cfg *config.Config) *ScheduledTaskHandler {
	return &ScheduledTaskHandler{db: db, cfg: cfg}
}

// HandleBlacklistCheck checks all org IPs against blacklists
func (h *ScheduledTaskHandler) HandleBlacklistCheck(ctx context.Context, task *asynq.Task) error {
	fmt.Println("Running scheduled blacklist check...")

	// Get all unique sending IPs (from domains or configured IPs)
	// For now, check the server's public IP
	// In production, you'd check each org's dedicated IPs

	// Get orgs with warmup in progress (they have IPs to check)
	rows, err := h.db.QueryContext(ctx, `
		SELECT DISTINCT org_id, ip_address FROM warmup_progress WHERE status = 'active'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var orgID int64
		var ipAddress string
		if err := rows.Scan(&orgID, &ipAddress); err != nil {
			continue
		}

		// Check this IP
		result := h.checkBlacklists(ctx, ipAddress)

		// Create alert if listed
		if result.ListedCount > 0 {
			h.createBlacklistAlert(ctx, orgID, ipAddress, result)
		}
	}

	return nil
}

// HandleWarmupAdvance advances warmup day for all active warmups
func (h *ScheduledTaskHandler) HandleWarmupAdvance(ctx context.Context, task *asynq.Task) error {
	fmt.Println("Running scheduled warmup day advance...")

	// Advance current_day for all active warmups
	_, err := h.db.ExecContext(ctx, `
		UPDATE warmup_progress
		SET current_day = current_day + 1, updated_at = NOW()
		WHERE status = 'active'
	`)
	if err != nil {
		return fmt.Errorf("failed to advance warmup days: %w", err)
	}

	// Check for completed warmups
	_, err = h.db.ExecContext(ctx, `
		UPDATE warmup_progress
		SET status = 'completed', completed_at = NOW(), updated_at = NOW()
		WHERE status = 'active' AND current_day > 30
	`)
	if err != nil {
		return fmt.Errorf("failed to mark completed warmups: %w", err)
	}

	return nil
}

// HandleBounceCheck checks bounce rates and auto-pauses if too high
func (h *ScheduledTaskHandler) HandleBounceCheck(ctx context.Context, task *asynq.Task) error {
	fmt.Println("Running scheduled bounce rate check...")

	// Check each org's bounce rate in the last 24 hours
	rows, err := h.db.QueryContext(ctx, `
		SELECT
			org_id,
			COUNT(*) as total_sent,
			SUM(CASE WHEN status = 'bounced' THEN 1 ELSE 0 END) as bounced
		FROM emails
		WHERE created_at >= NOW() - INTERVAL '24 hours'
		GROUP BY org_id
		HAVING COUNT(*) >= 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var orgID int64
		var totalSent, bounced int
		if err := rows.Scan(&orgID, &totalSent, &bounced); err != nil {
			continue
		}

		bounceRate := float64(bounced) / float64(totalSent) * 100

		// Auto-pause warmup if bounce rate > 5%
		if bounceRate > 5 {
			h.pauseWarmupForBounceRate(ctx, orgID, bounceRate)
		}

		// Create alert if bounce rate > 3%
		if bounceRate > 3 {
			h.createBounceRateAlert(ctx, orgID, bounceRate, totalSent, bounced)
		}
	}

	return nil
}

// HandleAlertDigest sends daily alert digest emails
func (h *ScheduledTaskHandler) HandleAlertDigest(ctx context.Context, task *asynq.Task) error {
	fmt.Println("Running scheduled alert digest...")

	// Get unacknowledged alerts from the last 24 hours grouped by org
	rows, err := h.db.QueryContext(ctx, `
		SELECT
			a.org_id,
			o.name as org_name,
			u.email as admin_email,
			COUNT(*) as alert_count,
			SUM(CASE WHEN a.severity = 'critical' THEN 1 ELSE 0 END) as critical_count
		FROM alerts a
		JOIN organizations o ON o.id = a.org_id
		JOIN users u ON u.org_id = a.org_id AND u.role = 'owner'
		WHERE a.acknowledged = false
		AND a.created_at >= NOW() - INTERVAL '24 hours'
		AND a.org_id > 0
		GROUP BY a.org_id, o.name, u.email
		HAVING COUNT(*) > 0
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	queueClient, err := NewQueueClient(h.cfg)
	if err != nil {
		return err
	}
	defer queueClient.Close()

	for rows.Next() {
		var orgID int64
		var orgName, adminEmail string
		var alertCount, criticalCount int

		if err := rows.Scan(&orgID, &orgName, &adminEmail, &alertCount, &criticalCount); err != nil {
			continue
		}

		// Queue alert digest email
		subject := fmt.Sprintf("[Mailat] You have %d unread alerts", alertCount)
		if criticalCount > 0 {
			subject = fmt.Sprintf("[URGENT] %d critical alerts require attention", criticalCount)
		}

		htmlBody := fmt.Sprintf(`
			<h2>Alert Summary for %s</h2>
			<p>You have <strong>%d</strong> unacknowledged alerts in the last 24 hours.</p>
			<p>Critical alerts: <strong>%d</strong></p>
			<p><a href="%s/dashboard/alerts">View all alerts</a></p>
		`, orgName, alertCount, criticalCount, h.cfg.WebUrl)

		alertsEmail := fmt.Sprintf("alerts@%s", h.cfg.AppDomain)
		payload := NewEmailSendPayload(0, orgID, alertsEmail, []string{adminEmail}, subject, htmlBody, "", "")
		queueClient.EnqueueEmailSend(payload)
	}

	return nil
}

// blacklistCheckResult holds RBL check results
type blacklistCheckResult struct {
	IPAddress   string
	ListedCount int
	Results     []blacklistResult
}

type blacklistResult struct {
	RBL       string
	Listed    bool
	Reason    string
	DelistURL string
}

// Common RBLs
var rblList = []struct {
	Name      string
	Zone      string
	DelistURL string
}{
	{"Spamhaus ZEN", "zen.spamhaus.org", "https://www.spamhaus.org/query/ip/"},
	{"Spamcop", "bl.spamcop.net", "https://www.spamcop.net/bl.shtml?"},
	{"Barracuda", "b.barracudacentral.org", "https://www.barracudacentral.org/lookups"},
	{"SORBS", "dnsbl.sorbs.net", "https://www.sorbs.net/lookup.shtml"},
	{"SpamEatingMonkey", "bl.spameatingmonkey.net", "https://spameatingmonkey.com/services/"},
}

// checkBlacklists checks an IP against RBLs
func (h *ScheduledTaskHandler) checkBlacklists(ctx context.Context, ipAddress string) blacklistCheckResult {
	result := blacklistCheckResult{
		IPAddress: ipAddress,
		Results:   make([]blacklistResult, 0, len(rblList)),
	}

	// Reverse IP for DNSBL lookup
	reversedIP := reverseIPAddress(ipAddress)

	for _, rbl := range rblList {
		br := blacklistResult{
			RBL:       rbl.Name,
			DelistURL: rbl.DelistURL + ipAddress,
		}

		query := fmt.Sprintf("%s.%s", reversedIP, rbl.Zone)
		addrs, err := net.LookupHost(query)
		if err == nil && len(addrs) > 0 {
			br.Listed = true
			br.Reason = fmt.Sprintf("Listed (response: %s)", addrs[0])
			result.ListedCount++
		}

		result.Results = append(result.Results, br)
	}

	// Store results
	resultsJSON, _ := json.Marshal(result.Results)
	h.db.ExecContext(ctx, `
		INSERT INTO blacklist_checks (ip_address, listed_count, results, checked_at)
		VALUES ($1, $2, $3, NOW())
	`, ipAddress, result.ListedCount, resultsJSON)

	return result
}

func reverseIPAddress(ip string) string {
	parts := strings.Split(ip, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}

func (h *ScheduledTaskHandler) createBlacklistAlert(ctx context.Context, orgID int64, ipAddress string, result blacklistCheckResult) {
	var listedRBLs []string
	for _, r := range result.Results {
		if r.Listed {
			listedRBLs = append(listedRBLs, r.RBL)
		}
	}

	alertData, _ := json.Marshal(map[string]any{
		"ipAddress":  ipAddress,
		"listedOn":   listedRBLs,
		"totalRBLs":  len(result.Results),
		"listedCount": result.ListedCount,
	})

	h.db.ExecContext(ctx, `
		INSERT INTO alerts (org_id, type, severity, title, message, data, acknowledged, created_at)
		VALUES ($1, 'blacklist', 'critical',
			$2, $3, $4, false, NOW())
	`, orgID,
		fmt.Sprintf("IP %s listed on %d blacklists", ipAddress, result.ListedCount),
		fmt.Sprintf("Your sending IP %s is listed on: %s. This will significantly impact deliverability.", ipAddress, strings.Join(listedRBLs, ", ")),
		alertData,
	)
}

func (h *ScheduledTaskHandler) pauseWarmupForBounceRate(ctx context.Context, orgID int64, bounceRate float64) {
	h.db.ExecContext(ctx, `
		UPDATE warmup_progress
		SET status = 'paused', pause_reason = $2, updated_at = NOW()
		WHERE org_id = $1 AND status = 'active'
	`, orgID, fmt.Sprintf("Auto-paused due to high bounce rate (%.2f%%)", bounceRate))

	// Also pause any sending campaigns
	h.db.ExecContext(ctx, `
		UPDATE campaigns
		SET status = 'paused', updated_at = NOW()
		WHERE org_id = $1 AND status = 'sending'
	`, orgID)
}

func (h *ScheduledTaskHandler) createBounceRateAlert(ctx context.Context, orgID int64, bounceRate float64, total, bounced int) {
	severity := "warning"
	if bounceRate > 5 {
		severity = "critical"
	}

	alertData, _ := json.Marshal(map[string]any{
		"bounceRate": bounceRate,
		"totalSent":  total,
		"bounced":    bounced,
	})

	h.db.ExecContext(ctx, `
		INSERT INTO alerts (org_id, type, severity, title, message, data, acknowledged, created_at)
		VALUES ($1, 'bounce_rate', $2,
			'High Bounce Rate Detected',
			$3, $4, false, NOW())
	`, orgID, severity,
		fmt.Sprintf("Your bounce rate is %.2f%% (%d bounces out of %d sent). This may affect deliverability.", bounceRate, bounced, total),
		alertData,
	)
}
