package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
)

var ErrSendingFeedbackConflict = errors.New("SES delivery feedback already uses another service; existing notification topics were preserved")

type SendingSetupResources struct{ Bucket, Region, TopicARN string }

// Sending resources have no SES receipt rules, receiving bucket policy, or DNS changes.
// Stable names make a retry after an interrupted setup reuse this organization's resources.
func (p *ReceivingProvider) SetupSendingResources(ctx context.Context, orgUUID string) (*SendingSetupResources, error) {
	if orgUUID == "" {
		return nil, fmt.Errorf("organization UUID is required")
	}
	account, err := p.getAccountID(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(orgUUID))
	name := "mailat-send-" + account + "-" + hex.EncodeToString(sum[:8])
	input := &s3.CreateBucketInput{Bucket: aws.String(name)}
	if p.region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(p.region)}
	}
	if _, err = p.s3Client.CreateBucket(ctx, input); err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			return nil, fmt.Errorf("create private sending storage: %w", err)
		}
	}
	if _, err = p.s3Client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{Bucket: aws.String(name), PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{BlockPublicAcls: aws.Bool(true), IgnorePublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true)}}); err != nil {
		return nil, err
	}
	if _, err = p.s3Client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{Bucket: aws.String(name), ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{Rules: []s3types.ServerSideEncryptionRule{{ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{SSEAlgorithm: s3types.ServerSideEncryptionAes256}}}}}); err != nil {
		return nil, err
	}
	topic, err := p.snsClient.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(name), Tags: []snstypes.Tag{{Key: aws.String("Application"), Value: aws.String("mailat")}, {Key: aws.String("Purpose"), Value: aws.String("sending-feedback")}}})
	if err != nil {
		return nil, err
	}
	arn := aws.ToString(topic.TopicArn)
	if arn == "" {
		return nil, fmt.Errorf("SNS did not return a topic")
	}
	policy, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []map[string]any{{"Effect": "Allow", "Principal": map[string]string{"Service": "ses.amazonaws.com"}, "Action": "sns:Publish", "Resource": arn, "Condition": map[string]any{"StringEquals": map[string]string{"AWS:SourceAccount": account}, "ArnLike": map[string]string{"AWS:SourceArn": fmt.Sprintf("arn:aws:ses:%s:%s:identity/*", p.region, account)}}}}})
	if _, err = p.snsClient.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{TopicArn: aws.String(arn), AttributeName: aws.String("Policy"), AttributeValue: aws.String(string(policy))}); err != nil {
		return nil, err
	}
	return &SendingSetupResources{Bucket: name, Region: p.region, TopicARN: arn}, nil
}

// Inspect all three channels before the first SES write. Never replace a topic
// configured by another application, even when the other two channels are absent.
func (p *ReceivingProvider) EnsureSendingFeedback(ctx context.Context, domain, topic string, trusted []string) error {
	current, err := p.sesClient.GetIdentityNotificationAttributes(ctx, &ses.GetIdentityNotificationAttributesInput{Identities: []string{domain}})
	if err != nil {
		return err
	}
	attrs, ok := current.NotificationAttributes[domain]
	if !ok {
		return fmt.Errorf("SES identity notification settings are unavailable")
	}
	allowed := map[string]bool{topic: true}
	for _, value := range trusted {
		if value != "" {
			allowed[value] = true
		}
	}
	channels := []struct {
		kind    sestypes.NotificationType
		topic   string
		headers bool
	}{{sestypes.NotificationTypeBounce, aws.ToString(attrs.BounceTopic), attrs.HeadersInBounceNotificationsEnabled}, {sestypes.NotificationTypeComplaint, aws.ToString(attrs.ComplaintTopic), attrs.HeadersInComplaintNotificationsEnabled}, {sestypes.NotificationTypeDelivery, aws.ToString(attrs.DeliveryTopic), attrs.HeadersInDeliveryNotificationsEnabled}}
	for _, channel := range channels {
		if channel.topic != "" && !allowed[channel.topic] {
			return ErrSendingFeedbackConflict
		}
	}
	for _, channel := range channels {
		if channel.topic == "" {
			if _, err = p.sesClient.SetIdentityNotificationTopic(ctx, &ses.SetIdentityNotificationTopicInput{Identity: aws.String(domain), NotificationType: channel.kind, SnsTopic: aws.String(topic)}); err != nil {
				return err
			}
		}
		if !channel.headers {
			if _, err = p.sesClient.SetIdentityHeadersInNotificationsEnabled(ctx, &ses.SetIdentityHeadersInNotificationsEnabledInput{Identity: aws.String(domain), NotificationType: channel.kind, Enabled: true}); err != nil {
				return err
			}
		}
	}
	// Do not disable email feedback forwarding: it may be used by another operator.
	after, err := p.sesClient.GetIdentityNotificationAttributes(ctx, &ses.GetIdentityNotificationAttributesInput{Identities: []string{domain}})
	if err != nil {
		return err
	}
	a, ok := after.NotificationAttributes[domain]
	if !ok {
		return fmt.Errorf("SES feedback could not be rechecked")
	}
	for _, value := range []string{aws.ToString(a.BounceTopic), aws.ToString(a.ComplaintTopic), aws.ToString(a.DeliveryTopic)} {
		if value == "" || !allowed[value] {
			return fmt.Errorf("SES feedback changed during setup; recheck before retrying")
		}
	}
	if !a.HeadersInBounceNotificationsEnabled || !a.HeadersInComplaintNotificationsEnabled || !a.HeadersInDeliveryNotificationsEnabled {
		return fmt.Errorf("SES notification headers are not enabled")
	}
	return nil
}

func (p *ReceivingProvider) SendingSubscriptionActive(ctx context.Context, topic, endpoint string) (bool, error) {
	token := ""
	seen := map[string]bool{}
	for pages := 0; pages < 100; pages++ {
		input := &sns.ListSubscriptionsByTopicInput{TopicArn: aws.String(topic)}
		if token != "" {
			input.NextToken = aws.String(token)
		}
		output, err := p.snsClient.ListSubscriptionsByTopic(ctx, input)
		if err != nil {
			return false, err
		}
		for _, sub := range output.Subscriptions {
			arn := aws.ToString(sub.SubscriptionArn)
			if aws.ToString(sub.Endpoint) == endpoint && aws.ToString(sub.Protocol) == "https" && strings.HasPrefix(arn, "arn:") {
				return true, nil
			}
		}
		token = aws.ToString(output.NextToken)
		if token == "" {
			return false, nil
		}
		if seen[token] {
			return false, fmt.Errorf("SNS subscription pagination repeated")
		}
		seen[token] = true
	}
	return false, fmt.Errorf("SNS subscription pagination limit reached")
}
