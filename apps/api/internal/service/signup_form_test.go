package service

import (
	"context"
	"database/sql"
	"errors"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"strings"
	"sync"
	"testing"
	"time"
)

type signupCapture struct {
	messages []model.SendEmailRequest
	fail     bool
}

func (c *signupCapture) SendEmailForUser(_ context.Context, org, user int64, r *model.SendEmailRequest) (*model.SendEmailResponse, error) {
	if org != 1 || user != 1 {
		return nil, errors.New("wrong sender")
	}
	if c.fail {
		return nil, errors.New("unavailable")
	}
	c.messages = append(c.messages, *r)
	return &model.SendEmailResponse{Status: "queued"}, nil
}
func signupFixture(t *testing.T) (*sql.DB, *SignupFormService, *ListService, *model.List, *model.SignupForm, *signupCapture) {
	t.Helper()
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	db.Exec(`UPDATE domains SET ses_verified=true`)
	cfg := &config.Config{JWTSecret: "signup-test-secret", WebUrl: "https://mail.example.test", EmailProvider: "ses"}
	cap := &signupCapture{}
	s := NewSignupFormService(db, cfg, cap)
	ls := NewListService(db, cfg)
	list, e := ls.CreateList(context.Background(), 1, &model.CreateListRequest{Name: "Newsletter"})
	if e != nil {
		t.Fatal(e)
	}
	var identity string
	db.QueryRow(`SELECT uuid FROM identities WHERE id=1`).Scan(&identity)
	f, e := s.Save(context.Background(), 1, 1, "", &model.SaveSignupFormRequest{ListID: list.UUID, IdentityID: identity, Name: "Signup", Title: "Welcome aboard", ConsentText: "I want two emails a month from Example.", ButtonText: "Join us", CollectName: true, Published: true})
	if e != nil {
		t.Fatal(e)
	}
	return db, s, ls, list, f, cap
}
func submitSignup(t *testing.T, s *SignupFormService, f *model.SignupForm, email string) error {
	t.Helper()
	p, e := s.Public(context.Background(), f.UUID)
	if e != nil {
		return e
	}
	_, e = s.Submit(context.Background(), f.UUID, "192.0.2.1", &model.SubmitSignupRequest{Email: email, FirstName: "New", Consent: true, Challenge: p.Challenge})
	return e
}
func signupToken(t *testing.T, c *signupCapture) string {
	t.Helper()
	if len(c.messages) == 0 {
		t.Fatal("no confirmation")
	}
	parts := strings.Split(c.messages[len(c.messages)-1].Text, "/subscribe/confirm#")
	if len(parts) != 2 {
		t.Fatal("missing fragment link")
	}
	return strings.Split(parts[1], "\n")[0]
}
func signupCount(t *testing.T, db *sql.DB, q string, want int, args ...any) {
	t.Helper()
	var n int
	if e := db.QueryRow(q, args...).Scan(&n); e != nil || n != want {
		t.Fatalf("count=%d want=%d err=%v query=%s", n, want, e, q)
	}
}
func TestSignupSingleDoubleAndSuppression(t *testing.T) {
	db, s, ls, list, f, cap := signupFixture(t)
	ctx := context.Background()
	if list.ConfirmationMode != "single" {
		t.Fatal("default")
	}
	for _, email := range []string{"Hello@reader.test", "hello@reader.test"} {
		if e := submitSignup(t, s, f, email); e != nil {
			t.Fatal(e)
		}
	}
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 1)
	signupCount(t, db, `SELECT count(*) FROM consent_audit WHERE source='signup_form'`, 1)
	signupCount(t, db, `SELECT count(*) FROM webhook_events WHERE event_type='contact.subscribed'`, 1)
	var id string
	db.QueryRow(`SELECT uuid FROM contacts LIMIT 1`).Scan(&id)
	audit, e := NewComplianceService(db, s.cfg).GetConsentAuditTrail(ctx, 1, id)
	if e != nil || len(audit) != 1 {
		t.Fatalf("audit: %v %v", audit, e)
	}
	if len(cap.messages) != 0 {
		t.Fatal("single sent mail")
	}
	if _, e = ls.UpdateList(ctx, 1, list.UUID, &model.UpdateListRequest{ConfirmationMode: "double"}); e != nil {
		t.Fatal(e)
	}
	if e = submitSignup(t, s, f, "double@reader.test"); e != nil {
		t.Fatal(e)
	}
	signupCount(t, db, `SELECT count(*) FROM contacts WHERE email='double@reader.test'`, 0)
	token := signupToken(t, cap)
	signupCount(t, db, `SELECT count(*) FROM signup_requests WHERE token_hash=$1`, 0, token)
	if _, e = s.Confirm(ctx, token, "192.0.2.1"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Confirm(ctx, token, "192.0.2.1"); e == nil {
		t.Fatal("replay")
	}
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 2)
	db.Exec(`UPDATE contacts SET status='unsubscribed' WHERE email='double@reader.test'`)
	before := len(cap.messages)
	if e = submitSignup(t, s, f, "double@reader.test"); e != nil {
		t.Fatal(e)
	}
	db.Exec(`INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'blocked@reader.test','complaint','test')`)
	if e = submitSignup(t, s, f, "blocked@reader.test"); e != nil {
		t.Fatal(e)
	}
	if len(cap.messages) != before {
		t.Fatal("blocked sent")
	}
}
func TestSignupIsolationPolicyAndValidation(t *testing.T) {
	db, s, ls, list, f, _ := signupFixture(t)
	ctx := context.Background()
	for _, a := range [][2]int64{{2, 3}, {1, 2}} {
		if _, e := s.Get(ctx, a[0], a[1], f.UUID); e == nil {
			t.Fatal("foreign form")
		}
	}
	if _, e := ls.UpdateList(ctx, 1, list.UUID, &model.UpdateListRequest{ConfirmationMode: "bad"}); e == nil {
		t.Fatal("invalid policy")
	}
	p, _ := s.Public(ctx, f.UUID)
	for _, req := range []*model.SubmitSignupRequest{{Email: "person@reader.test", Challenge: p.Challenge}, {Email: "Bad <person@reader.test>", Consent: true, Challenge: p.Challenge}} {
		if _, e := s.Submit(ctx, f.UUID, "192.0.2.1", req); e == nil {
			t.Fatal("invalid submission")
		}
	}
	if _, e := s.Submit(ctx, f.UUID, "192.0.2.1", &model.SubmitSignupRequest{Email: "bot@reader.test", Consent: true, Challenge: p.Challenge, Website: "spam"}); e != nil {
		t.Fatal(e)
	}
	signupCount(t, db, `SELECT count(*) FROM signup_requests`, 0)
	db.Exec(`UPDATE signup_forms SET version=version+1,identity_id=NULL WHERE id=$1`, f.ID)
	if _, e := s.Submit(ctx, f.UUID, "192.0.2.1", &model.SubmitSignupRequest{Email: "person@reader.test", Consent: true, Challenge: p.Challenge}); e == nil {
		t.Fatal("stale disclosure")
	}
	if _, e := ls.UpdateList(ctx, 1, list.UUID, &model.UpdateListRequest{ConfirmationMode: "double"}); e == nil {
		t.Fatal("missing sender")
	}
	db.Exec(`UPDATE signup_forms SET published=false WHERE id=$1`, f.ID)
	if _, e := s.Public(ctx, f.UUID); e == nil {
		t.Fatal("draft exposed")
	}
	if _, e := ls.UpdateList(ctx, 1, list.UUID, &model.UpdateListRequest{ConfirmationMode: "double"}); e != nil {
		t.Fatal(e)
	}
	var foreign string
	db.QueryRow(`SELECT uuid FROM identities WHERE id=2`).Scan(&foreign)
	if _, e := s.Save(ctx, 1, 1, "", &model.SaveSignupFormRequest{ListID: list.UUID, IdentityID: foreign, Name: "Attempt", Title: "Hi", ConsentText: "A real consent statement", ButtonText: "Join", Published: true}); e == nil {
		t.Fatal("foreign identity")
	}
}
func TestSignupFailureExpiryConcurrencyAndRate(t *testing.T) {
	db, s, ls, list, f, cap := signupFixture(t)
	ctx := context.Background()
	if _, e := ls.UpdateList(ctx, 1, list.UUID, &model.UpdateListRequest{ConfirmationMode: "double"}); e != nil {
		t.Fatal(e)
	}
	cap.fail = true
	if e := submitSignup(t, s, f, "retry@reader.test"); e == nil {
		t.Fatal("failure hidden")
	}
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 0)
	cap.fail = false
	if e := submitSignup(t, s, f, "retry@reader.test"); e != nil {
		t.Fatal(e)
	}
	token := signupToken(t, cap)
	db.Exec(`UPDATE signup_requests SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, signupDigest(token))
	if _, e := s.Confirm(ctx, token, "192.0.2.1"); e == nil {
		t.Fatal("expired token")
	}
	if e := submitSignup(t, s, f, "later@reader.test"); e != nil {
		t.Fatal(e)
	}
	token = signupToken(t, cap)
	db.Exec(`INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'later@reader.test','complaint','test')`)
	if _, e := s.Confirm(ctx, token, "192.0.2.1"); e != nil {
		t.Fatal(e)
	}
	signupCount(t, db, `SELECT count(*) FROM contacts WHERE email='later@reader.test'`, 0)
	if _, e := ls.UpdateList(ctx, 1, list.UUID, &model.UpdateListRequest{ConfirmationMode: "single"}); e != nil {
		t.Fatal(e)
	}
	p, _ := s.Public(ctx, f.UUID)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Submit(ctx, f.UUID, "192.0.2.2", &model.SubmitSignupRequest{Email: "parallel@reader.test", Consent: true, Challenge: p.Challenge})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	signupCount(t, db, `SELECT count(*) FROM contacts WHERE email='parallel@reader.test'`, 1)
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 1)
	for i := 0; i < 3; i++ {
		if e := s.rate(ctx, "testlimit", 3); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.rate(ctx, "testlimit", 3); e == nil {
		t.Fatal("unlimited")
	}
	db.Exec(`UPDATE signup_rate_limits SET window_start=now()-interval '2 hours'`)
	if e := s.rate(ctx, "testlimit", 3); e != nil {
		t.Fatal(e)
	}
}
func TestSignupChallenge(t *testing.T) {
	s := NewSignupFormService(nil, &config.Config{JWTSecret: "test"}, nil)
	f := &model.SignupForm{UUID: "fixture", Version: 2, ConfirmationMode: "single"}
	token := s.challenge(f, time.Now())
	if !s.validChallenge(f, token) {
		t.Fatal("fresh invalid")
	}
	f.Version++
	if s.validChallenge(f, token) {
		t.Fatal("stale valid")
	}
	f.Version--
	if s.validChallenge(f, s.challenge(f, time.Now().Add(-2*time.Hour))) || s.validChallenge(f, token+"x") {
		t.Fatal("bad challenge accepted")
	}
}

func TestSignupUnpublishExistingContactsAndErasure(t *testing.T) {
	db, s, ls, list, f, cap := signupFixture(t)
	ctx := context.Background()
	if err := submitSignup(t, s, f, "existing@reader.test"); err != nil {
		t.Fatal(err)
	}
	var contactUUID string
	db.QueryRow(`SELECT uuid FROM contacts WHERE email='existing@reader.test'`).Scan(&contactUUID)
	second, err := ls.CreateList(ctx, 1, &model.CreateListRequest{Name: "Second list", ConfirmationMode: "double"})
	if err != nil {
		t.Fatal(err)
	}
	otherForm, err := s.Save(ctx, 1, 1, "", &model.SaveSignupFormRequest{ListID: second.UUID, IdentityID: f.IdentityID, Name: "Second invitation", Title: "Join the second list", ConsentText: f.ConsentText, ButtonText: "Join", Published: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = submitSignup(t, s, otherForm, "existing@reader.test"); err != nil {
		t.Fatal(err)
	}
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 1)
	token := signupToken(t, cap)
	db.Exec(`UPDATE signup_forms SET published=false WHERE id=$1`, otherForm.ID)
	if _, err = s.Confirm(ctx, token, "192.0.2.1"); err == nil {
		t.Fatal("unpublished confirmation accepted")
	}
	db.Exec(`UPDATE signup_forms SET published=true WHERE id=$1`, otherForm.ID)
	if _, err = ls.UpdateList(ctx, 1, second.UUID, &model.UpdateListRequest{ConfirmationMode: "single"}); err != nil {
		t.Fatal(err)
	}
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 1)
	if _, err = s.Confirm(ctx, token, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	signupCount(t, db, `SELECT count(*) FROM signup_requests WHERE confirmation_mode='double' AND status='subscribed'`, 1)
	signupCount(t, db, `SELECT count(*) FROM list_contacts`, 2)
	compliance := NewComplianceService(db, s.cfg)
	export, err := compliance.ExportContactData(ctx, 1, contactUUID)
	if err != nil || export["signupHistory"] == nil {
		t.Fatal("signup export missing", err)
	}
	var contactID int64
	db.QueryRow(`SELECT id FROM contacts WHERE uuid=$1`, contactUUID).Scan(&contactID)
	unsub := compliance.encodeUnsubscribeData(UnsubscribeData{ContactID: contactID, OrgID: 1})
	if err = compliance.ProcessOneClickUnsubscribe(ctx, unsub, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(ctx, token, "192.0.2.1"); err == nil {
		t.Fatal("replayed after unsubscribe")
	}
	signupCount(t, db, `SELECT count(*) FROM contacts WHERE status='active'`, 0)
	if err = compliance.DeleteContactData(ctx, 1, contactUUID); err != nil {
		t.Fatal(err)
	}
	signupCount(t, db, `SELECT count(*) FROM signup_requests`, 0)
	if err = submitSignup(t, s, f, "existing@reader.test"); err != nil {
		t.Fatal(err)
	}
	signupCount(t, db, `SELECT count(*) FROM contacts`, 0)
	if _, err = ls.GetList(ctx, 1, list.UUID); err != nil {
		t.Fatal(err)
	}
}
