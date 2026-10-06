package service

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
)

func renderFixture() (campaignSnapshot, eligibleRecipient, renderOptions) {
	snap := campaignSnapshot{
		ID: 3, OrgID: 1, Subject: "Hi {{firstName}}", FromName: "News Team", FromEmail: "news@qa.test",
		HTMLContent: `<html><body><p>Hello {{ first_name }}</p><a href="https://shop.test/p?a=1&amp;b=2">Shop</a></body></html>`,
		TextContent: "Hello {{firstName}}", TrackOpens: true, TrackClicks: true,
		OrgName: "QA & Co", PostalAddress: "1 Main St\n<Springfield>",
	}
	rcpt := eligibleRecipient{ID: 9, ContactID: 4, Email: "reader@example.test", MessageUUID: "6f1c7b0e-5d2a-4a43-9a51-1d6c3f7b8e01", FirstName: "Ann", Attributes: map[string]any{}}
	return snap, rcpt, renderOptions{APIURL: "https://api.test/", WebURL: "https://app.test", Secret: trackingSecret, Mode: renderSend}
}

func mustRender(t *testing.T, snap campaignSnapshot, rcpt eligibleRecipient, opts renderOptions) (*provider.EmailMessage, []string) {
	t.Helper()
	msg, unknown, err := renderCampaignMessage(snap, rcpt, opts)
	if err != nil {
		t.Fatal(err)
	}
	return msg, unknown
}

func TestRenderPersonalisationEscapesHTML(t *testing.T) {
	snap, rcpt, opts := renderFixture()
	snap.Subject = "Hi {{firstName}}\r\nBcc: x@evil.test {{missing}}"
	snap.HTMLContent = `<p title="{{firstName}}">{{firstName}} {{ lastName }} {{plan}} {{score}} {{vip}} {{missing}} {{nested}}</p>`
	snap.TextContent = "{{firstName}} {{plan}} {{missing}} {{ other }}"
	rcpt.FirstName = `<script>alert("x")</script>{{lastName}}`
	rcpt.LastName = "O'Neil"
	rcpt.Attributes = map[string]any{"plan": "Pro & Max", "score": float64(1234567), "vip": true, "nested": map[string]any{"a": 1}}
	msg, unknown := mustRender(t, snap, rcpt, opts)

	if strings.Contains(msg.HTMLBody, "<script>") || !strings.Contains(msg.HTMLBody, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`) {
		t.Fatalf("html not escaped: %s", msg.HTMLBody)
	}
	if !strings.Contains(msg.HTMLBody, `title="&lt;script&gt;`) || !strings.Contains(msg.HTMLBody, "O&#39;Neil Pro &amp; Max 1234567 true") {
		t.Fatalf("html values: %s", msg.HTMLBody)
	}
	// Single pass: the {{lastName}} inside a value is not expanded.
	if !strings.Contains(msg.HTMLBody, "{{lastName}}") || !strings.HasPrefix(msg.TextBody, `<script>alert("x")</script>{{lastName}} Pro & Max  `) {
		t.Fatalf("value re-expanded or text escaped: %q", msg.TextBody)
	}
	if strings.ContainsAny(msg.Subject, "\r\n") || !strings.HasPrefix(msg.Subject, "Hi <script>") {
		t.Fatalf("subject=%q", msg.Subject)
	}
	if strings.Join(unknown, ",") != "missing,other" {
		t.Fatalf("unknown=%v", unknown)
	}
}

func TestRenderSubjectTruncation(t *testing.T) {
	snap, rcpt, opts := renderFixture()
	snap.Subject = strings.Repeat("é", 600) // 1200 bytes
	msg, _ := mustRender(t, snap, rcpt, opts)
	if len(msg.Subject) > maxSubjectBytes || !strings.HasPrefix(snap.Subject, msg.Subject) || len(msg.Subject) < maxSubjectBytes-1 {
		t.Fatalf("subject len=%d", len(msg.Subject))
	}
	opts.Mode = renderTest
	if msg, _ = mustRender(t, snap, rcpt, opts); !strings.HasPrefix(msg.Subject, "[Test] ") || len(msg.Subject) > maxSubjectBytes {
		t.Fatalf("test subject=%q", msg.Subject[:20])
	}
}

func TestRewriteTrackedLinks(t *testing.T) {
	wrap := func(i int, u string) string { return "T" + string(rune('0'+i)) + ":" + u }
	src := `<p>&nbsp;&copy; "q" 'a'</p>` +
		`<a href="https://a.test/x?y=1&amp;z=2" class='c'>A</a>` +
		`<a href="mailto:me@a.test">m</a><a href="tel:+1">t</a><a href="#top">h</a>` +
		`<A HREF='http://b.test'>B</A>` +
		`<a data-mailat-no-track href="https://skip.test">s</a>` +
		`<a href="javascript:alert(1)">j</a><a href="/relative">r</a>` +
		`<a href="https://long.test/` + strings.Repeat("x", 2100) + `">L</a>` +
		`<script>var s = '<a href="https://in-script.test">';</script><!-- <a href="https://in-comment.test"> -->` +
		`<a href=" https://c.test/ ">C</a><div <<a href=https://d.test>`
	got := rewriteTrackedLinks(src, wrap)
	for _, want := range []string{
		`<p>&nbsp;&copy; "q" 'a'</p>`,
		`<a href="T0:https://a.test/x?y=1&amp;z=2" class="c">A</a>`,
		`<a href="mailto:me@a.test">m</a><a href="tel:+1">t</a><a href="#top">h</a>`,
		`<a href="T1:http://b.test">B</A>`,
		`<a data-mailat-no-track href="https://skip.test">s</a>`,
		`<a href="javascript:alert(1)">j</a><a href="/relative">r</a>`,
		`<a href="https://long.test/xxx`,
		`<script>var s = '<a href="https://in-script.test">';</script><!-- <a href="https://in-comment.test"> -->`,
		`<a href="T2:https://c.test/">C</a>`,
		`<div <<a href=https://d.test>`, // swallowed by the malformed div: left alone
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	// Untouched documents and malformed markup round-trip byte for byte.
	for _, doc := range []string{`<p>plain &amp; <b>bold</p>`, `<p>a<a href="https://x`, `<<>>&& <a`} {
		if out := rewriteTrackedLinks(doc, wrap); out != doc {
			t.Fatalf("changed %q -> %q", doc, out)
		}
	}
}

func TestRenderTrackingAndFooter(t *testing.T) {
	snap, rcpt, opts := renderFixture()
	msg, _ := mustRender(t, snap, rcpt, opts)
	body := msg.HTMLBody

	clickPrefix := `href="https://api.test/api/v1/tracking/click/`
	i := strings.Index(body, clickPrefix)
	if i < 0 {
		t.Fatalf("link not rewritten: %s", body)
	}
	token := body[i+len(clickPrefix) : i+len(clickPrefix)+strings.Index(body[i+len(clickPrefix):], `"`)]
	d, err := decodeTrackingToken(trackingSecret, token)
	if err != nil || d.R != 9 || d.C != 3 || d.O != 1 || d.L != 0 || d.U != "https://shop.test/p?a=1&b=2" {
		t.Fatalf("click token %+v %v", d, err)
	}
	if strings.Count(body, "/api/v1/tracking/click/") != 1 {
		t.Fatal("footer link was tracked")
	}
	if !strings.Contains(body, `<img src="https://api.test/api/v1/tracking/open/`) || !strings.HasSuffix(body, `style="display:none"></body></html>`) {
		t.Fatalf("pixel missing or misplaced: %s", body)
	}
	if !strings.Contains(body, "QA &amp; Co<br>1 Main St<br>&lt;Springfield&gt;") || strings.Contains(body, "<Springfield>") {
		t.Fatalf("postal address not escaped: %s", body)
	}
	unsub := "https://app.test/unsubscribe#"
	j := strings.Index(body, unsub)
	if j < 0 || strings.Index(body, "</body>") < j {
		t.Fatalf("footer unsubscribe missing: %s", body)
	}
	if !strings.Contains(msg.TextBody, "QA & Co\n1 Main St\n<Springfield>\n") || !strings.Contains(msg.TextBody, "Unsubscribe: "+unsub) {
		t.Fatalf("text footer: %q", msg.TextBody)
	}

	// The unsubscribe token links contact, org and recipient.
	header := msg.Headers["List-Unsubscribe"]
	if !strings.HasPrefix(header, "<https://api.test/api/v1/unsubscribe/") || strings.Contains(header, "mailto:") {
		t.Fatalf("List-Unsubscribe=%q", header)
	}
	data, err := (&ComplianceService{cfg: cfgWithSecret()}).decodeUnsubscribeData(strings.TrimSuffix(strings.TrimPrefix(header, "<https://api.test/api/v1/unsubscribe/"), ">"))
	if err != nil || data.ContactID != 4 || data.OrgID != 1 || data.RecipientID != 9 || data.Test {
		t.Fatalf("unsubscribe token %+v %v", data, err)
	}

	// Toggles off: no pixel, no rewriting; footer still present.
	snap.TrackOpens, snap.TrackClicks = false, false
	msg, _ = mustRender(t, snap, rcpt, opts)
	if strings.Contains(msg.HTMLBody, "/tracking/") || !strings.Contains(msg.HTMLBody, `href="https://shop.test/p?a=1&amp;b=2"`) || !strings.Contains(msg.HTMLBody, unsub) {
		t.Fatalf("toggles off: %s", msg.HTMLBody)
	}
	// Opens only.
	snap.TrackOpens = true
	if msg, _ = mustRender(t, snap, rcpt, opts); strings.Contains(msg.HTMLBody, "/tracking/click/") || !strings.Contains(msg.HTMLBody, "/tracking/open/") {
		t.Fatalf("opens only: %s", msg.HTMLBody)
	}
	// Text-only campaign: no pixel, text footer still added.
	snap.HTMLContent = ""
	if msg, _ = mustRender(t, snap, rcpt, opts); msg.HTMLBody != "" || !strings.Contains(msg.TextBody, "Unsubscribe: "+unsub) {
		t.Fatalf("text only: %+v", msg)
	}
	// No <body>: the footer and pixel are appended.
	snap.HTMLContent = "<p>x</p>"
	if msg, _ = mustRender(t, snap, rcpt, opts); !strings.HasPrefix(msg.HTMLBody, "<p>x</p><div") || !strings.HasSuffix(msg.HTMLBody, `style="display:none">`) {
		t.Fatalf("append: %s", msg.HTMLBody)
	}
}

func cfgWithSecret() *config.Config { return &config.Config{JWTSecret: trackingSecret} }

func TestRenderTestAndPreviewModes(t *testing.T) {
	snap, rcpt, opts := renderFixture()
	rcpt.ID, rcpt.MessageUUID = 0, ""
	opts.Mode = renderTest
	msg, _ := mustRender(t, snap, rcpt, opts)
	if strings.Contains(msg.HTMLBody, "/tracking/") || !strings.HasPrefix(msg.Subject, "[Test] Hi Ann") {
		t.Fatalf("test mode: %s / %s", msg.Subject, msg.HTMLBody)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(msg.Headers["List-Unsubscribe"], "<https://api.test/api/v1/unsubscribe/"), ">")
	data, err := (&ComplianceService{cfg: cfgWithSecret()}).decodeUnsubscribeData(token)
	if err != nil || !data.Test || data.ContactID != 0 || data.RecipientID != 0 {
		t.Fatalf("test token %+v %v", data, err)
	}
	if _, ok := msg.Headers["X-Mailat-Message-ID"]; ok {
		t.Fatal("test send carries a message uuid")
	}

	opts.Mode, opts.Secret = renderPreview, ""
	snap.PostalAddress = ""
	msg, _ = mustRender(t, snap, rcpt, opts)
	if strings.Contains(msg.HTMLBody, "/tracking/") || !strings.Contains(msg.HTMLBody, `<a href="#"`) || len(msg.Headers) != 0 {
		t.Fatalf("preview: %+v", msg)
	}
}

func TestRenderRejectsMissingPrerequisites(t *testing.T) {
	snap, rcpt, opts := renderFixture()
	var mv *provider.MailValidationError
	snap.PostalAddress = "  "
	if _, _, err := renderCampaignMessage(snap, rcpt, opts); !errors.As(err, &mv) {
		t.Fatalf("missing postal address: %v", err)
	}
	snap, rcpt, opts = renderFixture()
	snap.HTMLContent, snap.TextContent = "", ""
	if _, _, err := renderCampaignMessage(snap, rcpt, opts); !errors.As(err, &mv) {
		t.Fatalf("empty content: %v", err)
	}
	snap, rcpt, opts = renderFixture()
	opts.Secret = ""
	if _, _, err := renderCampaignMessage(snap, rcpt, opts); err == nil {
		t.Fatal("missing secret accepted")
	}
}

func TestRenderedMIMEHeaders(t *testing.T) {
	snap, rcpt, opts := renderFixture()
	snap.ReplyTo = "reply@qa.test"
	msg, _ := mustRender(t, snap, rcpt, opts)
	raw, recipients, err := provider.BuildMailMIME(msg)
	if err != nil {
		t.Fatal(err)
	}
	mime := string(raw)
	head := mime[:strings.Index(mime, "\r\n\r\n")]
	for _, want := range []string{
		"From: \"News Team\" <news@qa.test>", "To: <reader@example.test>", "Reply-To: <reply@qa.test>",
		"List-Unsubscribe: <https://api.test/api/v1/unsubscribe/", "List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"X-Mailat-Message-ID: 6f1c7b0e-5d2a-4a43-9a51-1d6c3f7b8e01",
	} {
		if !strings.Contains(head, want) {
			t.Fatalf("missing %q in headers:\n%s", want, head)
		}
	}
	if strings.Contains(head, "Bcc") || strings.Contains(head, "mailto:") || len(recipients) != 1 || recipients[0] != "reader@example.test" {
		t.Fatalf("headers:\n%s\nrecipients=%v", head, recipients)
	}
	if _, err := url.Parse(strings.TrimSuffix(strings.SplitN(strings.SplitN(head, "List-Unsubscribe: <", 2)[1], ">", 2)[0], ">")); err != nil {
		t.Fatal(err)
	}
}
