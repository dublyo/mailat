package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
)

const trackingSecret = "tracking-test-secret-tracking-test-secret"

func TestTrackingTokenRoundTripAndTamper(t *testing.T) {
	open := OpenToken(trackingSecret, 7, 3, 1)
	d, err := decodeTrackingToken(trackingSecret, open)
	if err != nil || d.R != 7 || d.C != 3 || d.O != 1 || d.U != "" {
		t.Fatalf("open round trip: %+v %v", d, err)
	}
	click := ClickToken(trackingSecret, 7, 3, 1, 2, "https://example.test/a?b=1&c=2")
	d, err = decodeTrackingToken(trackingSecret, click)
	if err != nil || d.L != 2 || d.U != "https://example.test/a?b=1&c=2" {
		t.Fatalf("click round trip: %+v %v", d, err)
	}
	if strings.Contains(click, "@") || strings.ContainsAny(click, "+/=") {
		t.Fatalf("token is not url-safe: %s", click)
	}
	if _, err = decodeTrackingToken("another-secret", click); err == nil {
		t.Fatal("token verified with the wrong secret")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(click)
	raw[len(raw)-1] ^= 1
	if _, err = decodeTrackingToken(trackingSecret, base64.RawURLEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered MAC accepted")
	}
	// Changing the payload (another recipient) without re-signing fails.
	payload := strings.Replace(string(raw[:len(raw)-trackingMACLen]), `"r":7`, `"r":8`, 1)
	forged := base64.RawURLEncoding.EncodeToString(append([]byte(payload), raw[len(raw)-trackingMACLen:]...))
	if _, err = decodeTrackingToken(trackingSecret, forged); err == nil {
		t.Fatal("forged payload accepted")
	}
	// The pre-M3 format (JSON + 8-byte MAC keyed directly by JWTSecret) is rejected.
	old, _ := json.Marshal(map[string]any{"e": 1, "c": 3})
	mac := hmac.New(sha256.New, []byte(trackingSecret))
	mac.Write(old)
	legacy := base64.URLEncoding.EncodeToString(append(old, mac.Sum(nil)[:8]...))
	if _, err = decodeTrackingToken(trackingSecret, legacy); err == nil {
		t.Fatal("legacy 8-byte token accepted")
	}
	for _, bad := range []string{"", "!!", "a"} {
		if _, err = decodeTrackingToken(trackingSecret, bad); err == nil {
			t.Fatalf("garbage %q accepted", bad)
		}
	}
}

func TestTrackableURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.test/x":                      true,
		"HTTP://example.test":                         true,
		"javascript:alert(1)":                         false,
		"data:text/html,hi":                           false,
		"//example.test/x":                            false,
		"mailto:a@b.test":                             false,
		"https://":                                    false,
		"https://e.test/" + strings.Repeat("a", 2048): false,
		"https://e.test/\r\nx":                        false,
	} {
		if trackableURL(u) != want {
			t.Errorf("trackableURL(%q) != %v", u, want)
		}
	}
	svc := &TrackingService{cfg: &config.Config{JWTSecret: trackingSecret, WebUrl: "https://app.test"}}
	if got, err := svc.ProcessClickEvent(context.Background(), ClickToken(trackingSecret, 1, 1, 1, 0, "javascript:alert(1)"), "", ""); err == nil || got != "" {
		t.Fatalf("javascript target redirected: %q %v", got, err)
	}
	if svc.HomeURL() != "https://app.test" {
		t.Fatal("home url")
	}
}

type trackingFixture struct {
	db  *sql.DB
	svc *TrackingService
}

// Campaign 1 (org 1) has recipients 1 and 2; campaign 2 belongs to org 2.
func newTrackingFixture(t *testing.T) *trackingFixture {
	t.Helper()
	db := newAudienceFixture(t)
	addContact(t, db, 1, 1, "a@x.test", "active", 1)
	addContact(t, db, 2, 1, "b@x.test", "active", 1)
	mustExec(t, db, `INSERT INTO campaigns(id,org_id,name,subject,from_name,from_email,list_id,status,updated_at) VALUES(2,2,'C2','S','F','f@other.test',2,'sending',now());
		INSERT INTO campaign_recipients(id,campaign_id,org_id,contact_id,email,status) VALUES(1,1,1,1,'a@x.test','sent'),(2,1,1,2,'b@x.test','sent'),(3,2,2,NULL,'z@other.test','sent')`)
	return &trackingFixture{db: db, svc: NewTrackingService(db, &config.Config{JWTSecret: trackingSecret, WebUrl: "https://app.test"})}
}

func (f *trackingFixture) open(t *testing.T, r, c, o int64) {
	t.Helper()
	if err := f.svc.ProcessOpenEvent(context.Background(), OpenToken(trackingSecret, r, c, o), "198.51.100.7", strings.Repeat("u", 600)); err != nil {
		t.Fatal(err)
	}
}

func (f *trackingFixture) click(t *testing.T, r, c, o int64, target string) {
	t.Helper()
	got, err := f.svc.ProcessClickEvent(context.Background(), ClickToken(trackingSecret, r, c, o, 1, target), "198.51.100.7", "ua")
	if err != nil || got != target {
		t.Fatalf("click -> %q %v", got, err)
	}
}

func TestTrackingOpensAreUniquePerRecipient(t *testing.T) {
	f := newTrackingFixture(t)
	f.open(t, 1, 1, 1)
	f.open(t, 1, 1, 1)
	count(t, f.db, 1, `SELECT open_count FROM campaigns WHERE id=1`)
	count(t, f.db, 2, `SELECT open_count FROM campaign_recipients WHERE id=1 AND first_opened_at IS NOT NULL`)
	count(t, f.db, 2, `SELECT count(*) FROM campaign_events WHERE recipient_id=1 AND event_type='open' AND ip_address='198.51.100.7' AND length(user_agent)=512`)
	count(t, f.db, 2, `SELECT engagement_score FROM contacts WHERE id=1`)
	// The old .gif suffix is the controller's job; a click token is not an open.
	if err := f.svc.ProcessOpenEvent(context.Background(), ClickToken(trackingSecret, 1, 1, 1, 0, "https://x.test"), "", ""); err == nil {
		t.Fatal("click token accepted as open")
	}
}

func TestTrackingClickCountsAsFirstOpen(t *testing.T) {
	f := newTrackingFixture(t)
	f.click(t, 2, 1, 1, "https://example.test/offer")
	f.click(t, 2, 1, 1, "https://example.test/offer")
	count(t, f.db, 1, `SELECT click_count FROM campaigns WHERE id=1`)
	count(t, f.db, 1, `SELECT open_count FROM campaigns WHERE id=1`)
	count(t, f.db, 1, `SELECT count(*) FROM campaign_recipients WHERE id=2 AND click_count=2 AND open_count=1 AND first_clicked_at IS NOT NULL AND first_opened_at IS NOT NULL`)
	count(t, f.db, 2, `SELECT count(*) FROM campaign_events WHERE event_type='click' AND url='https://example.test/offer' AND link_index=1`)
	count(t, f.db, 1, `SELECT count(*) FROM campaign_events WHERE event_type='open'`)
	// A later real open does not count a second unique open.
	f.open(t, 2, 1, 1)
	count(t, f.db, 1, `SELECT open_count FROM campaigns WHERE id=1`)
}

func TestTrackingTogglesOff(t *testing.T) {
	f := newTrackingFixture(t)
	mustExec(t, f.db, `UPDATE campaigns SET track_opens=false, track_clicks=false WHERE id=1`)
	f.open(t, 1, 1, 1)
	f.click(t, 1, 1, 1, "https://example.test/still-redirects")
	count(t, f.db, 0, `SELECT count(*) FROM campaign_events`)
	count(t, f.db, 0, `SELECT open_count+click_count FROM campaigns WHERE id=1`)
	count(t, f.db, 0, `SELECT open_count+click_count FROM campaign_recipients WHERE id=1`)
	// Clicks tracked but opens off: no implied open.
	mustExec(t, f.db, `UPDATE campaigns SET track_clicks=true WHERE id=1`)
	f.click(t, 1, 1, 1, "https://example.test/x")
	count(t, f.db, 1, `SELECT click_count FROM campaigns WHERE id=1`)
	count(t, f.db, 0, `SELECT open_count FROM campaigns WHERE id=1`)
}

func TestTrackingIgnoresForeignTokens(t *testing.T) {
	f := newTrackingFixture(t)
	f.open(t, 3, 1, 1) // recipient of another org's campaign
	f.open(t, 1, 2, 1) // wrong campaign
	f.open(t, 1, 1, 2) // wrong org
	f.click(t, 3, 1, 1, "https://example.test/")
	count(t, f.db, 0, `SELECT count(*) FROM campaign_events`)
	count(t, f.db, 0, `SELECT COALESCE(sum(open_count+click_count),0) FROM campaigns`)
	count(t, f.db, 0, `SELECT COALESCE(sum(engagement_score),0) FROM contacts`)
}

func TestTrackingStoredEventsAreCapped(t *testing.T) {
	f := newTrackingFixture(t)
	for i := 0; i < maxStoredTrackingEvents+1; i++ {
		f.open(t, 1, 1, 1)
	}
	count(t, f.db, maxStoredTrackingEvents, `SELECT count(*) FROM campaign_events WHERE recipient_id=1`)
	count(t, f.db, maxStoredTrackingEvents+1, `SELECT open_count FROM campaign_recipients WHERE id=1`)
	count(t, f.db, 1, `SELECT open_count FROM campaigns WHERE id=1`)
	count(t, f.db, maxStoredTrackingEvents, `SELECT engagement_score FROM contacts WHERE id=1`)
}

// A signed template link is personalised at redirect time; the stored event
// keeps the template, so neither the token nor campaign_events holds the address.
func TestTrackingClickFillsTemplateLinks(t *testing.T) {
	f := newTrackingFixture(t)
	mustExec(t, f.db, `UPDATE contacts SET first_name='Ann' WHERE id=1; UPDATE contacts SET first_name='A B' WHERE id=2`)
	tmpl := "https://shop.test/?e={{ email }}&n={{firstName}}"
	click := func(r, c, o int64) string {
		t.Helper()
		got, err := f.svc.ProcessClickEvent(context.Background(), ClickToken(trackingSecret, r, c, o, 0, tmpl), "", "ua")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := click(1, 1, 1); got != "https://shop.test/?e=a@x.test&n=Ann" {
		t.Fatalf("personalised target %q", got)
	}
	// A value that breaks the URL is dropped rather than failing the redirect.
	if got := click(2, 1, 1); got != "https://shop.test/?e=&n=" {
		t.Fatalf("invalid value target %q", got)
	}
	// No contact (erased or deleted): variables render empty.
	if got := click(3, 2, 2); got != "https://shop.test/?e=&n=" {
		t.Fatalf("no contact target %q", got)
	}
	count(t, f.db, 3, `SELECT count(*) FROM campaign_events WHERE event_type='click' AND url=$1`, tmpl)
	count(t, f.db, 0, `SELECT count(*) FROM campaign_events WHERE url LIKE '%@%'`)
}
