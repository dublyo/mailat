package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

var ErrDomainNotFound = errors.New("domain not found")

type DomainService struct {
	db              *sql.DB
	cfg             *config.Config
	emailProvider   provider.EmailProvider
	dmarcResolver   provider.DMARCResolver
	sendingProvider sendingSetupProvider
	mxResolver      MXResolver
	mxCache         mxLookupCache
}

func NewDomainService(db *sql.DB, cfg *config.Config) *DomainService {
	svc := &DomainService{db: db, cfg: cfg}

	// Initialize email provider for domain verification
	ctx := context.Background()
	if cfg.EmailProvider == "ses" && cfg.AWSAccessKeyID != "" {
		sesProvider, err := provider.NewSESProvider(ctx, &provider.SESConfig{
			Region:           cfg.AWSRegion,
			AccessKeyID:      cfg.AWSAccessKeyID,
			SecretAccessKey:  cfg.AWSSecretAccessKey,
			ConfigurationSet: cfg.SESConfigurationSet,
		})
		if err != nil {
			fmt.Printf("Warning: Failed to create SES provider for domain service: %v\n", err)
		} else {
			svc.emailProvider = sesProvider
			fmt.Println("DomainService initialized with AWS SES provider")
		}
	}

	return svc
}

// CreateDomain adds a new domain with verification records
func (s *DomainService) CreateDomain(ctx context.Context, orgID int64, req *model.CreateDomainRequest) (*model.Domain, error) {
	domainName := strings.ToLower(req.Name)
	if s.cfg.EmailProvider == "ses" && s.emailProvider == nil {
		return nil, fmt.Errorf("SES is not configured; domain creation is unavailable")
	}

	// Check if domain already exists for this org
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM domains WHERE org_id = $1 AND name = $2)
	`, orgID, domainName).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing domain: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("domain already registered")
	}

	// Check organization's domain limit
	var domainCount int
	var maxDomains int
	err = s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM domains WHERE org_id = $1),
			COALESCE((SELECT max_domains FROM organizations WHERE id = $1), $2)
	`, orgID, s.cfg.DefaultMaxDomains).Scan(&domainCount, &maxDomains)
	if err != nil {
		return nil, fmt.Errorf("failed to check domain limit: %w", err)
	}
	if !s.cfg.DisableAppLimits && maxDomains > 0 && domainCount >= maxDomains {
		return nil, fmt.Errorf("domain limit reached: organization can have a maximum of %d domains", maxDomains)
	}

	// Generate verification token
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	verificationToken := fmt.Sprintf("ue-verify-%s", base64.RawURLEncoding.EncodeToString(tokenBytes))

	// Generate DKIM keypair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate DKIM key: %w", err)
	}

	// Encode private key to PEM
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Encode public key for DNS (base64 of DER)
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal public key: %w", err)
	}
	publicKeyB64 := base64.StdEncoding.EncodeToString(publicKeyDER)

	selector := "mail"

	// Keep absent tokens consistent with the schema default and read paths.
	// In particular, SMTP creation must not persist NULL while reload returns [].
	sesDkimTokens := make([]string, 0)
	var sesVerificationResult *provider.DomainVerificationResult
	emailProvider := "smtp"

	if s.emailProvider != nil && s.emailProvider.Name() == "ses" {
		emailProvider = "ses"
		var err error
		sesVerificationResult, err = s.emailProvider.VerifyDomain(ctx, domainName)
		if err != nil {
			return nil, fmt.Errorf("failed to register domain with SES: %w", err)
		} else {
			// Extract DKIM tokens
			for _, rec := range sesVerificationResult.DKIMRecords {
				// Extract token from CNAME name (e.g., "token._domainkey.example.com")
				parts := strings.Split(rec.Name, "._domainkey.")
				if len(parts) > 0 {
					sesDkimTokens = append(sesDkimTokens, parts[0])
				}
			}
			fmt.Printf("Domain %s registered with SES, DKIM tokens: %v\n", domainName, sesDkimTokens)
		}
	}

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()
	if !s.cfg.DisableAppLimits {
		// Serialize optional operator caps, including concurrent requests from
		// different users. A separate count sees commits made while waiting.
		if err = tx.QueryRowContext(ctx, `SELECT max_domains FROM organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&maxDomains); err != nil {
			return nil, fmt.Errorf("lock organization domain limit: %w", err)
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE org_id=$1`, orgID).Scan(&domainCount); err != nil {
			return nil, fmt.Errorf("failed to count domains: %w", err)
		}
		if maxDomains > 0 && domainCount >= maxDomains {
			return nil, fmt.Errorf("domain limit reached: organization can have a maximum of %d domains", maxDomains)
		}
	}

	// Create domain with SES fields (columns per internal/database/migrations)
	var domain model.Domain
	domainUUID := uuid.New().String()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO domains (uuid, org_id, name, verification_token, dkim_selector,
		                     dkim_public_key, dkim_private_key, ses_dkim_tokens, email_provider, status, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', NOW())
		RETURNING id, uuid, org_id, name, status, verification_token, dkim_selector,
		          dkim_public_key, email_provider, ses_dkim_tokens, verified_at, created_at, updated_at
	`, domainUUID, orgID, domainName, verificationToken, selector,
		publicKeyB64, string(privateKeyPEM), pq.Array(sesDkimTokens), emailProvider).Scan(
		&domain.ID, &domain.UUID, &domain.OrgID, &domain.Name, &domain.Status,
		&domain.VerificationToken, &domain.DKIMSelector, &domain.DKIMPublicKey,
		&domain.EmailProvider, pq.Array(&domain.SESDKIMTokens),
		&domain.VerifiedAt, &domain.CreatedAt, &domain.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create domain: %w", err)
	}

	// Build DNS records based on email provider
	var records []struct {
		RecordType    string
		Hostname      string
		ExpectedValue string
	}

	if emailProvider == "ses" && sesVerificationResult != nil {
		// SES DKIM CNAME records
		for _, rec := range sesVerificationResult.DKIMRecords {
			records = append(records, struct {
				RecordType    string
				Hostname      string
				ExpectedValue string
			}{
				RecordType:    rec.Type,
				Hostname:      rec.Name,
				ExpectedValue: rec.Value,
			})
		}

		// SES authenticates SPF on its MAIL FROM subdomain. Adding an apex SPF
		// alongside another mail provider's policy would make both policies invalid.

		// SES DMARC record
		if sesVerificationResult.DMARCRecord != nil {
			records = append(records, struct {
				RecordType    string
				Hostname      string
				ExpectedValue string
			}{
				RecordType:    sesVerificationResult.DMARCRecord.Type,
				Hostname:      sesVerificationResult.DMARCRecord.Name,
				ExpectedValue: provider.DefaultDMARCValue,
			})
		}

		// SES MAIL FROM DNS records (MX and SPF for bounce subdomain)
		for _, rec := range sesVerificationResult.MailFromRecords {
			value := rec.Value
			if rec.Type == "MX" && rec.Priority > 0 {
				value = fmt.Sprintf("%d %s", rec.Priority, rec.Value)
			}
			records = append(records, struct {
				RecordType    string
				Hostname      string
				ExpectedValue string
			}{
				RecordType:    rec.Type,
				Hostname:      rec.Name,
				ExpectedValue: value,
			})
		}

		// Sending must not change inbound routing at the domain root. The MAIL FROM
		// subdomain MX above is separate from the user's existing inbox provider.
		// Keep verification record for our internal verification
		records = append(records, struct {
			RecordType    string
			Hostname      string
			ExpectedValue string
		}{
			RecordType:    "TXT",
			Hostname:      fmt.Sprintf("_verification.%s", domain.Name),
			ExpectedValue: verificationToken,
		})
	} else {
		// SMTP DNS records (self-hosted)
		records = []struct {
			RecordType    string
			Hostname      string
			ExpectedValue string
		}{
			{
				RecordType:    "TXT",
				Hostname:      fmt.Sprintf("_verification.%s", domain.Name),
				ExpectedValue: verificationToken,
			},
			{
				RecordType:    "MX",
				Hostname:      domain.Name,
				ExpectedValue: fmt.Sprintf("10 mail.%s", domain.Name),
			},
			{
				RecordType:    "TXT",
				Hostname:      domain.Name,
				ExpectedValue: fmt.Sprintf("v=spf1 include:mail.%s ~all", domain.Name),
			},
			{
				RecordType:    "TXT",
				Hostname:      fmt.Sprintf("%s._domainkey.%s", selector, domain.Name),
				ExpectedValue: fmt.Sprintf("v=DKIM1; k=rsa; p=%s", publicKeyB64),
			},
			{
				RecordType:    "TXT",
				Hostname:      fmt.Sprintf("_dmarc.%s", domain.Name),
				ExpectedValue: provider.DefaultDMARCValue,
			},
		}
	}

	for _, rec := range records {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO domain_dns_records (domain_id, record_type, hostname, expected_value, verified)
			VALUES ($1, $2, $3, $4, false)
		`, domain.ID, rec.RecordType, rec.Hostname, rec.ExpectedValue)
		if err != nil {
			return nil, fmt.Errorf("failed to create DNS record: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return &domain, nil
}

// GetDomain retrieves a domain by UUID. Nullable legacy metadata is read using
// schema defaults; a missing key or SES status must never imply verification.
func (s *DomainService) GetDomain(ctx context.Context, orgID int64, domainUUID string) (*model.Domain, error) {
	var domain model.Domain
	var sesDkimTokens []string
	var sesIdentityArn sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, name, status, verification_token, dkim_selector,
		       COALESCE(dkim_public_key, ''), COALESCE(email_provider, 'ses'),
		       COALESCE(ses_verified, false), COALESCE(ses_dkim_tokens, '{}'::text[]), ses_identity_arn,
		       mx_verified, spf_verified, dkim_verified, dmarc_verified,
		       receiving_enabled, verified_at, created_at, updated_at
		FROM domains
		WHERE uuid = $1 AND org_id = $2
	`, domainUUID, orgID).Scan(
		&domain.ID, &domain.UUID, &domain.OrgID, &domain.Name, &domain.Status,
		&domain.VerificationToken, &domain.DKIMSelector, &domain.DKIMPublicKey,
		&domain.EmailProvider, &domain.SESVerified, pq.Array(&sesDkimTokens), &sesIdentityArn,
		&domain.MXVerified, &domain.SPFVerified, &domain.DKIMVerified, &domain.DMARCVerified,
		&domain.ReceivingEnabled, &domain.VerifiedAt, &domain.CreatedAt, &domain.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrDomainNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query domain: %w", err)
	}

	domain.SESDKIMTokens = sesDkimTokens
	if sesIdentityArn.Valid {
		domain.SESIdentityArn = sesIdentityArn.String
	}

	return &domain, nil
}

// ListDomains returns all domains for an organization
func (s *DomainService) ListDomains(ctx context.Context, orgID int64) ([]*model.Domain, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, org_id, name, status, verification_token, dkim_selector, COALESCE(dkim_public_key, ''),
		       COALESCE(email_provider, 'ses'), COALESCE(ses_verified, false),
		       COALESCE(ses_dkim_tokens, '{}'::text[]), ses_identity_arn,
		       mx_verified, spf_verified, dkim_verified, dmarc_verified,
		       receiving_enabled, verified_at, created_at, updated_at
		FROM domains
		WHERE org_id = $1
		ORDER BY created_at DESC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to query domains: %w", err)
	}
	defer rows.Close()

	domains := make([]*model.Domain, 0)
	for rows.Next() {
		var domain model.Domain
		var sesDkimTokens []string
		var sesIdentityArn sql.NullString
		if err := rows.Scan(&domain.ID, &domain.UUID, &domain.OrgID, &domain.Name,
			&domain.Status, &domain.VerificationToken, &domain.DKIMSelector, &domain.DKIMPublicKey,
			&domain.EmailProvider, &domain.SESVerified, pq.Array(&sesDkimTokens), &sesIdentityArn,
			&domain.MXVerified, &domain.SPFVerified, &domain.DKIMVerified, &domain.DMARCVerified,
			&domain.ReceivingEnabled, &domain.VerifiedAt, &domain.CreatedAt, &domain.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan domain: %w", err)
		}
		domain.SESDKIMTokens = sesDkimTokens
		if sesIdentityArn.Valid {
			domain.SESIdentityArn = sesIdentityArn.String
		}
		domains = append(domains, &domain)
	}

	return domains, rows.Err()
}

// GetDNSRecords returns DNS records for a domain
func (s *DomainService) GetDNSRecords(ctx context.Context, domainID int64) ([]*model.DomainDNSRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.domain_id, r.record_type, r.hostname, r.expected_value, r.actual_value, r.verified, r.last_checked_at,
		       d.name, COALESCE(d.email_provider, 'ses')
		FROM domain_dns_records r JOIN domains d ON d.id=r.domain_id
		WHERE r.domain_id = $1
		ORDER BY r.record_type, r.hostname
	`, domainID)
	if err != nil {
		return nil, fmt.Errorf("failed to query DNS records: %w", err)
	}
	defer rows.Close()

	records := make([]*model.DomainDNSRecord, 0)
	for rows.Next() {
		var rec model.DomainDNSRecord
		var actualValue sql.NullString
		var lastCheckedAt sql.NullTime
		var domainName, emailProvider string
		if err := rows.Scan(&rec.ID, &rec.DomainID, &rec.RecordType, &rec.Hostname,
			&rec.Value, &actualValue, &rec.Verified, &lastCheckedAt, &domainName, &emailProvider); err != nil {
			return nil, fmt.Errorf("failed to scan DNS record: %w", err)
		}
		// Legacy onboarding stored apex MX/SPF as if SES owned inbound delivery.
		// Exclude them from sending instructions and VerifyDNS without changing live DNS.
		if emailProvider == "ses" && isDomainRootHostname(rec.Hostname, domainName) &&
			(strings.EqualFold(rec.RecordType, "MX") || (strings.EqualFold(rec.RecordType, "TXT") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rec.Value)), "v=spf1"))) {
			continue
		}
		if lastCheckedAt.Valid {
			rec.VerifiedAt = &lastCheckedAt.Time
		}
		records = append(records, &rec)
	}

	return records, rows.Err()
}

func isDomainRootHostname(hostname, domain string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return hostname == "" || hostname == "@" || hostname == domain
}

// GetDMARC resolves only a domain owned by the caller's organization. The result
// is advisory; automatic DNS setup repeats the inspection at write time.
func (s *DomainService) GetDMARC(ctx context.Context, orgID int64, domainUUID string) (provider.DMARCInspection, error) {
	domain, err := s.GetDomain(ctx, orgID, domainUUID)
	if err != nil {
		return provider.DMARCInspection{}, err
	}
	return provider.InspectDMARC(ctx, domain.Name, s.dmarcResolver), nil
}

// VerifyDNS checks DNS records and updates verification status
func (s *DomainService) VerifyDNS(ctx context.Context, domainID int64) (map[string]bool, error) {
	// Get domain details including email provider
	var domainName, emailProvider string
	err := s.db.QueryRowContext(ctx, `
		SELECT name, COALESCE(email_provider, 'ses') FROM domains WHERE id = $1
	`, domainID).Scan(&domainName, &emailProvider)
	if err != nil {
		return nil, fmt.Errorf("failed to get domain: %w", err)
	}

	records, err := s.GetDNSRecords(ctx, domainID)
	if err != nil {
		return nil, err
	}

	results := make(map[string]bool)
	mxVerified := false
	spfVerified := false
	dkimVerified := false
	dmarcVerified := false
	verificationVerified := false
	sesVerified := false

	// For SES domains, check SES verification status first
	if emailProvider == "ses" && s.emailProvider != nil {
		identity, err := s.emailProvider.CheckDomainVerification(ctx, domainName)
		if err == nil && identity != nil {
			sesVerified = identity.Verified
			dkimVerified = sesVerified // SES Easy DKIM handles DKIM
			fmt.Printf("SES verification status for %s: %v\n", domainName, sesVerified)
		}
	}

	for _, rec := range records {
		verified := false
		var actualValue string

		switch rec.RecordType {
		case "TXT":
			if strings.EqualFold(strings.TrimSuffix(rec.Hostname, "."), "_dmarc."+domainName) {
				// A preserved or inherited policy need not match our default. Validate
				// the whole record set so duplicates cannot pass a prefix-only check.
				inspection := provider.InspectDMARC(ctx, domainName, s.dmarcResolver)
				verified, actualValue = inspection.Verified, inspection.Value
			} else {
				verified, actualValue = s.verifyTXTRecord(rec.Hostname, rec.Value)
			}
			// Determine which TXT record this is
			if strings.Contains(rec.Hostname, "_verification") {
				verificationVerified = verified
			} else if strings.Contains(rec.Hostname, "_domainkey") {
				// For SES, DKIM is handled by CNAME records
				if emailProvider != "ses" {
					dkimVerified = verified
				}
			} else if strings.Contains(rec.Hostname, "_dmarc") {
				dmarcVerified = verified
			} else if strings.HasPrefix(rec.Value, "v=spf1") {
				spfVerified = verified
			}
		case "MX":
			verified, actualValue = s.verifyMXRecord(rec.Hostname, rec.Value)
			mxVerified = verified
		case "CNAME":
			verified, actualValue = s.verifyCNAMERecord(rec.Hostname, rec.Value)
			// For SES DKIM CNAME records
			if strings.Contains(rec.Hostname, "_domainkey") {
				dkimVerified = dkimVerified || verified
			}
		}

		results[rec.Hostname] = verified

		// Update record verification status
		s.db.ExecContext(ctx, `
			UPDATE domain_dns_records
			SET verified = $1, actual_value = $2, last_checked_at = NOW()
			WHERE id = $3
		`, verified, actualValue, rec.ID)
	}

	// Update domain verification flags
	s.db.ExecContext(ctx, `
		UPDATE domains
		SET mx_verified = $1, spf_verified = $2, dkim_verified = $3, dmarc_verified = $4,
		    ses_verified = $5, last_dns_check_at = NOW(), updated_at = NOW()
		WHERE id = $6
	`, mxVerified, spfVerified, dkimVerified, dmarcVerified, sesVerified, domainID)

	// Activate domain based on provider requirements
	shouldActivate := false
	if emailProvider == "ses" {
		// For SES: require SES verification and our verification token
		shouldActivate = sesVerified && verificationVerified
	} else {
		// For SMTP: require MX and verification token
		shouldActivate = verificationVerified && mxVerified
	}

	if shouldActivate {
		s.db.ExecContext(ctx, `
			UPDATE domains SET status = 'active', verified_at = NOW(), updated_at = NOW()
			WHERE id = $1 AND status = 'pending'
		`, domainID)
	}

	return results, nil
}

func (s *DomainService) verifyTXTRecord(hostname, expectedValue string) (bool, string) {
	records, err := net.LookupTXT(hostname)
	if err != nil {
		return false, ""
	}

	for _, record := range records {
		if strings.Contains(record, expectedValue) || record == expectedValue {
			return true, record
		}
		// For SPF/DKIM/DMARC, check if the prefix matches
		if strings.HasPrefix(expectedValue, "v=") && strings.HasPrefix(record, strings.Split(expectedValue, ";")[0]) {
			return true, record
		}
	}

	if len(records) > 0 {
		return false, records[0]
	}
	return false, ""
}

func (s *DomainService) verifyMXRecord(hostname, expectedValue string) (bool, string) {
	records, err := net.LookupMX(strings.TrimSuffix(hostname, ".") + ".")
	if err != nil {
		return false, ""
	}

	// Extract expected host and priority
	parts := strings.SplitN(expectedValue, " ", 2)
	if len(parts) < 2 {
		return false, ""
	}
	expectedHost := strings.TrimSuffix(parts[1], ".")

	var actualValues []string
	for _, mx := range records {
		mxHost := strings.TrimSuffix(mx.Host, ".")
		actualValues = append(actualValues, fmt.Sprintf("%d %s", mx.Pref, mxHost))
		if strings.EqualFold(mxHost, expectedHost) {
			return true, fmt.Sprintf("%d %s", mx.Pref, mxHost)
		}
	}

	if len(actualValues) > 0 {
		return false, actualValues[0]
	}
	return false, ""
}

func (s *DomainService) verifyCNAMERecord(hostname, expectedValue string) (bool, string) {
	cname, err := net.LookupCNAME(hostname)
	if err != nil {
		return false, ""
	}

	expected := strings.TrimSuffix(expectedValue, ".")
	actual := strings.TrimSuffix(cname, ".")
	return strings.EqualFold(actual, expected), actual
}

// DeleteDomain removes a domain and its records
func (s *DomainService) DeleteDomain(ctx context.Context, orgID int64, domainUUID string) error {
	if _, err := uuid.Parse(domainUUID); err != nil {
		return ErrDomainNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The domain lock keeps a mailbox from being created while it is deleted;
	// deleting it would cascade the mailbox logins' identities away.
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM domains WHERE uuid=$1 AND org_id=$2 FOR UPDATE`, domainUUID, orgID).Scan(&id)
	if err == sql.ErrNoRows {
		return ErrDomainNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to delete domain: %w", err)
	}
	var mailboxes int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_accounts WHERE domain_id=$1 AND removed_at IS NULL`, id).Scan(&mailboxes); err != nil {
		return err
	}
	if mailboxes > 0 {
		return orgError(http.StatusConflict, fmt.Sprintf("Remove the %d mailboxes on this domain first", mailboxes))
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM domains WHERE id=$1`, id); err != nil {
		return fmt.Errorf("failed to delete domain: %w", err)
	}
	return tx.Commit()
}

// InitiateSESVerification registers an existing domain with AWS SES
func (s *DomainService) InitiateSESVerification(ctx context.Context, domainID int64) ([]map[string]string, error) {
	// Get domain details
	var domainName, verificationToken string
	err := s.db.QueryRowContext(ctx, `
		SELECT name, verification_token FROM domains WHERE id = $1
	`, domainID).Scan(&domainName, &verificationToken)
	if err != nil {
		return nil, fmt.Errorf("domain not found: %w", err)
	}

	// Check if we have SES provider
	if s.emailProvider == nil || s.emailProvider.Name() != "ses" {
		// Try to create SES provider if not available
		if s.cfg.AWSAccessKeyID != "" {
			sesProvider, err := provider.NewSESProvider(ctx, &provider.SESConfig{
				Region:           s.cfg.AWSRegion,
				AccessKeyID:      s.cfg.AWSAccessKeyID,
				SecretAccessKey:  s.cfg.AWSSecretAccessKey,
				ConfigurationSet: s.cfg.SESConfigurationSet,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create SES provider: %w", err)
			}
			s.emailProvider = sesProvider
		} else {
			return nil, fmt.Errorf("AWS SES is not configured")
		}
	}

	// Register domain with SES
	sesVerificationResult, err := s.emailProvider.VerifyDomain(ctx, domainName)
	if err != nil {
		return nil, fmt.Errorf("failed to register domain with SES: %w", err)
	}

	// Extract DKIM tokens
	var sesDkimTokens []string
	var sesRecords []map[string]string

	for _, rec := range sesVerificationResult.DKIMRecords {
		parts := strings.Split(rec.Name, "._domainkey.")
		if len(parts) > 0 {
			sesDkimTokens = append(sesDkimTokens, parts[0])
		}
		sesRecords = append(sesRecords, map[string]string{
			"type":  rec.Type,
			"name":  rec.Name,
			"value": rec.Value,
		})
	}

	// Update domain with SES info
	_, err = s.db.ExecContext(ctx, `
		UPDATE domains
		SET email_provider = 'ses', ses_dkim_tokens = $1, updated_at = NOW()
		WHERE id = $2
	`, pq.Array(sesDkimTokens), domainID)
	if err != nil {
		return nil, fmt.Errorf("failed to update domain: %w", err)
	}

	// VerifyDNS requires this token even after SES verifies the domain. Preserve its
	// checked state on re-init, and repair installations where an earlier re-init removed it.
	verificationHostname := "_verification." + domainName
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO domain_dns_records(domain_id,record_type,hostname,expected_value,verified)
		VALUES($1,'TXT',$2,$3,false)
		ON CONFLICT(domain_id,record_type,hostname) DO UPDATE SET expected_value=EXCLUDED.expected_value
	`, domainID, verificationHostname, verificationToken)
	if err != nil {
		return nil, fmt.Errorf("failed to preserve domain verification record: %w", err)
	}
	sesRecords = append(sesRecords, map[string]string{"type": "TXT", "name": verificationHostname, "value": verificationToken})

	// Replace provider DNS records without dropping the internal ownership check.
	_, err = s.db.ExecContext(ctx, `DELETE FROM domain_dns_records WHERE domain_id=$1 AND NOT (record_type='TXT' AND hostname=$2)`, domainID, verificationHostname)
	if err != nil {
		fmt.Printf("Warning: Failed to delete old DNS records: %v\n", err)
	}

	// Add SES DKIM CNAME records
	for _, rec := range sesVerificationResult.DKIMRecords {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO domain_dns_records (domain_id, record_type, hostname, expected_value, verified)
			VALUES ($1, $2, $3, $4, false)
		`, domainID, rec.Type, rec.Name, rec.Value)
		if err != nil {
			fmt.Printf("Warning: Failed to insert DNS record: %v\n", err)
		}
	}

	// MAIL FROM SPF below is sufficient; preserve the root domain's other senders.

	// Add DMARC record
	if sesVerificationResult.DMARCRecord != nil {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO domain_dns_records (domain_id, record_type, hostname, expected_value, verified)
			VALUES ($1, $2, $3, $4, false)
		`, domainID, sesVerificationResult.DMARCRecord.Type, sesVerificationResult.DMARCRecord.Name, provider.DefaultDMARCValue)
		sesRecords = append(sesRecords, map[string]string{
			"type":  sesVerificationResult.DMARCRecord.Type,
			"name":  sesVerificationResult.DMARCRecord.Name,
			"value": provider.DefaultDMARCValue,
		})
	}

	// Add MAIL FROM DNS records (MX and SPF for bounce subdomain)
	for _, rec := range sesVerificationResult.MailFromRecords {
		value := rec.Value
		if rec.Type == "MX" && rec.Priority > 0 {
			value = fmt.Sprintf("%d %s", rec.Priority, rec.Value)
		}
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO domain_dns_records (domain_id, record_type, hostname, expected_value, verified)
			VALUES ($1, $2, $3, $4, false)
		`, domainID, rec.Type, rec.Name, value)
		if err != nil {
			fmt.Printf("Warning: Failed to insert MAIL FROM DNS record: %v\n", err)
		}
		sesRecords = append(sesRecords, map[string]string{
			"type":  rec.Type,
			"name":  rec.Name,
			"value": value,
		})
	}

	// Receiving has a separate, explicit setup flow. Registering a sender must
	// never offer an inbound MX that could replace an existing inbox provider.

	fmt.Printf("Domain %s registered with SES, DKIM tokens: %v, MAIL FROM: %s\n", domainName, sesDkimTokens, sesVerificationResult.MailFromDomain)
	return sesRecords, nil
}

// CheckSESVerificationStatus checks the SES verification status for a domain
func (s *DomainService) CheckSESVerificationStatus(ctx context.Context, domainID int64) (map[string]interface{}, error) {
	var domainName string
	err := s.db.QueryRowContext(ctx, `
		SELECT name FROM domains WHERE id = $1
	`, domainID).Scan(&domainName)
	if err != nil {
		return nil, fmt.Errorf("domain not found: %w", err)
	}

	if s.emailProvider == nil || s.emailProvider.Name() != "ses" {
		return nil, fmt.Errorf("SES provider not configured")
	}

	identity, err := s.emailProvider.CheckDomainVerification(ctx, domainName)
	if err != nil {
		return nil, fmt.Errorf("failed to check SES verification: %w", err)
	}

	// Update domain SES status
	_, err = s.db.ExecContext(ctx, `
		UPDATE domains SET ses_verified = $1, updated_at = NOW() WHERE id = $2
	`, identity.Verified, domainID)
	if err != nil {
		fmt.Printf("Warning: Failed to update SES status: %v\n", err)
	}

	return map[string]interface{}{
		"verified":         identity.Verified,
		"domain":           identity.Domain,
		"dkimTokens":       identity.DKIMTokens,
		"mailFromDomain":   identity.MailFromDomain,
		"mailFromVerified": identity.MailFromVerified,
	}, nil
}

// GetCloudflareZones lists all zones available for the API token
func (s *DomainService) GetCloudflareZones(ctx context.Context, apiToken string) ([]provider.CloudflareZone, error) {
	return provider.CloudflareListZones(ctx, apiToken)
}

// ErrReceivingNotEnabled means the root MX was requested before the owner
// enabled receiving for the domain.
var ErrReceivingNotEnabled = errors.New("enable receiving for this domain before adding its MX record")

// AddDNSToCloudflare adds the required DNS records to Cloudflare
func (s *DomainService) AddDNSToCloudflare(ctx context.Context, domainID int64, apiToken, zoneID string) ([]map[string]interface{}, error) {
	return s.AddDNSToCloudflareScoped(ctx, domainID, apiToken, zoneID, false)
}

// AddDNSToCloudflareScoped adds the domain's records, or only its receiving
// MX when onlyReceivingMX is set. The root MX is written only while receiving
// is enabled and the zone has no other root MX; a conflict changes nothing.
func (s *DomainService) AddDNSToCloudflareScoped(ctx context.Context, domainID int64, apiToken, zoneID string, onlyReceivingMX bool) ([]map[string]interface{}, error) {
	// Load records before taking the lock, so a small connection pool does not
	// deadlock waiting for a second connection while this transaction is open.
	records, err := s.GetDNSRecords(ctx, domainID)
	if err != nil {
		return nil, err
	}
	var orgID int64
	var preName string
	if err := s.db.QueryRowContext(ctx, `SELECT org_id, name FROM domains WHERE id=$1`, domainID).Scan(&orgID, &preName); err != nil {
		return nil, fmt.Errorf("domain not found: %w", err)
	}
	receivingMX, err := s.receivingMXRecord(ctx, orgID, domainID, preName)
	if err != nil {
		return nil, fmt.Errorf("failed to load receiving record: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to lock domain DNS setup: %w", err)
	}
	defer tx.Rollback()
	// Serialize setup across API replicas; a second request must inspect DNS
	// after the first finishes. DNS changes are external, so this is only a lock.
	var domainName, emailProvider string
	var receivingEnabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT name, COALESCE(email_provider, 'ses'), receiving_enabled FROM domains WHERE id = $1 FOR UPDATE
	`, domainID).Scan(&domainName, &emailProvider, &receivingEnabled)
	if err != nil {
		return nil, fmt.Errorf("domain not found: %w", err)
	}
	// SES instructions hide the root MX; offer it here only once the owner has
	// opted into receiving. Legacy rows with the same value are not duplicated.
	isReceivingMX := func(rec *model.DomainDNSRecord) bool {
		return strings.EqualFold(rec.RecordType, "MX") && isDomainRootHostname(rec.Hostname, domainName) &&
			strings.EqualFold(strings.Join(strings.Fields(strings.TrimSuffix(rec.Value, ".")), " "), receivingMX.Value)
	}
	if onlyReceivingMX {
		if !receivingEnabled {
			return nil, ErrReceivingNotEnabled
		}
		records = []*model.DomainDNSRecord{{RecordType: "MX", Hostname: domainName, Value: receivingMX.Value}}
	} else if receivingEnabled {
		present := false
		for _, rec := range records {
			present = present || isReceivingMX(rec)
		}
		if !present {
			records = append(records, &model.DomainDNSRecord{RecordType: "MX", Hostname: domainName, Value: receivingMX.Value})
		}
	}
	// Different organizations can connect the same domain. Coordinate those
	// requests too, since DNS uniqueness is by hostname rather than database ID.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "mailat:dns:"+strings.ToLower(domainName)); err != nil {
		return nil, fmt.Errorf("failed to serialize DNS setup: %w", err)
	}

	// If no zone ID provided, try to find it by domain name
	if zoneID == "" {
		zones, err := provider.CloudflareListZones(ctx, apiToken)
		if err != nil {
			return nil, fmt.Errorf("failed to list Cloudflare zones: %w", err)
		}
		for _, zone := range zones {
			if zone.Name == domainName || strings.HasSuffix(domainName, "."+zone.Name) {
				zoneID = zone.ID
				break
			}
		}
		if zoneID == "" {
			return nil, fmt.Errorf("could not find Cloudflare zone for domain %s", domainName)
		}
	}

	if err := provider.CloudflareValidateZone(ctx, apiToken, zoneID, domainName); err != nil {
		return nil, fmt.Errorf("Cloudflare zone cannot manage this domain: %w", err)
	}

	var results []map[string]interface{}

	for _, rec := range records {
		result := map[string]interface{}{
			"hostname": rec.Hostname,
			"type":     rec.RecordType,
			"value":    rec.Value,
		}
		// Protect legacy records too: older SES onboarding stored an inbound MX.
		// Only subdomain MX records (such as SES MAIL FROM) belong in automatic setup.
		hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rec.Hostname)), ".")
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domainName)), ".")
		if strings.EqualFold(rec.RecordType, "MX") && !strings.HasSuffix(hostname, "."+domain) {
			if !receivingEnabled || !isReceivingMX(rec) {
				result["success"] = false
				result["skipped"] = true
				result["status"] = "skipped"
				result["reason"] = "Receiving MX records require separate manual setup; existing mail routing was preserved."
				if isReceivingMX(rec) {
					result["reason"] = "Receiving is off for this domain; enable receiving first. Existing mail routing was preserved."
				}
				results = append(results, result)
				continue
			}
			// No companions: an existing root SPF must not block the MX, while
			// any other root MX (or a root CNAME) is reported as a conflict.
			result["hostname"], result["value"] = domainName, receivingMX.Value
			status, err := provider.CloudflareCreateDNSRecord(ctx, apiToken, zoneID, "MX", domainName, receivingMX.Value)
			result["status"] = status
			result["receiving"] = true
			if err != nil {
				result["success"] = false
				var conflict *provider.CloudflareDNSConflictError
				if errors.As(err, &conflict) {
					result["skipped"] = true
					result["reason"] = "The domain root already has a conflicting record (another MX, a null MX or a CNAME); nothing was changed. Review it in Cloudflare to receive mail here."
				} else {
					result["status"] = "failed"
					result["error"] = err.Error()
				}
			} else {
				result["success"] = true
				s.mxCache.forget(domain)
			}
			results = append(results, result)
			continue
		}
		if strings.EqualFold(rec.RecordType, "TXT") && isDomainRootHostname(rec.Hostname, domainName) && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rec.Value)), "v=spf1") {
			result["success"] = false
			result["skipped"] = true
			result["status"] = "skipped"
			result["reason"] = "Root SPF requires manual review with your existing mail providers; no policy was changed."
			results = append(results, result)
			continue
		}
		if strings.EqualFold(rec.RecordType, "TXT") && hostname == "_dmarc."+domain {
			if emailProvider != "ses" {
				result["success"], result["skipped"], result["status"] = false, true, "skipped"
				result["reason"] = "Review this manual SMTP domain's DMARC policy with its mail providers."
			} else {
				// Always use the approved default when missing, including domains
				// whose stored suggestions predate conditional DMARC onboarding.
				status, inspection, ensureErr := provider.CloudflareEnsureDMARC(ctx, apiToken, zoneID, domainName, s.dmarcResolver)
				result["status"], result["dmarc"] = status, inspection
				result["value"] = inspection.Value
				if inspection.Value == "" {
					result["value"] = provider.DefaultDMARCValue
				}
				result["success"] = ensureErr == nil && (status == "created" || status == "preserved")
				result["reason"] = inspection.Reason
				if ensureErr != nil {
					if status == "conflict" {
						result["skipped"] = true
						result["reason"] = ensureErr.Error()
					} else {
						result["error"] = ensureErr.Error()
					}
				}
			}
			results = append(results, result)
			continue
		}

		// Add record to Cloudflare
		var companions []provider.DNSRecord
		for _, other := range records {
			if !strings.EqualFold(strings.TrimSuffix(other.Hostname, "."), strings.TrimSuffix(rec.Hostname, ".")) || other.ID == rec.ID {
				continue
			}
			companion := provider.DNSRecord{Type: other.RecordType, Name: other.Hostname, Value: other.Value}
			if other.RecordType == "MX" {
				if _, err := fmt.Sscanf(other.Value, "%d %s", &companion.Priority, &companion.Value); err != nil {
					continue
				}
			} else if other.RecordType != "TXT" || !strings.HasPrefix(strings.ToLower(other.Value), "v=spf1") {
				continue
			}
			companions = append(companions, companion)
		}
		status, err := provider.CloudflareCreateDNSRecord(ctx, apiToken, zoneID, rec.RecordType, rec.Hostname, rec.Value, companions...)
		result["status"] = status
		if err != nil {
			result["success"] = false
			var conflict *provider.CloudflareDNSConflictError
			if errors.As(err, &conflict) {
				result["skipped"] = true
				result["reason"] = err.Error()
			} else {
				result["status"] = "failed"
				result["error"] = err.Error()
			}
		} else {
			result["success"] = true
		}

		results = append(results, result)
	}

	return results, nil
}
