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

	"github.com/aws/smithy-go"
)

const MaxAttachmentBytes = 10 * 1024 * 1024
const MaxMailRecipients = 50

type MailValidationError struct{ Message string }

func (e *MailValidationError) Error() string { return e.Message }

// IsDefinitiveSendError distinguishes a rejected request from an uncertain transport result.
// A timeout is not proof of rejection: the service may have accepted the message already.
func IsDefinitiveSendError(err error) bool {
	var validation *MailValidationError
	if errors.As(err, &validation) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorFault() == smithy.FaultClient
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
		// Only threading and standard one-click unsubscribe headers are accepted from callers.
		switch strings.ToLower(k) {
		case "in-reply-to", "references", "list-unsubscribe", "list-unsubscribe-post", "x-mailat-message-id":
			writeMailHeader(&output, k, v)
		}
	}
	var body bytes.Buffer
	mixed := multipart.NewWriter(&body)
	textHeaders := textproto.MIMEHeader{}
	var alternative bytes.Buffer
	alternatives := multipart.NewWriter(&alternative)
	for _, part := range []struct{ media, data string }{{"text/plain", msg.TextBody}, {"text/html", msg.HTMLBody}} {
		if part.data == "" && !(part.media == "text/plain" && msg.TextBody == "" && msg.HTMLBody == "") {
			continue
		}
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
	for _, line := range bytes.Split(output.Bytes(), []byte("\r\n")) {
		if len(line) > 998 {
			return nil, nil, &MailValidationError{"message contains an excessively long header line"}
		}
	}
	return output.Bytes(), recipients, nil
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
