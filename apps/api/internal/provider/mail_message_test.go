package provider

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
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

func TestMailMIMEHeaderAllowlist(t *testing.T) {
	raw, _, err := BuildMailMIME(&EmailMessage{From: "a@example.test", To: []string{"b@example.test"}, Subject: "s", TextBody: "x", Headers: map[string]string{
		"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All", "X-Mailat-Loop": "aa,bb",
		"X-Mailat-Forwarded-For": "me@example.test", "X-Custom": "drop", "Bcc": "evil@example.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All", "X-Mailat-Loop": "aa,bb", "X-Mailat-Forwarded-For": "me@example.test", "X-Custom": "", "Bcc": ""} {
		if got := m.Header.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	if _, _, err = BuildMailMIME(&EmailMessage{From: "a@example.test", To: []string{"b@example.test"}, Subject: "s", TextBody: "x", Headers: map[string]string{"Auto-Submitted": "no\r\nBcc: evil@example.test"}}); err == nil {
		t.Fatal("CR/LF in an allowed header accepted")
	}
}

// Without attachments a message is what mail clients send: one text part on
// its own, or text+HTML as multipart/alternative. multipart/mixed is only
// used when there are attachments.
func TestMailMIMEStructureFollowsContent(t *testing.T) {
	read := func(t *testing.T, m *EmailMessage) (string, map[string]string, *mail.Message) {
		t.Helper()
		raw, _, err := BuildMailMIME(m)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		media, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		return media, params, msg
	}
	base := func() *EmailMessage {
		return &EmailMessage{From: "a@example.test", To: []string{"b@example.test"}, Subject: "Hello there"}
	}

	// Text only: a single quoted-printable text/plain body.
	m := base()
	m.TextBody = "héllo = world " + strings.Repeat("long ", 40)
	media, params, msg := read(t, m)
	if media != "text/plain" || params["charset"] != "UTF-8" || msg.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Fatalf("text only: %s %v %q", media, params, msg.Header.Get("Content-Transfer-Encoding"))
	}
	if body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body)); string(body) != m.TextBody {
		t.Fatalf("text only body: %q", body)
	}

	// HTML only: a single text/html body.
	m = base()
	m.HTMLBody = "<p>hello</p>"
	if media, _, _ = read(t, m); media != "text/html" {
		t.Fatalf("html only: %s", media)
	}

	// Text and HTML: multipart/alternative with exactly those two parts, in order.
	m = base()
	m.TextBody, m.HTMLBody = "hello", "<p>hello</p>"
	media, params, msg = read(t, m)
	if media != "multipart/alternative" {
		t.Fatalf("text+html: %s", media)
	}
	r := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		types = append(types, pt)
	}
	if strings.Join(types, ",") != "text/plain,text/html" {
		t.Fatalf("alternative parts: %v", types)
	}

	// With an attachment: multipart/mixed holding the alternative part first.
	m.Attachments = []Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("x")}}
	media, params, msg = read(t, m)
	if media != "multipart/mixed" {
		t.Fatalf("with attachment: %s", media)
	}
	first, err := multipart.NewReader(msg.Body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if pt, _, _ := mime.ParseMediaType(first.Header.Get("Content-Type")); pt != "multipart/alternative" {
		t.Fatalf("first mixed part: %s", pt)
	}

	// An attachment with no text keeps the empty text/plain part inside mixed.
	m = base()
	m.Attachments = []Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("x")}}
	if media, _, _ = read(t, m); media != "multipart/mixed" {
		t.Fatalf("attachment only: %s", media)
	}
}
