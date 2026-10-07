package service

import (
	"net/mail"
	"regexp"
	"strings"
)

// ArrivalVerdicts are the SES receipt verdicts and placement of one mailbox copy.
type ArrivalVerdicts struct {
	Spam, Virus, SPF, DKIM, DMARC string
	DMARCReport                   bool
	Folder                        string
}

// Local parts that never belong to a person (RFC 3834 §2 and common list/bounce
// managers). The no-reply forms also cover "donotreply" and suffixes.
var systemSenderLocal = regexp.MustCompile(`^(mailer-daemon|postmaster|listserv|majordomo|(do-?not-?|no[-_.]?)reply.*|bounce.*|owner-.+|.+-request)$`)

// AutoReplyEligible applies the RFC 3834 and backscatter guards to one arrival.
// sender is the From address, envelopeFrom the SMTP MAIL FROM, ownAddrs the
// addresses that must never be answered (the identity, its recipients and any
// org identity), rcpts the identity address plus the envelope recipients it
// received for. It returns false with a short reason when no reply may be sent.
func AutoReplyEligible(h mail.Header, envelopeFrom, sender string, ownAddrs, rcpts []string, v ArrivalVerdicts) (bool, string) {
	if value := strings.ToLower(strings.TrimSpace(h.Get("Auto-Submitted"))); value != "" && value != "no" {
		return false, "auto-submitted"
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "bulk", "list", "junk":
		return false, "precedence"
	}
	for _, name := range []string{"List-Id", "List-Unsubscribe", "List-Post"} {
		if len(h[name]) > 0 {
			return false, "list-mail"
		}
	}
	for _, value := range h["X-Auto-Response-Suppress"] {
		for _, token := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(token)) {
			case "all", "oof", "autoreply":
				return false, "auto-response-suppressed"
			}
		}
	}
	if len(h["X-Mailat-Loop"]) > 0 {
		return false, "mail-loop"
	}
	envelope := strings.Trim(strings.TrimSpace(envelopeFrom), "<>")
	if envelope == "" {
		return false, "null-sender"
	}
	sender = strings.ToLower(strings.TrimSpace(sender))
	at := strings.LastIndex(sender, "@")
	if at < 1 || at == len(sender)-1 {
		return false, "no-sender"
	}
	if systemSenderLocal.MatchString(sender[:at]) {
		return false, "system-sender"
	}
	for _, own := range ownAddrs {
		if strings.EqualFold(strings.TrimSpace(own), sender) {
			return false, "own-address"
		}
	}
	if strings.EqualFold(v.Spam, "FAIL") || strings.EqualFold(v.Virus, "FAIL") {
		return false, "spam"
	}
	if v.DMARCReport {
		return false, "dmarc-report"
	}
	if v.Folder == "spam" || v.Folder == "trash" {
		return false, "folder"
	}
	// Backscatter: answer only a From address the receipt proved genuine.
	// DMARC PASS aligns From with SPF or DKIM. Without a DMARC pass (GRAY when
	// the domain has no policy, PROCESSING_FAILED or missing) only an SPF PASS
	// for an envelope sender in exactly the From domain counts; an SPF pass for
	// some other envelope domain says nothing about the From address.
	if !strings.EqualFold(v.DMARC, "PASS") {
		envelopeDomain := ""
		if i := strings.LastIndex(envelope, "@"); i >= 0 {
			envelopeDomain = envelope[i+1:]
		}
		if strings.EqualFold(v.DMARC, "FAIL") || !strings.EqualFold(v.SPF, "PASS") || !strings.EqualFold(envelopeDomain, sender[at+1:]) {
			return false, "unauthenticated"
		}
	}
	if strings.EqualFold(v.SPF, "FAIL") && strings.EqualFold(v.DKIM, "FAIL") {
		return false, "unauthenticated"
	}
	if !addressedTo(h, rcpts) {
		return false, "not-addressed"
	}
	return true, ""
}

// addressedTo reports whether any rcpt appears in To or Cc (RFC 3834 §2: no
// reply to Bcc or catch-all deliveries).
func addressedTo(h mail.Header, rcpts []string) bool {
	for _, key := range []string{"To", "Cc"} {
		for _, value := range h[key] {
			list, err := mail.ParseAddressList(value)
			if err != nil {
				continue
			}
			for _, a := range list {
				for _, r := range rcpts {
					if r != "" && strings.EqualFold(a.Address, strings.TrimSpace(r)) {
						return true
					}
				}
			}
		}
	}
	return false
}

// LoopTokens returns every X-Mailat-Loop token, across all instances of the
// header and comma-joined values, lowercased.
func LoopTokens(h mail.Header) []string {
	var tokens []string
	for _, value := range h["X-Mailat-Loop"] {
		for _, token := range strings.Split(value, ",") {
			if token = strings.ToLower(strings.TrimSpace(token)); token != "" {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

// maxForwardHops caps Mailat-to-Mailat forwarding chains.
const maxForwardHops = 3

// ForwardLoopBlocked reports whether a forward tagged tag must not run: the
// message already passed through it, or through too many forwards.
func ForwardLoopBlocked(h mail.Header, tag string) bool {
	tokens := LoopTokens(h)
	if len(tokens) >= maxForwardHops {
		return true
	}
	for _, token := range tokens {
		if token == strings.ToLower(tag) {
			return true
		}
	}
	return false
}

// excludedSender matches a rule's exclude patterns against the sender address.
// A pattern with "*" is a whole-address wildcard; any other pattern matches as
// a case-insensitive substring (so "@example.com" excludes that domain).
func excludedSender(patterns []string, sender string) bool {
	sender = strings.ToLower(sender)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.Contains(p, "*") {
			parts := strings.Split(p, "*")
			for i := range parts {
				parts[i] = regexp.QuoteMeta(parts[i])
			}
			if regexp.MustCompile(`^` + strings.Join(parts, `.*`) + `$`).MatchString(sender) {
				return true
			}
		} else if strings.Contains(sender, p) {
			return true
		}
	}
	return false
}

// autoReplySubject is the rule subject, or "Re: <original>" when the rule has
// none, with header line breaks removed.
func autoReplySubject(ruleSubject, original string) string {
	clean := func(s string) string {
		return strings.TrimSpace(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' }), " "))
	}
	if s := clean(ruleSubject); s != "" {
		return clipUTF8(s, 500)
	}
	original = clean(original)
	if original == "" {
		original = "(no subject)"
	}
	if len(original) >= 3 && strings.EqualFold(original[:3], "re:") {
		return clipUTF8(original, 500)
	}
	return clipUTF8("Re: "+original, 500)
}
