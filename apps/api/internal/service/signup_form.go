package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/google/uuid"
)

type SignupError struct {
	Status  int
	Message string
}

func (e *SignupError) Error() string               { return e.Message }
func signupError(status int, message string) error { return &SignupError{status, message} }

type signupSender interface {
	SendEmailForUser(context.Context, int64, int64, *model.SendEmailRequest) (*model.SendEmailResponse, error)
}
type SignupFormService struct {
	db     *sql.DB
	cfg    *config.Config
	sender signupSender
}

func NewSignupFormService(db *sql.DB, cfg *config.Config, sender signupSender) *SignupFormService {
	return &SignupFormService{db, cfg, sender}
}

const formSelect = `SELECT f.id,f.uuid,f.org_id,f.list_id,l.uuid,l.name,f.created_by,COALESCE(i.uuid::text,''),COALESCE(i.email,''),f.name,f.title,f.description,f.consent_text,f.button_text,f.privacy_url,f.collect_name,f.published,f.version,l.confirmation_mode,f.created_at FROM signup_forms f JOIN lists l ON l.id=f.list_id AND l.org_id=f.org_id LEFT JOIN identities i ON i.id=f.identity_id `

type formScanner interface{ Scan(...any) error }

func scanSignupForm(row formScanner) (*model.SignupForm, error) {
	f := &model.SignupForm{}
	err := row.Scan(&f.ID, &f.UUID, &f.OrgID, &f.ListID, &f.ListUUID, &f.ListName, &f.CreatedBy, &f.IdentityID, &f.FromEmail, &f.Name, &f.Title, &f.Description, &f.ConsentText, &f.ButtonText, &f.PrivacyURL, &f.CollectName, &f.Published, &f.Version, &f.ConfirmationMode, &f.CreatedAt)
	return f, err
}
func (s *SignupFormService) Get(ctx context.Context, org, user int64, id string) (*model.SignupForm, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, signupError(404, "Form not found")
	}
	f, err := scanSignupForm(s.db.QueryRowContext(ctx, formSelect+`WHERE f.uuid=$1 AND f.org_id=$2 AND f.created_by=$3`, id, org, user))
	if err == sql.ErrNoRows {
		return nil, signupError(404, "Form not found")
	}
	return f, err
}
func (s *SignupFormService) List(ctx context.Context, org, user int64) ([]model.SignupForm, error) {
	rows, err := s.db.QueryContext(ctx, formSelect+`WHERE f.org_id=$1 AND f.created_by=$2 ORDER BY f.created_at DESC`, org, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	forms := []model.SignupForm{}
	for rows.Next() {
		f, err := scanSignupForm(rows)
		if err != nil {
			return nil, err
		}
		forms = append(forms, *f)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range forms {
		err = s.db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE status='subscribed'),count(*) FILTER(WHERE status='pending' AND expires_at>now()) FROM signup_requests WHERE form_id=$1`, forms[i].ID).Scan(&forms[i].Subscribed, &forms[i].Pending)
		if err != nil {
			return nil, err
		}
	}
	return forms, nil
}
func validateSignupForm(r *model.SaveSignupFormRequest) error {
	r.Name = strings.TrimSpace(r.Name)
	r.Title = strings.TrimSpace(r.Title)
	r.ConsentText = strings.TrimSpace(r.ConsentText)
	r.ButtonText = strings.TrimSpace(r.ButtonText)
	r.PrivacyURL = strings.TrimSpace(r.PrivacyURL)
	for _, field := range []struct {
		value, name string
		min, max    int
	}{{r.Name, "Name", 1, 100}, {r.Title, "Title", 1, 150}, {r.Description, "Introduction", 0, 1000}, {r.ConsentText, "Consent wording", 10, 1000}, {r.ButtonText, "Button label", 1, 60}} {
		if len(field.value) < field.min || len(field.value) > field.max || strings.ContainsRune(field.value, 0) {
			return signupError(400, fmt.Sprintf("%s must contain %d–%d bytes", field.name, field.min, field.max))
		}
	}
	if _, err := uuid.Parse(r.ListID); err != nil {
		return signupError(400, "Choose a contact list")
	}
	if r.IdentityID != "" {
		if _, err := uuid.Parse(r.IdentityID); err != nil {
			return signupError(400, "Choose a sending identity")
		}
	}
	if r.PrivacyURL != "" {
		u, e := url.Parse(r.PrivacyURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(r.PrivacyURL) > 2048 {
			return signupError(400, "Privacy policy must be a valid HTTPS URL")
		}
	}
	return nil
}
func (s *SignupFormService) readySender(ctx context.Context, q eventoutbox.DBTX, f *model.SignupForm) error {
	var ready bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities i JOIN domains d ON d.id=i.domain_id JOIN users u ON u.id=i.user_id WHERE i.uuid::text=$1 AND i.user_id=$2 AND i.kind='personal' AND u.org_id=$3 AND u.status='active' AND i.can_send AND d.org_id=$3 AND d.status='active' AND (NOT $4 OR COALESCE(d.ses_verified,false)))`, f.IdentityID, f.CreatedBy, f.OrgID, s.cfg.EmailProvider == "ses").Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return signupError(400, "Choose your verified sending identity for double opt-in")
	}
	return nil
}
func (s *SignupFormService) Save(ctx context.Context, org, user int64, id string, r *model.SaveSignupFormRequest) (*model.SignupForm, error) {
	if err := validateSignupForm(r); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var listID int
	var mode, kind string
	err = tx.QueryRowContext(ctx, `SELECT id,confirmation_mode,type FROM lists WHERE uuid=$1 AND org_id=$2 FOR UPDATE`, r.ListID, org).Scan(&listID, &mode, &kind)
	if err == sql.ErrNoRows {
		return nil, signupError(400, "Choose a list in your organization")
	}
	if err != nil {
		return nil, err
	}
	if kind != "static" {
		return nil, signupError(400, "Signup forms require a static list")
	}
	var identity any
	if r.IdentityID != "" {
		var n int64
		err = tx.QueryRowContext(ctx, `SELECT i.id FROM identities i JOIN users u ON u.id=i.user_id JOIN domains d ON d.id=i.domain_id WHERE i.uuid=$1 AND i.user_id=$2 AND i.kind='personal' AND u.org_id=$3 AND d.org_id=$3`, r.IdentityID, user, org).Scan(&n)
		if err == sql.ErrNoRows {
			return nil, signupError(400, "Choose your own sending identity")
		}
		if err != nil {
			return nil, err
		}
		identity = n
	}
	if r.Published && mode == "double" {
		if err = s.readySender(ctx, tx, &model.SignupForm{IdentityID: r.IdentityID, CreatedBy: user, OrgID: org}); err != nil {
			return nil, err
		}
	}
	if id == "" {
		err = tx.QueryRowContext(ctx, `INSERT INTO signup_forms(org_id,list_id,created_by,identity_id,name,title,description,consent_text,button_text,privacy_url,collect_name,published) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING uuid`, org, listID, user, identity, r.Name, r.Title, r.Description, r.ConsentText, r.ButtonText, r.PrivacyURL, r.CollectName, r.Published).Scan(&id)
	} else {
		if _, e := uuid.Parse(id); e != nil {
			return nil, signupError(404, "Form not found")
		}
		var n int
		err = tx.QueryRowContext(ctx, `UPDATE signup_forms SET identity_id=$1,name=$2,title=$3,description=$4,consent_text=$5,button_text=$6,privacy_url=$7,collect_name=$8,published=$9,version=version+1,updated_at=now() WHERE uuid=$10 AND org_id=$11 AND created_by=$12 AND list_id=$13 RETURNING id`, identity, r.Name, r.Title, r.Description, r.ConsentText, r.ButtonText, r.PrivacyURL, r.CollectName, r.Published, id, org, user, listID).Scan(&n)
		if err == sql.ErrNoRows {
			return nil, signupError(404, "Form not found or its list cannot be changed")
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, org, user, id)
}
func (s *SignupFormService) Delete(ctx context.Context, org, user int64, id string) error {
	f, err := s.Get(ctx, org, user, id)
	if err != nil {
		return err
	}
	// Consent audit records remain with the contact; only this form's signup history is removed.
	_, err = s.db.ExecContext(ctx, `DELETE FROM signup_forms WHERE id=$1 AND org_id=$2 AND created_by=$3`, f.ID, org, user)
	return err
}
func (s *SignupFormService) Entries(ctx context.Context, org, user int64, id string, page int) (*model.SignupEntries, error) {
	f, err := s.Get(ctx, org, user, id)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	out := &model.SignupEntries{Items: []model.SignupEntry{}, Page: page, PageSize: 25}
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM signup_requests WHERE form_id=$1`, f.ID).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,email,first_name,CASE WHEN status='pending' AND expires_at<=now() THEN 'expired' ELSE status END,confirmation_mode,created_at,confirmed_at FROM signup_requests WHERE form_id=$1 ORDER BY created_at DESC,id LIMIT 25 OFFSET $2`, f.ID, (page-1)*25)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x model.SignupEntry
		if err = rows.Scan(&x.ID, &x.Email, &x.FirstName, &x.Status, &x.ConfirmationMode, &x.CreatedAt, &x.ConfirmedAt); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, x)
	}
	return out, rows.Err()
}
func (s *SignupFormService) publicForm(ctx context.Context, q eventoutbox.DBTX, id string, lock bool) (*model.SignupForm, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, signupError(404, "This form is not available")
	}
	suffix := `WHERE f.uuid=$1 AND f.published AND EXISTS(SELECT 1 FROM users u WHERE u.id=f.created_by AND u.org_id=f.org_id AND u.status='active')`
	if lock {
		suffix += ` FOR UPDATE OF l FOR SHARE OF f`
	}
	f, err := scanSignupForm(q.QueryRowContext(ctx, formSelect+suffix, id))
	if err == sql.ErrNoRows {
		return nil, signupError(404, "This form is not available")
	}
	return f, err
}
func (s *SignupFormService) challenge(f *model.SignupForm, at time.Time) string {
	raw := fmt.Sprintf("%s:%d:%s:%d", f.UUID, f.Version, f.ConfirmationMode, at.Unix())
	m := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	m.Write([]byte("signup-form:" + raw))
	return base64.RawURLEncoding.EncodeToString([]byte(raw)) + "." + hex.EncodeToString(m.Sum(nil))
}
func (s *SignupFormService) validChallenge(f *model.SignupForm, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	fields := strings.Split(string(raw), ":")
	if len(fields) != 4 {
		return false
	}
	at, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || at > time.Now().Unix() || time.Now().Unix()-at > 3600 {
		return false
	}
	return hmac.Equal([]byte(token), []byte(s.challenge(f, time.Unix(at, 0))))
}
func (s *SignupFormService) Public(ctx context.Context, id string) (*model.PublicSignupForm, error) {
	f, err := s.publicForm(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	return &model.PublicSignupForm{UUID: f.UUID, Title: f.Title, Description: f.Description, ConsentText: f.ConsentText, ButtonText: f.ButtonText, PrivacyURL: f.PrivacyURL, CollectName: f.CollectName, ConfirmationMode: f.ConfirmationMode, Challenge: s.challenge(f, time.Now())}, nil
}
func signupDigest(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
func (s *SignupFormService) rate(ctx context.Context, key string, limit int) error {
	// Database windows, not in-memory counters, apply equally on all API replicas.
	m := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	m.Write([]byte(key))
	key = hex.EncodeToString(m.Sum(nil))
	var hits int
	err := s.db.QueryRowContext(ctx, `INSERT INTO signup_rate_limits(key,window_start,hits) VALUES($1,date_trunc('hour',now()),1) ON CONFLICT(key) DO UPDATE SET window_start=date_trunc('hour',now()),hits=CASE WHEN signup_rate_limits.window_start=date_trunc('hour',now()) THEN signup_rate_limits.hits+1 ELSE 1 END WHERE signup_rate_limits.window_start<date_trunc('hour',now()) OR signup_rate_limits.hits<$2 RETURNING hits`, key, limit).Scan(&hits)
	if err == sql.ErrNoRows {
		return signupError(429, "Too many attempts. Please try again in an hour.")
	}
	return err
}
func (s *SignupFormService) blocked(ctx context.Context, tx *sql.Tx, org int64, email string) (bool, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM contacts WHERE org_id=$1 AND lower(email)=$2 ORDER BY id LIMIT 1 FOR UPDATE`, org, email).Scan(&status)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && status != "active" {
		return true, nil
	}
	var blocked bool
	err = tx.QueryRowContext(ctx, `SELECT `+suppressedSQL("$1", "$2::text"), org, email).Scan(&blocked)
	return blocked, err
}

// subscribe records consent with the IP and user agent of the request that
// gave it: the form submit for single opt-in, the confirmation for double.
func (s *SignupFormService) subscribe(ctx context.Context, tx *sql.Tx, f *model.SignupForm, requestID, email, name, disclosure, mode, ip, ua string) (bool, error) {
	blocked, err := s.blocked(ctx, tx, f.OrgID, email)
	if err != nil || blocked {
		return false, err
	}
	var contactID int64
	var contactUUID string
	err = tx.QueryRowContext(ctx, `SELECT id,uuid FROM contacts WHERE org_id=$1 AND lower(email)=$2 ORDER BY id LIMIT 1`, f.OrgID, email).Scan(&contactID, &contactUUID)
	if err == sql.ErrNoRows {
		err = tx.QueryRowContext(ctx, `INSERT INTO contacts(org_id,email,first_name,last_name,status,consent_source,consent_timestamp,consent_ip,consent_user_agent,created_source,updated_at) VALUES($1,$2,$3,'','active','signup_form',now(),$4,$5,'signup_form',now()) RETURNING id,uuid`, f.OrgID, email, name, ip, ua).Scan(&contactID, &contactUUID)
	}
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO list_contacts(list_id,contact_id,source) VALUES($1,$2,'signup_form') ON CONFLICT(list_id,contact_id) DO NOTHING`, f.ListID, contactID)
	if err != nil {
		return false, err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if added > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO consent_audit(contact_id,org_id,action,source,list_id,details,ip_address,user_agent) VALUES($1,$2,'subscribe','signup_form',$3,$4,$5,$6)`, contactID, f.OrgID, f.ListID, fmt.Sprintf("Form %s; policy %s; disclosure: %s", f.UUID, mode, disclosure), ip, ua)
		if err != nil {
			return false, err
		}
		err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "contact.subscribed", OrgID: f.OrgID, UserID: f.CreatedBy, DedupeKey: "signup:" + requestID, Data: map[string]any{"contact_id": contactUUID, "email": email, "list_id": f.ListUUID, "form_id": f.UUID, "confirmation_mode": mode}})
		if err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE lists SET contact_count=(SELECT count(*) FROM list_contacts WHERE list_id=$1),updated_at=now() WHERE id=$1`, f.ListID)
	return err == nil, err
}
func (s *SignupFormService) Submit(ctx context.Context, id, ip, ua string, r *model.SubmitSignupRequest) (*model.SignupResult, error) {
	ua = consentUserAgent(ua)
	accepted := &model.SignupResult{Message: "Thanks! Your request has been received. If confirmation is needed, check your inbox."}
	if err := s.rate(ctx, "ip:"+ip, 30); err != nil {
		return nil, err
	}
	f, err := s.publicForm(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	if r.Website != "" {
		return accepted, nil
	}
	if !r.Consent {
		return nil, signupError(400, "Please accept the subscription wording to continue")
	}
	email := strings.ToLower(strings.TrimSpace(r.Email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 255 || strings.ContainsAny(email, "\r\n") || !strings.Contains(email, ".") {
		return nil, signupError(400, "Enter a valid email address")
	}
	name := strings.TrimSpace(r.FirstName)
	if !f.CollectName {
		name = ""
	}
	if len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return nil, signupError(400, "Name must be at most 100 bytes on one line")
	}
	if !s.validChallenge(f, r.Challenge) {
		return nil, signupError(409, "This form changed or expired. Reload it and try again.")
	}
	if err = s.rate(ctx, fmt.Sprintf("email:%d:%s", f.OrgID, email), 3); err != nil {
		return nil, err
	}
	if err = s.rate(ctx, "form:"+id, 1000); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	f, err = s.publicForm(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if !s.validChallenge(f, r.Challenge) {
		return nil, signupError(409, "This form changed. Reload it and try again.")
	}
	// Serialize the same address across forms without blocking unrelated subscribers.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),hashtext($2))`, fmt.Sprint(f.OrgID), email); err != nil {
		return nil, err
	}
	blocked, err := s.blocked(ctx, tx, f.OrgID, email)
	if err != nil {
		return nil, err
	}
	if blocked {
		return accepted, nil
	}
	var member bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM list_contacts lc JOIN contacts c ON c.id=lc.contact_id WHERE lc.list_id=$1 AND c.org_id=$2 AND lower(c.email)=$3)`, f.ListID, f.OrgID, email).Scan(&member)
	if err != nil {
		return nil, err
	}
	if member {
		return accepted, nil
	}
	var recent bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM signup_requests WHERE form_id=$1 AND email=$2 AND status='pending' AND sent_at>now()-interval '1 minute')`, f.ID, email).Scan(&recent)
	if err != nil {
		return nil, err
	}
	if recent {
		return accepted, nil
	}
	var rawToken, hash string
	if f.ConfirmationMode == "double" {
		if err = s.readySender(ctx, tx, f); err != nil {
			return nil, signupError(503, "This signup form is temporarily unavailable. Please try again later.")
		}
		token := make([]byte, 32)
		if _, err = rand.Read(token); err != nil {
			return nil, err
		}
		rawToken = base64.RawURLEncoding.EncodeToString(token)
		hash = signupDigest(rawToken)
	}
	var requestID string
	err = tx.QueryRowContext(ctx, `INSERT INTO signup_requests(form_id,email,first_name,status,confirmation_mode,disclosure,form_version,token_hash,expires_at) VALUES($1,$2,$3,'pending',$4,$5,$6,NULLIF($7,''),now()+interval '24 hours') ON CONFLICT(form_id,email) DO UPDATE SET first_name=EXCLUDED.first_name,status='pending',confirmation_mode=EXCLUDED.confirmation_mode,disclosure=EXCLUDED.disclosure,form_version=EXCLUDED.form_version,token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,sent_at=NULL,confirmed_at=NULL,updated_at=now() RETURNING id`, f.ID, email, name, f.ConfirmationMode, f.ConsentText, f.Version, hash).Scan(&requestID)
	if err != nil {
		return nil, err
	}
	if f.ConfirmationMode == "single" {
		ok, e := s.subscribe(ctx, tx, f, requestID, email, name, f.ConsentText, "single", ip, ua)
		if e != nil {
			return nil, e
		}
		if !ok {
			return accepted, nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE signup_requests SET status='subscribed',confirmed_at=now(),expires_at=NULL,updated_at=now() WHERE id=$1`, requestID)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if rawToken != "" {
		base := strings.TrimRight(s.cfg.WebUrl, "/")
		u, e := url.Parse(base)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, signupError(503, "The signup service needs a public web URL")
		}
		link := base + "/subscribe/confirm#" + rawToken
		text := "Please confirm your subscription to " + f.Title + ".\n\n" + f.ConsentText + "\n\n" + link + "\n\nThis link expires in 24 hours. If you did not request this, ignore this email."
		body := "<p>Please confirm your subscription to " + html.EscapeString(f.Title) + ".</p><p>" + html.EscapeString(f.ConsentText) + "</p><p><a href=\"" + html.EscapeString(link) + "\">Confirm my subscription</a></p><p>This link expires in 24 hours. If you did not request this, ignore this email.</p>"
		if s.sender == nil {
			return nil, signupError(503, "Confirmation email could not be sent. Please try again later.")
		}
		_, err = s.sender.SendEmailForUser(ctx, f.OrgID, f.CreatedBy, &model.SendEmailRequest{From: f.FromEmail, To: []string{email}, Subject: "Confirm your subscription", Text: text, HTML: body, IdempotencyKey: "signup-" + hash})
		if err != nil {
			return nil, signupError(503, "Confirmation email could not be sent. Please try again later.")
		}
		_, err = s.db.ExecContext(ctx, `UPDATE signup_requests SET sent_at=now() WHERE id=$1 AND token_hash=$2`, requestID, hash)
		if err != nil {
			return nil, err
		}
	}
	return accepted, nil
}
func (s *SignupFormService) Confirm(ctx context.Context, token, ip, ua string) (*model.SignupResult, error) {
	ua = consentUserAgent(ua)
	if err := s.rate(ctx, "confirm:"+ip, 60); err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, signupError(410, "This confirmation link is invalid or expired. Please sign up again.")
	}
	hash := signupDigest(token)
	var formID string
	err = s.db.QueryRowContext(ctx, `SELECT f.uuid FROM signup_requests r JOIN signup_forms f ON f.id=r.form_id WHERE r.token_hash=$1 AND r.status='pending' AND r.expires_at>now()`, hash).Scan(&formID)
	if err == sql.ErrNoRows {
		return nil, signupError(410, "This confirmation link has expired or was already used. Please sign up again if needed.")
	}
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	f, err := s.publicForm(ctx, tx, formID, true)
	if err != nil {
		return nil, err
	}
	var id, email, name, disclosure, mode string
	// Resolve email before locking the request to retain the same lock order as Submit.
	err = tx.QueryRowContext(ctx, `SELECT email FROM signup_requests WHERE token_hash=$1`, hash).Scan(&email)
	if err != nil {
		return nil, signupError(410, "This confirmation link is no longer available")
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),hashtext($2))`, fmt.Sprint(f.OrgID), email); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id,email,first_name,disclosure,confirmation_mode FROM signup_requests WHERE token_hash=$1 AND status='pending' AND expires_at>now() FOR UPDATE`, hash).Scan(&id, &email, &name, &disclosure, &mode)
	if err == sql.ErrNoRows {
		return nil, signupError(410, "This confirmation link has expired or was already used")
	}
	if err != nil {
		return nil, err
	}
	ok, err := s.subscribe(ctx, tx, f, id, email, name, disclosure, mode, ip, ua)
	if err != nil {
		return nil, err
	}
	status := "blocked"
	if ok {
		status = "subscribed"
	}
	_, err = tx.ExecContext(ctx, `UPDATE signup_requests SET status=$1,token_hash=NULL,expires_at=NULL,confirmed_at=CASE WHEN $1='subscribed' THEN now() ELSE NULL END,updated_at=now() WHERE id=$2`, status, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &model.SignupResult{Message: "Thanks! Your confirmation has been processed. You can close this page."}, nil
}

// consentUserAgent bounds the stored user agent; it is evidence, not input.
func consentUserAgent(ua string) string {
	if len(ua) > 512 {
		ua = ua[:512]
	}
	return strings.ToValidUTF8(ua, "")
}
func (s *SignupFormService) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Delete stale pending personal data; accepted consent remains in the contact audit.
			s.db.ExecContext(ctx, `DELETE FROM signup_rate_limits WHERE window_start<now()-interval '2 days'`)
			s.db.ExecContext(ctx, `DELETE FROM signup_requests WHERE status='pending' AND expires_at<now()-interval '30 days'`)
		}
	}
}
