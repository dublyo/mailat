package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/worker"
)

// Forwarding sends a copy of each arriving message to one verified outside
// address. The destination must confirm through a link (hashed token, 48 h,
// 10 wrong tries); a complaint or permanent bounce suspends the forward.
const (
	forwardMaxPerIdentity  = 5
	forwardVerifyTTL       = 48 * time.Hour
	forwardVerifyMaxFails  = 10
	forwardResendCooldown  = 60 * time.Second
	forwardResendPerDay    = 3
	forwardDisplayNameMax  = 64
	forwardDefaultMaxBytes = 10 << 20
	forwardDefaultDaily    = 200
)

// EmailForward is one forward of an identity's incoming mail. Status is
// pending (awaiting verification), active, paused or suspended.
type EmailForward struct {
	UUID            string     `json:"uuid"`
	IdentityUUID    string     `json:"identityUuid"`
	IdentityEmail   string     `json:"identityEmail"`
	ForwardTo       string     `json:"forwardTo"`
	KeepCopy        bool       `json:"keepCopy"`
	Status          string     `json:"status"`
	Active          bool       `json:"active"`
	Verified        bool       `json:"verified"`
	VerifiedAt      *time.Time `json:"verifiedAt,omitempty"`
	VerifyExpiresAt *time.Time `json:"verifyExpiresAt,omitempty"`
	LastError       *string    `json:"lastError,omitempty"`
	ForwardCount    int        `json:"forwardCount"`
	LastForwardedAt *time.Time `json:"lastForwardedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// CreateEmailForwardInput is the input for creating an email forward.
type CreateEmailForwardInput struct {
	IdentityUUID string `json:"identityUuid"`
	ForwardTo    string `json:"forwardTo"`
	KeepCopy     bool   `json:"keepCopy"` // false archives (never deletes) the local copy of forwarded mail
}

// UpdateEmailForwardInput pauses or resumes a forward or changes keepCopy.
type UpdateEmailForwardInput struct {
	Active   *bool `json:"active,omitempty"` // resume only a verified, paused forward
	KeepCopy *bool `json:"keepCopy,omitempty"`
}

// ForwardValidationError is a rejected forward request; its message is safe to show.
type ForwardValidationError struct{ Message string }

func (e *ForwardValidationError) Error() string { return e.Message }

var (
	ErrForwardNotFound = errors.New("email forward not found")
	// ErrForwardConflict is a duplicate destination or the per-identity limit.
	ErrForwardConflict = errors.New("forward conflict")
	// ErrForwardResendLimited is the resend cooldown or the daily resend limit.
	ErrForwardResendLimited = errors.New("verification email resend limit reached")
	// ErrForwardVerifyFailed is every verification failure, so callers learn nothing.
	ErrForwardVerifyFailed = errors.New("invalid or expired verification link")
	// Shared mailbox members never lose their copies to a forward.
	errSharedForwardKeepCopy = &ForwardValidationError{Message: "Forwards from a shared mailbox must keep a copy"}
)

const forwardColumns = `f.uuid::text, i.uuid::text, lower(i.email), f.forward_to, f.keep_copy, f.status, COALESCE(f.active,false), COALESCE(f.verified,false),
	f.verified_at, f.verify_expires_at, f.last_error, COALESCE(f.forward_count,0), f.last_forwarded_at, f.created_at, COALESCE(f.updated_at,f.created_at)`

func scanForward(row rowScanner) (*EmailForward, error) {
	var f EmailForward
	err := row.Scan(&f.UUID, &f.IdentityUUID, &f.IdentityEmail, &f.ForwardTo, &f.KeepCopy, &f.Status, &f.Active, &f.Verified,
		&f.VerifiedAt, &f.VerifyExpiresAt, &f.LastError, &f.ForwardCount, &f.LastForwardedAt, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *AutoReplyService) loadForward(ctx context.Context, q queryer, userID int64, forwardUUID string) (*EmailForward, error) {
	if _, err := uuid.Parse(forwardUUID); err != nil {
		return nil, ErrForwardNotFound
	}
	f, err := scanForward(q.QueryRowContext(ctx, `SELECT `+forwardColumns+` FROM email_forwards f JOIN identities i ON i.id=f.identity_id
		WHERE f.uuid=$1 AND f.user_id=$2`, forwardUUID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrForwardNotFound
	}
	return f, err
}

// newForwardToken returns a 256-bit URL-safe token and its stored sha256 hex.
func newForwardToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, forwardTokenHash(raw), nil
}

func forwardTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func normalizeForwardDestination(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(raw)
	if err != nil || strings.ContainsAny(raw, "\r\n") || !strings.EqualFold(addr.Address, raw) || len(raw) > 255 || !strings.Contains(raw, "@") {
		return "", &ForwardValidationError{Message: "forwardTo must be a single email address"}
	}
	return strings.ToLower(addr.Address), nil
}

// forwardDestinationInternal reports whether dest is an identity or on a domain
// the org receives for: those must use inbox rules or shared mailboxes.
func forwardDestinationInternal(ctx context.Context, q queryer, orgID int64, dest string) (bool, error) {
	domain := dest[strings.LastIndex(dest, "@")+1:]
	var internal bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1)
		OR EXISTS(SELECT 1 FROM domains WHERE org_id=$2 AND lower(name)=$3 AND COALESCE(receiving_enabled,false))`, dest, orgID, domain).Scan(&internal)
	return internal, err
}

func transactionalSuppressed(ctx context.Context, q queryer, orgID int64, email string) (bool, error) {
	var suppressed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM suppression_list WHERE org_id=$1 AND lower(email)=lower($2))`, orgID, email).Scan(&suppressed)
	return suppressed, err
}

// CreateEmailForward stores a pending forward and emails the destination a
// verification link. Nothing is forwarded until the link is opened.
func (s *AutoReplyService) CreateEmailForward(ctx context.Context, userID, orgID int64, input *CreateEmailForwardInput) (*EmailForward, error) {
	dest, err := normalizeForwardDestination(input.ForwardTo)
	if err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(input.IdentityUUID); err != nil {
		return nil, &ForwardValidationError{Message: "identityUuid is required"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Locking the identity serializes the per-identity forward count. A shared
	// identity needs can_manage and always keeps its members' copies.
	var identityID int64
	var allowed, shared, canSend, domainActive, sesVerified bool
	err = tx.QueryRowContext(ctx, `SELECT i.id,`+identityAccessSQL("i", "$3", identityCanManage)+`,i.kind='shared',COALESCE(i.can_send,false),d.status='active',COALESCE(d.ses_verified,false)
		FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.uuid=$1 AND d.org_id=$2 FOR UPDATE OF i`, input.IdentityUUID, orgID, userID).
		Scan(&identityID, &allowed, &shared, &canSend, &domainActive, &sesVerified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !allowed) {
		return nil, &ForwardValidationError{Message: "identity not found"}
	}
	if err != nil {
		return nil, err
	}
	if shared && !input.KeepCopy {
		return nil, errSharedForwardKeepCopy
	}
	if !canSend || !domainActive || (s.cfg.EmailProvider == "ses" && !sesVerified) {
		return nil, &ForwardValidationError{Message: "This identity cannot send mail, so it cannot forward"}
	}
	internal, err := forwardDestinationInternal(ctx, tx, orgID, dest)
	if err != nil {
		return nil, err
	}
	if internal {
		return nil, &ForwardValidationError{Message: "Forward only to outside addresses; use inbox filters or shared mailboxes inside your organization"}
	}
	suppressed, err := transactionalSuppressed(ctx, tx, orgID, dest)
	if err != nil {
		return nil, err
	}
	if suppressed {
		return nil, &ForwardValidationError{Message: "That address is on the suppression list"}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM email_forwards WHERE identity_id=$1`, identityID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= forwardMaxPerIdentity {
		return nil, fmt.Errorf("%w: an identity can have at most %d forwards", ErrForwardConflict, forwardMaxPerIdentity)
	}
	raw, hash, err := newForwardToken()
	if err != nil {
		return nil, err
	}
	var forwardID int64
	var forwardUUID string
	err = tx.QueryRowContext(ctx, `INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,keep_copy,active,verified,status,
			verify_token_hash,verify_expires_at,verify_send_day,verify_send_count,verify_last_sent_at,updated_at)
		VALUES($1,$2,$3,$4,$5,false,false,'pending',$6,now()+make_interval(secs=>$7),current_date,1,now(),now())
		ON CONFLICT (identity_id,forward_to) DO NOTHING RETURNING id,uuid::text`,
		userID, orgID, identityID, dest, input.KeepCopy, hash, forwardVerifyTTL.Seconds()).Scan(&forwardID, &forwardUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: this identity already forwards to that address", ErrForwardConflict)
	}
	if err != nil {
		return nil, err
	}
	payload, err := s.sendForwardVerification(ctx, tx, orgID, identityID, userID, forwardUUID, dest, raw)
	if err != nil {
		return nil, err
	}
	forward, err := s.loadForward(ctx, tx, userID, forwardUUID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(payload)
	return forward, nil
}

// sendForwardVerification queues the verification mail in tx. The token
// travels in the URL fragment, so it never reaches server logs or Referer.
func (s *AutoReplyService) sendForwardVerification(ctx context.Context, tx *sql.Tx, orgID, identityID, userID int64, forwardUUID, dest, token string) (*worker.EmailSendPayload, error) {
	if s.sender == nil {
		return nil, ErrProviderNotConfigured
	}
	var identityEmail, sendDay string
	var sendCount int
	if err := tx.QueryRowContext(ctx, `SELECT lower(i.email),to_char(f.verify_send_day,'YYYYMMDD'),f.verify_send_count
		FROM email_forwards f JOIN identities i ON i.id=f.identity_id WHERE f.uuid=$1`, forwardUUID).Scan(&identityEmail, &sendDay, &sendCount); err != nil {
		return nil, err
	}
	link := strings.TrimRight(s.cfg.WebUrl, "/") + "/forwards/verify#id=" + forwardUUID + "&token=" + token
	text := fmt.Sprintf("%s asked to forward its incoming mail to this address through Mailat.\n\n"+
		"To confirm, open this link within 48 hours:\n%s\n\n"+
		"If you did not expect this, ignore this email. Nothing is forwarded unless you confirm.\n", identityEmail, link)
	htmlBody := fmt.Sprintf(`<p>%s asked to forward its incoming mail to this address through Mailat.</p>`+
		`<p><a href="%s">Confirm forwarding</a> (the link works for 48 hours).</p>`+
		`<p>If you did not expect this, ignore this email. Nothing is forwarded unless you confirm.</p>`, html.EscapeString(identityEmail), html.EscapeString(link))
	// The day is part of the key because the send counter restarts daily.
	_, payload, err := s.sender.SendAutomated(ctx, tx, &AutomatedSend{
		OrgID: orgID, IdentityID: identityID, ActingUserID: userID, To: dest,
		Subject: "Confirm forwarding from " + identityEmail, Text: text, HTML: htmlBody,
		Headers: map[string]string{"Auto-Submitted": "auto-generated", "X-Auto-Response-Suppress": "All"},
		Kind:    "forward_verify", Ref: forwardUUID, DedupeKey: fmt.Sprintf("mailat:fv:%s:%s:%d", forwardUUID, sendDay, sendCount),
	})
	return payload, err
}

// VerifyEmailForward activates a pending forward when token matches. Every
// failure returns ErrForwardVerifyFailed; wrong tokens count toward a lock.
func (s *AutoReplyService) VerifyEmailForward(ctx context.Context, forwardUUID, token string) error {
	if _, err := uuid.Parse(forwardUUID); err != nil || token == "" || len(token) > 128 {
		return ErrForwardVerifyFailed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var status string
	var stored sql.NullString
	var live bool
	var failures int
	err = tx.QueryRowContext(ctx, `SELECT id,status,verify_token_hash,COALESCE(verify_expires_at>now(),false),verify_failures
		FROM email_forwards WHERE uuid=$1 FOR UPDATE`, forwardUUID).Scan(&id, &status, &stored, &live, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForwardVerifyFailed
	}
	if err != nil {
		return err
	}
	if status != "pending" || !stored.Valid || !live || failures >= forwardVerifyMaxFails {
		return ErrForwardVerifyFailed
	}
	if subtle.ConstantTimeCompare([]byte(forwardTokenHash(token)), []byte(stored.String)) != 1 {
		if _, err = tx.ExecContext(ctx, `UPDATE email_forwards SET verify_failures=verify_failures+1,updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrForwardVerifyFailed
	}
	if _, err = tx.ExecContext(ctx, `UPDATE email_forwards SET status='active',active=true,verified=true,verified_at=now(),
		verify_token_hash=NULL,verify_expires_at=NULL,verify_failures=0,last_error=NULL,updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ResendForwardVerification rotates the token of a pending or suspended
// forward and mails it again: one per minute, three per UTC day. A suspended
// forward needs this fresh consent before it runs again.
func (s *AutoReplyService) ResendForwardVerification(ctx context.Context, userID, orgID int64, forwardUUID string) (*EmailForward, error) {
	if _, err := uuid.Parse(forwardUUID); err != nil {
		return nil, ErrForwardNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var identityID int64
	var dest, status string
	var coolingDown bool
	var sentToday int
	err = tx.QueryRowContext(ctx, `SELECT identity_id,forward_to,status,
			COALESCE(verify_last_sent_at>now()-make_interval(secs=>$4),false),
			CASE WHEN verify_send_day=current_date THEN verify_send_count ELSE 0 END
		FROM email_forwards WHERE uuid=$1 AND user_id=$2 AND org_id=$3 FOR UPDATE`, forwardUUID, userID, orgID, forwardResendCooldown.Seconds()).
		Scan(&identityID, &dest, &status, &coolingDown, &sentToday)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrForwardNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" && status != "suspended" {
		return nil, &ForwardValidationError{Message: "This forward is already verified"}
	}
	if coolingDown || sentToday >= forwardResendPerDay {
		return nil, ErrForwardResendLimited
	}
	internal, err := forwardDestinationInternal(ctx, tx, orgID, dest)
	if err != nil {
		return nil, err
	}
	if internal {
		return nil, &ForwardValidationError{Message: "Forward only to outside addresses; use inbox filters or shared mailboxes inside your organization"}
	}
	raw, hash, err := newForwardToken()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE email_forwards SET status='pending',active=false,verified=false,verified_at=NULL,
			verify_token_hash=$2,verify_expires_at=now()+make_interval(secs=>$3),verify_failures=0,
			verify_send_count=CASE WHEN verify_send_day=current_date THEN verify_send_count+1 ELSE 1 END,
			verify_send_day=current_date,verify_last_sent_at=now(),updated_at=now() WHERE uuid=$1`,
		forwardUUID, hash, forwardVerifyTTL.Seconds()); err != nil {
		return nil, err
	}
	payload, err := s.sendForwardVerification(ctx, tx, orgID, identityID, userID, forwardUUID, dest, raw)
	if err != nil {
		return nil, err
	}
	forward, err := s.loadForward(ctx, tx, userID, forwardUUID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatch(payload)
	return forward, nil
}

// UpdateEmailForward pauses or resumes a forward, or changes keepCopy.
func (s *AutoReplyService) UpdateEmailForward(ctx context.Context, userID int64, forwardUUID string, input *UpdateEmailForwardInput) (*EmailForward, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = uuid.Parse(forwardUUID); err != nil {
		return nil, ErrForwardNotFound
	}
	if _, err = tx.ExecContext(ctx, `SELECT 1 FROM email_forwards WHERE uuid=$1 AND user_id=$2 FOR UPDATE`, forwardUUID, userID); err != nil {
		return nil, err
	}
	current, err := s.loadForward(ctx, tx, userID, forwardUUID)
	if err != nil {
		return nil, err
	}
	status := current.Status
	if input.Active != nil {
		switch {
		case *input.Active && status == "paused" && current.Verified:
			status = "active"
		case *input.Active && status == "active", !*input.Active && status == "paused":
		case !*input.Active && status == "active":
			status = "paused"
		case *input.Active:
			return nil, &ForwardValidationError{Message: "Only a verified, paused forward can be resumed; resend the verification email first"}
		default:
			return nil, &ForwardValidationError{Message: "Only an active forward can be paused"}
		}
	}
	keepCopy := current.KeepCopy
	if input.KeepCopy != nil {
		keepCopy = *input.KeepCopy
	}
	if !keepCopy {
		var shared bool
		if err = tx.QueryRowContext(ctx, `SELECT i.kind='shared' FROM email_forwards f JOIN identities i ON i.id=f.identity_id WHERE f.uuid=$1`, forwardUUID).Scan(&shared); err != nil {
			return nil, err
		}
		if shared {
			return nil, errSharedForwardKeepCopy
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE email_forwards SET status=$2,active=($2='active'),keep_copy=$3,updated_at=now() WHERE uuid=$1`, forwardUUID, status, keepCopy); err != nil {
		return nil, err
	}
	updated, err := s.loadForward(ctx, tx, userID, forwardUUID)
	if err != nil {
		return nil, err
	}
	return updated, tx.Commit()
}

// ListEmailForwards lists the user's forwards, newest first.
func (s *AutoReplyService) ListEmailForwards(ctx context.Context, userID int64) ([]*EmailForward, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+forwardColumns+` FROM email_forwards f JOIN identities i ON i.id=f.identity_id
		WHERE f.user_id=$1 ORDER BY f.created_at DESC, f.id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list email forwards: %w", err)
	}
	defer rows.Close()
	forwards := []*EmailForward{}
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to read email forward: %w", err)
		}
		forwards = append(forwards, f)
	}
	return forwards, rows.Err()
}

// DeleteEmailForward deletes a forward; queued jobs for it finish skipped.
func (s *AutoReplyService) DeleteEmailForward(ctx context.Context, userID int64, forwardUUID string) error {
	if _, err := uuid.Parse(forwardUUID); err != nil {
		return ErrForwardNotFound
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM email_forwards WHERE uuid=$1 AND user_id=$2`, forwardUUID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete email forward: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrForwardNotFound
	}
	return nil
}

// ForwardSource is what BuildForwardMessage needs from the original message.
type ForwardSource struct {
	FromName, FromEmail, ReplyTo string
	IdentityEmail                string
	LoopTokens                   []string
	Tag                          string
}

// ForwardMessage is the rewritten envelope of a forward: the body, subject and
// attachments are the original ones.
type ForwardMessage struct {
	FromName string
	ReplyTo  string
	Headers  map[string]string
}

var loopTokenPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// BuildForwardMessage sends as "<original name or address> via Mailat" from
// the identity, so DMARC aligns, with replies going to the original sender.
// X-Mailat-Loop carries earlier hop tags plus this forward's tag.
func BuildForwardMessage(src ForwardSource) ForwardMessage {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, strings.TrimSpace(src.FromName))
	if name = strings.TrimSpace(name); name == "" {
		name = strings.TrimSpace(src.FromEmail)
	}
	const suffix = " via Mailat"
	if name == "" {
		name = "Unknown sender"
	}
	if limit := forwardDisplayNameMax - utf8.RuneCountInString(suffix); utf8.RuneCountInString(name) > limit {
		name = strings.TrimSpace(string([]rune(name)[:limit-1])) + "…"
	}
	replyTo := ""
	for _, candidate := range []string{src.ReplyTo, src.FromEmail} {
		if a, err := mail.ParseAddress(strings.TrimSpace(candidate)); err == nil && !strings.ContainsAny(candidate, "\r\n") {
			replyTo = a.Address
			break
		}
	}
	var tokens []string
	for _, t := range src.LoopTokens {
		if t = strings.ToLower(strings.TrimSpace(t)); loopTokenPattern.MatchString(t) {
			tokens = append(tokens, t)
		}
	}
	tokens = append(tokens, src.Tag)
	return ForwardMessage{
		FromName: name + suffix,
		ReplyTo:  replyTo,
		Headers:  map[string]string{"X-Mailat-Loop": strings.Join(tokens, ", "), "X-Mailat-Forwarded-For": src.IdentityEmail},
	}
}

// forwardLoopTag is this forward's hop token: stable per install, opaque outside it.
func forwardLoopTag(orgUUID, forwardUUID string) string {
	sum := sha256.Sum256([]byte(orgUUID + "|" + forwardUUID))
	return hex.EncodeToString(sum[:])[:16]
}

// arrivalForward is the forward job payload: the arrival facts that are not
// stored on the mailbox copy.
type arrivalForward struct {
	LoopTokens  []string `json:"loopTokens,omitempty"`
	DMARCReport bool     `json:"dmarcReport,omitempty"`
}

func (r *ArrivalRunner) forwardMaxBytes() int {
	if r.cfg != nil && r.cfg.ForwardMaxBytes > 0 {
		return r.cfg.ForwardMaxBytes
	}
	return forwardDefaultMaxBytes
}

func (r *ArrivalRunner) forwardDailyLimit() int {
	if r.cfg != nil && r.cfg.ForwardDailyLimit > 0 {
		return r.cfg.ForwardDailyLimit
	}
	return forwardDefaultDaily
}

// setForwardState records a skip on the forward itself; it runs after the
// job transaction rolled back, together with the job's completion.
func setForwardState(forwardID int64, suspend bool, lastError string) func(context.Context, queryer) error {
	return func(ctx context.Context, q queryer) error {
		_, err := q.ExecContext(ctx, `UPDATE email_forwards SET last_error=$2,
				status=CASE WHEN $3 THEN 'suspended' ELSE status END, active=CASE WHEN $3 THEN false ELSE active END, updated_at=now()
			WHERE id=$1`, forwardID, clipUTF8(lastError, 500), suspend)
		return err
	}
}

// runForward re-checks the forward, applies the loop, size, suppression and
// daily guards, and enqueues the rewritten copy, all in tx.
func (r *ArrivalRunner) runForward(ctx context.Context, tx *sql.Tx, job *arrivalJob) (arrivalResult, error) {
	var p arrivalForward
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return arrivalFailed("invalid-payload"), nil
		}
	}
	var forwardUUID, dest, status, orgUUID string
	var ownerID int64
	err := tx.QueryRowContext(ctx, `SELECT f.uuid::text,lower(f.forward_to),f.status,f.user_id,o.uuid::text
		FROM email_forwards f JOIN organizations o ON o.id=f.org_id
		WHERE f.id=$1 AND f.org_id=$2 AND f.identity_id=$3 FOR UPDATE OF f`, job.RuleID, job.OrgID, job.IdentityID).
		Scan(&forwardUUID, &dest, &status, &ownerID, &orgUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return arrivalSkipped("forward-gone"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}
	if status != "active" {
		return arrivalSkipped("forward-" + status), nil
	}
	if r.tx == nil || r.tx.emailProvider == nil {
		return arrivalFailed("provider-not-configured"), nil
	}
	var identityEmail string
	var owned, canSend bool
	err = tx.QueryRowContext(ctx, `SELECT lower(i.email),`+identityAccessSQL("i", "$2", identityCanManage)+`,
			COALESCE(i.can_send,false) AND d.status='active' AND (COALESCE(d.ses_verified,false) OR $4<>'ses')
		FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.id=$1 AND d.org_id=$3`,
		job.IdentityID, ownerID, job.OrgID, r.tx.cfg.EmailProvider).Scan(&identityEmail, &owned, &canSend)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !owned) {
		return arrivalSkipped("identity-not-owned"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}
	if !canSend {
		return arrivalSkipped("identity-cannot-send"), nil
	}

	// The job's copy may have been deleted; any surviving copy has the same content.
	var (
		emailID                                   int64
		fromEmail, fromName, replyTo, subject     string
		textBody, htmlBody, spamVerdict, virusVer string
		size                                      int
	)
	err = tx.QueryRowContext(ctx, `SELECT id,COALESCE(from_email,''),COALESCE(from_name,''),COALESCE(reply_to,''),COALESCE(subject,''),
			COALESCE(text_body,''),COALESCE(html_body,''),COALESCE(size_bytes,0),COALESCE(spam_verdict,''),COALESCE(virus_verdict,'')
		FROM received_emails WHERE identity_id=$1 AND ses_message_id=$2 AND direction='inbound'
		ORDER BY (id=$3) DESC, id LIMIT 1`, job.IdentityID, job.SESMessageID, job.EmailID).
		Scan(&emailID, &fromEmail, &fromName, &replyTo, &subject, &textBody, &htmlBody, &size, &spamVerdict, &virusVer)
	if errors.Is(err, sql.ErrNoRows) {
		return arrivalSkipped("source-deleted"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}
	tag := forwardLoopTag(orgUUID, forwardUUID)
	loopHeader := mail.Header{"X-Mailat-Loop": {strings.Join(p.LoopTokens, ",")}}
	switch {
	case strings.EqualFold(spamVerdict, "FAIL") || strings.EqualFold(virusVer, "FAIL"):
		return arrivalSkipped("spam"), nil
	case p.DMARCReport:
		return arrivalSkipped("dmarc-report"), nil
	case ForwardLoopBlocked(loopHeader, tag):
		return arrivalSkipped("mail-loop"), nil
	case strings.EqualFold(strings.TrimSpace(fromEmail), dest):
		return arrivalSkipped("sender-is-destination"), nil
	}
	internal, err := forwardDestinationInternal(ctx, tx, job.OrgID, dest)
	if err != nil {
		return arrivalResult{}, err
	}
	if internal {
		return arrivalSkipped("destination-internal"), nil
	}
	if size > r.forwardMaxBytes() {
		result := arrivalSkipped("too-large")
		result.After = setForwardState(job.RuleID, false, fmt.Sprintf("A %d-byte message was too large to forward; it stays in your mailbox", size))
		return result, nil
	}
	suppressed, err := transactionalSuppressed(ctx, tx, job.OrgID, dest)
	if err != nil {
		return arrivalResult{}, err
	}
	if suppressed {
		result := arrivalSkipped("suppressed")
		result.After = setForwardState(job.RuleID, true, "The destination is on the suppression list, so forwarding stopped")
		return result, nil
	}
	var capped int64
	err = tx.QueryRowContext(ctx, `UPDATE email_forwards SET window_count=CASE WHEN window_day=current_date THEN window_count+1 ELSE 1 END,
			window_day=current_date WHERE id=$1 AND (window_day IS DISTINCT FROM current_date OR window_count<$2) RETURNING id`,
		job.RuleID, r.forwardDailyLimit()).Scan(&capped)
	if errors.Is(err, sql.ErrNoRows) {
		result := arrivalSkipped("daily-cap")
		result.After = setForwardState(job.RuleID, false, "The daily forwarding limit was reached; later mail stays in your mailbox")
		return result, nil
	}
	if err != nil {
		return arrivalResult{}, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT filename,content_type,size_bytes,s3_bucket,s3_key,COALESCE(content_id,''),is_inline
		FROM email_attachments WHERE received_email_id=$1 ORDER BY id`, emailID)
	if err != nil {
		return arrivalResult{}, err
	}
	var attachments []S3Attachment
	for rows.Next() {
		var a S3Attachment
		if err = rows.Scan(&a.Name, &a.ContentType, &a.Size, &a.Bucket, &a.Key, &a.ContentID, &a.Inline); err != nil {
			rows.Close()
			return arrivalResult{}, err
		}
		attachments = append(attachments, a)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return arrivalResult{}, err
	}

	msg := BuildForwardMessage(ForwardSource{FromName: fromName, FromEmail: fromEmail, ReplyTo: replyTo, IdentityEmail: identityEmail, LoopTokens: p.LoopTokens, Tag: tag})
	if textBody == "" && htmlBody == "" && len(attachments) == 0 {
		textBody = " "
	}
	_, payload, err := r.tx.SendAutomated(ctx, tx, &AutomatedSend{
		OrgID: job.OrgID, IdentityID: job.IdentityID, ActingUserID: ownerID, To: dest,
		Subject: clipUTF8(strings.Join(strings.Fields(subject), " "), 1000), Text: textBody, HTML: htmlBody,
		ReplyTo: msg.ReplyTo, FromName: msg.FromName, Headers: msg.Headers, S3Attachments: attachments,
		Kind: "forward", Ref: forwardUUID, DedupeKey: systemDedupeKey(fmt.Sprintf("mailat:fw:%d:", job.RuleID), job.SESMessageID),
	})
	if err != nil {
		result, err := systemSendFailure(err)
		if err == nil {
			result.After = setForwardState(job.RuleID, result.Reason == "suppressed", "Forwarding failed: "+result.Reason)
		}
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE email_forwards SET forward_count=COALESCE(forward_count,0)+1,last_forwarded_at=now(),last_error=NULL,updated_at=now() WHERE id=$1`, job.RuleID); err != nil {
		return arrivalResult{}, err
	}
	return arrivalResult{Status: "done", Payload: payload}, nil
}

// keepCopyArchive archives a new inbox copy when an active forward of the
// identity has keepCopy=false. Mail is never deleted. It returns the copy's
// final folder.
func keepCopyArchive(ctx context.Context, tx *sql.Tx, identityID, emailID int64, folder string) (string, error) {
	if folder != "inbox" {
		return folder, nil
	}
	var archive bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM email_forwards WHERE identity_id=$1 AND status='active' AND NOT keep_copy)`, identityID).Scan(&archive); err != nil {
		return folder, err
	}
	if !archive {
		return folder, nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE received_emails SET folder='archive',is_archived=true,is_read=true,read_at=COALESCE(read_at,now()),updated_at=now() WHERE id=$1`, emailID)
	return "archive", err
}
