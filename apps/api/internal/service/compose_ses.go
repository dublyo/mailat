package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"mime"
	"net/mail"
	"path"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const maxComposeBodyBytes = 2 * 1024 * 1024

var ErrDraftConflict = errors.New("draft changed in another session; reload it before saving or sending")
var ErrSubmissionConflict = errors.New("submission key was already used for different content")

type mailboxSender struct {
	identityID, domainID, orgID, userID int64
	email, name, domain, bucket         string
}
type preparedAttachment struct {
	name, contentType, bucket, key, checksum string
	contentID                                string
	inline                                   bool
	sourceID                                 string
	originalEmailID                          int64
	size                                     int
	data                                     []byte
}

func normalizeSenderAlias(alias, domain string) (string, error) {
	if strings.ContainsAny(alias, "\r\n") {
		return "", &provider.MailValidationError{Message: "invalid From address"}
	}
	a, err := mail.ParseAddress(strings.TrimSpace(alias))
	if err != nil || !strings.EqualFold(a.Address, strings.TrimSpace(alias)) {
		return "", &provider.MailValidationError{Message: "From must be a plain email address"}
	}
	i := strings.LastIndexByte(a.Address, '@')
	if i < 1 || !strings.EqualFold(a.Address[i+1:], domain) {
		return "", &provider.MailValidationError{Message: "From must belong to the selected identity's verified domain"}
	}
	return strings.ToLower(a.Address), nil
}

// errMemberAlias rejects a member's From address other than the identity
// address or a +tag form of it.
var errMemberAlias = &provider.MailValidationError{Message: "From must be the identity address or a +tag form of it"}

// memberAliasAllowed reports whether alias is identity itself or local+tag@domain.
func memberAliasAllowed(identity, alias string) bool {
	identity, alias = strings.ToLower(identity), strings.ToLower(alias)
	if alias == identity {
		return true
	}
	at := strings.LastIndexByte(identity, '@')
	if at < 1 {
		return false
	}
	local, domain := identity[:at], identity[at:]
	tag, ok := strings.CutPrefix(alias, local+"+")
	return ok && strings.HasSuffix(tag, domain) && len(tag) > len(domain) && !strings.Contains(strings.TrimSuffix(tag, domain), "@")
}

// Domain ownership is checked independently of SES account-wide identity
// verification. The sender is a personal identity the user owns, or a shared
// identity the user may send as. Owners and admins may use any free address on
// the identity's domain; members only the identity address or a +tag of it.
func (s *ComposeService) authorizeMailboxSender(ctx context.Context, userID, identityID int64, alias string) (*mailboxSender, error) {
	sender := &mailboxSender{userID: userID}
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT i.id,d.id,u.org_id,i.email,COALESCE(i.display_name,''),d.name,
	 COALESCE(NULLIF(d.attachment_s3_bucket,''),NULLIF(d.receiving_s3_bucket,''),rc.s3_bucket,''),COALESCE(u.role,'')
	 FROM identities i JOIN users u ON u.id=$2 JOIN domains d ON d.id=i.domain_id
	 LEFT JOIN receiving_configs rc ON rc.org_id=u.org_id
	 WHERE i.id=$1 AND `+identityAccessSQL("i", "$2", identityCanSend)+` AND i.can_send=true AND d.org_id=u.org_id
	 AND d.status='active' AND d.ses_verified=true`, identityID, userID).Scan(&sender.identityID, &sender.domainID, &sender.orgID, &sender.email, &sender.name, &sender.domain, &sender.bucket, &role)
	if err == sql.ErrNoRows {
		return nil, &provider.MailValidationError{Message: "select an authorized sending identity on a verified SES domain"}
	}
	if err != nil {
		return nil, err
	}
	identityEmail := sender.email
	if alias == "" {
		alias = identityEmail
	}
	sender.email, err = normalizeSenderAlias(alias, sender.domain)
	if err != nil {
		return nil, err
	}
	if role != "owner" && role != "admin" && !memberAliasAllowed(identityEmail, sender.email) {
		return nil, errMemberAlias
	}
	// Any other identity's address is foreign unless it is the user's own personal identity.
	var otherOwner bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE LOWER(email)=$1 AND id<>$3 AND NOT (kind='personal' AND user_id=$2))`, sender.email, userID, identityID).Scan(&otherOwner)
	if err != nil {
		return nil, err
	}
	if otherOwner {
		return nil, &provider.MailValidationError{Message: "that From address belongs to another user"}
	}
	return sender, nil
}

func normalizeCompose(email *ComposeEmail, sending bool) error {
	if email.References == nil {
		email.References = []string{}
	}
	if len(email.Subject) > 1000 || strings.ContainsAny(email.Subject, "\r\n") {
		return &provider.MailValidationError{Message: "subject must be at most 1000 bytes without line breaks"}
	}
	if len(email.TextBody)+len(email.HTMLBody) > maxComposeBodyBytes {
		return &provider.MailValidationError{Message: "message body exceeds 2 MiB"}
	}
	if len(email.To)+len(email.Cc)+len(email.Bcc) > provider.MaxMailRecipients {
		return &provider.MailValidationError{Message: "maximum 50 recipients per message"}
	}
	if sending && len(email.To)+len(email.Cc)+len(email.Bcc) == 0 {
		return &provider.MailValidationError{Message: "at least one recipient is required"}
	}
	if sending && email.TextBody == "" && email.HTMLBody == "" && len(email.Attachments) == 0 {
		return &provider.MailValidationError{Message: "message content is required"}
	}
	for _, group := range [][]EmailAddress{email.To, email.Cc, email.Bcc, email.ReplyTo} {
		for i := range group {
			if strings.ContainsAny(group[i].Email+group[i].Name, "\r\n") {
				return &provider.MailValidationError{Message: "addresses must not contain line breaks"}
			}
			if len(group[i].Email) > 320 || len(group[i].Name) > 255 {
				return &provider.MailValidationError{Message: "address is too long"}
			}
			if !sending && group[i].Email == "" {
				continue
			}
			a, err := mail.ParseAddress(group[i].Email)
			if err != nil {
				if sending {
					return &provider.MailValidationError{Message: "invalid recipient email address"}
				}
				continue
			}
			group[i].Email = a.Address
			if group[i].Name == "" {
				group[i].Name = a.Name
			}
		}
	}
	if len(email.ReplyTo) > 1 {
		return &provider.MailValidationError{Message: "only one Reply-To address is supported"}
	}
	if len(email.References) > 100 {
		return &provider.MailValidationError{Message: "too many References headers"}
	}
	for _, value := range append(append([]string{}, email.References...), email.InReplyTo) {
		if strings.ContainsAny(value, "\r\n") || len(value) > 1000 {
			return &provider.MailValidationError{Message: "invalid threading header"}
		}
	}
	if len(email.Attachments) > 50 {
		return &provider.MailValidationError{Message: "maximum 50 attachments"}
	}
	return nil
}

func composeFingerprint(email *ComposeEmail) string {
	copy := *email
	copy.SubmissionKey = ""
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mailboxAddresses(addresses []EmailAddress) []string {
	result := make([]string, 0, len(addresses))
	for _, a := range addresses {
		if a.Name == "" {
			result = append(result, a.Email)
		} else {
			result = append(result, (&mail.Address{Name: a.Name, Address: a.Email}).String())
		}
	}
	return result
}

func (s *ComposeService) prepareMailboxAttachments(ctx context.Context, sender *mailboxSender, refs []AttachmentRef) ([]preparedAttachment, error) {
	result := make([]preparedAttachment, 0, len(refs))
	total := 0
	for _, ref := range refs {
		var a preparedAttachment
		if ref.BlobID != "" && ref.Content != "" {
			return nil, &provider.MailValidationError{Message: "provide an attachment reference or content, not both"}
		}
		if ref.BlobID != "" {
			a.sourceID = ref.BlobID
			if _, err := uuid.Parse(ref.BlobID); err != nil {
				return nil, &provider.MailValidationError{Message: "invalid attachment reference"}
			}
			err := s.db.QueryRowContext(ctx, `SELECT ea.filename,ea.content_type,ea.size_bytes,ea.s3_bucket,ea.s3_key,COALESCE(ea.checksum,''),COALESCE(ea.content_id,''),ea.is_inline,ea.received_email_id
			 FROM email_attachments ea JOIN received_emails re ON re.id=ea.received_email_id
			 WHERE ea.uuid=$1 AND re.mailbox_owner_id=$2
			 UNION ALL SELECT filename,content_type,size_bytes,s3_bucket,s3_key,checksum,'',false,0 FROM compose_uploads WHERE uuid=$1 AND user_id=$2 LIMIT 1`, ref.BlobID, sender.userID).Scan(&a.name, &a.contentType, &a.size, &a.bucket, &a.key, &a.checksum, &a.contentID, &a.inline, &a.originalEmailID)
			if err == sql.ErrNoRows {
				return nil, &provider.MailValidationError{Message: "attachment is unavailable or not owned by you"}
			}
			if err != nil {
				return nil, err
			}
			if a.size < 0 || total+a.size > provider.MaxAttachmentBytes {
				return nil, &provider.MailValidationError{Message: "attachments exceed 10 MiB"}
			}
			if s.attachments == nil {
				return nil, fmt.Errorf("attachment storage is unavailable")
			}
			a.data, err = s.attachments.Get(ctx, a.bucket, a.key)
			if err != nil {
				return nil, fmt.Errorf("could not read attachment: %w", err)
			}
		} else {
			if len(ref.Content) > base64.StdEncoding.EncodedLen(provider.MaxAttachmentBytes) {
				return nil, &provider.MailValidationError{Message: "attachments exceed 10 MiB"}
			}
			data, err := base64.StdEncoding.Strict().DecodeString(ref.Content)
			if err != nil {
				return nil, &provider.MailValidationError{Message: "attachment content must be base64"}
			}
			a.data = data
			a.name = path.Base(strings.ReplaceAll(ref.Name, "\\", "/"))
			a.contentType = ref.Type
			a.contentID = strings.Trim(ref.CID, "<>")
			a.inline = ref.Disposition == "inline"
			if strings.ContainsAny(a.contentID, "\r\n<>") {
				return nil, &provider.MailValidationError{Message: "invalid attachment content ID"}
			}
			if a.name == "." || a.name == "" || strings.ContainsAny(a.name, "\r\n") || len(a.name) > 255 {
				return nil, &provider.MailValidationError{Message: "invalid attachment filename"}
			}
			media, _, err := mime.ParseMediaType(a.contentType)
			if err != nil {
				media = "application/octet-stream"
			}
			a.contentType = media
			a.bucket = sender.bucket
			if a.bucket == "" {
				return nil, &provider.MailValidationError{Message: "attachment storage is not ready; run the domain setup-sending operation before adding attachments"}
			}
		}
		a.size = len(a.data)
		total += a.size
		if total > provider.MaxAttachmentBytes {
			return nil, &provider.MailValidationError{Message: "attachments exceed the 10 MiB total limit"}
		}
		sum := sha256.Sum256(a.data)
		a.checksum = hex.EncodeToString(sum[:])
		if ref.BlobID == "" {
			a.key = fmt.Sprintf("mailat/attachments/%d/%d/%s", sender.orgID, sender.userID, a.checksum)
			if s.attachments == nil {
				return nil, fmt.Errorf("attachment storage is unavailable")
			}
			if err := s.attachments.Put(ctx, a.bucket, a.key, a.data); err != nil {
				return nil, fmt.Errorf("attachment could not be stored: %w", err)
			}
		}
		result = append(result, a)
	}
	return result, nil
}

func insertMailboxAttachments(ctx context.Context, tx *sql.Tx, emailID int64, attachments []preparedAttachment) error {
	for _, a := range attachments {
		id := uuid.NewString()
		// Preserve references when a saved draft is edited repeatedly. Forwarding creates a new copy.
		if a.originalEmailID == emailID && a.sourceID != "" {
			id = a.sourceID
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO email_attachments(uuid,received_email_id,filename,content_type,size_bytes,s3_bucket,s3_key,checksum,content_id,is_inline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10)`, id, emailID, a.name, a.contentType, a.size, a.bucket, a.key, a.checksum, a.contentID, a.inline)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *ComposeService) existingSubmission(ctx context.Context, userID int64, key, hash string) (*SendEmailResult, error) {
	var result SendEmailResult
	var storedHash string
	var sentAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT email_uuid,message_id,status,COALESCE(send_error,''),sent_at,request_hash
	 FROM compose_submission_keys WHERE user_id=$1 AND submission_key=$2`, userID, key).Scan(&result.EmailID, &result.MessageID, &result.Status, &result.SendError, &sentAt, &storedHash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != storedHash {
		return nil, ErrSubmissionConflict
	}
	if sentAt.Valid {
		result.SentAt = sentAt.Time
	}
	return &result, nil
}

func (s *ComposeService) sendMailboxEmail(ctx context.Context, userID int64, email *ComposeEmail) (*SendEmailResult, error) {
	if s.emailProvider == nil {
		return nil, fmt.Errorf("SES is not configured; sending has not been attempted")
	}
	if len(email.SubmissionKey) < 8 || len(email.SubmissionKey) > 128 || strings.ContainsAny(email.SubmissionKey, "\r\n") {
		return nil, &provider.MailValidationError{Message: "an Idempotency-Key header of 8 to 128 characters is required"}
	}
	if err := normalizeCompose(email, true); err != nil {
		return nil, err
	}
	sender, err := s.authorizeMailboxSender(ctx, userID, email.IdentityID, email.From.Email)
	if err != nil {
		return nil, err
	}
	email.From = EmailAddress{Email: sender.email, Name: sender.name}
	hash := composeFingerprint(email)
	if existing, err := s.existingSubmission(ctx, userID, email.SubmissionKey, hash); err != nil || existing != nil {
		return existing, err
	}
	var recipients []string
	for _, group := range [][]EmailAddress{email.To, email.Cc, email.Bcc} {
		for _, address := range group {
			recipients = append(recipients, strings.ToLower(address.Email))
		}
	}
	var suppressed string
	err = s.db.QueryRowContext(ctx, `SELECT email FROM suppression_list WHERE org_id=$1 AND LOWER(email)=ANY($2) LIMIT 1`, sender.orgID, pq.Array(recipients)).Scan(&suppressed)
	if err == nil {
		return nil, &provider.MailValidationError{Message: "a recipient is suppressed because of a previous delivery or complaint event"}
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	attachments, err := s.prepareMailboxAttachments(ctx, sender, email.Attachments)
	if err != nil {
		return nil, err
	}
	msg := &provider.EmailMessage{From: (&mail.Address{Name: sender.name, Address: sender.email}).String(), To: mailboxAddresses(email.To), Cc: mailboxAddresses(email.Cc), Bcc: mailboxAddresses(email.Bcc), Subject: email.Subject, TextBody: email.TextBody, HTMLBody: email.HTMLBody, Headers: map[string]string{}}
	if len(email.ReplyTo) > 0 {
		msg.ReplyTo = mailboxAddresses(email.ReplyTo)[0]
	}
	if email.InReplyTo != "" {
		msg.Headers["In-Reply-To"] = email.InReplyTo
	}
	if len(email.References) > 0 {
		msg.Headers["References"] = strings.Join(email.References, " ")
	}
	for _, a := range attachments {
		msg.Attachments = append(msg.Attachments, provider.Attachment{Filename: a.name, ContentType: a.contentType, Data: a.data, ContentID: a.contentID, Inline: a.inline})
	}
	// Validate the exact MIME before a durable send claim or quota reservation.
	if _, _, err := provider.BuildMailMIME(msg); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	messageID := "<" + id + "@" + sender.domain + ">"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Reserve independently of the mailbox row so deleting Sent cannot reuse the key.
	claim, err := tx.ExecContext(ctx, `INSERT INTO compose_submission_keys(user_id,submission_key,request_hash,email_uuid,message_id,identity_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, userID, email.SubmissionKey, hash, id, messageID, sender.identityID)
	if err != nil {
		return nil, err
	}
	claimed, err := claim.RowsAffected()
	if err != nil {
		return nil, err
	}
	if claimed == 0 {
		tx.Rollback()
		return s.existingSubmission(ctx, userID, email.SubmissionKey, hash)
	}
	if email.DraftID != "" {
		var version int
		err = tx.QueryRowContext(ctx, `SELECT re.draft_version FROM received_emails re
		 WHERE re.uuid=$1 AND re.mailbox_owner_id=$2 AND re.direction='outbound' AND re.send_status='draft' FOR UPDATE OF re`, email.DraftID, userID).Scan(&version)
		if err == sql.ErrNoRows || (err == nil && version != email.DraftVersion) {
			tx.Rollback()
			// A concurrent request with the same key may have just consumed this draft.
			if existing, lookupErr := s.existingSubmission(ctx, userID, email.SubmissionKey, hash); lookupErr != nil || existing != nil {
				return existing, lookupErr
			}
			return nil, ErrDraftConflict
		}
		if err != nil {
			return nil, err
		}
	}
	var emailID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,from_name,to_emails,cc_emails,bcc_emails,reply_to,subject,text_body,html_body,in_reply_to,"references",snippet,has_attachments,folder,is_read,direction,send_status,submission_key,submission_hash,submitter_user_id,mailbox_owner_id,updated_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,'outbox',true,'outbound','sending',$19,$20,$21,$21,NOW()) RETURNING id`, id, sender.orgID, sender.domainID, sender.identityID, messageID, sender.email, sender.name, pq.Array(mailboxAddresses(email.To)), pq.Array(mailboxAddresses(email.Cc)), pq.Array(mailboxAddresses(email.Bcc)), msg.ReplyTo, email.Subject, email.TextBody, email.HTMLBody, email.InReplyTo, pq.Array(email.References), composeSnippet(email.TextBody), len(attachments) > 0, email.SubmissionKey, hash, userID).Scan(&emailID)
	if err != nil {
		tx.Rollback()
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return s.existingSubmission(ctx, userID, email.SubmissionKey, hash)
		}
		return nil, err
	}
	if err = insertMailboxAttachments(ctx, tx, emailID, attachments); err != nil {
		return nil, err
	}
	if email.DraftID != "" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM received_emails WHERE uuid=$1`, email.DraftID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	result := &SendEmailResult{EmailID: id, MessageID: messageID, Status: "sending"}
	// Reserve once after the unique submission claim. Replays return before reaching this line.
	if err = reserveMonthlySend(ctx, s.db, s.cfg, sender.orgID); err != nil {
		result.Status = "failed"
		result.SendError = err.Error()
		s.finishMailboxSubmission(emailID, result, "")
		return result, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msg.Headers["X-Mailat-Message-ID"] = id
	providerResult, sendErr := s.sendWithThrottleRetry(sendCtx, msg)
	providerID := ""
	if providerResult != nil {
		providerID = providerResult.MessageID
	}
	if sendErr != nil {
		result.Status = "unknown"
		result.SendError = "The provider response was uncertain. Do not resend automatically; check delivery before creating another attempt."
		if provider.ClassifySendError(sendErr) == provider.SendThrottled {
			// A throttle proves SES did not accept the message, so a retry is safe.
			result.Status = "failed"
			result.Retryable = true
			result.SendError = "Amazon SES is rate limiting this account. Nothing was sent; you can retry in a minute."
		} else if provider.IsDefinitiveSendError(sendErr) {
			result.Status = "failed"
			result.SendError = "SES rejected the message: " + sendErr.Error()
		}
	} else {
		result.Status = "sent"
		result.SentAt = time.Now().UTC()
	}
	if err = s.finishMailboxSubmission(emailID, result, providerID); err != nil {
		// Acceptance may have happened even when the final DB write fails. Preserve this distinction.
		result.Status = "unknown"
		result.SendError = "The provider attempt finished but its final status could not be saved. Check delivery before resending."
	}
	return result, nil
}

// composeThrottleDelays are the pauses between in-request attempts after a throttle.
var composeThrottleDelays = []time.Duration{500 * time.Millisecond, time.Second}

// sendWithThrottleRetry submits once and resubmits only after an explicit SES
// throttle, which is a pre-acceptance rejection and so cannot duplicate mail.
// Any other error, including a timeout, is returned from the attempt that saw it.
func (s *ComposeService) sendWithThrottleRetry(ctx context.Context, msg *provider.EmailMessage) (*provider.SendResult, error) {
	sleep := s.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	result, err := s.emailProvider.SendEmail(ctx, msg)
	for _, delay := range composeThrottleDelays {
		if err == nil || provider.ClassifySendError(err) != provider.SendThrottled {
			break
		}
		sleep(time.Duration(float64(delay) * (0.8 + 0.4*rand.Float64())))
		if ctx.Err() != nil {
			break // keep the throttle: nothing was accepted
		}
		result, err = s.emailProvider.SendEmail(ctx, msg)
	}
	return result, err
}

func (s *ComposeService) finishMailboxSubmission(emailID int64, result *SendEmailResult, providerID string) error {
	// The browser may disconnect after SES accepts mail; status persistence must outlive that request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	folder := "outbox"
	var sentAt interface{}
	if result.Status == "sent" {
		folder = "sent"
		sentAt = result.SentAt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE received_emails SET send_status=CASE WHEN send_status IN ('delivered','bounced','complained') THEN send_status ELSE $2 END,send_error=CASE WHEN send_status IN ('bounced','complained') THEN send_error ELSE NULLIF($3,'') END,sent_at=COALESCE(sent_at,$4),folder=CASE WHEN send_status IN ('delivered','bounced','complained') THEN 'sent' ELSE $5 END,ses_message_id=COALESCE(NULLIF($6,''),ses_message_id),updated_at=NOW() WHERE id=$1`, emailID, result.Status, result.SendError, sentAt, folder, providerID)
	if err != nil {
		return err
	}
	// The row may have been removed by another administrative path; the receipt survives.
	_, err = tx.ExecContext(ctx, `UPDATE compose_submission_keys SET status=CASE WHEN status IN ('delivered','bounced','complained') THEN status ELSE $2 END,send_error=CASE WHEN status IN ('bounced','complained') THEN send_error ELSE NULLIF($3,'') END,sent_at=COALESCE(sent_at,$4),ses_message_id=COALESCE(NULLIF($5,''),ses_message_id),updated_at=NOW() WHERE email_uuid=$1`, result.EmailID, result.Status, result.SendError, sentAt, providerID)
	if err != nil {
		return err
	}
	var org, user, identity int64
	var from, subject string
	var to []string
	err = tx.QueryRowContext(ctx, `SELECT u.org_id,k.user_id,COALESCE(k.identity_id,e.identity_id,0),COALESCE(e.from_email,''),COALESCE(e.subject,''),COALESCE(e.to_emails,'{}'::text[]) FROM compose_submission_keys k JOIN users u ON u.id=k.user_id LEFT JOIN received_emails e ON e.uuid=k.email_uuid WHERE k.email_uuid=$1`, result.EmailID).Scan(&org, &user, &identity, &from, &subject, pq.Array(&to))
	if err != nil {
		return err
	}
	if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email." + result.Status, OrgID: org, UserID: user, IdentityID: identity, MessageUUID: result.EmailID, DedupeKey: result.Status + ":" + result.EmailID, Data: map[string]any{"status": result.Status, "messageId": result.MessageID, "providerMessageId": providerID, "from": from, "to": to, "subject": subject}}); err != nil {
		return err
	}
	return tx.Commit()
}

func composeSnippet(text string) string {
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) > 200 {
		r = r[:200]
	}
	return string(r)
}

func (s *ComposeService) saveMailboxDraft(ctx context.Context, userID int64, id string, email *ComposeEmail) (*DraftResult, error) {
	if err := normalizeCompose(email, false); err != nil {
		return nil, err
	}
	sender, err := s.authorizeMailboxSender(ctx, userID, email.IdentityID, email.From.Email)
	if err != nil {
		return nil, err
	}
	attachments, err := s.prepareMailboxAttachments(ctx, sender, email.Attachments)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := &DraftResult{ID: id, IdentityID: sender.identityID, Version: 1}
	var dbID int64
	replyTo := ""
	if len(email.ReplyTo) > 0 {
		replyTo = mailboxAddresses(email.ReplyTo)[0]
	}
	if id == "" {
		result.ID = uuid.NewString()
		err = tx.QueryRowContext(ctx, `INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,from_name,to_emails,cc_emails,bcc_emails,reply_to,subject,text_body,html_body,in_reply_to,"references",snippet,has_attachments,folder,is_read,direction,send_status,submitter_user_id,mailbox_owner_id,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,'drafts',true,'outbound','draft',$19,$19,NOW()) RETURNING id,created_at,updated_at`, result.ID, sender.orgID, sender.domainID, sender.identityID, "<"+result.ID+"@"+sender.domain+">", sender.email, sender.name, pq.Array(mailboxAddresses(email.To)), pq.Array(mailboxAddresses(email.Cc)), pq.Array(mailboxAddresses(email.Bcc)), replyTo, email.Subject, email.TextBody, email.HTMLBody, email.InReplyTo, pq.Array(email.References), composeSnippet(email.TextBody), len(attachments) > 0, userID).Scan(&dbID, &result.CreatedAt, &result.UpdatedAt)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE received_emails re SET identity_id=$3,domain_id=$4,from_email=$5,from_name=$6,to_emails=$7,cc_emails=$8,bcc_emails=$9,reply_to=$10,subject=$11,text_body=$12,html_body=$13,in_reply_to=$14,"references"=$15,snippet=$16,has_attachments=$17,mailbox_owner_id=$2,draft_version=draft_version+1,updated_at=NOW()
		 WHERE re.uuid=$1 AND re.submitter_user_id=$2 AND re.send_status='draft' AND re.direction='outbound' AND re.draft_version=$18
		 RETURNING id,created_at,updated_at,draft_version`, id, userID, sender.identityID, sender.domainID, sender.email, sender.name, pq.Array(mailboxAddresses(email.To)), pq.Array(mailboxAddresses(email.Cc)), pq.Array(mailboxAddresses(email.Bcc)), replyTo, email.Subject, email.TextBody, email.HTMLBody, email.InReplyTo, pq.Array(email.References), composeSnippet(email.TextBody), len(attachments) > 0, email.DraftVersion).Scan(&dbID, &result.CreatedAt, &result.UpdatedAt, &result.Version)
		if err == sql.ErrNoRows {
			return nil, ErrDraftConflict
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM email_attachments WHERE received_email_id=$1`, dbID)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = insertMailboxAttachments(ctx, tx, dbID, attachments); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ComposeService) deleteMailboxDraft(ctx context.Context, userID int64, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM received_emails WHERE uuid=$1 AND submitter_user_id=$2 AND direction='outbound' AND send_status='draft'`, id, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return &provider.MailValidationError{Message: "draft not found"}
	}
	return nil
}

func (s *ComposeService) uploadMailboxAttachment(ctx context.Context, userID, identityID int64, data []byte, filename, contentType string) (*AttachmentRef, error) {
	if len(data) > provider.MaxAttachmentBytes {
		return nil, &provider.MailValidationError{Message: "attachment exceeds 10 MiB"}
	}
	sender, err := s.authorizeMailboxSender(ctx, userID, identityID, "")
	if err != nil {
		return nil, err
	}
	attachments, err := s.prepareMailboxAttachments(ctx, sender, []AttachmentRef{{Name: filename, Type: contentType, Content: base64.StdEncoding.EncodeToString(data)}})
	if err != nil {
		return nil, err
	}
	a := attachments[0]
	id := uuid.NewString()
	_, err = s.db.ExecContext(ctx, `INSERT INTO compose_uploads(uuid,user_id,org_id,filename,content_type,size_bytes,s3_bucket,s3_key,checksum) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, userID, sender.orgID, a.name, a.contentType, a.size, a.bucket, a.key, a.checksum)
	if err != nil {
		return nil, err
	}
	return &AttachmentRef{BlobID: id, Name: a.name, Type: a.contentType, Size: a.size}, nil
}
