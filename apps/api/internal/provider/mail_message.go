package provider

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

const MaxAttachmentBytes = 10 * 1024 * 1024
const MaxMailRecipients = 50

type MailValidationError struct{ Message string }

func (e *MailValidationError) Error() string { return e.Message }

// SendErrorClass is what a failed SendEmail call proves about the message.
type SendErrorClass int

const (
	// SendUncertain: the provider may have accepted the message (timeouts, 5xx,
	// transport errors). It must never be resubmitted automatically.
	SendUncertain SendErrorClass = iota
	// SendRejected: the request was refused outright and will not succeed as is.
	SendRejected
	// SendThrottled: a rate or quota limit refused the request before acceptance,
	// so retrying later cannot duplicate the message.
	SendThrottled
)

var throttleCodes = map[string]bool{
	"TooManyRequestsException": true,
	"Throttling":               true,
	"ThrottlingException":      true,
	"LimitExceededException":   true,
}

// ClassifySendError maps a send error to its class. Throttles are checked first
// because SES reports them as client faults too.
func ClassifySendError(err error) SendErrorClass {
	if err == nil {
		return SendUncertain
	}
	var tooMany *types.TooManyRequestsException
	var limit *types.LimitExceededException
	if errors.As(err, &tooMany) || errors.As(err, &limit) {
		return SendThrottled
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && throttleCodes[apiErr.ErrorCode()] {
		return SendThrottled
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.ResponseError != nil && respErr.Response != nil && respErr.HTTPStatusCode() == 429 {
		return SendThrottled
	}
	var validation *MailValidationError
	if errors.As(err, &validation) {
		return SendRejected
	}
	// Remaining client faults (MessageRejected, SendingPausedException,
	// AccountSuspendedException, MailFromDomainNotVerifiedException, ...) are terminal.
	if apiErr != nil && apiErr.ErrorFault() == smithy.FaultClient {
		return SendRejected
	}
	return SendUncertain
}

// IsDefinitiveSendError distinguishes a rejected request from an uncertain transport result.
// A timeout is not proof of rejection: the service may have accepted the message already.
// Throttles are not definitive: they are retried later.
func IsDefinitiveSendError(err error) bool {
	return ClassifySendError(err) == SendRejected
}

// IsQuotaExhausted reports a throttle caused by the account's sending quota
// (typically the daily limit) rather than the per-second rate.
func IsQuotaExhausted(err error) bool {
	if ClassifySendError(err) != SendThrottled {
		return false
	}
	var limit *types.LimitExceededException
	var apiErr smithy.APIError
	if errors.As(err, &limit) || (errors.As(err, &apiErr) && apiErr.ErrorCode() == "LimitExceededException") {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "quota") || strings.Contains(text, "daily")
}

// SESErrorCode returns the AWS API error code wrapped in err, or "".
func SESErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func singleAddress(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", &MailValidationError{"email addresses must not contain line breaks"}
	}
	a, err := mail.ParseAddress(value)
	if err != nil || !strings.Contains(a.Address, "@") {
		return "", &MailValidationError{"invalid email address"}
	}
	return a.String(), nil
}

// BuildMailMIME uses the standard MIME writers and deliberately omits a Bcc header.
// Bcc recipients are supplied only through the SES envelope.
func BuildMailMIME(msg *EmailMessage) ([]byte, []string, error) {
	if msg == nil {
		return nil, nil, &MailValidationError{"email is required"}
	}
	from, err := singleAddress(msg.From)
	if err != nil {
		return nil, nil, err
	}
	if len(msg.To)+len(msg.Cc)+len(msg.Bcc) == 0 || len(msg.To)+len(msg.Cc)+len(msg.Bcc) > MaxMailRecipients {
		return nil, nil, &MailValidationError{"email must have between 1 and 50 recipients"}
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, nil, &MailValidationError{"subject must not contain line breaks"}
	}
	if len(msg.Subject) > 1000 {
		return nil, nil, &MailValidationError{"subject is too long"}
	}
	if msg.TextBody == "" && msg.HTMLBody == "" && len(msg.Attachments) == 0 {
		return nil, nil, &MailValidationError{"email content is required"}
	}
	var recipients []string
	var to, cc []string
	for group, addresses := range [][]string{msg.To, msg.Cc, msg.Bcc} {
		for _, value := range addresses {
			normalized, err := singleAddress(value)
			if err != nil {
				return nil, nil, err
			}
			a, _ := mail.ParseAddress(normalized)
			recipients = append(recipients, a.Address)
			if group == 0 {
				to = append(to, normalized)
			}
			if group == 1 {
				cc = append(cc, normalized)
			}
		}
	}
	var output bytes.Buffer
	writeMailHeader(&output, "From", from)
	if len(to) > 0 {
		writeMailHeader(&output, "To", strings.Join(to, ", "))
	}
	if len(cc) > 0 {
		writeMailHeader(&output, "Cc", strings.Join(cc, ", "))
	}
	if msg.ReplyTo != "" {
		r, err := singleAddress(msg.ReplyTo)
		if err != nil {
			return nil, nil, err
		}
		writeMailHeader(&output, "Reply-To", r)
	}
	writeMailHeader(&output, "Subject", mime.QEncoding.Encode("UTF-8", msg.Subject))
	writeMailHeader(&output, "Date", time.Now().UTC().Format(time.RFC1123Z))
	writeMailHeader(&output, "MIME-Version", "1.0")
	keys := make([]string, 0, len(msg.Headers))
	for k := range msg.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := msg.Headers[k]
		if strings.ContainsAny(k+v, "\r\n") || strings.ContainsAny(k, ": \t") {
			return nil, nil, &MailValidationError{"invalid mail header"}
		}
		if AllowedMailHeader(k) {
			writeMailHeader(&output, k, v)
		}
	}
	type textPart struct{ media, data string }
	var parts []textPart
	for _, part := range []textPart{{"text/plain", msg.TextBody}, {"text/html", msg.HTMLBody}} {
		if part.data == "" && !(part.media == "text/plain" && msg.TextBody == "" && msg.HTMLBody == "") {
			continue
		}
		parts = append(parts, part)
	}
	// Without attachments, send what mail clients send: one text part on its
	// own, or text and HTML as multipart/alternative. multipart/mixed wrapping
	// a lone alternative part is valid but unusual, and filters notice it.
	if len(msg.Attachments) == 0 && len(parts) == 1 {
		fmt.Fprintf(&output, "Content-Type: %s; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", parts[0].media)
		q := quotedprintable.NewWriter(&output)
		if _, err := q.Write([]byte(parts[0].data)); err != nil {
			return nil, nil, err
		}
		if err := q.Close(); err != nil {
			return nil, nil, err
		}
		return finishMailMIME(&output, recipients)
	}
	var body bytes.Buffer
	mixed := multipart.NewWriter(&body)
	textHeaders := textproto.MIMEHeader{}
	var alternative bytes.Buffer
	alternatives := multipart.NewWriter(&alternative)
	for _, part := range parts {
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Type", part.media+"; charset=UTF-8")
		headers.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := alternatives.CreatePart(headers)
		if err != nil {
			return nil, nil, err
		}
		q := quotedprintable.NewWriter(writer)
		if _, err = q.Write([]byte(part.data)); err != nil {
			return nil, nil, err
		}
		if err = q.Close(); err != nil {
			return nil, nil, err
		}
	}
	if err := alternatives.Close(); err != nil {
		return nil, nil, err
	}
	if len(msg.Attachments) == 0 {
		fmt.Fprintf(&output, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alternatives.Boundary()}))
		output.Write(alternative.Bytes())
		return finishMailMIME(&output, recipients)
	}
	textHeaders.Set("Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alternatives.Boundary()}))
	w, err := mixed.CreatePart(textHeaders)
	if err != nil {
		return nil, nil, err
	}
	if _, err = w.Write(alternative.Bytes()); err != nil {
		return nil, nil, err
	}
	total := 0
	for _, attachment := range msg.Attachments {
		total += len(attachment.Data)
		if total > MaxAttachmentBytes {
			return nil, nil, &MailValidationError{"attachments exceed the 10 MiB total limit"}
		}
		if strings.ContainsAny(attachment.Filename+attachment.ContentType, "\r\n") {
			return nil, nil, &MailValidationError{"invalid attachment metadata"}
		}
		contentType, _, err := mime.ParseMediaType(attachment.ContentType)
		if err != nil {
			contentType = "application/octet-stream"
		}
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Type", mime.FormatMediaType(contentType, map[string]string{"name": attachment.Filename}))
		disposition := "attachment"
		if attachment.Inline && attachment.ContentID != "" {
			disposition = "inline"
		}
		headers.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.Filename}))
		if attachment.ContentID != "" {
			if strings.ContainsAny(attachment.ContentID, "\r\n<>") {
				return nil, nil, &MailValidationError{"invalid attachment content ID"}
			}
			headers.Set("Content-ID", "<"+attachment.ContentID+">")
		}
		headers.Set("Content-Transfer-Encoding", "base64")
		writer, err := mixed.CreatePart(headers)
		if err != nil {
			return nil, nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(attachment.Data)
		for len(encoded) > 76 {
			fmt.Fprint(writer, encoded[:76]+"\r\n")
			encoded = encoded[76:]
		}
		fmt.Fprint(writer, encoded+"\r\n")
	}
	if err := mixed.Close(); err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(&output, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mixed.Boundary()}))
	output.Write(body.Bytes())
	return finishMailMIME(&output, recipients)
}

// finishMailMIME applies the RFC 5322 998-octet line limit to the whole message.
func finishMailMIME(output *bytes.Buffer, recipients []string) ([]byte, []string, error) {
	for _, line := range bytes.Split(output.Bytes(), []byte("\r\n")) {
		if len(line) > 998 {
			return nil, nil, &MailValidationError{"message contains an excessively long header line"}
		}
	}
	return output.Bytes(), recipients, nil
}

// AllowedMailHeader reports whether callers may set the header: threading,
// one-click unsubscribe, RFC 3834 auto-reply markers and Mailat's own tracing
// and forwarding-loop headers. Everything else is dropped.
func AllowedMailHeader(name string) bool {
	switch strings.ToLower(name) {
	case "in-reply-to", "references", "list-unsubscribe", "list-unsubscribe-post", "x-mailat-message-id",
		"auto-submitted", "x-auto-response-suppress", "x-mailat-loop", "x-mailat-forwarded-for":
		return true
	}
	return false
}

// Fold only on existing whitespace, keeping encoded words and addresses intact.
func writeMailHeader(output *bytes.Buffer, name, value string) {
	line := name + ": "
	for _, word := range strings.SplitAfter(value, " ") {
		if len(line)+len(word) > 78 && len(line) > len(name)+2 {
			output.WriteString(strings.TrimRight(line, " ") + "\r\n")
			line = " "
		}
		line += word
	}
	output.WriteString(strings.TrimRight(line, " ") + "\r\n")
}
