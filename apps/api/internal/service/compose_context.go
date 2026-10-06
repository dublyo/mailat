package service

import (
	"context"
	"fmt"
	"html"
	"net/mail"
	"strings"

	"github.com/google/uuid"
)

// SES contexts use mailbox UUIDs and the same ownership check as detail/download.
func (s *ComposeService) mailboxReplyContext(ctx context.Context, userID int64, id string, replyAll, forward bool) (*ComposeEmail, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, fmt.Errorf("invalid email UUID")
	}
	original, err := (&InboxService{db: s.db}).GetReceivedEmail(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	var ownAddress, domain string
	if err = s.db.QueryRowContext(ctx, `SELECT i.email,d.name FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.id=$1 AND `+identityAccessSQL("i", "$2", identityCanSend), original.IdentityID, userID).Scan(&ownAddress, &domain); err != nil {
		return nil, fmt.Errorf("email not found")
	}
	alias := ownAddress
	candidates := original.EnvelopeRecipients
	if original.Direction == "outbound" {
		candidates = []string{original.FromEmail}
	}
	for _, value := range candidates {
		if a, e := mail.ParseAddress(value); e == nil && strings.EqualFold(extractDomain(a.Address), domain) {
			alias = a.Address
			break
		}
	}
	// This check also prevents replying as an explicitly reserved address of another user.
	sender, err := s.authorizeMailboxSender(ctx, userID, original.IdentityID, alias)
	if err == errMemberAlias {
		// A member replying to catch-all mail answers from the identity address.
		sender, err = s.authorizeMailboxSender(ctx, userID, original.IdentityID, ownAddress)
	}
	if err != nil {
		return nil, err
	}
	result := &ComposeEmail{IdentityID: original.IdentityID, From: EmailAddress{Email: sender.email, Name: sender.name}, To: []EmailAddress{}, Cc: []EmailAddress{}, Bcc: []EmailAddress{}, References: []string{}, Attachments: []AttachmentRef{}}
	if forward {
		result.Subject = s.buildForwardSubject(original.Subject)
		result.TextBody = "\n\n---------- Forwarded message ----------\nFrom: " + original.FromEmail + "\nSubject: " + original.Subject + "\n\n" + original.TextBody
		if original.HTMLBody != "" {
			result.HTMLBody = "<p>Forwarded message from " + html.EscapeString(original.FromEmail) + "</p>" + original.HTMLBody
		}
		for _, a := range original.Attachments {
			disposition := "attachment"
			if a.IsInline {
				disposition = "inline"
			}
			result.Attachments = append(result.Attachments, AttachmentRef{BlobID: a.UUID, Name: a.Filename, Type: a.ContentType, Size: int(a.SizeBytes), Disposition: disposition, CID: a.ContentID})
		}
		return result, nil
	}
	result.Subject = s.buildReplySubject(original.Subject)
	result.InReplyTo = original.MessageID
	result.References = s.buildReferences(original.References, []string{original.MessageID})
	exclude := map[string]bool{strings.ToLower(ownAddress): true, strings.ToLower(alias): true}
	for _, a := range original.EnvelopeRecipients {
		exclude[strings.ToLower(a)] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT lower(email) FROM identities WHERE user_id=$1 AND kind='personal'`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			rows.Close()
			return nil, err
		}
		exclude[a] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	add := func(dst *[]EmailAddress, values []string) {
		for _, v := range values {
			a, e := mail.ParseAddress(v)
			if e != nil {
				continue
			}
			key := strings.ToLower(a.Address)
			if exclude[key] || seen[key] {
				continue
			}
			seen[key] = true
			*dst = append(*dst, EmailAddress{Email: a.Address, Name: a.Name})
		}
	}
	if original.Direction == "outbound" {
		add(&result.To, original.ToEmails)
	} else if original.ReplyTo != "" {
		add(&result.To, []string{original.ReplyTo})
	} else {
		add(&result.To, []string{original.FromEmail})
	}
	if replyAll {
		add(&result.To, original.ToEmails)
		add(&result.Cc, original.CcEmails)
	}
	// Never expose original Bcc as reply-all recipients.
	result.TextBody = "\n\nOn " + original.ReceivedAt.Format("2006-01-02 15:04 MST") + ", " + original.FromEmail + " wrote:\n" + original.TextBody
	if original.HTMLBody != "" {
		result.HTMLBody = "<p></p><blockquote>" + original.HTMLBody + "</blockquote>"
		for _, a := range original.Attachments {
			if a.IsInline {
				result.Attachments = append(result.Attachments, AttachmentRef{BlobID: a.UUID, Name: a.Filename, Type: a.ContentType, Size: int(a.SizeBytes), Disposition: "inline", CID: a.ContentID})
			}
		}
	}
	return result, nil
}
