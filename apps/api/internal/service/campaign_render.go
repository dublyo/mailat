package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
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
	// Automation marks an automation message: ID is the automation and the
	// recipient ID an automation_messages row, so tokens point there.
	Automation bool
}

// trackingData is the signed tracking payload for one recipient of snap.
func (snap campaignSnapshot) trackingData(recipientID int64) TrackingData {
	d := TrackingData{R: recipientID, C: snap.ID, O: snap.OrgID}
	if snap.Automation {
		d.K = trackingKindAutomation
	}
	return d
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

// checkCampaignLinkBases requires absolute http(s) API and web URLs: the
// List-Unsubscribe header and footer link are built from them, and a relative
// or empty base would leave recipients without a working unsubscribe.
func checkCampaignLinkBases(apiURL, webURL string) error {
	for _, base := range []struct{ name, value string }{{"API_URL", apiURL}, {"WEB_URL", webURL}} {
		u, err := url.Parse(strings.TrimSpace(base.value))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return &provider.MailValidationError{Message: base.name + " must be an absolute http(s) URL so campaign unsubscribe links work"}
		}
	}
	return nil
}

// renderCampaignMessage personalises, tracks and footers one campaign message.
// It returns the message and the template variables that had no value.
// recipient.ID is the campaign_recipients row (0 for tests and previews).
func renderCampaignMessage(snap campaignSnapshot, rcpt eligibleRecipient, opts renderOptions) (*provider.EmailMessage, []string, error) {
	if opts.Mode != renderPreview && opts.Secret == "" {
		return nil, nil, errors.New("campaign render: missing signing secret")
	}
	if opts.Mode != renderPreview {
		if err := checkCampaignLinkBases(opts.APIURL, opts.WebURL); err != nil {
			return nil, nil, err
		}
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

	apiURL := strings.TrimRight(opts.APIURL, "/")
	webURL := strings.TrimRight(opts.WebURL, "/")
	var unsubToken string
	switch opts.Mode {
	case renderSend:
		data := UnsubscribeData{ContactID: rcpt.ContactID, OrgID: snap.OrgID, RecipientID: rcpt.ID}
		if snap.Automation {
			data.RecipientID, data.AutomationMessageID = 0, rcpt.ID
		}
		unsubToken = encodeUnsubscribeToken(opts.Secret, data)
	case renderTest:
		unsubToken = encodeUnsubscribeToken(opts.Secret, UnsubscribeData{OrgID: snap.OrgID, Test: true})
	}
	unsubPage := "#"
	if unsubToken != "" {
		unsubPage = webURL + "/unsubscribe#" + unsubToken
	}

	tracking := opts.Mode == renderSend && rcpt.ID > 0
	htmlBody := snap.HTMLContent
	if htmlBody != "" && tracking && snap.TrackClicks {
		// Links are signed before personalisation, so a token carries the
		// template URL (never the recipient's data); the click handler fills
		// the variables in at redirect time.
		htmlBody = rewriteTrackedLinks(htmlBody, func(index int, target string) string {
			d := snap.trackingData(rcpt.ID)
			d.L, d.U = index, target
			return apiURL + "/api/v1/tracking/click/" + encodeTrackingToken(opts.Secret, d)
		})
	}
	htmlBody = p.apply(htmlBody, true)
	textBody := p.apply(snap.TextContent, false)
	if htmlBody != "" {
		at, closers, err := footerInsertion(htmlBody)
		if err != nil {
			return nil, nil, err
		}
		// The footer is added after rewriting so its links are never tracked.
		tail := closers + htmlFooter(snap.OrgName, snap.PostalAddress, unsubPage)
		if tracking && snap.TrackOpens {
			tail += `<img src="` + html.EscapeString(apiURL+"/api/v1/tracking/open/"+encodeTrackingToken(opts.Secret, snap.trackingData(rcpt.ID))+".gif") + `" width="1" height="1" alt="" style="display:none">`
		}
		htmlBody = htmlBody[:at] + tail + htmlBody[at:]
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
		if skip || href < 0 || !trackableTemplate(strings.TrimSpace(tok.Attr[href].Val)) {
			b.WriteString(raw)
			continue
		}
		tok.Attr[href].Val = wrap(index, strings.TrimSpace(tok.Attr[href].Val))
		index++
		b.WriteString(tok.String())
	}
}

// Elements whose end tag the footer never emits: void elements, the document
// structure, and p (the footer's own <div> already closes an open p).
var footerNoClose = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true, "keygen": true,
	"html": true, "head": true, "body": true, "p": true,
}

// Elements after which the tokenizer reads raw text up to the matching end tag.
var rawTextElements = map[string]bool{
	"iframe": true, "noembed": true, "noframes": true, "noscript": true, "plaintext": true,
	"script": true, "style": true, "textarea": true, "title": true, "xmp": true,
}

var errUnfinishedHTML = &provider.MailValidationError{Message: "HTML content ends inside an unfinished tag or a <plaintext> element"}

// footerInsertion picks where the compliance footer goes so it always lands
// in rendered body content: before the last </body> seen in normal markup
// (never one inside a comment or raw-text element), else at the end. closers
// ends whatever is still open there (an unterminated comment, raw-text element
// or wrapper such as a hidden <div>). A document that ends inside a tag or a
// <plaintext> element cannot be closed reliably and is rejected.
func footerInsertion(doc string) (at int, closers string, err error) {
	z := html.NewTokenizer(strings.NewReader(doc))
	var stack []string
	closeAll := func() string {
		var b strings.Builder
		for i := len(stack) - 1; i >= 0; i-- {
			b.WriteString("</" + stack[i] + ">")
		}
		return b.String()
	}
	foreign := func() bool {
		for _, n := range stack {
			if n == "svg" || n == "math" {
				return true
			}
		}
		return false
	}
	at, offset, fix := -1, 0, ""
	for {
		tt := z.Next()
		raw := string(z.Raw())
		offset += len(raw)
		if tt == html.ErrorToken {
			if z.Err() != io.EOF || raw != "" {
				return 0, "", errUnfinishedHTML // e.g. "</di" at the end
			}
			break
		}
		last := offset == len(doc)
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			if last && !strings.HasSuffix(raw, ">") {
				return 0, "", errUnfinishedHTML
			}
			name, _ := z.TagName()
			n := string(name)
			// Self-closing is honoured only in svg/math; raw-text tags switch the
			// tokenizer to raw text either way.
			if rawTextElements[n] || (!footerNoClose[n] && (tt == html.StartTagToken || !foreign())) {
				stack = append(stack, n)
			}
		case html.EndTagToken:
			if last && !strings.HasSuffix(raw, ">") {
				return 0, "", errUnfinishedHTML
			}
			name, _ := z.TagName()
			n := string(name)
			if n == "body" {
				at, closers = offset-len(raw), closeAll()
				continue
			}
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == n {
					stack = stack[:i]
					break
				}
			}
		case html.CommentToken, html.DoctypeToken:
			if !last {
				break
			}
			if strings.HasPrefix(raw, "<!--") {
				if !strings.HasSuffix(raw, "-->") && !strings.HasSuffix(raw, "--!>") {
					fix = "-->"
				}
			} else if !strings.HasSuffix(raw, ">") {
				fix = ">"
			}
		}
	}
	if at < 0 {
		for _, n := range stack {
			if n == "plaintext" {
				return 0, "", errUnfinishedHTML
			}
		}
		at, closers = len(doc), fix+closeAll()
	}
	// Prove it: markup appended at this point must tokenize as a tag of its own.
	prefix := doc[:at] + closers
	z = html.NewTokenizer(strings.NewReader(prefix + "<mailat-footer>"))
	for offset = 0; offset < len(prefix); {
		if z.Next() == html.ErrorToken {
			return 0, "", errUnfinishedHTML
		}
		offset += len(z.Raw())
	}
	if offset != len(prefix) || z.Next() != html.StartTagToken {
		return 0, "", errUnfinishedHTML
	}
	if name, _ := z.TagName(); string(name) != "mailat-footer" {
		return 0, "", errUnfinishedHTML
	}
	return at, closers, nil
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
