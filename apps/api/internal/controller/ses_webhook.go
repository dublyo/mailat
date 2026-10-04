package controller

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
)

type SESWebhookController struct{ receivingService *service.ReceivingService }

func NewSESWebhookController(s *service.ReceivingService) *SESWebhookController {
	return &SESWebhookController{receivingService: s}
}

var errSNSCertificateUnavailable = errors.New("SNS certificate temporarily unavailable")

var snsHTTPClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("SNS redirects are not allowed") }}

func (c *SESWebhookController) HandleIncoming(r *ghttp.Request) {
	if c.receivingService == nil {
		r.Response.WriteStatus(http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Request.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		response.BadRequest(r, "Invalid webhook body")
		return
	}
	var notification model.SNSNotification
	if err = json.Unmarshal(body, &notification); err != nil {
		response.BadRequest(r, "Invalid SNS notification")
		return
	}
	auth, err := c.receivingService.AuthorizeNotification(r.Context(), notification.TopicArn, r.Get("secret").String())
	if err != nil {
		if errors.Is(err, service.ErrWebhookAuthorization) {
			response.Unauthorized(r, "Invalid webhook authorization")
		} else {
			r.Response.WriteStatus(http.StatusServiceUnavailable)
		}
		return
	}
	if err = verifySNSSignature(&notification); err != nil {
		if errors.Is(err, errSNSCertificateUnavailable) {
			r.Response.WriteStatus(http.StatusServiceUnavailable)
		} else {
			response.Unauthorized(r, "Invalid SNS signature")
		}
		return
	}
	topicParts := strings.Split(notification.TopicArn, ":")
	if len(topicParts) != 6 || topicParts[2] != "sns" || topicParts[3] != auth.Region {
		response.Unauthorized(r, "Invalid SNS topic")
		return
	}
	certURL, _ := url.Parse(notification.SigningCertURL)
	if certURL.Hostname() != "sns."+auth.Region+".amazonaws.com" {
		response.Unauthorized(r, "Invalid SNS signing region")
		return
	}
	switch notification.Type {
	case "SubscriptionConfirmation":
		if err = confirmSNSSubscription(notification.SubscribeURL, notification.TopicArn, notification.Token, auth.Region); err != nil {
			r.Response.WriteStatus(http.StatusBadGateway)
			return
		}
		if err = c.receivingService.ConfirmNotificationSubscription(r.Context(), auth); err != nil {
			r.Response.WriteStatus(http.StatusServiceUnavailable)
			return
		}
		response.Success(r, map[string]string{"status": "subscription_confirmed"})
	case "Notification":
		payload, parseErr := parseSESNotification(notification.Message)
		if parseErr != nil {
			response.BadRequest(r, "Invalid SES notification")
			return
		}
		if payload.NotificationType == "Received" {
			err = c.receivingService.ProcessIncomingEmail(r.Context(), auth, payload)
		} else {
			err = c.receivingService.ProcessDeliveryEvent(r.Context(), auth, notification.MessageId, payload)
		}
		// Returning 5xx preserves SNS's durable retry, unlike acknowledging partial work.
		if err != nil {
			r.Response.WriteStatus(http.StatusServiceUnavailable)
			return
		}
		response.Success(r, map[string]string{"status": "processed"})
	case "UnsubscribeConfirmation":
		response.Success(r, map[string]string{"status": "unsubscribed"})
	default:
		response.BadRequest(r, "Unsupported SNS notification type")
	}
}

// SES configuration-set publishing uses eventType; identity feedback uses
// notificationType. Both arrive signed by SNS and have the same event payload.
func parseSESNotification(message string) (*model.SESNotification, error) {
	var payload struct {
		model.SESNotification
		EventType string `json:"eventType"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return nil, err
	}
	if payload.NotificationType == "" {
		payload.NotificationType = payload.EventType
	} else if payload.EventType != "" && payload.EventType != payload.NotificationType {
		return nil, fmt.Errorf("conflicting SES event types")
	}
	if payload.NotificationType == "" {
		return nil, fmt.Errorf("missing SES event type")
	}
	return &payload.SESNotification, nil
}

func confirmSNSSubscription(rawURL, topic, token, region string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != "sns."+region+".amazonaws.com" || u.Path != "/" || u.Fragment != "" {
		return fmt.Errorf("invalid confirmation URL")
	}
	q := u.Query()
	if q.Get("Action") != "ConfirmSubscription" || q.Get("TopicArn") != topic || q.Get("Token") != token || token == "" {
		return fmt.Errorf("confirmation does not match signed notification")
	}
	resp, err := snsHTTPClient.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("confirmation failed")
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 256*1024))
	return err
}
func verifySNSSignature(n *model.SNSNotification) error {
	u, err := url.Parse(n.SigningCertURL)
	if err != nil || !isAWSCertURL(u) {
		return fmt.Errorf("invalid SNS certificate URL")
	}
	if n.Type != "Notification" && n.Type != "SubscriptionConfirmation" && n.Type != "UnsubscribeConfirmation" {
		return fmt.Errorf("invalid notification type")
	}
	stamp, err := time.Parse(time.RFC3339, n.Timestamp)
	if err != nil || stamp.After(time.Now().Add(5*time.Minute)) {
		return fmt.Errorf("invalid SNS timestamp")
	}
	algorithm := x509.SHA1WithRSA
	switch n.SignatureVersion {
	case "1":
	case "2":
		algorithm = x509.SHA256WithRSA
	default:
		return fmt.Errorf("unsupported signature version")
	}
	signature, err := base64.StdEncoding.DecodeString(n.Signature)
	if err != nil || len(signature) == 0 {
		return fmt.Errorf("invalid signature")
	}
	resp, err := snsHTTPClient.Get(u.String())
	if err != nil {
		return fmt.Errorf("%w: %v", errSNSCertificateUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return errSNSCertificateUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("certificate download rejected")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if err != nil {
		return fmt.Errorf("%w: %v", errSNSCertificateUnavailable, err)
	}
	if len(data) > 256*1024 {
		return fmt.Errorf("invalid certificate size")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return fmt.Errorf("invalid certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return fmt.Errorf("expired signing certificate")
	}
	return cert.CheckSignature(algorithm, []byte(buildStringToSign(n)), signature)
}
func isAWSCertURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && regexp.MustCompile(`^sns\.[a-z0-9-]+\.amazonaws\.com$`).MatchString(u.Host) && regexp.MustCompile(`^/SimpleNotificationService-[A-Za-z0-9_-]+\.pem$`).MatchString(u.Path)
}
func buildStringToSign(n *model.SNSNotification) string {
	var b strings.Builder
	add := func(name, value string) {
		b.WriteString(name)
		b.WriteByte('\n')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	add("Message", n.Message)
	add("MessageId", n.MessageId)
	if n.Type == "Notification" {
		if n.Subject != "" {
			add("Subject", n.Subject)
		}
	} else {
		add("SubscribeURL", n.SubscribeURL)
	}
	add("Timestamp", n.Timestamp)
	if n.Type != "Notification" {
		add("Token", n.Token)
	}
	add("TopicArn", n.TopicArn)
	add("Type", n.Type)
	return b.String()
}
