package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func TestSendingSetupIsPrivateAndDoesNotEnableReceiving(t *testing.T) {
	actions := map[string]int{}
	var bucketURLs, topicNames []string
	var policy string
	topics := map[string]string{}
	headers := map[string]bool{}
	transport := receivingTransport(func(r *http.Request) (*http.Response, error) {
		body := []byte{}
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		q, _ := url.ParseQuery(string(body))
		action := q.Get("Action")
		actions[action]++
		output := ""
		switch action {
		case "GetCallerIdentity":
			output = `<GetCallerIdentityResponse><GetCallerIdentityResult><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`
		case "CreateTopic":
			topicNames = append(topicNames, q.Get("Name"))
			output = `<CreateTopicResponse><CreateTopicResult><TopicArn>arn:aws:sns:us-east-1:123456789012:mailat-send-test</TopicArn></CreateTopicResult></CreateTopicResponse>`
		case "SetTopicAttributes":
			policy = q.Get("AttributeValue")
		case "GetIdentityNotificationAttributes":
			output = fmt.Sprintf(`<GetIdentityNotificationAttributesResponse><GetIdentityNotificationAttributesResult><NotificationAttributes><entry><key>send.test</key><value><BounceTopic>%s</BounceTopic><ComplaintTopic>%s</ComplaintTopic><DeliveryTopic>%s</DeliveryTopic><HeadersInBounceNotificationsEnabled>%t</HeadersInBounceNotificationsEnabled><HeadersInComplaintNotificationsEnabled>%t</HeadersInComplaintNotificationsEnabled><HeadersInDeliveryNotificationsEnabled>%t</HeadersInDeliveryNotificationsEnabled></value></entry></NotificationAttributes></GetIdentityNotificationAttributesResult></GetIdentityNotificationAttributesResponse>`, topics["Bounce"], topics["Complaint"], topics["Delivery"], headers["Bounce"], headers["Complaint"], headers["Delivery"])
		case "SetIdentityNotificationTopic":
			topics[q.Get("NotificationType")] = q.Get("SnsTopic")
		case "SetIdentityHeadersInNotificationsEnabled":
			headers[q.Get("NotificationType")] = q.Get("Enabled") == "true"
		}
		if action == "" {
			bucketURLs = append(bucketURLs, r.URL.String())
			if r.URL.Query().Has("policy") {
				t.Error("sending setup must not grant SES bucket writes")
			}
		}
		if output == "" && action != "" {
			output = "<" + action + "Response><" + action + "Result/></" + action + "Response>"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(output))}, nil
	})
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: transport, RetryMaxAttempts: 1}
	p := &ReceivingProvider{s3Client: s3.NewFromConfig(cfg), sesClient: ses.NewFromConfig(cfg), snsClient: sns.NewFromConfig(cfg), stsClient: sts.NewFromConfig(cfg), region: "us-east-1"}
	one, err := p.SetupSendingResources(context.Background(), "organization-stable-uuid")
	if err != nil {
		t.Fatal(err)
	}
	two, err := p.SetupSendingResources(context.Background(), "organization-stable-uuid")
	if err != nil || one.Bucket != two.Bucket || topicNames[0] != topicNames[1] {
		t.Fatal("setup is not stable", one, two, err)
	}
	if !strings.Contains(strings.Join(bucketURLs, " "), "publicAccessBlock") || !strings.Contains(strings.Join(bucketURLs, " "), "encryption") || !strings.Contains(policy, "AWS:SourceAccount") || !strings.Contains(policy, "identity/*") {
		t.Fatal("private storage or account restricted identity policy absent")
	}
	if err = p.EnsureSendingFeedback(context.Background(), "send.test", one.TopicARN, nil); err != nil {
		t.Fatal(err)
	}
	if actions["SetIdentityNotificationTopic"] != 3 || actions["SetIdentityHeadersInNotificationsEnabled"] != 3 {
		t.Fatal("incomplete feedback", actions)
	}
	before := actions["SetIdentityNotificationTopic"]
	topics["Complaint"] = "arn:aws:sns:us-east-1:123456789012:other-service"
	if err = p.EnsureSendingFeedback(context.Background(), "send.test", one.TopicARN, nil); err != ErrSendingFeedbackConflict || actions["SetIdentityNotificationTopic"] != before {
		t.Fatal("foreign topic overwritten", err, actions)
	}
	if err = p.EnsureSendingFeedback(context.Background(), "send.test", one.TopicARN, []string{topics["Complaint"]}); err != nil || actions["SetIdentityNotificationTopic"] != before {
		t.Fatal("trusted existing receiving topic not preserved", err)
	}
	for _, action := range []string{"DescribeActiveReceiptRuleSet", "CreateReceiptRuleSet", "CreateReceiptRule", "DeleteReceiptRule", "SetActiveReceiptRuleSet", "SetIdentityFeedbackForwardingEnabled", "Subscribe"} {
		if actions[action] != 0 {
			t.Fatalf("unexpected receiving/forwarding/subscription action %s", action)
		}
	}
}
