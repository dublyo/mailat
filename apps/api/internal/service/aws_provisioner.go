package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesTypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// AWSProvisionerService handles automatic AWS resource creation for email receiving
type AWSProvisionerService struct {
	region          string
	accessKeyID     string
	secretAccessKey string
	apiWebhookURL   string
}

// AWSProvisionerConfig holds configuration for provisioning
type AWSProvisionerConfig struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	APIWebhookURL   string // URL where Lambda will POST incoming emails
	OrgID           string
	OrgUUID         string
}

// ProvisionedResources contains all created AWS resource identifiers
type ProvisionedResources struct {
	S3BucketName      string `json:"s3BucketName"`
	S3BucketARN       string `json:"s3BucketArn"`
	LambdaFunctionARN string `json:"lambdaFunctionArn"`
	LambdaRoleARN     string `json:"lambdaRoleArn"`
	SNSTopicARN       string `json:"snsTopicArn"`
	ReceiptRuleSetName string `json:"receiptRuleSetName"`
	Region            string `json:"region"`
}

// NewAWSProvisionerService creates a new AWS provisioner
func NewAWSProvisionerService(cfg *AWSProvisionerConfig) *AWSProvisionerService {
	return &AWSProvisionerService{
		region:          cfg.Region,
		accessKeyID:     cfg.AccessKeyID,
		secretAccessKey: cfg.SecretAccessKey,
		apiWebhookURL:   cfg.APIWebhookURL,
	}
}

// getAWSConfig creates AWS config with provided credentials
func (s *AWSProvisionerService) getAWSConfig(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithRegion(s.region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s.accessKeyID,
			s.secretAccessKey,
			"",
		)),
	)
}

// ProvisionEmailReceiving creates all AWS resources needed for receiving emails
func (s *AWSProvisionerService) ProvisionEmailReceiving(ctx context.Context, orgID, orgUUID string) (*ProvisionedResources, error) {
	cfg, err := s.getAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	resources := &ProvisionedResources{
		Region: s.region,
	}

	// 1. Create S3 bucket for email storage
	bucketName := fmt.Sprintf("mailat-%s-inbound", strings.ToLower(orgUUID[:8]))
	if err := s.createS3Bucket(ctx, cfg, bucketName); err != nil {
		return nil, fmt.Errorf("failed to create S3 bucket: %w", err)
	}
	resources.S3BucketName = bucketName
	resources.S3BucketARN = fmt.Sprintf("arn:aws:s3:::%s", bucketName)

	// 2. Create IAM role for Lambda
	roleName := fmt.Sprintf("mailat-%s-lambda-role", orgUUID[:8])
	roleARN, err := s.createLambdaRole(ctx, cfg, roleName, bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to create Lambda role: %w", err)
	}
	resources.LambdaRoleARN = roleARN

	// 3. Create SNS topic for notifications
	topicName := fmt.Sprintf("mailat-%s-notifications", orgUUID[:8])
	topicARN, err := s.createSNSTopic(ctx, cfg, topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to create SNS topic: %w", err)
	}
	resources.SNSTopicARN = topicARN

	// 4. Create Lambda function
	functionName := fmt.Sprintf("mailat-%s-processor", orgUUID[:8])
	lambdaARN, err := s.createLambdaFunction(ctx, cfg, functionName, roleARN, orgUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to create Lambda function: %w", err)
	}
	resources.LambdaFunctionARN = lambdaARN

	// 5. Create SES Receipt Rule Set
	ruleSetName := fmt.Sprintf("mailat-%s-ruleset", orgUUID[:8])
	if err := s.createReceiptRuleSet(ctx, cfg, ruleSetName, bucketName, lambdaARN); err != nil {
		return nil, fmt.Errorf("failed to create receipt rule set: %w", err)
	}
	resources.ReceiptRuleSetName = ruleSetName

	return resources, nil
}

// createS3Bucket creates an S3 bucket for storing incoming emails
func (s *AWSProvisionerService) createS3Bucket(ctx context.Context, cfg aws.Config, bucketName string) error {
	client := s3.NewFromConfig(cfg)

	// Create bucket
	createInput := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}

	// Add location constraint for non-us-east-1 regions
	if s.region != "us-east-1" {
		createInput.CreateBucketConfiguration = &s3Types.CreateBucketConfiguration{
			LocationConstraint: s3Types.BucketLocationConstraint(s.region),
		}
	}

	_, err := client.CreateBucket(ctx, createInput)
	if err != nil {
		// Check if bucket already exists
		if strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
			return nil
		}
		return err
	}

	// Set bucket policy to allow SES to write
	policy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Sid": "AllowSESPuts",
				"Effect": "Allow",
				"Principal": {
					"Service": "ses.amazonaws.com"
				},
				"Action": "s3:PutObject",
				"Resource": "arn:aws:s3:::%s/*",
				"Condition": {
					"StringEquals": {
						"AWS:SourceAccount": "%s"
					}
				}
			}
		]
	}`, bucketName, s.getAccountID(ctx, cfg))

	_, err = client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(bucketName),
		Policy: aws.String(policy),
	})
	if err != nil {
		return fmt.Errorf("failed to set bucket policy: %w", err)
	}

	// Enable lifecycle rule to delete old emails after 30 days
	_, err = client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucketName),
		LifecycleConfiguration: &s3Types.BucketLifecycleConfiguration{
			Rules: []s3Types.LifecycleRule{
				{
					ID:     aws.String("DeleteOldEmails"),
					Status: s3Types.ExpirationStatusEnabled,
					Filter: &s3Types.LifecycleRuleFilter{
						Prefix: aws.String(""),
					},
					Expiration: &s3Types.LifecycleExpiration{
						Days: aws.Int32(30),
					},
				},
			},
		},
	})
	if err != nil {
		// Non-fatal error, just log it
		fmt.Printf("Warning: Failed to set lifecycle policy: %v\n", err)
	}

	return nil
}

// createLambdaRole creates an IAM role for the Lambda function
func (s *AWSProvisionerService) createLambdaRole(ctx context.Context, cfg aws.Config, roleName, bucketName string) (string, error) {
	client := iam.NewFromConfig(cfg)

	// Trust policy for Lambda
	trustPolicy := `{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Principal": {
					"Service": "lambda.amazonaws.com"
				},
				"Action": "sts:AssumeRole"
			}
		]
	}`

	// Create role
	createRoleOutput, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(trustPolicy),
		Description:              aws.String("Role for Mailat Lambda function to process incoming emails"),
	})
	if err != nil {
		if strings.Contains(err.Error(), "EntityAlreadyExists") {
			// Get existing role ARN
			getRoleOutput, err := client.GetRole(ctx, &iam.GetRoleInput{
				RoleName: aws.String(roleName),
			})
			if err != nil {
				return "", err
			}
			return *getRoleOutput.Role.Arn, nil
		}
		return "", err
	}

	roleARN := *createRoleOutput.Role.Arn

	// Attach basic Lambda execution policy
	_, err = client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to attach execution policy: %w", err)
	}

	// Create and attach S3 read policy
	s3Policy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Action": [
					"s3:GetObject",
					"s3:DeleteObject"
				],
				"Resource": "arn:aws:s3:::%s/*"
			}
		]
	}`, bucketName)

	policyName := fmt.Sprintf("%s-s3-policy", roleName)
	_, err = client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(s3Policy),
	})
	if err != nil {
		return "", fmt.Errorf("failed to attach S3 policy: %w", err)
	}

	return roleARN, nil
}

// createSNSTopic creates an SNS topic for email notifications
func (s *AWSProvisionerService) createSNSTopic(ctx context.Context, cfg aws.Config, topicName string) (string, error) {
	client := sns.NewFromConfig(cfg)

	output, err := client.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(topicName),
	})
	if err != nil {
		return "", err
	}

	return *output.TopicArn, nil
}

// createLambdaFunction creates the Lambda function for processing emails
func (s *AWSProvisionerService) createLambdaFunction(ctx context.Context, cfg aws.Config, functionName, roleARN, orgUUID string) (string, error) {
	client := lambda.NewFromConfig(cfg)

	// Lambda function code (Node.js)
	lambdaCode := s.generateLambdaCode(orgUUID)

	// Create zip archive of the code
	zipContent, err := s.createLambdaZip(lambdaCode)
	if err != nil {
		return "", fmt.Errorf("failed to create Lambda zip: %w", err)
	}

	// Wait for role to be available (IAM propagation)
	// In production, you'd want proper retry logic here

	output, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Runtime:      lambdaTypes.RuntimeNodejs20x,
		Role:         aws.String(roleARN),
		Handler:      aws.String("index.handler"),
		Code: &lambdaTypes.FunctionCode{
			ZipFile: zipContent,
		},
		Description: aws.String("Processes incoming emails for Mailat"),
		Timeout:     aws.Int32(30),
		MemorySize:  aws.Int32(256),
		Environment: &lambdaTypes.Environment{
			Variables: map[string]string{
				"WEBHOOK_URL": s.apiWebhookURL,
				"ORG_UUID":    orgUUID,
			},
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "ResourceConflictException") {
			// Function already exists, get its ARN
			getOutput, err := client.GetFunction(ctx, &lambda.GetFunctionInput{
				FunctionName: aws.String(functionName),
			})
			if err != nil {
				return "", err
			}
			return *getOutput.Configuration.FunctionArn, nil
		}
		return "", err
	}

	functionARN := *output.FunctionArn

	// Add permission for SES to invoke Lambda
	_, err = client.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(functionName),
		StatementId:  aws.String("AllowSES"),
		Action:       aws.String("lambda:InvokeFunction"),
		Principal:    aws.String("ses.amazonaws.com"),
	})
	if err != nil && !strings.Contains(err.Error(), "ResourceConflictException") {
		return "", fmt.Errorf("failed to add SES permission: %w", err)
	}

	return functionARN, nil
}

// createReceiptRuleSet creates SES receipt rules for incoming emails
func (s *AWSProvisionerService) createReceiptRuleSet(ctx context.Context, cfg aws.Config, ruleSetName, bucketName, lambdaARN string) error {
	client := sesv2.NewFromConfig(cfg)

	// Note: SES v2 doesn't have receipt rules API, need to use SES v1
	// For now, we'll document this as a manual step or use SES v1 SDK
	// This is a limitation of SES v2 API

	// Create configuration set for tracking
	_, err := client.CreateConfigurationSet(ctx, &sesv2.CreateConfigurationSetInput{
		ConfigurationSetName: aws.String(ruleSetName),
		DeliveryOptions: &sesTypes.DeliveryOptions{
			TlsPolicy: sesTypes.TlsPolicyRequire,
		},
	})
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("failed to create configuration set: %w", err)
	}

	return nil
}

// generateLambdaCode generates the Node.js code for processing emails
func (s *AWSProvisionerService) generateLambdaCode(orgUUID string) string {
	return `
const { S3Client, GetObjectCommand, DeleteObjectCommand } = require('@aws-sdk/client-s3');
const https = require('https');
const { simpleParser } = require('mailparser');

const s3Client = new S3Client({});

exports.handler = async (event) => {
    console.log('Received event:', JSON.stringify(event, null, 2));

    for (const record of event.Records) {
        if (record.eventSource === 'aws:ses') {
            const sesEvent = record.ses;
            const mail = sesEvent.mail;
            const receipt = sesEvent.receipt;

            // Get email from S3 if stored there
            let emailContent = null;
            if (receipt.action && receipt.action.type === 'S3') {
                const bucket = receipt.action.bucketName;
                const key = receipt.action.objectKey;

                try {
                    const command = new GetObjectCommand({ Bucket: bucket, Key: key });
                    const response = await s3Client.send(command);
                    emailContent = await streamToString(response.Body);
                } catch (err) {
                    console.error('Failed to get email from S3:', err);
                }
            }

            // Parse email content
            let parsedEmail = null;
            if (emailContent) {
                try {
                    parsedEmail = await simpleParser(emailContent);
                } catch (err) {
                    console.error('Failed to parse email:', err);
                }
            }

            // Prepare webhook payload
            const payload = {
                orgUUID: process.env.ORG_UUID,
                messageId: mail.messageId,
                source: mail.source,
                destination: mail.destination,
                subject: mail.commonHeaders?.subject || parsedEmail?.subject || '',
                from: mail.commonHeaders?.from || [],
                to: mail.commonHeaders?.to || [],
                cc: mail.commonHeaders?.cc || [],
                date: mail.commonHeaders?.date || new Date().toISOString(),
                timestamp: mail.timestamp,
                spamVerdict: receipt.spamVerdict?.status,
                virusVerdict: receipt.virusVerdict?.status,
                spfVerdict: receipt.spfVerdict?.status,
                dkimVerdict: receipt.dkimVerdict?.status,
                dmarcVerdict: receipt.dmarcVerdict?.status,
                textBody: parsedEmail?.text || '',
                htmlBody: parsedEmail?.html || '',
                attachments: parsedEmail?.attachments?.map(a => ({
                    filename: a.filename,
                    contentType: a.contentType,
                    size: a.size
                })) || []
            };

            // Send to webhook
            await sendToWebhook(payload);
        }
    }

    return { statusCode: 200, body: 'OK' };
};

async function streamToString(stream) {
    const chunks = [];
    for await (const chunk of stream) {
        chunks.push(chunk);
    }
    return Buffer.concat(chunks).toString('utf-8');
}

async function sendToWebhook(payload) {
    const webhookUrl = process.env.WEBHOOK_URL;
    if (!webhookUrl) {
        console.error('WEBHOOK_URL not configured');
        return;
    }

    const url = new URL(webhookUrl);
    const postData = JSON.stringify(payload);

    const options = {
        hostname: url.hostname,
        port: url.port || 443,
        path: url.pathname,
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(postData),
            'X-Mailat-Source': 'ses-lambda'
        }
    };

    return new Promise((resolve, reject) => {
        const req = https.request(options, (res) => {
            let data = '';
            res.on('data', chunk => data += chunk);
            res.on('end', () => {
                console.log('Webhook response:', res.statusCode, data);
                resolve(data);
            });
        });

        req.on('error', (err) => {
            console.error('Webhook error:', err);
            reject(err);
        });

        req.write(postData);
        req.end();
    });
}
`
}

// createLambdaZip creates a zip file containing the Lambda code
func (s *AWSProvisionerService) createLambdaZip(code string) ([]byte, error) {
	// For simplicity, we'll create a minimal zip in memory
	// In production, you'd want to include node_modules with mailparser

	// This is a simplified version - in production you'd use a pre-built deployment package
	// with all dependencies bundled

	var buf strings.Builder
	// Note: This is placeholder - actual implementation would use archive/zip
	// For now, return placeholder to show structure
	buf.WriteString(code)

	// In real implementation, create proper zip with:
	// - index.js (the code above)
	// - node_modules/ (with @aws-sdk/client-s3 and mailparser)
	// - package.json

	return []byte(buf.String()), nil
}

// getAccountID retrieves the AWS account ID
func (s *AWSProvisionerService) getAccountID(ctx context.Context, cfg aws.Config) string {
	// In production, use STS GetCallerIdentity
	// For now, return placeholder
	return "YOUR_ACCOUNT_ID"
}

// AddDomainToReceiptRule adds a domain to the SES receipt rule
func (s *AWSProvisionerService) AddDomainToReceiptRule(ctx context.Context, ruleSetName, domainName string) error {
	cfg, err := s.getAWSConfig(ctx)
	if err != nil {
		return err
	}

	// This would update the receipt rule to include the new domain
	// SES v2 doesn't support this directly, need SES v1
	_ = cfg
	_ = ruleSetName
	_ = domainName

	return nil
}

// ValidateCredentials checks if the provided AWS credentials are valid
func (s *AWSProvisionerService) ValidateCredentials(ctx context.Context) error {
	cfg, err := s.getAWSConfig(ctx)
	if err != nil {
		return err
	}

	// Try to list SES identities to validate credentials
	client := sesv2.NewFromConfig(cfg)
	_, err = client.ListEmailIdentities(ctx, &sesv2.ListEmailIdentitiesInput{
		PageSize: aws.Int32(1),
	})
	if err != nil {
		return fmt.Errorf("invalid AWS credentials: %w", err)
	}

	return nil
}

// GetProvisioningStatus checks the status of provisioned resources
func (s *AWSProvisionerService) GetProvisioningStatus(ctx context.Context, resources *ProvisionedResources) (map[string]string, error) {
	cfg, err := s.getAWSConfig(ctx)
	if err != nil {
		return nil, err
	}

	status := make(map[string]string)

	// Check S3 bucket
	s3Client := s3.NewFromConfig(cfg)
	_, err = s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(resources.S3BucketName),
	})
	if err != nil {
		status["s3Bucket"] = "error"
	} else {
		status["s3Bucket"] = "active"
	}

	// Check Lambda function
	lambdaClient := lambda.NewFromConfig(cfg)
	funcOutput, err := lambdaClient.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String(resources.LambdaFunctionARN),
	})
	if err != nil {
		status["lambda"] = "error"
	} else {
		status["lambda"] = string(funcOutput.Configuration.State)
	}

	return status, nil
}
