package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/dublyo/mailat/api/internal/provider"
)

type sendingSetupProvider interface {
	SetupSendingResources(context.Context, string) (*provider.SendingSetupResources, error)
	EnsureSendingFeedback(context.Context, string, string, []string) error
	SubscribeWebhook(context.Context, string, string) error
	SendingSubscriptionActive(context.Context, string, string) (bool, error)
	WebhookURL(string) string
}
type SendingReadiness struct {
	DomainUUID         string    `json:"domainUuid"`
	StorageReady       bool      `json:"storageReady"`
	FeedbackConfigured bool      `json:"feedbackConfigured"`
	SubscriptionStatus string    `json:"subscriptionStatus"`
	FeedbackReady      bool      `json:"feedbackReady"`
	Reason             string    `json:"reason"`
	CheckedAt          time.Time `json:"checkedAt"`
}

func (s *DomainService) GetSendingStatus(ctx context.Context, orgID int64, id string) (*SendingReadiness, error) {
	r := &SendingReadiness{DomainUUID: id, CheckedAt: time.Now().UTC()}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(d.attachment_s3_bucket,''),NULLIF(d.receiving_s3_bucket,''),'')<>'',d.sending_feedback_ready,COALESCE(c.status,'not_configured'),d.sending_setup_error FROM domains d LEFT JOIN sending_configs c ON c.org_id=d.org_id WHERE d.uuid=$1 AND d.org_id=$2`, id, orgID).Scan(&r.StorageReady, &r.FeedbackConfigured, &r.SubscriptionStatus, &r.Reason)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("domain not found")
	}
	if err != nil {
		return nil, err
	}
	r.FeedbackReady = r.FeedbackConfigured && r.SubscriptionStatus == "active"
	if r.Reason == "" && !r.FeedbackReady {
		r.Reason = "Set up sending resources, then wait for SNS subscription confirmation"
	}
	return r, nil
}
func (s *DomainService) EnsureDomainSendingResources(ctx context.Context, orgID, domainID int64) (*SendingReadiness, error) {
	var id, name, orgUUID, status string
	var verified bool
	err := s.db.QueryRowContext(ctx, `SELECT d.uuid,d.name,o.uuid,d.status,d.ses_verified FROM domains d JOIN organizations o ON o.id=d.org_id WHERE d.id=$1 AND d.org_id=$2`, domainID, orgID).Scan(&id, &name, &orgUUID, &status, &verified)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("domain not found")
	}
	if err != nil {
		return nil, err
	}
	if status != "active" || !verified {
		return nil, &provider.MailValidationError{Message: "verify the SES domain before setting up sending resources"}
	}
	p := s.sendingProvider
	if p == nil {
		if s.cfg == nil || s.cfg.EmailProvider != "ses" || s.cfg.AWSAccessKeyID == "" {
			return nil, fmt.Errorf("SES setup is unavailable")
		}
		endpoint, e := url.Parse(s.cfg.APIUrl)
		if e != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
			return nil, &provider.MailValidationError{Message: "API_URL must be a public HTTPS URL for SNS confirmation"}
		}
		p, err = provider.NewReceivingProvider(&provider.ReceivingConfig{Region: s.cfg.AWSRegion, AccessKeyID: s.cfg.AWSAccessKeyID, SecretAccessKey: s.cfg.AWSSecretAccessKey, WebhookBaseURL: s.cfg.APIUrl})
		if err != nil {
			return nil, err
		}
	}
	// A session lock serializes retries, while committed credentials remain visible
	// to SNS confirmation requests on another DB connection before Subscribe.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended('mailat:sending:'||$1::text,0))`, orgID); err != nil {
		return nil, err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('mailat:sending:'||$1::text,0))`, orgID)
	var bucket, region, topic, secret string
	err = conn.QueryRowContext(ctx, `SELECT s3_bucket,s3_region,sns_topic_arn,webhook_secret FROM sending_configs WHERE org_id=$1`, orgID).Scan(&bucket, &region, &topic, &secret)
	if err == sql.ErrNoRows {
		resources, e := p.SetupSendingResources(ctx, orgUUID)
		if e != nil {
			return nil, fmt.Errorf("sending resource setup failed; retry setup after checking AWS permissions")
		}
		bucket, region, topic, secret = resources.Bucket, resources.Region, resources.TopicARN, generateRandomToken(32)
		if _, err = conn.ExecContext(ctx, `INSERT INTO sending_configs(org_id,s3_bucket,s3_region,sns_topic_arn,webhook_secret,status) VALUES($1,$2,$3,$4,$5,'pending')`, orgID, bucket, region, topic, secret); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, `UPDATE domains SET attachment_s3_bucket=$1,sending_setup_at=NOW(),sending_setup_error='' WHERE id=$2 AND org_id=$3`, bucket, domainID, orgID); err != nil {
		return nil, err
	}
	fail := func(reason string) (*SendingReadiness, error) {
		_, e := conn.ExecContext(ctx, `UPDATE domains SET sending_feedback_ready=false,sending_setup_error=$1 WHERE id=$2 AND org_id=$3`, reason, domainID, orgID)
		if e != nil {
			return nil, e
		}
		return s.GetSendingStatus(ctx, orgID, id)
	}
	endpoint := p.WebhookURL(secret)
	if err = p.SubscribeWebhook(ctx, topic, endpoint); err != nil {
		return fail("SNS subscription could not be requested; retry setup")
	}
	var trusted string
	err = conn.QueryRowContext(ctx, `SELECT sns_topic_arn FROM receiving_configs WHERE org_id=$1 AND status IN ('active','pending')`, orgID).Scan(&trusted)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err = p.EnsureSendingFeedback(ctx, name, topic, []string{trusted}); err != nil {
		reason := "SES feedback setup could not finish; retry setup"
		if errors.Is(err, provider.ErrSendingFeedbackConflict) {
			reason = provider.ErrSendingFeedbackConflict.Error()
		}
		return fail(reason)
	}
	active, err := p.SendingSubscriptionActive(ctx, topic, endpoint)
	if err != nil {
		return fail("SNS subscription could not be verified; retry setup")
	}
	state := "pending"
	if active {
		state = "active"
	}
	if _, err = conn.ExecContext(ctx, `UPDATE sending_configs SET status=$1,updated_at=NOW() WHERE org_id=$2`, state, orgID); err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, `UPDATE domains SET sending_feedback_ready=true,sending_setup_error='' WHERE id=$1 AND org_id=$2`, domainID, orgID); err != nil {
		return nil, err
	}
	return s.GetSendingStatus(ctx, orgID, id)
}

// Called only after the controller verifies the SNS signature and confirms with SNS.
func (s *ReceivingService) ConfirmNotificationSubscription(ctx context.Context, auth *ReceivingAuthorization) error {
	table := "receiving_configs"
	if auth.SendingOnly {
		table = "sending_configs"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET status='active',updated_at=NOW() WHERE org_id=$1 AND sns_topic_arn=$2`, auth.OrgID, auth.TopicARN)
	return err
}
