package provider

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

func TestMailMIMEPreservesAttachmentAndHidesBcc(t *testing.T) {
	data := []byte{0, 1, 2, 255, 128, 42}
	raw, envelope, err := BuildMailMIME(&EmailMessage{From: "Owner <owner@example.test>", To: []string{"receiver@example.test"}, Cc: []string{"copy@example.test"}, Bcc: []string{"hidden@example.test"}, ReplyTo: "reply@example.test", Subject: "مرحبا", TextBody: "hello", HTMLBody: "<p>hello</p>", Headers: map[string]string{"In-Reply-To": "<thread@example.test>"}, Attachments: []Attachment{{Filename: "résumé.pdf", ContentType: "application/pdf", Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 3 {
		t.Fatal(envelope)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Get("Bcc") != "" || bytes.Contains(raw, []byte("hidden@example.test")) {
		t.Fatal("Bcc leaked into raw MIME")
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != "مرحبا" {
		t.Fatalf("subject=%q err=%v", subject, err)
	}
	_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	body, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.Header.Get("Content-Type"), "multipart/alternative") {
		t.Fatal("missing text/HTML alternatives")
	}
	attachment, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("attachment bytes differ: %v", err)
	}
	_, disposition, err := mime.ParseMediaType(attachment.Header.Get("Content-Disposition"))
	if err != nil || disposition["filename"] != "résumé.pdf" {
		t.Fatal("filename not encoded correctly")
	}
}

func TestMailMIMERejectsHeaderInjectionAndOversize(t *testing.T) {
	base := EmailMessage{From: "owner@example.test", To: []string{"to@example.test"}, Subject: "safe", TextBody: "hello"}
	for _, mutate := range []func(*EmailMessage){
		func(m *EmailMessage) { m.Subject = "safe\r\nBcc: bad@example.test" },
		func(m *EmailMessage) { m.From = "owner@example.test\nBcc: bad@example.test" },
		func(m *EmailMessage) { m.Headers = map[string]string{"References": "<x>\r\nBcc: bad@example.test"} },
		func(m *EmailMessage) { m.Attachments = []Attachment{{Filename: "bad\r\nheader", Data: []byte("x")}} },
		func(m *EmailMessage) {
			m.Attachments = []Attachment{{Filename: "large.bin", Data: make([]byte, MaxAttachmentBytes+1)}}
		},
	} {
		m := base
		mutate(&m)
		if _, _, err := BuildMailMIME(&m); err == nil {
			t.Fatal("unsafe mail accepted")
		}
	}
	if IsDefinitiveSendError(errors.New("timeout after request wrote")) {
		t.Fatal("timeout must remain ambiguous")
	}
	if !IsDefinitiveSendError(&smithy.GenericAPIError{Code: "MessageRejected", Fault: smithy.FaultClient}) {
		t.Fatal("provider rejection not classified")
	}
}
