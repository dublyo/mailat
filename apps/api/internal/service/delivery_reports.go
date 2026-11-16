package service

import (
	"mime"
	"net/mail"
	"regexp"
	"strings"
)

// Bounce notices are filed in the Reports folder (with verified DMARC reports)
// instead of the Inbox. This only decides placement: the message is still
// stored, searchable and listed in All Mail, so a false match never loses
// mail. Spam and virus verdicts still win.

var deliveryReportSubject = regexp.MustCompile(`(?i)^\s*(delivery status notification|undeliver(ed|able)|mail delivery (failed|failure|subsystem)|returned mail|delivery (failure|has failed|incomplete|delayed|notification)|failure notice|non[- ]?delivery)`)

// deliveryStatusReport reports whether a message is a delivery status
// notification: an RFC 3464 multipart/report with report-type=delivery-status,
// or a classic bounce from MAILER-DAEMON or postmaster with a bounce subject.
func deliveryStatusReport(parsed *parsedIncoming) bool {
	if parsed == nil || reportForwardedByPerson(parsed) {
		return false
	}
	if mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type")); err == nil &&
		mediaType == "multipart/report" && strings.EqualFold(params["report-type"], "delivery-status") {
		return true
	}
	local := strings.ToLower(senderLocalPart(parsed.Header.Get("From")))
	if local != "mailer-daemon" && local != "postmaster" {
		return false
	}
	return deliveryReportSubject.MatchString(decodeMIMEHeader(parsed.Header.Get("Subject")))
}

// A person forwarding or replying to a bounce wants it seen in the Inbox.
func reportForwardedByPerson(parsed *parsedIncoming) bool {
	return dmarcConversationPrefix.MatchString(decodeMIMEHeader(parsed.Header.Get("Subject")))
}

func senderLocalPart(from string) string {
	address := ""
	if parsed, err := mail.ParseAddress(from); err == nil {
		address = parsed.Address
	} else if list, err := mail.ParseAddressList(from); err == nil && len(list) > 0 {
		address = list[0].Address
	}
	if at := strings.LastIndex(address, "@"); at > 0 {
		return address[:at]
	}
	return ""
}
