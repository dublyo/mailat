package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/service"
)

// AWSSetupHandler handles AWS provisioning requests
type AWSSetupHandler struct {
	db  *sql.DB
	cfg *config.Config
}

// NewAWSSetupHandler creates a new AWS setup handler
func NewAWSSetupHandler(db *sql.DB, cfg *config.Config) *AWSSetupHandler {
	return &AWSSetupHandler{db: db, cfg: cfg}
}

// ValidateCredentialsRequest represents the request to validate AWS credentials
type ValidateCredentialsRequest struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// ProvisionRequest represents the request to provision AWS resources
type ProvisionRequest struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// ValidateCredentials checks if AWS credentials are valid
func (h *AWSSetupHandler) ValidateCredentials(r *ghttp.Request) {
	ctx := r.GetCtx()

	// Get user from context (set by auth middleware)
	userID := r.Get("userID")
	if userID == nil {
		r.Response.WriteStatus(http.StatusUnauthorized)
		r.Response.WriteJson(map[string]string{"error": "unauthorized"})
		return
	}

	var req ValidateCredentialsRequest
	if err := json.NewDecoder(r.Request.Body).Decode(&req); err != nil {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]string{"error": "invalid request"})
		return
	}

	// Validate required fields
	if req.Region == "" || req.AccessKeyID == "" || req.SecretAccessKey == "" {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]string{"error": "missing required fields"})
		return
	}

	// Create provisioner and validate
	provisioner := service.NewAWSProvisionerService(&service.AWSProvisionerConfig{
		Region:          req.Region,
		AccessKeyID:     req.AccessKeyID,
		SecretAccessKey: req.SecretAccessKey,
	})

	if err := provisioner.ValidateCredentials(ctx); err != nil {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]interface{}{
			"valid":   false,
			"error":   err.Error(),
			"message": "AWS credentials are invalid or don't have sufficient permissions",
		})
		return
	}

	r.Response.WriteJson(map[string]interface{}{
		"valid":   true,
		"message": "AWS credentials are valid",
	})
}

// ProvisionResources provisions all AWS resources for email receiving
func (h *AWSSetupHandler) ProvisionResources(r *ghttp.Request) {
	ctx := r.GetCtx()

	// Get user from context
	userID := r.Get("userID")
	if userID == nil {
		r.Response.WriteStatus(http.StatusUnauthorized)
		r.Response.WriteJson(map[string]string{"error": "unauthorized"})
		return
	}

	// Get org ID from user
	var orgID int64
	var orgUUID string
	err := h.db.QueryRowContext(ctx, `
		SELECT o.id, o.uuid FROM organizations o
		JOIN users u ON u.org_id = o.id
		WHERE u.id = $1
	`, userID).Scan(&orgID, &orgUUID)
	if err != nil {
		r.Response.WriteStatus(http.StatusInternalServerError)
		r.Response.WriteJson(map[string]string{"error": "failed to get organization"})
		return
	}

	var req ProvisionRequest
	if err := json.NewDecoder(r.Request.Body).Decode(&req); err != nil {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]string{"error": "invalid request"})
		return
	}

	// Validate required fields
	if req.Region == "" || req.AccessKeyID == "" || req.SecretAccessKey == "" {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]string{"error": "missing required fields"})
		return
	}

	// Build webhook URL
	webhookURL := h.cfg.APIUrl + "/api/v1/webhooks/inbound-email"

	// Create provisioner
	provisioner := service.NewAWSProvisionerService(&service.AWSProvisionerConfig{
		Region:          req.Region,
		AccessKeyID:     req.AccessKeyID,
		SecretAccessKey: req.SecretAccessKey,
		APIWebhookURL:   webhookURL,
		OrgUUID:         orgUUID,
	})

	// Provision resources
	resources, err := provisioner.ProvisionEmailReceiving(ctx, fmt.Sprintf("%d", orgID), orgUUID)
	if err != nil {
		r.Response.WriteStatus(http.StatusInternalServerError)
		r.Response.WriteJson(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"message": "Failed to provision AWS resources",
		})
		return
	}

	// Store AWS credentials and resource info in database
	_, err = h.db.ExecContext(ctx, `
		UPDATE organizations SET
			aws_region = $2,
			aws_access_key_id = $3,
			aws_secret_access_key = $4,
			aws_s3_bucket = $5,
			aws_lambda_arn = $6,
			aws_sns_topic_arn = $7,
			aws_receipt_rule_set = $8,
			aws_provisioned = true,
			updated_at = NOW()
		WHERE id = $1
	`, orgID, req.Region, req.AccessKeyID, req.SecretAccessKey,
		resources.S3BucketName, resources.LambdaFunctionARN,
		resources.SNSTopicARN, resources.ReceiptRuleSetName)
	if err != nil {
		// Non-fatal, just log
		r.Response.WriteJson(map[string]interface{}{
			"success":   true,
			"resources": resources,
			"warning":   "Resources created but failed to store in database: " + err.Error(),
		})
		return
	}

	r.Response.WriteJson(map[string]interface{}{
		"success":   true,
		"resources": resources,
		"message":   "AWS resources provisioned successfully",
		"nextSteps": []string{
			"1. Add MX records to your domains pointing to SES",
			"2. Verify your domains in AWS SES console",
			"3. Enable the receipt rule set in SES console",
		},
	})
}

// GetProvisioningStatus returns the current provisioning status
func (h *AWSSetupHandler) GetProvisioningStatus(r *ghttp.Request) {
	ctx := r.GetCtx()

	// Get user from context
	userID := r.Get("userID")
	if userID == nil {
		r.Response.WriteStatus(http.StatusUnauthorized)
		r.Response.WriteJson(map[string]string{"error": "unauthorized"})
		return
	}

	// Get org provisioning info
	var orgID int64
	var awsProvisioned bool
	var awsRegion, awsS3Bucket, awsLambdaARN, awsSNSTopicARN, awsReceiptRuleSet sql.NullString

	err := h.db.QueryRowContext(ctx, `
		SELECT o.id, COALESCE(o.aws_provisioned, false),
			   o.aws_region, o.aws_s3_bucket, o.aws_lambda_arn,
			   o.aws_sns_topic_arn, o.aws_receipt_rule_set
		FROM organizations o
		JOIN users u ON u.org_id = o.id
		WHERE u.id = $1
	`, userID).Scan(&orgID, &awsProvisioned,
		&awsRegion, &awsS3Bucket, &awsLambdaARN,
		&awsSNSTopicARN, &awsReceiptRuleSet)
	if err != nil {
		r.Response.WriteStatus(http.StatusInternalServerError)
		r.Response.WriteJson(map[string]string{"error": "failed to get organization"})
		return
	}

	if !awsProvisioned {
		r.Response.WriteJson(map[string]interface{}{
			"provisioned": false,
			"message":     "AWS resources not yet provisioned",
		})
		return
	}

	r.Response.WriteJson(map[string]interface{}{
		"provisioned": true,
		"resources": map[string]interface{}{
			"region":         awsRegion.String,
			"s3Bucket":       awsS3Bucket.String,
			"lambdaArn":      awsLambdaARN.String,
			"snsTopicArn":    awsSNSTopicARN.String,
			"receiptRuleSet": awsReceiptRuleSet.String,
		},
	})
}

// GetDNSRecordsForSES returns the DNS records needed for SES receiving
func (h *AWSSetupHandler) GetDNSRecordsForSES(r *ghttp.Request) {
	ctx := r.GetCtx()

	// Get user from context
	userID := r.Get("userID")
	if userID == nil {
		r.Response.WriteStatus(http.StatusUnauthorized)
		r.Response.WriteJson(map[string]string{"error": "unauthorized"})
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		r.Response.WriteStatus(http.StatusBadRequest)
		r.Response.WriteJson(map[string]string{"error": "domain uuid required"})
		return
	}

	// Get domain and org info
	var domainName, awsRegion string
	err := h.db.QueryRowContext(ctx, `
		SELECT d.name, COALESCE(o.aws_region, 'us-east-1')
		FROM domains d
		JOIN organizations o ON d.org_id = o.id
		JOIN users u ON u.org_id = o.id
		WHERE d.uuid = $1 AND u.id = $2
	`, domainUUID, userID).Scan(&domainName, &awsRegion)
	if err != nil {
		r.Response.WriteStatus(http.StatusNotFound)
		r.Response.WriteJson(map[string]string{"error": "domain not found"})
		return
	}

	// Generate MX record for SES receiving
	mxValue := "inbound-smtp." + awsRegion + ".amazonaws.com"

	r.Response.WriteJson(map[string]interface{}{
		"domain": domainName,
		"records": []map[string]interface{}{
			{
				"type":     "MX",
				"name":     domainName,
				"value":    "10 " + mxValue,
				"purpose":  "Receive emails via Amazon SES",
				"required": true,
			},
		},
		"instructions": []string{
			"Add the MX record to your DNS provider",
			"After adding, verify the domain in AWS SES console",
			"Enable the receipt rule in SES to start receiving emails",
		},
	})
}
