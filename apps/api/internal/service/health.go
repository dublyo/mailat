package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/dublyo/mailat/api/internal/config"
)

// HealthService handles email health monitoring and operations
type HealthService struct {
	db  *sql.DB
	cfg *config.Config
}

// BlacklistResult contains the result of a blacklist check
type BlacklistResult struct {
	RBL       string    `json:"rbl"`
	Listed    bool      `json:"listed"`
	Reason    string    `json:"reason,omitempty"`
	DelistURL string    `json:"delistUrl,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// BlacklistCheckResponse contains all blacklist check results
type BlacklistCheckResponse struct {
	IPAddress    string            `json:"ipAddress"`
	TotalChecked int               `json:"totalChecked"`
	ListedCount  int               `json:"listedCount"`
	Results      []BlacklistResult `json:"results"`
	CheckedAt    time.Time         `json:"checkedAt"`
}

// ReputationMetrics contains sender reputation metrics
type ReputationMetrics struct {
	OrgID           int64                    `json:"orgId"`
	Period          string                   `json:"period"`
	Score           int                      `json:"score"`
	TotalSent       int                      `json:"totalSent"`
	TotalDelivered  int                      `json:"totalDelivered"`
	TotalBounced    int                      `json:"totalBounced"`
	TotalFailed     int                      `json:"totalFailed"`
	TotalComplaints int                      `json:"totalComplaints"`
	TotalReceived   int                      `json:"totalReceived"`
	TotalSpam       int                      `json:"totalSpam"`
	DeliveryRate    float64                  `json:"deliveryRate"`
	BounceRate      float64                  `json:"bounceRate"`
	ComplaintRate   float64                  `json:"complaintRate"`
	SpamRate        float64                  `json:"spamRate"`
	ByDomain        map[string]DomainMetrics `json:"byDomain,omitempty"`
}

// DomainMetrics contains metrics for a specific domain
type DomainMetrics struct {
	Domain        string  `json:"domain"`
	Sent          int     `json:"sent"`
	Delivered     int     `json:"delivered"`
	Bounced       int     `json:"bounced"`
	Complaints    int     `json:"complaints"`
	DeliveryRate  float64 `json:"deliveryRate"`
	BounceRate    float64 `json:"bounceRate"`
	ComplaintRate float64 `json:"complaintRate"`
}

// WarmupSchedule defines a warmup schedule
type WarmupSchedule struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Days        []int  `json:"days"` // Daily limits for each day
	TotalDays   int    `json:"totalDays"`
}

// WarmupStatus contains current warmup status for an IP
type WarmupStatus struct {
	IPAddress    string     `json:"ipAddress"`
	ScheduleName string     `json:"scheduleName"`
	CurrentDay   int        `json:"currentDay"`
	DailyLimit   int        `json:"dailyLimit"`
	SentToday    int        `json:"sentToday"`
	TotalSent    int        `json:"totalSent"`
	Status       string     `json:"status"` // active, paused, completed
	PauseReason  string     `json:"pauseReason,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

// Alert represents a system alert
type Alert struct {
	ID           int64     `json:"id"`
	OrgID        int64     `json:"orgId"`
	Type         string    `json:"type"` // blacklist, bounce_rate, complaint_rate, quota, warmup
	Severity     string    `json:"severity"` // info, warning, critical
	Title        string    `json:"title"`
	Message      string    `json:"message"`
	Data         any       `json:"data,omitempty"`
	Acknowledged bool      `json:"acknowledged"`
	CreatedAt    time.Time `json:"createdAt"`
}

// QuotaStatus contains quota usage information
type QuotaStatus struct {
	OrgID             int64   `json:"orgId"`
	MonthlyLimit      int     `json:"monthlyLimit"`
	DailyLimit        int     `json:"dailyLimit"`
	MonthlyUsed       int     `json:"monthlyUsed"`
	DailyUsed         int     `json:"dailyUsed"`
	MonthlyRemaining  int     `json:"monthlyRemaining"`
	DailyRemaining    int     `json:"dailyRemaining"`
	MonthlyPercentage float64 `json:"monthlyPercentage"`
	DailyPercentage   float64 `json:"dailyPercentage"`
}

// SESAccountLimits contains AWS SES sending limits
type SESAccountLimits struct {
	Max24HourSend   float64 `json:"max24HourSend"`
	MaxSendRate     float64 `json:"maxSendRate"`
	SentLast24Hours float64 `json:"sentLast24Hours"`
	SendingEnabled  bool    `json:"sendingEnabled"`
	SandboxMode     bool    `json:"sandboxMode"`
	Remaining24Hour float64 `json:"remaining24Hour"`
	UsagePercentage float64 `json:"usagePercentage"`
}

// EmailHealthSummary contains comprehensive email health metrics
type EmailHealthSummary struct {
	SendingMetrics   ReputationMetrics    `json:"sendingMetrics"`
	SESLimits        *SESAccountLimits    `json:"sesLimits,omitempty"`
	ReceivingMetrics ReceivingMetrics     `json:"receivingMetrics"`
	AuthStatus       AuthenticationStatus `json:"authStatus"`
	Warnings         []HealthWarning      `json:"warnings"`
	HealthScore      int                  `json:"healthScore"`
	HealthStatus     string               `json:"healthStatus"` // excellent, good, warning, critical
}

// ReceivingMetrics contains email receiving metrics
type ReceivingMetrics struct {
	TotalReceived int            `json:"totalReceived"`
	TotalSpam     int            `json:"totalSpam"`
	TotalVirus    int            `json:"totalVirus"`
	TotalRead     int            `json:"totalRead"`
	SpamRate      float64        `json:"spamRate"`
	ReadRate      float64        `json:"readRate"`
	ByIdentity    map[string]int `json:"byIdentity,omitempty"`
}

// AuthenticationStatus contains DKIM/SPF/DMARC status for domains
type AuthenticationStatus struct {
	TotalDomains    int                `json:"totalDomains"`
	VerifiedDomains int                `json:"verifiedDomains"`
	DKIMConfigured  int                `json:"dkimConfigured"`
	SPFConfigured   int                `json:"spfConfigured"`
	DMARCConfigured int                `json:"dmarcConfigured"`
	Domains         []DomainAuthStatus `json:"domains"`
}

// DomainAuthStatus contains authentication status for a single domain
type DomainAuthStatus struct {
	Domain        string `json:"domain"`
	DKIMVerified  bool   `json:"dkimVerified"`
	SPFVerified   bool   `json:"spfVerified"`
	DMARCVerified bool   `json:"dmarcVerified"`
	SESVerified   bool   `json:"sesVerified"`
}

// HealthWarning represents a health warning
type HealthWarning struct {
	Type     string `json:"type"`
	Severity string `json:"severity"` // info, warning, critical
	Title    string `json:"title"`
	Message  string `json:"message"`
	Action   string `json:"action,omitempty"`
}

// Common RBLs to check
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
	{"UCEPROTECT", "dnsbl-1.uceprotect.net", "https://www.uceprotect.net/en/rblcheck.php"},
	{"Mailspike", "bl.mailspike.net", "https://mailspike.org/"},
}

// Warmup schedules
var warmupSchedules = map[string]WarmupSchedule{
	"conservative": {
		Name:        "conservative",
		Description: "Conservative 30-day warmup for new IPs",
		Days:        []int{20, 50, 100, 200, 400, 600, 800, 1000, 1200, 1400, 1600, 1800, 2000, 2400, 2800, 3200, 4000, 5000, 6000, 7000, 8000, 9000, 10000, 12000, 14000, 16000, 18000, 20000, 25000, 30000},
		TotalDays:   30,
	},
	"moderate": {
		Name:        "moderate",
		Description: "Moderate 21-day warmup",
		Days:        []int{50, 100, 300, 600, 1000, 1500, 2000, 3000, 4000, 5000, 6000, 8000, 10000, 12000, 15000, 18000, 22000, 27000, 35000, 45000, 60000},
		TotalDays:   21,
	},
	"aggressive": {
		Name:        "aggressive",
		Description: "Aggressive 14-day warmup for established senders",
		Days:        []int{100, 500, 1000, 2000, 4000, 7000, 10000, 15000, 20000, 30000, 45000, 60000, 80000, 100000},
		TotalDays:   14,
	},
}

func NewHealthService(db *sql.DB, cfg *config.Config) *HealthService {
	return &HealthService{db: db, cfg: cfg}
}

// GetServerIP gets the server's public IP address
func (s *HealthService) GetServerIP() (string, error) {
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, svc := range services {
		resp, err := client.Get(svc)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			buf := make([]byte, 50)
			n, _ := resp.Body.Read(buf)
			ip := strings.TrimSpace(string(buf[:n]))
			if net.ParseIP(ip) != nil {
				return ip, nil
			}
		}
	}

	return "", fmt.Errorf("could not determine server IP")
}

// CheckBlacklists checks if an IP is listed on major RBLs
// If ipAddress is empty, it will auto-detect the server IP
func (s *HealthService) CheckBlacklists(ctx context.Context, ipAddress string) (*BlacklistCheckResponse, error) {
	// Auto-detect IP if not provided
	if ipAddress == "" {
		detectedIP, err := s.GetServerIP()
		if err != nil {
			return nil, fmt.Errorf("could not determine IP address: %w", err)
		}
		ipAddress = detectedIP
	}

	// Validate IP
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP address: %s", ipAddress)
	}

	// Reverse the IP for DNSBL lookup
	reversedIP := reverseIP(ip.String())

	response := &BlacklistCheckResponse{
		IPAddress: ipAddress,
		Results:   make([]BlacklistResult, 0, len(rblList)),
		CheckedAt: time.Now(),
	}

	for _, rbl := range rblList {
		result := BlacklistResult{
			RBL:       rbl.Name,
			Listed:    false,
			DelistURL: rbl.DelistURL + ipAddress,
			CheckedAt: time.Now(),
		}

		// Query the RBL with timeout
		queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		query := fmt.Sprintf("%s.%s", reversedIP, rbl.Zone)
		addrs, err := net.DefaultResolver.LookupHost(queryCtx, query)
		cancel()

		if err == nil && len(addrs) > 0 {
			result.Listed = true
			result.Reason = fmt.Sprintf("Listed (response: %s)", addrs[0])
			response.ListedCount++
		}

		response.Results = append(response.Results, result)
		response.TotalChecked++
	}

	// Store results (ignore errors, table might not exist)
	resultsJSON, _ := json.Marshal(response.Results)
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO blacklist_checks (ip_address, listed_count, results, checked_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (ip_address) DO UPDATE SET
			listed_count = EXCLUDED.listed_count,
			results = EXCLUDED.results,
			checked_at = EXCLUDED.checked_at
	`, ipAddress, response.ListedCount, resultsJSON, response.CheckedAt)

	// Create alert if listed
	if response.ListedCount > 0 {
		s.createAlert(ctx, 0, "blacklist", "critical",
			fmt.Sprintf("IP %s listed on %d blacklists", ipAddress, response.ListedCount),
			fmt.Sprintf("Your IP %s is listed on %d blacklists. This may affect email deliverability.", ipAddress, response.ListedCount),
			response,
		)
	}

	return response, nil
}

// GetReputationMetrics retrieves sender reputation metrics
func (s *HealthService) GetReputationMetrics(ctx context.Context, orgID int64, period string) (*ReputationMetrics, error) {
	var startDate time.Time
	switch period {
	case "day":
		startDate = time.Now().AddDate(0, 0, -1)
	case "week":
		startDate = time.Now().AddDate(0, 0, -7)
	case "month":
		startDate = time.Now().AddDate(0, -1, 0)
	default:
		startDate = time.Now().AddDate(0, 0, -30)
		period = "30days"
	}

	metrics := &ReputationMetrics{
		OrgID:    orgID,
		Period:   period,
		ByDomain: make(map[string]DomainMetrics),
	}

	// Get transactional email metrics (sending)
	// status values: queued, sending, sent, failed, bounced, complained
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total_sent,
			COALESCE(SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END), 0) as delivered,
			COALESCE(SUM(CASE WHEN status = 'bounced' THEN 1 ELSE 0 END), 0) as bounced,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(CASE WHEN status = 'complained' THEN 1 ELSE 0 END), 0) as complaints
		FROM transactional_emails
		WHERE org_id = $1 AND created_at >= $2
	`, orgID, startDate).Scan(&metrics.TotalSent, &metrics.TotalDelivered, &metrics.TotalBounced, &metrics.TotalFailed, &metrics.TotalComplaints)
	if err != nil && err != sql.ErrNoRows {
		// Table might not exist or be empty, return zero metrics
		metrics.TotalSent = 0
		metrics.TotalDelivered = 0
		metrics.TotalBounced = 0
		metrics.TotalFailed = 0
		metrics.TotalComplaints = 0
	}

	// Get received email metrics
	var totalReceived, spamCount, readCount int
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total_received,
			COALESCE(SUM(CASE WHEN is_spam = true THEN 1 ELSE 0 END), 0) as spam,
			COALESCE(SUM(CASE WHEN is_read = true THEN 1 ELSE 0 END), 0) as read_count
		FROM received_emails
		WHERE org_id = $1 AND created_at >= $2
	`, orgID, startDate).Scan(&totalReceived, &spamCount, &readCount)
	if err == nil {
		metrics.TotalReceived = totalReceived
		metrics.TotalSpam = spamCount
	}

	// Calculate rates
	if metrics.TotalSent > 0 {
		metrics.DeliveryRate = float64(metrics.TotalDelivered) / float64(metrics.TotalSent) * 100
		metrics.BounceRate = float64(metrics.TotalBounced) / float64(metrics.TotalSent) * 100
		metrics.ComplaintRate = float64(metrics.TotalComplaints) / float64(metrics.TotalSent) * 100
	} else {
		// Default to 100% delivery rate when no emails sent
		metrics.DeliveryRate = 100
	}
	if metrics.TotalReceived > 0 {
		metrics.SpamRate = float64(metrics.TotalSpam) / float64(metrics.TotalReceived) * 100
	}

	// Calculate reputation score (0-100)
	metrics.Score = calculateReputationScore(metrics)

	// Get metrics by recipient domain (top 20)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			SPLIT_PART(to_addresses[1], '@', 2) as domain,
			COUNT(*) as sent,
			COALESCE(SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END), 0) as delivered,
			COALESCE(SUM(CASE WHEN status = 'bounced' THEN 1 ELSE 0 END), 0) as bounced,
			COALESCE(SUM(CASE WHEN status = 'complained' THEN 1 ELSE 0 END), 0) as complaints
		FROM transactional_emails
		WHERE org_id = $1 AND created_at >= $2 AND ARRAY_LENGTH(to_addresses, 1) > 0
		GROUP BY domain
		HAVING SPLIT_PART(to_addresses[1], '@', 2) IS NOT NULL AND SPLIT_PART(to_addresses[1], '@', 2) != ''
		ORDER BY sent DESC
		LIMIT 20
	`, orgID, startDate)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var dm DomainMetrics
			if err := rows.Scan(&dm.Domain, &dm.Sent, &dm.Delivered, &dm.Bounced, &dm.Complaints); err == nil {
				if dm.Sent > 0 && dm.Domain != "" {
					dm.DeliveryRate = float64(dm.Delivered) / float64(dm.Sent) * 100
					dm.BounceRate = float64(dm.Bounced) / float64(dm.Sent) * 100
					dm.ComplaintRate = float64(dm.Complaints) / float64(dm.Sent) * 100
					metrics.ByDomain[dm.Domain] = dm
				}
			}
		}
	}

	// Check for high bounce/complaint rates and create alerts
	if metrics.TotalSent >= 100 {
		if metrics.BounceRate > 5 {
			s.createAlert(ctx, orgID, "bounce_rate", "warning",
				"High Bounce Rate Detected",
				fmt.Sprintf("Your bounce rate is %.2f%% which exceeds the recommended 5%%. This may affect deliverability.", metrics.BounceRate),
				metrics,
			)
		}
		if metrics.ComplaintRate > 0.1 {
			s.createAlert(ctx, orgID, "complaint_rate", "critical",
				"High Complaint Rate Detected",
				fmt.Sprintf("Your complaint rate is %.3f%% which exceeds the recommended 0.1%%. This is a serious deliverability concern.", metrics.ComplaintRate),
				metrics,
			)
		}
	}

	return metrics, nil
}

// calculateReputationScore calculates a reputation score from 0-100
func calculateReputationScore(metrics *ReputationMetrics) int {
	if metrics.TotalSent == 0 && metrics.TotalReceived == 0 {
		return 100 // No data, assume good
	}

	score := 100.0

	// Penalize for bounce rate (max -30 points)
	if metrics.BounceRate > 0 {
		bounceDeduction := metrics.BounceRate * 6 // 5% bounce = -30 points
		if bounceDeduction > 30 {
			bounceDeduction = 30
		}
		score -= bounceDeduction
	}

	// Penalize for complaint rate (max -40 points)
	if metrics.ComplaintRate > 0 {
		complaintDeduction := metrics.ComplaintRate * 400 // 0.1% complaint = -40 points
		if complaintDeduction > 40 {
			complaintDeduction = 40
		}
		score -= complaintDeduction
	}

	// Penalize for low delivery rate (max -20 points)
	if metrics.TotalSent > 0 && metrics.DeliveryRate < 95 {
		deliveryDeduction := (95 - metrics.DeliveryRate) * 0.4
		if deliveryDeduction > 20 {
			deliveryDeduction = 20
		}
		score -= deliveryDeduction
	}

	// Penalize for high spam rate on received emails (max -10 points)
	if metrics.SpamRate > 5 {
		spamDeduction := (metrics.SpamRate - 5) * 2
		if spamDeduction > 10 {
			spamDeduction = 10
		}
		score -= spamDeduction
	}

	if score < 0 {
		score = 0
	}
	return int(score)
}

// GetSESAccountLimits retrieves AWS SES account sending limits
func (s *HealthService) GetSESAccountLimits(ctx context.Context) (*SESAccountLimits, error) {
	// Check if AWS credentials are configured
	if s.cfg.AWSAccessKeyID == "" || s.cfg.AWSSecretAccessKey == "" {
		return nil, fmt.Errorf("AWS credentials not configured")
	}

	// Create AWS config
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(s.cfg.AWSRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s.cfg.AWSAccessKeyID,
			s.cfg.AWSSecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS config: %w", err)
	}

	// Create SES client
	sesClient := ses.NewFromConfig(awsCfg)

	// Get account sending quota
	quotaResp, err := sesClient.GetSendQuota(ctx, &ses.GetSendQuotaInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get SES quota: %w", err)
	}

	limits := &SESAccountLimits{
		Max24HourSend:   quotaResp.Max24HourSend,
		MaxSendRate:     quotaResp.MaxSendRate,
		SentLast24Hours: quotaResp.SentLast24Hours,
		SendingEnabled:  true, // Assume enabled if we can query
		SandboxMode:     quotaResp.Max24HourSend <= 200, // Sandbox typically has 200/day limit
	}

	limits.Remaining24Hour = limits.Max24HourSend - limits.SentLast24Hours
	if limits.Remaining24Hour < 0 {
		limits.Remaining24Hour = 0
	}

	if limits.Max24HourSend > 0 {
		limits.UsagePercentage = (limits.SentLast24Hours / limits.Max24HourSend) * 100
	}

	return limits, nil
}

// GetEmailHealthSummary returns comprehensive email health metrics
func (s *HealthService) GetEmailHealthSummary(ctx context.Context, orgID int64) (*EmailHealthSummary, error) {
	summary := &EmailHealthSummary{
		Warnings: make([]HealthWarning, 0),
	}

	// Get sending metrics
	metrics, err := s.GetReputationMetrics(ctx, orgID, "30days")
	if err == nil {
		summary.SendingMetrics = *metrics
	}

	// Get SES limits
	sesLimits, err := s.GetSESAccountLimits(ctx)
	if err == nil {
		summary.SESLimits = sesLimits

		// Add warning if approaching SES limit
		if sesLimits.UsagePercentage > 80 {
			summary.Warnings = append(summary.Warnings, HealthWarning{
				Type:     "ses_limit",
				Severity: "warning",
				Title:    "Approaching SES Daily Limit",
				Message:  fmt.Sprintf("You've used %.1f%% of your AWS SES daily sending limit (%.0f/%.0f)", sesLimits.UsagePercentage, sesLimits.SentLast24Hours, sesLimits.Max24HourSend),
				Action:   "Request a limit increase from AWS SES console",
			})
		}

		// Add warning if in sandbox mode
		if sesLimits.SandboxMode {
			summary.Warnings = append(summary.Warnings, HealthWarning{
				Type:     "ses_sandbox",
				Severity: "info",
				Title:    "AWS SES Sandbox Mode",
				Message:  "Your SES account is in sandbox mode. You can only send to verified email addresses.",
				Action:   "Request production access from AWS SES console",
			})
		}
	}

	// Get receiving metrics
	var receivedTotal, spamTotal, readTotal int
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN is_spam = true THEN 1 ELSE 0 END), 0) as spam,
			COALESCE(SUM(CASE WHEN is_read = true THEN 1 ELSE 0 END), 0) as read_count
		FROM received_emails
		WHERE org_id = $1 AND created_at >= NOW() - INTERVAL '30 days'
	`, orgID).Scan(&receivedTotal, &spamTotal, &readTotal)
	if err == nil {
		summary.ReceivingMetrics = ReceivingMetrics{
			TotalReceived: receivedTotal,
			TotalSpam:     spamTotal,
			TotalRead:     readTotal,
		}
		if receivedTotal > 0 {
			summary.ReceivingMetrics.SpamRate = float64(spamTotal) / float64(receivedTotal) * 100
			summary.ReceivingMetrics.ReadRate = float64(readTotal) / float64(receivedTotal) * 100
		}
	}

	// Get authentication status
	var totalDomains, verifiedDomains, dkimConfigured, spfConfigured, dmarcConfigured int
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, COALESCE(ses_verified, false), COALESCE(dkim_verified, false), COALESCE(spf_verified, false), COALESCE(dmarc_verified, false)
		FROM domains WHERE org_id = $1
	`, orgID)
	if err == nil {
		defer rows.Close()
		var domains []DomainAuthStatus
		for rows.Next() {
			var d DomainAuthStatus
			if err := rows.Scan(&d.Domain, &d.SESVerified, &d.DKIMVerified, &d.SPFVerified, &d.DMARCVerified); err == nil {
				domains = append(domains, d)
				totalDomains++
				if d.SESVerified {
					verifiedDomains++
				}
				if d.DKIMVerified {
					dkimConfigured++
				}
				if d.SPFVerified {
					spfConfigured++
				}
				if d.DMARCVerified {
					dmarcConfigured++
				}
			}
		}
		summary.AuthStatus = AuthenticationStatus{
			TotalDomains:    totalDomains,
			VerifiedDomains: verifiedDomains,
			DKIMConfigured:  dkimConfigured,
			SPFConfigured:   spfConfigured,
			DMARCConfigured: dmarcConfigured,
			Domains:         domains,
		}

		// Add warnings for unverified domains
		if totalDomains > 0 && verifiedDomains < totalDomains {
			summary.Warnings = append(summary.Warnings, HealthWarning{
				Type:     "domain_verification",
				Severity: "warning",
				Title:    "Unverified Domains",
				Message:  fmt.Sprintf("%d of %d domains are not verified", totalDomains-verifiedDomains, totalDomains),
				Action:   "Complete domain verification in AWS SES",
			})
		}

		// Add warnings for missing DKIM/SPF/DMARC
		if totalDomains > 0 && dkimConfigured < totalDomains {
			summary.Warnings = append(summary.Warnings, HealthWarning{
				Type:     "dkim_missing",
				Severity: "warning",
				Title:    "DKIM Not Configured",
				Message:  fmt.Sprintf("%d domains are missing DKIM configuration", totalDomains-dkimConfigured),
				Action:   "Add DKIM DNS records for your domains",
			})
		}
	}

	// Add warnings for high bounce/complaint rates
	if summary.SendingMetrics.TotalSent >= 100 {
		if summary.SendingMetrics.BounceRate > 5 {
			summary.Warnings = append(summary.Warnings, HealthWarning{
				Type:     "high_bounce_rate",
				Severity: "warning",
				Title:    "High Bounce Rate",
				Message:  fmt.Sprintf("Your bounce rate is %.2f%% (recommended: <5%%)", summary.SendingMetrics.BounceRate),
				Action:   "Clean your email lists and verify addresses before sending",
			})
		}
		if summary.SendingMetrics.ComplaintRate > 0.1 {
			summary.Warnings = append(summary.Warnings, HealthWarning{
				Type:     "high_complaint_rate",
				Severity: "critical",
				Title:    "High Complaint Rate",
				Message:  fmt.Sprintf("Your complaint rate is %.3f%% (recommended: <0.1%%)", summary.SendingMetrics.ComplaintRate),
				Action:   "Review your email content and ensure recipients have opted in",
			})
		}
	}

	// Calculate overall health score
	summary.HealthScore = summary.SendingMetrics.Score
	if len(summary.Warnings) > 0 {
		// Reduce score based on warnings
		criticalCount := 0
		warningCount := 0
		for _, w := range summary.Warnings {
			if w.Severity == "critical" {
				criticalCount++
			} else if w.Severity == "warning" {
				warningCount++
			}
		}
		summary.HealthScore -= criticalCount * 15
		summary.HealthScore -= warningCount * 5
		if summary.HealthScore < 0 {
			summary.HealthScore = 0
		}
	}

	// Determine health status
	if summary.HealthScore >= 90 {
		summary.HealthStatus = "excellent"
	} else if summary.HealthScore >= 70 {
		summary.HealthStatus = "good"
	} else if summary.HealthScore >= 50 {
		summary.HealthStatus = "warning"
	} else {
		summary.HealthStatus = "critical"
	}

	return summary, nil
}

// StartWarmup starts IP warmup for an organization
func (s *HealthService) StartWarmup(ctx context.Context, orgID int64, ipAddress string, scheduleName string) (*WarmupStatus, error) {
	schedule, ok := warmupSchedules[scheduleName]
	if !ok {
		schedule = warmupSchedules["conservative"]
		scheduleName = "conservative"
	}

	// Check if warmup already exists
	var existing int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM warmup_progress WHERE org_id = $1 AND ip_address = $2
	`, orgID, ipAddress).Scan(&existing)
	if err == nil && existing > 0 {
		return nil, fmt.Errorf("warmup already exists for this IP")
	}

	// Create warmup record
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO warmup_progress (org_id, ip_address, schedule_name, current_day, status, started_at, created_at, updated_at)
		VALUES ($1, $2, $3, 1, 'active', NOW(), NOW(), NOW())
	`, orgID, ipAddress, scheduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to start warmup: %w", err)
	}

	return &WarmupStatus{
		IPAddress:    ipAddress,
		ScheduleName: scheduleName,
		CurrentDay:   1,
		DailyLimit:   schedule.Days[0],
		SentToday:    0,
		Status:       "active",
		StartedAt:    time.Now(),
	}, nil
}

// GetWarmupStatus returns current warmup status
func (s *HealthService) GetWarmupStatus(ctx context.Context, orgID int64, ipAddress string) (*WarmupStatus, error) {
	var status WarmupStatus
	var pauseReason sql.NullString
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT ip_address, schedule_name, current_day, status, pause_reason, started_at, completed_at
		FROM warmup_progress
		WHERE org_id = $1 AND ip_address = $2
	`, orgID, ipAddress).Scan(
		&status.IPAddress, &status.ScheduleName, &status.CurrentDay,
		&status.Status, &pauseReason, &status.StartedAt, &completedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no warmup found for this IP")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get warmup status: %w", err)
	}

	if pauseReason.Valid {
		status.PauseReason = pauseReason.String
	}
	if completedAt.Valid {
		status.CompletedAt = &completedAt.Time
	}

	// Get daily limit based on schedule
	schedule := warmupSchedules[status.ScheduleName]
	if status.CurrentDay <= len(schedule.Days) {
		status.DailyLimit = schedule.Days[status.CurrentDay-1]
	}

	// Get sent count for today
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactional_emails
		WHERE org_id = $1 AND created_at >= CURRENT_DATE
	`, orgID).Scan(&status.SentToday)

	// Get total sent
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactional_emails
		WHERE org_id = $1 AND created_at >= $2
	`, orgID, status.StartedAt).Scan(&status.TotalSent)

	return &status, nil
}

// GetWarmupSchedules returns available warmup schedules
func (s *HealthService) GetWarmupSchedules() []WarmupSchedule {
	schedules := make([]WarmupSchedule, 0, len(warmupSchedules))
	for _, schedule := range warmupSchedules {
		schedules = append(schedules, schedule)
	}
	return schedules
}

// GetQuotaStatus returns quota usage for an organization
func (s *HealthService) GetQuotaStatus(ctx context.Context, orgID int64) (*QuotaStatus, error) {
	var status QuotaStatus
	status.OrgID = orgID

	// Get org limits
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(monthly_email_limit, 10000) FROM organizations WHERE id = $1
	`, orgID).Scan(&status.MonthlyLimit)
	if err != nil {
		// Use default if org not found
		status.MonthlyLimit = 10000
	}

	// Set daily limit as 1/30 of monthly or minimum 100
	status.DailyLimit = status.MonthlyLimit / 30
	if status.DailyLimit < 100 {
		status.DailyLimit = 100
	}

	// Get monthly usage
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactional_emails
		WHERE org_id = $1 AND created_at >= DATE_TRUNC('month', CURRENT_DATE)
	`, orgID).Scan(&status.MonthlyUsed)

	// Get daily usage
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactional_emails
		WHERE org_id = $1 AND created_at >= CURRENT_DATE
	`, orgID).Scan(&status.DailyUsed)

	// Calculate remaining
	status.MonthlyRemaining = status.MonthlyLimit - status.MonthlyUsed
	if status.MonthlyRemaining < 0 {
		status.MonthlyRemaining = 0
	}
	status.DailyRemaining = status.DailyLimit - status.DailyUsed
	if status.DailyRemaining < 0 {
		status.DailyRemaining = 0
	}

	// Calculate percentages
	if status.MonthlyLimit > 0 {
		status.MonthlyPercentage = float64(status.MonthlyUsed) / float64(status.MonthlyLimit) * 100
	}
	if status.DailyLimit > 0 {
		status.DailyPercentage = float64(status.DailyUsed) / float64(status.DailyLimit) * 100
	}

	// Create alert if approaching limits
	if status.MonthlyPercentage >= 80 {
		s.createAlert(ctx, orgID, "quota", "warning",
			"Approaching Monthly Quota",
			fmt.Sprintf("You've used %.1f%% of your monthly email quota (%d/%d).", status.MonthlyPercentage, status.MonthlyUsed, status.MonthlyLimit),
			status,
		)
	}

	return &status, nil
}

// GetAlerts returns alerts for an organization
func (s *HealthService) GetAlerts(ctx context.Context, orgID int64, unacknowledgedOnly bool) ([]Alert, error) {
	query := `
		SELECT id, org_id, type, severity, title, message, data, acknowledged, created_at
		FROM alerts
		WHERE (org_id = $1 OR org_id = 0)
	`
	if unacknowledgedOnly {
		query += " AND acknowledged = false"
	}
	query += " ORDER BY created_at DESC LIMIT 100"

	rows, err := s.db.QueryContext(ctx, query, orgID)
	if err != nil {
		return []Alert{}, nil // Return empty array on error
	}
	defer rows.Close()

	alerts := make([]Alert, 0)
	for rows.Next() {
		var a Alert
		var dataJSON []byte
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Type, &a.Severity, &a.Title, &a.Message, &dataJSON, &a.Acknowledged, &a.CreatedAt); err != nil {
			continue
		}
		if len(dataJSON) > 0 {
			json.Unmarshal(dataJSON, &a.Data)
		}
		alerts = append(alerts, a)
	}

	return alerts, nil
}

// AcknowledgeAlert acknowledges an alert
func (s *HealthService) AcknowledgeAlert(ctx context.Context, orgID int64, alertID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE alerts SET acknowledged = true
		WHERE id = $1 AND (org_id = $2 OR org_id = 0)
	`, alertID, orgID)
	if err != nil {
		return fmt.Errorf("failed to acknowledge alert: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("alert not found")
	}

	return nil
}

// GetDeliveryLogs returns recent delivery logs
func (s *HealthService) GetDeliveryLogs(ctx context.Context, orgID int64, page, pageSize int, status string, emailID int64) (map[string]interface{}, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	// Get logs from transactional_emails directly
	query := `
		SELECT id, uuid, subject, to_addresses, status, created_at
		FROM transactional_emails
		WHERE org_id = $1
	`
	args := []interface{}{orgID}
	argIndex := 2

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argIndex)
		args = append(args, status)
		argIndex++
	}
	if emailID > 0 {
		query += fmt.Sprintf(" AND id = $%d", argIndex)
		args = append(args, emailID)
		argIndex++
	}

	// Count total
	var total int
	countQuery := strings.Replace(query, "id, uuid, subject, to_addresses, status, created_at", "COUNT(*)", 1)
	s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)

	// Get logs
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIndex, argIndex+1)
	args = append(args, pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return map[string]interface{}{
			"logs":       []map[string]interface{}{},
			"total":      0,
			"page":       page,
			"pageSize":   pageSize,
			"totalPages": 0,
		}, nil
	}
	defer rows.Close()

	logs := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id int64
		var uuid, subject, emailStatus string
		var toAddresses []string
		var createdAt time.Time

		if err := rows.Scan(&id, &uuid, &subject, &toAddresses, &emailStatus, &createdAt); err != nil {
			continue
		}

		logs = append(logs, map[string]interface{}{
			"id":        id,
			"uuid":      uuid,
			"subject":   subject,
			"to":        toAddresses,
			"status":    emailStatus,
			"createdAt": createdAt,
		})
	}

	totalPages := (total + pageSize - 1) / pageSize

	return map[string]interface{}{
		"logs":       logs,
		"total":      total,
		"page":       page,
		"pageSize":   pageSize,
		"totalPages": totalPages,
	}, nil
}

// createAlert creates a new alert
func (s *HealthService) createAlert(ctx context.Context, orgID int64, alertType, severity, title, message string, data interface{}) {
	dataJSON, _ := json.Marshal(data)
	s.db.ExecContext(ctx, `
		INSERT INTO alerts (org_id, type, severity, title, message, data, acknowledged, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, false, NOW())
		ON CONFLICT DO NOTHING
	`, orgID, alertType, severity, title, message, dataJSON)
}

// reverseIP reverses an IP address for DNSBL lookup
func reverseIP(ip string) string {
	parts := strings.Split(ip, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}
