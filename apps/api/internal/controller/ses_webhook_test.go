package controller

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
)

type snsRoundTrip func(*http.Request) (*http.Response, error)

func (f snsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSNSCertificateURL(t *testing.T) {
	for _, raw := range []string{"http://sns.us-east-1.amazonaws.com/SimpleNotificationService-x.pem", "https://sns.us-east-1.amazonaws.com.evil.test/SimpleNotificationService-x.pem", "https://sns.us-east-1.amazonaws.com:443/SimpleNotificationService-x.pem", "https://sns.us-east-1.amazonaws.com/anything", "https://user@sns.us-east-1.amazonaws.com/SimpleNotificationService-x.pem"} {
		u, _ := url.Parse(raw)
		if isAWSCertURL(u) {
			t.Errorf("accepted %s", raw)
		}
	}
	u, _ := url.Parse("https://sns.us-east-1.amazonaws.com/SimpleNotificationService-x.pem")
	if !isAWSCertURL(u) {
		t.Fatal("rejected AWS certificate path")
	}
}
func TestSNSSignatures(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "SNS"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	previous := snsHTTPClient
	t.Cleanup(func() { snsHTTPClient = previous })
	snsHTTPClient = &http.Client{Transport: snsRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(cert))), Header: http.Header{}}, nil
	})}
	for _, version := range []string{"1", "2"} {
		n := model.SNSNotification{Type: "Notification", Message: "trusted message", MessageId: "event-1", TopicArn: "arn:aws:sns:us-east-1:123456789012:inbox", Timestamp: time.Now().UTC().Format(time.RFC3339), SignatureVersion: version, SigningCertURL: "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-x.pem"}
		var digest []byte
		algorithm := crypto.SHA1
		if version == "1" {
			h := sha1.Sum([]byte(buildStringToSign(&n)))
			digest = h[:]
		} else {
			h := sha256.Sum256([]byte(buildStringToSign(&n)))
			digest = h[:]
			algorithm = crypto.SHA256
		}
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, algorithm, digest)
		if err != nil {
			t.Fatal(err)
		}
		n.Signature = base64.StdEncoding.EncodeToString(sig)
		if err = verifySNSSignature(&n); err != nil {
			t.Fatalf("valid v%s: %v", version, err)
		}
		n.Message = "tampered"
		if err = verifySNSSignature(&n); err == nil {
			t.Fatal("accepted tampered notification")
		}
	}
}
func TestSNSConfirmationRejectsArbitraryFetch(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/", "https://example.com/", "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=wrong&Token=x"} {
		if err := confirmSNSSubscription(raw, "arn:aws:sns:us-east-1:123456789012:inbox", "token", "us-east-1"); err == nil {
			t.Fatal("accepted unsafe confirmation URL")
		}
	}
	n := model.SNSNotification{Type: "SubscriptionConfirmation", Message: "m", MessageId: "id", SubscribeURL: "https://sns.us-east-1.amazonaws.com/", Timestamp: "time", Token: "token", TopicArn: "topic"}
	expected := "Message\nm\nMessageId\nid\nSubscribeURL\nhttps://sns.us-east-1.amazonaws.com/\nTimestamp\ntime\nToken\ntoken\nTopicArn\ntopic\nType\nSubscriptionConfirmation\n"
	if got := buildStringToSign(&n); got != expected {
		t.Fatalf("wrong canonical order: %q", got)
	}
}

func TestSESConfigurationSetEventType(t *testing.T) {
	for _, input := range []string{`{"eventType":"Delivery","mail":{"messageId":"ses-1"}}`, `{"notificationType":"Delivery","mail":{"messageId":"ses-1"}}`} {
		n, err := parseSESNotification(input)
		if err != nil || n.NotificationType != "Delivery" || n.Mail.MessageId != "ses-1" {
			t.Fatalf("normalization failed: %#v %v", n, err)
		}
	}
	for _, input := range []string{`{}`, `{"eventType":"Bounce","notificationType":"Delivery"}`} {
		if _, err := parseSESNotification(input); err == nil {
			t.Fatal("accepted missing/conflicting event type")
		}
	}
}

func TestSNSCertificateFetchFailureIsRetryable(t *testing.T) {
	previous := snsHTTPClient
	t.Cleanup(func() { snsHTTPClient = previous })
	snsHTTPClient = &http.Client{Transport: snsRoundTrip(func(r *http.Request) (*http.Response, error) { return nil, errors.New("temporary network failure") })}
	n := &model.SNSNotification{Type: "Notification", Timestamp: time.Now().UTC().Format(time.RFC3339), SignatureVersion: "2", Signature: "eA==", SigningCertURL: "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-x.pem"}
	if err := verifySNSSignature(n); !errors.Is(err, errSNSCertificateUnavailable) {
		t.Fatalf("network failure lost retry classification: %v", err)
	}
}
