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
	}
	for _, local := range []string{"mailer-daemon", "MAILER-DAEMON", "postmaster", "noreply", "no-reply", "no_reply", "noreply-alerts", "donotreply", "do-not-reply", "bounce", "bounces+abc", "bounce-123", "news-request", "owner-list", "listserv", "majordomo"} {
		tests = append(tests, guardCase{name: "system sender " + local, sender: local + "@example.org", reason: "system-sender"})
	}
	for _, local := range []string{"owner", "replyguy", "requests", "mailer"} {
		tests = append(tests, guardCase{name: "person " + local, sender: local + "@example.org", reason: ""})
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
