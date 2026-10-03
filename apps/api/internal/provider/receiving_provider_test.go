package provider

import (
	"context"
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

type receivingTransport func(*http.Request) (*http.Response, error)

func (f receivingTransport) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestReceivingSetupPreservesActiveRuleSet(t *testing.T) {
	actions := map[string]int{}
	var ruleBody, bucketPolicy, topicPolicy string
	transport := receivingTransport(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		q, _ := url.ParseQuery(string(body))
		action := q.Get("Action")
		actions[action]++
		output := ""
		switch action {
		case "DescribeActiveReceiptRuleSet":
			output = `<DescribeActiveReceiptRuleSetResponse><DescribeActiveReceiptRuleSetResult><Metadata><Name>existing-shared-rules</Name></Metadata></DescribeActiveReceiptRuleSetResult></DescribeActiveReceiptRuleSetResponse>`
		case "GetCallerIdentity":
			output = `<GetCallerIdentityResponse><GetCallerIdentityResult><Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/test</Arn><UserId>test</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`
		case "CreateTopic":
			output = `<CreateTopicResponse><CreateTopicResult><TopicArn>arn:aws:sns:us-east-1:123456789012:mailat-test</TopicArn></CreateTopicResult></CreateTopicResponse>`
		case "CreateReceiptRule":
			ruleBody = string(body)
			output = `<CreateReceiptRuleResponse><CreateReceiptRuleResult/></CreateReceiptRuleResponse>`
		case "SetTopicAttributes":
			topicPolicy = q.Get("AttributeValue")
			output = `<SetTopicAttributesResponse><SetTopicAttributesResult/></SetTopicAttributesResponse>`
		default:
			if action != "" {
				output = "<" + action + "Response><" + action + "Result/></" + action + "Response>"
			}
		}
		if r.URL.Query().Has("policy") {
			bucketPolicy = string(body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(output))}, nil
	})
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: transport}
	provider := &ReceivingProvider{s3Client: s3.NewFromConfig(cfg), sesClient: ses.NewFromConfig(cfg), snsClient: sns.NewFromConfig(cfg), stsClient: sts.NewFromConfig(cfg), region: "us-east-1", webhookURL: "https://api.example.test"}
	result, err := provider.SetupReceiving(context.Background(), 1, "one.test")
	if err != nil {
		t.Fatal(err)
	}
	if result.RuleSetName != "existing-shared-rules" || actions["SetActiveReceiptRuleSet"] != 0 || actions["Subscribe"] != 0 {
		t.Fatalf("displaced active rules or subscribed before persistence: %#v %#v", result, actions)
	}
	rule, _ := url.ParseQuery(ruleBody)
	if rule.Get("RuleSetName") != "existing-shared-rules" || rule.Get("Rule.Actions.member.1.S3Action.TopicArn") == "" {
		t.Fatalf("missing trusted S3 topic action: %s", ruleBody)
	}
	if !strings.Contains(bucketPolicy, "AWS:SourceAccount") || !strings.Contains(topicPolicy, "123456789012") {
		t.Fatal("missing account policy restrictions")
	}
}
