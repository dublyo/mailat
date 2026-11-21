package service

import (
	"net/mail"
	"reflect"
	"strings"
	"testing"
)

func guardHeader(extra ...string) mail.Header {
	h := mail.Header{"From": {"Sender <sender@example.org>"}, "To": {"Owner <owner@own.test>"}}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, ":")
		key := strings.TrimSpace(k)
		h[key] = append(h[key], strings.TrimSpace(v))
	}
	return h
}

type guardCase struct {
	name             string
	h                mail.Header
	envelope, sender string
	own, rcpts       []string
	v                func(*ArrivalVerdicts)
	reason           string
}

func TestAutoReplyEligible(t *testing.T) {
	pass := ArrivalVerdicts{Spam: "PASS", Virus: "PASS", SPF: "PASS", DKIM: "PASS", DMARC: "PASS", Folder: "inbox"}
	rcpts := []string{"owner@own.test"}
	own := []string{"owner@own.test"}
	tests := []guardCase{
		{name: "eligible", reason: ""},
		{name: "auto-submitted no is fine", h: guardHeader("Auto-Submitted: No"), reason: ""},
		{name: "auto-submitted auto-replied", h: guardHeader("Auto-Submitted: auto-replied"), reason: "auto-submitted"},
		{name: "auto-submitted auto-generated", h: guardHeader("Auto-Submitted: auto-generated; owner-email=x"), reason: "auto-submitted"},
		{name: "precedence bulk", h: guardHeader("Precedence: bulk"), reason: "precedence"},
		{name: "precedence list", h: guardHeader("Precedence: List"), reason: "precedence"},
		{name: "precedence junk", h: guardHeader("Precedence: junk"), reason: "precedence"},
		{name: "precedence first-class", h: guardHeader("Precedence: first-class"), reason: ""},
		{name: "list-id", h: guardHeader("List-Id: <news.example.org>"), reason: "list-mail"},
		{name: "list-unsubscribe", h: guardHeader("List-Unsubscribe: <mailto:u@example.org>"), reason: "list-mail"},
		{name: "list-post", h: guardHeader("List-Post: <mailto:l@example.org>"), reason: "list-mail"},
		{name: "x-auto-response-suppress oof", h: guardHeader("X-Auto-Response-Suppress: DR, OOF"), reason: "auto-response-suppressed"},
		{name: "x-auto-response-suppress all", h: guardHeader("X-Auto-Response-Suppress: All"), reason: "auto-response-suppressed"},
		{name: "x-auto-response-suppress autoreply", h: guardHeader("X-Auto-Response-Suppress: AutoReply"), reason: "auto-response-suppressed"},
		{name: "x-auto-response-suppress rn only", h: guardHeader("X-Auto-Response-Suppress: RN, NRN"), reason: ""},
		{name: "mailat loop", h: guardHeader("X-Mailat-Loop: abc"), reason: "mail-loop"},
		{name: "null envelope", envelope: "<>", reason: "null-sender"},
		{name: "empty envelope", envelope: " ", reason: "null-sender"},
		{name: "own address", own: []string{"owner@own.test", "SENDER@example.org"}, reason: "own-address"},
		{name: "spam", v: func(v *ArrivalVerdicts) { v.Spam = "FAIL" }, reason: "spam"},
		{name: "virus", v: func(v *ArrivalVerdicts) { v.Virus = "FAIL" }, reason: "spam"},
		{name: "dmarc report", v: func(v *ArrivalVerdicts) { v.DMARCReport = true }, reason: "dmarc-report"},
		{name: "spam folder", v: func(v *ArrivalVerdicts) { v.Folder = "spam" }, reason: "folder"},
		{name: "trash folder", v: func(v *ArrivalVerdicts) { v.Folder = "trash" }, reason: "folder"},
		{name: "archive folder replies", v: func(v *ArrivalVerdicts) { v.Folder = "archive" }, reason: ""},
		{name: "dmarc fail", v: func(v *ArrivalVerdicts) { v.DMARC = "FAIL" }, reason: "unauthenticated"},
		{name: "spf and dkim fail", v: func(v *ArrivalVerdicts) { v.SPF, v.DKIM = "FAIL", "FAIL" }, reason: "unauthenticated"},
		{name: "spf fail only", v: func(v *ArrivalVerdicts) { v.SPF = "FAIL" }, reason: ""},
		{name: "no dmarc policy, foreign envelope", v: func(v *ArrivalVerdicts) { v.DMARC = "GRAY" }, reason: "unauthenticated"},
		{name: "dmarc processing failed", v: func(v *ArrivalVerdicts) { v.DMARC = "PROCESSING_FAILED" }, reason: "unauthenticated"},
		{name: "dmarc verdict missing", v: func(v *ArrivalVerdicts) { v.DMARC = "" }, reason: "unauthenticated"},
		{name: "no dmarc policy, same-domain spf pass", envelope: "bounce-1@EXAMPLE.org", v: func(v *ArrivalVerdicts) { v.DMARC = "GRAY" }, reason: ""},
		{name: "no dmarc policy, same-domain spf softfail", envelope: "bounce-1@example.org", v: func(v *ArrivalVerdicts) { v.DMARC, v.SPF = "GRAY", "GRAY" }, reason: "unauthenticated"},
		{name: "dmarc fail despite same-domain spf pass", envelope: "bounce-1@example.org", v: func(v *ArrivalVerdicts) { v.DMARC = "FAIL" }, reason: "unauthenticated"},
		{name: "no dmarc policy, envelope without domain", envelope: "example.org", v: func(v *ArrivalVerdicts) { v.DMARC = "GRAY" }, reason: "unauthenticated"},
		{name: "no dmarc policy, envelope subdomain", envelope: "b@mail.example.org", v: func(v *ArrivalVerdicts) { v.DMARC = "GRAY" }, reason: "unauthenticated"},
		{name: "dkim fail only", v: func(v *ArrivalVerdicts) { v.DKIM = "FAIL" }, reason: ""},
		{name: "bcc", h: mail.Header{"From": {"sender@example.org"}, "To": {"someone@else.test"}}, reason: "not-addressed"},
		{name: "cc counts", h: mail.Header{"From": {"sender@example.org"}, "To": {"x@else.test"}, "Cc": {"a@else.test, OWNER@own.test"}}, reason: ""},
		{name: "catch-all recipient in to", h: mail.Header{"From": {"sender@example.org"}, "To": {"sales@own.test"}}, rcpts: []string{"owner@own.test", "sales@own.test"}, reason: ""},
		{name: "no sender", sender: "not-an-address", reason: "no-sender"},
		// From spoofing: the reply must go to the one mailbox DMARC covered.
		{name: "display name hides address", h: mail.Header{"From": {`"<victim@third.example>" <sender@example.org>`}, "To": {"owner@own.test"}}, sender: "victim@third.example", reason: "ambiguous-from"},
		{name: "display name address is not the sender", h: mail.Header{"From": {`"<victim@third.example>" <sender@example.org>`}, "To": {"owner@own.test"}}, reason: ""},
		{name: "two from addresses", h: mail.Header{"From": {"victim@third.example, sender@example.org"}, "To": {"owner@own.test"}}, reason: "ambiguous-from"},
		{name: "two from headers", h: mail.Header{"From": {"sender@example.org", "victim@third.example"}, "To": {"owner@own.test"}}, reason: "ambiguous-from"},
		{name: "unparseable from", h: mail.Header{"From": {"<victim@third.example> <sender@example.org>"}, "To": {"owner@own.test"}}, reason: "ambiguous-from"},
		{name: "from differs from sender", h: mail.Header{"From": {"victim@third.example"}, "To": {"owner@own.test"}}, reason: "ambiguous-from"},
		{name: "encoded display name", h: mail.Header{"From": {"=?utf-8?q?J=C3=BCrgen?= <sender@example.org>"}, "To": {"owner@own.test"}}, reason: ""},
		{name: "provider from agrees", v: func(v *ArrivalVerdicts) { v.ProviderFrom = []string{`"Sender" <SENDER@example.org>`} }, reason: ""},
		{name: "provider from differs", v: func(v *ArrivalVerdicts) { v.ProviderFrom = []string{"victim@third.example"} }, reason: "ambiguous-from"},
		{name: "provider saw two from addresses", v: func(v *ArrivalVerdicts) { v.ProviderFrom = []string{"sender@example.org", "victim@third.example"} }, reason: "ambiguous-from"},
	}
	for _, local := range []string{"mailer-daemon", "MAILER-DAEMON", "postmaster", "noreply", "no-reply", "no_reply", "noreply-alerts", "donotreply", "do-not-reply", "bounce", "bounces+abc", "bounce-123", "news-request", "owner-list", "listserv", "majordomo"} {
		tests = append(tests, guardCase{name: "system sender " + local, sender: local + "@example.org", reason: "system-sender"})
	}
	for _, local := range []string{"owner", "replyguy", "requests", "mailer"} {
		tests = append(tests, guardCase{name: "person " + local, h: mail.Header{"From": {local + "@example.org"}, "To": {"owner@own.test"}}, sender: local + "@example.org", reason: ""})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.h
			if h == nil {
				h = guardHeader()
			}
			envelope := tt.envelope
			if envelope == "" {
				envelope = "bounce-123@mail.example.org"
			}
			sender := tt.sender
			if sender == "" {
				sender = "sender@example.org"
			}
			ownAddrs, r := own, rcpts
			if tt.own != nil {
				ownAddrs = tt.own
			}
			if tt.rcpts != nil {
				r = tt.rcpts
			}
			v := pass
			if tt.v != nil {
				tt.v(&v)
			}
			ok, reason := AutoReplyEligible(h, envelope, sender, ownAddrs, r, v)
			if ok != (tt.reason == "") || reason != tt.reason {
				t.Fatalf("got (%v, %q), want reason %q", ok, reason, tt.reason)
			}
		})
	}
}

func TestLoopTokensAndForwardLoop(t *testing.T) {
	h := mail.Header{"X-Mailat-Loop": {"AAA, bbb", " ,ccc "}}
	if got := LoopTokens(h); !reflect.DeepEqual(got, []string{"aaa", "bbb", "ccc"}) {
		t.Fatal(got)
	}
	if !ForwardLoopBlocked(h, "zzz") {
		t.Fatal("three hops must block")
	}
	two := mail.Header{"X-Mailat-Loop": {"aaa", "bbb"}}
	if !ForwardLoopBlocked(two, "BBB") || ForwardLoopBlocked(two, "ccc") {
		t.Fatal("own tag must block and a new tag pass")
	}
	if ForwardLoopBlocked(mail.Header{}, "aaa") || LoopTokens(mail.Header{}) != nil {
		t.Fatal("no header means no loop")
	}
}

func TestExcludedSenderAndSubject(t *testing.T) {
	patterns := []string{"@partner.test", "*@*.corp.test", " ", "boss@home.test"}
	for sender, want := range map[string]bool{
		"a@partner.test": true, "x@eu.corp.test": true, "x@corp.test": false, "BOSS@home.test": true, "friend@home.test": false,
	} {
		if got := excludedSender(patterns, sender); got != want {
			t.Errorf("%s: got %v", sender, got)
		}
	}
	for _, tc := range []struct{ rule, original, want string }{
		{"Out of office", "Hello", "Out of office"},
		{"", "Hello", "Re: Hello"},
		{"", "RE: Hello", "RE: Hello"},
		{"", "", "Re: (no subject)"},
		{"", "a\r\nBcc: x", "Re: a Bcc: x"},
	} {
		if got := autoReplySubject(tc.rule, tc.original); got != tc.want {
			t.Errorf("%q/%q: got %q", tc.rule, tc.original, got)
		}
	}
}

func TestParseAddressHeader(t *testing.T) {
	for _, tt := range []struct {
		name          string
		values        []string
		address, disp string
		strict        bool
	}{
		{name: "missing"},
		{name: "plain", values: []string{"Sender <sender@example.org>"}, address: "sender@example.org", disp: "Sender", strict: true},
		{name: "bare", values: []string{"sender@example.org"}, address: "sender@example.org", strict: true},
		{name: "quoted angle address in name", values: []string{`"<victim@third.example>" <attacker@evil.example>`}, address: "attacker@evil.example", disp: "<victim@third.example>", strict: true},
		{name: "quoted comma in name", values: []string{`"Acme, Inc." <a@acme.test>`}, address: "a@acme.test", disp: "Acme, Inc.", strict: true},
		{name: "encoded name", values: []string{"=?iso-8859-1?q?J=FCrgen?= <j@example.org>"}, address: "j@example.org", disp: "Jürgen", strict: true},
		{name: "list takes first, not strict", values: []string{"a@example.org, b@example.org"}, address: "a@example.org"},
		{name: "repeated header not strict", values: []string{"a@example.org", "b@example.org"}, address: "a@example.org"},
		{name: "two angle addresses rejected", values: []string{"<victim@third.example> <attacker@evil.example>"}},
		{name: "fallback single angle address", values: []string{"victim@third.example <attacker@evil.example>"}, address: "attacker@evil.example", disp: "victim@third.example"},
		{name: "unclosed angle rejected", values: []string{"Foo <a@example.org"}},
		{name: "garbage", values: []string{"not an address"}},
		{name: "empty group", values: []string{"undisclosed-recipients:;"}},
		// net/mail leaves encoded words in a quoted display name undecoded.
		{name: "quoted B-encoded name", values: []string{`"=?UTF-8?B?w6nDqQ==?=" <a@example.com>`}, address: "a@example.com", disp: "éé", strict: true},
		{name: "quoted Q-encoded name", values: []string{`"=?utf-8?q?=C3=A9t=C3=A9?=" <a@example.com>`}, address: "a@example.com", disp: "été", strict: true},
		{name: "quoted Arabic name", values: []string{`"=?UTF-8?B?2YXYrdmF2K8g2LnZhNmK?=" <m@example.com>`}, address: "m@example.com", disp: "محمد علي", strict: true},
		{name: "quoted name with only literal markers", values: []string{`"Price =? 5 ?=" <a@example.com>`}, address: "a@example.com", disp: "Price =? 5 ?=", strict: true},
		// Addresses net/mail rejects that real mobile carriers send: kept, never strict.
		{name: "lenient bare dotted local part", values: []string{"foo..bar@docomo.ne.jp"}, address: "foo..bar@docomo.ne.jp"},
		{name: "lenient angle dotted local part", values: []string{"<foo..bar@docomo.ne.jp>"}, address: "foo..bar@docomo.ne.jp"},
		{name: "lenient named trailing dot", values: []string{"Taro <foo.@ezweb.ne.jp>"}, address: "foo.@ezweb.ne.jp", disp: "Taro"},
		{name: "lenient angle trimmed", values: []string{"Taro < foo.@ezweb.ne.jp >"}, address: "foo.@ezweb.ne.jp", disp: "Taro"},
		{name: "bare token without at", values: []string{"MAILER-DAEMON"}},
		{name: "bare token two ats", values: []string{"a@b@example.org"}},
		{name: "bare token empty domain", values: []string{"foo..bar@"}},
		{name: "angle with whitespace inside", values: []string{"Taro <foo. bar@ezweb.ne.jp>"}},
		{name: "angle with two ats", values: []string{"Taro <a@b@ezweb.ne.jp>"}},
		{name: "angle without at", values: []string{"Mail Delivery <MAILER-DAEMON>"}},
		{name: "bare token with comment", values: []string{"foo..bar@docomo.ne.jp(Taro)"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			address, disp, strict := parseAddressHeader(tt.values)
			if address != tt.address || disp != tt.disp || strict != tt.strict {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", address, disp, strict, tt.address, tt.disp, tt.strict)
			}
		})
	}
}
