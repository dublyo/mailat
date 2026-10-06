package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/dublyo/mailat/api/internal/provider"
)

// campaignSnapshot is the campaign content and sender as stored at send time.
type campaignSnapshot struct {
	ID            int64
	OrgID         int64
	Subject       string
	HTMLContent   string
	TextContent   string
	FromName      string
	FromEmail     string
	ReplyTo       string
	TrackOpens    bool
	TrackClicks   bool
	OrgName       string
	PostalAddress string
}

type renderMode int

const (
	// renderSend: real recipient, tracking per campaign toggles, signed links.
	renderSend renderMode = iota
	// renderTest: tracking off, "[Test] " subject, test unsubscribe token.
	renderTest
	// renderPreview: tracking off, "#" unsubscribe link, no tokens.
	renderPreview
)

type renderOptions struct {
	APIURL string // API base for tracking and List-Unsubscribe
	WebURL string // web app base for the footer unsubscribe page
	Secret string // JWTSecret; tracking and unsubscribe tokens derive from it
	Mode   renderMode
}

const maxSubjectBytes = 998

var templateVarRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]{0,63})\s*\}\}`)
var closingBodyRe = regexp.MustCompile(`(?i)</body\s*>`)

// renderCampaignMessage personalises, tracks and footers one campaign message.
// It returns the message and the template variables that had no value.
// recipient.ID is the campaign_recipients row (0 for tests and previews).
func renderCampaignMessage(snap campaignSnapshot, rcpt eligibleRecipient, opts renderOptions) (*provider.EmailMessage, []string, error) {
	if opts.Mode != renderPreview && opts.Secret == "" {
		return nil, nil, errors.New("campaign render: missing signing secret")
	}
	if opts.Mode == renderSend && strings.TrimSpace(snap.PostalAddress) == "" {
		return nil, nil, &provider.MailValidationError{Message: "set the organization's postal address before sending campaigns"}
	}
	if snap.HTMLContent == "" && snap.TextContent == "" {
		return nil, nil, &provider.MailValidationError{Message: "campaign content is required"}
	}
	p := personaliser{rcpt: rcpt}
	subject := sanitizeSubject(p.apply(snap.Subject, false))
	if opts.Mode == renderTest {
		subject = sanitizeSubject("[Test] " + subject)
	}
	htmlBody := p.apply(snap.HTMLContent, true)
	textBody := p.apply(snap.TextContent, false)

	apiURL := strings.TrimRight(opts.APIURL, "/")
	webURL := strings.TrimRight(opts.WebURL, "/")
	var unsubToken string
	switch opts.Mode {
	case renderSend:
		unsubToken = encodeUnsubscribeToken(opts.Secret, UnsubscribeData{ContactID: rcpt.ContactID, OrgID: snap.OrgID, RecipientID: rcpt.ID})
	case renderTest:
		unsubToken = encodeUnsubscribeToken(opts.Secret, UnsubscribeData{OrgID: snap.OrgID, Test: true})
	}
	unsubPage := "#"
	if unsubToken != "" {
		unsubPage = webURL + "/unsubscribe#" + unsubToken
	}

	tracking := opts.Mode == renderSend && rcpt.ID > 0
	if htmlBody != "" {
		if tracking && snap.TrackClicks {
			htmlBody = rewriteTrackedLinks(htmlBody, func(index int, target string) string {
				return apiURL + "/api/v1/tracking/click/" + ClickToken(opts.Secret, rcpt.ID, snap.ID, snap.OrgID, index, target)
			})
		}
		// The footer is added after rewriting so its links are never tracked.
		tail := htmlFooter(snap.OrgName, snap.PostalAddress, unsubPage)
		if tracking && snap.TrackOpens {
			tail += `<img src="` + html.EscapeString(apiURL+"/api/v1/tracking/open/"+OpenToken(opts.Secret, rcpt.ID, snap.ID, snap.OrgID)+".gif") + `" width="1" height="1" alt="" style="display:none">`
		}
		htmlBody = insertBeforeBodyEnd(htmlBody, tail)
	}
	if textBody != "" {
		textBody += textFooter(snap.OrgName, snap.PostalAddress, unsubPage)
	}

	msg := &provider.EmailMessage{
		From:     (&mail.Address{Name: snap.FromName, Address: snap.FromEmail}).String(),
		To:       []string{rcpt.Email},
		ReplyTo:  snap.ReplyTo,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
		Headers:  map[string]string{},
	}
	if unsubToken != "" {
		msg.Headers["List-Unsubscribe"] = "<" + apiURL + "/api/v1/unsubscribe/" + unsubToken + ">"
		msg.Headers["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
	}
	if rcpt.MessageUUID != "" {
		msg.Headers["X-Mailat-Message-ID"] = rcpt.MessageUUID
	}
	return msg, p.unknown, nil
}

// personaliser substitutes {{variables}} in a single regex pass, so a
// substituted value is never expanded again.
type personaliser struct {
	rcpt    eligibleRecipient
	unknown []string
}

func (p *personaliser) apply(s string, escape bool) string {
	if s == "" {
		return s
	}
	return templateVarRe.ReplaceAllStringFunc(s, func(m string) string {
		v := p.value(templateVarRe.FindStringSubmatch(m)[1])
		if escape {
			return html.EscapeString(v)
		}
		return v
	})
}

func (p *personaliser) value(key string) string {
	switch key {
	case "email":
		return p.rcpt.Email
	case "firstName", "first_name":
		return p.rcpt.FirstName
	case "lastName", "last_name":
		return p.rcpt.LastName
	}
	v, ok := p.rcpt.Attributes[key]
	if !ok {
		for _, k := range p.unknown {
			if k == key {
				return ""
			}
		}
		p.unknown = append(p.unknown, key)
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool, int, int64, json.Number:
		return fmt.Sprint(t)
	}
	return "" // null, arrays and objects render empty
}

// sanitizeSubject removes line breaks and truncates to the RFC 5322 line
// limit on a UTF-8 boundary.
func sanitizeSubject(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	if len(s) <= maxSubjectBytes {
		return s
	}
	s = s[:maxSubjectBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// rewriteTrackedLinks replaces http(s) <a href> targets (≤2048 chars) with
// wrap(index, target), index counting rewritten links in document order.
// Links marked data-mailat-no-track are left alone. Every other token is
// copied byte for byte, so entities, quoting and malformed markup survive.
func rewriteTrackedLinks(src string, wrap func(index int, target string) string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	b.Grow(len(src) + 256)
	index := 0
	for {
		tt := z.Next()
		raw := string(z.Raw())
		if tt == html.ErrorToken {
			b.WriteString(raw)
			if z.Err() != io.EOF {
				return src
			}
			return b.String()
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			b.WriteString(raw)
			continue
		}
		tok := z.Token()
		if tok.DataAtom != atom.A {
			b.WriteString(raw)
			continue
		}
		href, skip := -1, false
		for i, a := range tok.Attr {
			switch {
			case a.Namespace != "":
			case a.Key == "data-mailat-no-track":
				skip = true
			case a.Key == "href" && href < 0:
				href = i
			}
		}
		if skip || href < 0 || !trackableURL(strings.TrimSpace(tok.Attr[href].Val)) {
			b.WriteString(raw)
			continue
		}
		tok.Attr[href].Val = wrap(index, strings.TrimSpace(tok.Attr[href].Val))
		index++
		b.WriteString(tok.String())
	}
}

func insertBeforeBodyEnd(doc, tail string) string {
	locs := closingBodyRe.FindAllStringIndex(doc, -1)
	if len(locs) == 0 {
		return doc + tail
	}
	at := locs[len(locs)-1][0]
	return doc[:at] + tail + doc[at:]
}

func postalHTML(addr string) string {
	addr = strings.ReplaceAll(strings.TrimSpace(addr), "\r\n", "\n")
	return strings.ReplaceAll(html.EscapeString(addr), "\n", "<br>")
}

func htmlFooter(orgName, postal, unsubURL string) string {
	var b strings.Builder
	b.WriteString(`<div style="margin-top:32px;padding-top:16px;border-top:1px solid #e5e7eb;font-family:Arial,sans-serif;font-size:12px;line-height:1.5;color:#6b7280;text-align:center">`)
	if orgName != "" || strings.TrimSpace(postal) != "" {
		b.WriteString(`<p style="margin:0 0 8px">`)
		if orgName != "" {
			b.WriteString(html.EscapeString(orgName))
			if strings.TrimSpace(postal) != "" {
				b.WriteString("<br>")
			}
		}
		b.WriteString(postalHTML(postal))
		b.WriteString(`</p>`)
	}
	b.WriteString(`<p style="margin:0"><a href="` + html.EscapeString(unsubURL) + `" style="color:#6b7280;text-decoration:underline">Unsubscribe</a></p></div>`)
	return b.String()
}

func textFooter(orgName, postal, unsubURL string) string {
	var b strings.Builder
	b.WriteString("\n\n--\n")
	if orgName != "" {
		b.WriteString(orgName + "\n")
	}
	if p := strings.TrimSpace(strings.ReplaceAll(postal, "\r\n", "\n")); p != "" {
		b.WriteString(p + "\n")
	}
	b.WriteString("\nUnsubscribe: " + unsubURL + "\n")
	return b.String()
}
