package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/gogf/gf/v2/frame/g"
)

// ReceivingConfig contains configuration for email receiving setup
type ReceivingConfig struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	WebhookBaseURL  string // Base URL for SNS webhook (e.g., https://api.example.com)
}

// ReceivingSetupResult contains the result of setting up email receiving
type ReceivingSetupResult struct {
	S3Bucket       string
	S3Region       string
	SNSTopicArn    string
	RuleSetName    string
	RuleName       string
	WebhookURL     string
	WebhookSecret  string
}

// ReceivingProvider handles AWS email receiving setup
type ReceivingProvider struct {
	s3Client  *s3.Client
	sesClient *ses.Client
	snsClient *sns.Client
	region    string
	webhookURL string
}

// NewReceivingProvider creates a new receiving provider
func NewReceivingProvider(cfg *ReceivingConfig) (*ReceivingProvider, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &ReceivingProvider{
		s3Client:   s3.NewFromConfig(awsCfg),
		sesClient:  ses.NewFromConfig(awsCfg),
		snsClient:  sns.NewFromConfig(awsCfg),
		region:     cfg.Region,
		webhookURL: cfg.WebhookBaseURL,
	}, nil
}

// SetupReceiving sets up all AWS resources for email receiving for a domain
func (p *ReceivingProvider) SetupReceiving(ctx context.Context, orgID int64, domain string) (*ReceivingSetupResult, error) {
	g.Log().Infof(ctx, "Setting up email receiving for domain: %s (org: %d)", domain, orgID)

	// Generate unique identifiers
	suffix := generateShortID()
	bucketName := fmt.Sprintf("mailat-%d-%s", orgID, suffix)
	topicName := fmt.Sprintf("mailat-incoming-%d-%s", orgID, suffix)
	ruleSetName := fmt.Sprintf("mailat-rules-%d", orgID)
	ruleName := fmt.Sprintf("receive-%s", strings.ReplaceAll(domain, ".", "-"))
	webhookSecret := generateWebhookSecret()

	// 1. Create S3 bucket
	g.Log().Infof(ctx, "Creating S3 bucket: %s", bucketName)
	if err := p.createS3Bucket(ctx, bucketName); err != nil {
		g.Log().Errorf(ctx, "Failed to create S3 bucket %s: %v", bucketName, err)
		return nil, fmt.Errorf("failed to create S3 bucket: %w", err)
	}
	g.Log().Infof(ctx, "S3 bucket created successfully: %s", bucketName)

	// 2. Create SNS topic
	g.Log().Infof(ctx, "Creating SNS topic: %s", topicName)
	topicArn, err := p.createSNSTopic(ctx, topicName)
	if err != nil {
		g.Log().Errorf(ctx, "Failed to create SNS topic %s: %v", topicName, err)
		return nil, fmt.Errorf("failed to create SNS topic: %w", err)
	}
	g.Log().Infof(ctx, "SNS topic created successfully: %s", topicArn)

	// 3. Subscribe webhook to SNS topic
	webhookURL := fmt.Sprintf("%s/api/v1/webhooks/ses/incoming?secret=%s", p.webhookURL, webhookSecret)
	g.Log().Infof(ctx, "Subscribing webhook to SNS topic: %s", webhookURL)
	if err := p.subscribeSNSWebhook(ctx, topicArn, webhookURL); err != nil {
		g.Log().Errorf(ctx, "Failed to subscribe webhook: %v", err)
		return nil, fmt.Errorf("failed to subscribe webhook: %w", err)
	}
	g.Log().Infof(ctx, "Webhook subscribed successfully")

	// 4. Create or get receipt rule set
	g.Log().Infof(ctx, "Setting up receipt rule set: %s", ruleSetName)
	if err := p.ensureReceiptRuleSet(ctx, ruleSetName); err != nil {
		g.Log().Errorf(ctx, "Failed to ensure receipt rule set: %v", err)
		return nil, fmt.Errorf("failed to ensure receipt rule set: %w", err)
	}
	g.Log().Infof(ctx, "Receipt rule set ready: %s", ruleSetName)

	// 5. Create receipt rule for the domain
	g.Log().Infof(ctx, "Creating receipt rule: %s for domain: %s", ruleName, domain)
	if err := p.createReceiptRule(ctx, ruleSetName, ruleName, domain, bucketName, topicArn); err != nil {
		g.Log().Errorf(ctx, "Failed to create receipt rule: %v", err)
		return nil, fmt.Errorf("failed to create receipt rule: %w", err)
	}
	g.Log().Infof(ctx, "Receipt rule created successfully: %s", ruleName)

	// 6. Set the rule set as active
	g.Log().Infof(ctx, "Activating receipt rule set: %s", ruleSetName)
	if err := p.activateReceiptRuleSet(ctx, ruleSetName); err != nil {
		// This might fail if it's already active, which is fine
		g.Log().Warningf(ctx, "Could not activate rule set (may already be active): %v", err)
	}

	return &ReceivingSetupResult{
		S3Bucket:      bucketName,
		S3Region:      p.region,
		SNSTopicArn:   topicArn,
		RuleSetName:   ruleSetName,
		RuleName:      ruleName,
		WebhookURL:    webhookURL,
		WebhookSecret: webhookSecret,
	}, nil
}

// createS3Bucket creates an S3 bucket with proper configuration
func (p *ReceivingProvider) createS3Bucket(ctx context.Context, bucketName string) error {
	createInput := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}

	// Only add LocationConstraint for non-us-east-1 regions
	if p.region != "us-east-1" {
		createInput.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(p.region),
		}
	}

	_, err := p.s3Client.CreateBucket(ctx, createInput)
	if err != nil {
		// Check if bucket already exists
		if strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
			g.Log().Infof(ctx, "S3 bucket already exists: %s", bucketName)
			return nil
		}
		return err
	}

	// Add bucket policy to allow SES to write
	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Sid":    "AllowSESPuts",
				"Effect": "Allow",
				"Principal": map[string]string{
					"Service": "ses.amazonaws.com",
				},
				"Action":   "s3:PutObject",
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/*", bucketName),
			},
		},
	}

	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("failed to marshal bucket policy: %w", err)
	}

	_, err = p.s3Client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(bucketName),
		Policy: aws.String(string(policyJSON)),
	})
	if err != nil {
		g.Log().Warningf(ctx, "Failed to set bucket policy (SES may not have permissions): %v", err)
	}

	// Enable versioning for safety
	_, err = p.s3Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucketName),
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: s3types.BucketVersioningStatusEnabled,
		},
	})
	if err != nil {
		g.Log().Warningf(ctx, "Failed to enable versioning: %v", err)
	}

	// Set lifecycle rule to delete old emails (90 days)
	_, err = p.s3Client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucketName),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{
				{
					ID:     aws.String("delete-old-emails"),
					Status: s3types.ExpirationStatusEnabled,
					Filter: &s3types.LifecycleRuleFilter{
						Prefix: aws.String(""),
					},
					Expiration: &s3types.LifecycleExpiration{
						Days: aws.Int32(90),
					},
				},
			},
		},
	})
	if err != nil {
		g.Log().Warningf(ctx, "Failed to set lifecycle policy: %v", err)
	}

	return nil
}

// createSNSTopic creates an SNS topic for email notifications
func (p *ReceivingProvider) createSNSTopic(ctx context.Context, topicName string) (string, error) {
	result, err := p.snsClient.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(topicName),
		Tags: []snstypes.Tag{
			{Key: aws.String("Application"), Value: aws.String("mailat")},
			{Key: aws.String("Purpose"), Value: aws.String("incoming-emails")},
		},
	})
	if err != nil {
		return "", err
	}

	return *result.TopicArn, nil
}

// subscribeSNSWebhook subscribes an HTTPS endpoint to the SNS topic
func (p *ReceivingProvider) subscribeSNSWebhook(ctx context.Context, topicArn, webhookURL string) error {
	_, err := p.snsClient.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topicArn),
		Protocol: aws.String("https"),
		Endpoint: aws.String(webhookURL),
	})
	return err
}

// ensureReceiptRuleSet creates a receipt rule set if it doesn't exist
func (p *ReceivingProvider) ensureReceiptRuleSet(ctx context.Context, ruleSetName string) error {
	// Try to create the rule set
	_, err := p.sesClient.CreateReceiptRuleSet(ctx, &ses.CreateReceiptRuleSetInput{
		RuleSetName: aws.String(ruleSetName),
	})
	if err != nil {
		// Check if it already exists
		if strings.Contains(err.Error(), "AlreadyExists") {
			return nil
		}
		return err
	}

	return nil
}

// createReceiptRule creates a receipt rule for a domain
func (p *ReceivingProvider) createReceiptRule(ctx context.Context, ruleSetName, ruleName, domain, bucketName, topicArn string) error {
	// First, try to delete existing rule with same name
	_, _ = p.sesClient.DeleteReceiptRule(ctx, &ses.DeleteReceiptRuleInput{
		RuleSetName: aws.String(ruleSetName),
		RuleName:    aws.String(ruleName),
	})

	_, err := p.sesClient.CreateReceiptRule(ctx, &ses.CreateReceiptRuleInput{
		RuleSetName: aws.String(ruleSetName),
		Rule: &sestypes.ReceiptRule{
			Name:       aws.String(ruleName),
			Enabled:    true,
			TlsPolicy:  sestypes.TlsPolicyOptional,
			ScanEnabled: true,
			Recipients: []string{domain}, // Catch all for domain
			Actions: []sestypes.ReceiptAction{
				{
					S3Action: &sestypes.S3Action{
						BucketName:      aws.String(bucketName),
						ObjectKeyPrefix: aws.String(fmt.Sprintf("incoming/%s/", domain)),
					},
				},
				{
					SNSAction: &sestypes.SNSAction{
						TopicArn: aws.String(topicArn),
						Encoding: sestypes.SNSActionEncodingUtf8,
					},
				},
			},
		},
	})

	return err
}

// activateReceiptRuleSet sets a rule set as the active one
func (p *ReceivingProvider) activateReceiptRuleSet(ctx context.Context, ruleSetName string) error {
	_, err := p.sesClient.SetActiveReceiptRuleSet(ctx, &ses.SetActiveReceiptRuleSetInput{
		RuleSetName: aws.String(ruleSetName),
	})
	return err
}

// DeleteReceiving removes all AWS resources for email receiving
func (p *ReceivingProvider) DeleteReceiving(ctx context.Context, result *ReceivingSetupResult) error {
	// Delete receipt rule
	if result.RuleName != "" && result.RuleSetName != "" {
		_, err := p.sesClient.DeleteReceiptRule(ctx, &ses.DeleteReceiptRuleInput{
			RuleSetName: aws.String(result.RuleSetName),
			RuleName:    aws.String(result.RuleName),
		})
		if err != nil {
			g.Log().Warningf(ctx, "Failed to delete receipt rule: %v", err)
		}
	}

	// Delete SNS subscriptions
	if result.SNSTopicArn != "" {
		// List and delete all subscriptions
		listResult, err := p.snsClient.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{
			TopicArn: aws.String(result.SNSTopicArn),
		})
		if err == nil {
			for _, sub := range listResult.Subscriptions {
				if sub.SubscriptionArn != nil && *sub.SubscriptionArn != "PendingConfirmation" {
					_, _ = p.snsClient.Unsubscribe(ctx, &sns.UnsubscribeInput{
						SubscriptionArn: sub.SubscriptionArn,
					})
				}
			}
		}

		// Delete topic
		_, err = p.snsClient.DeleteTopic(ctx, &sns.DeleteTopicInput{
			TopicArn: aws.String(result.SNSTopicArn),
		})
		if err != nil {
			g.Log().Warningf(ctx, "Failed to delete SNS topic: %v", err)
		}
	}

	// Note: We don't delete the S3 bucket as it may contain emails
	// The lifecycle policy will clean up old emails

	return nil
}

// GetEmailFromS3 retrieves an email from S3
func (p *ReceivingProvider) GetEmailFromS3(ctx context.Context, bucket, key string) ([]byte, error) {
	result, err := p.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()

	buf := make([]byte, *result.ContentLength)
	_, err = result.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, err
	}

	return buf, nil
}

// GeneratePresignedURL generates a presigned URL for downloading an attachment
func (p *ReceivingProvider) GeneratePresignedURL(ctx context.Context, bucket, key string, expirySeconds int64) (string, error) {
	presignClient := s3.NewPresignClient(p.s3Client)

	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(60*15)) // 15 minutes

	if err != nil {
		return "", err
	}

	return result.URL, nil
}

// getAccountID attempts to get the AWS account ID
func (p *ReceivingProvider) getAccountID(ctx context.Context) string {
	// For simplicity, we'll return "*" which is less restrictive
	// In production, you'd use STS GetCallerIdentity
	return "*"
}

// generateShortID generates a short random ID
func generateShortID() string {
	bytes := make([]byte, 4)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// generateWebhookSecret generates a webhook secret
func generateWebhookSecret() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// AddDomainToReceiving adds a new domain to an existing receiving setup
func (p *ReceivingProvider) AddDomainToReceiving(ctx context.Context, ruleSetName, domain, bucketName, topicArn string) error {
	ruleName := fmt.Sprintf("receive-%s", strings.ReplaceAll(domain, ".", "-"))
	return p.createReceiptRule(ctx, ruleSetName, ruleName, domain, bucketName, topicArn)
}

// RemoveDomainFromReceiving removes a domain from receiving
func (p *ReceivingProvider) RemoveDomainFromReceiving(ctx context.Context, ruleSetName, domain string) error {
	ruleName := fmt.Sprintf("receive-%s", strings.ReplaceAll(domain, ".", "-"))
	_, err := p.sesClient.DeleteReceiptRule(ctx, &ses.DeleteReceiptRuleInput{
		RuleSetName: aws.String(ruleSetName),
		RuleName:    aws.String(ruleName),
	})
	return err
}
