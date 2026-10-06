package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/mail"
	"strings"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
)

var errCampaignSenderUnauthorized = &provider.MailValidationError{Message: "From must be one of your sending identities on an active, SES-verified domain"}

// campaignSenderPredicate is shared by the create-time lookup and runtime
// revalidation: an active user in the org owns a can_send identity on an
// active, SES-verified domain of the same org.
const campaignSenderPredicate = `u.org_id=$3 AND u.status='active' AND i.can_send AND d.org_id=$3 AND d.status='active' AND d.ses_verified`

// resolveCampaignSender maps a From address to an identity the user owns. It is
// an exact identity match (identities.email is globally unique), stricter than
// the mailbox alias rule.
func resolveCampaignSender(ctx context.Context, q eventoutbox.DBTX, orgID, userID int64, fromEmail string) (identityID, domainID int64, err error) {
	if strings.ContainsAny(fromEmail, "\r\n") {
		return 0, 0, &provider.MailValidationError{Message: "invalid From address"}
	}
	a, perr := mail.ParseAddress(strings.TrimSpace(fromEmail))
	if perr != nil || !strings.EqualFold(a.Address, strings.TrimSpace(fromEmail)) {
		return 0, 0, &provider.MailValidationError{Message: "From must be a plain email address"}
	}
	if userID <= 0 {
		return 0, 0, errCampaignSenderUnauthorized
	}
	err = q.QueryRowContext(ctx, `SELECT i.id, d.id FROM identities i JOIN users u ON u.id=i.user_id JOIN domains d ON d.id=i.domain_id
		WHERE lower(i.email)=lower($1) AND i.user_id=$2 AND `+campaignSenderPredicate, a.Address, userID, orgID).Scan(&identityID, &domainID)
	if err == sql.ErrNoRows {
		return 0, 0, errCampaignSenderUnauthorized
	}
	if err != nil {
		return 0, 0, fmt.Errorf("failed to resolve campaign sender: %w", err)
	}
	return identityID, domainID, nil
}

// revalidateCampaignSender checks the identity stored on a campaign is still
// owned by its creator, still matches the From address and is still sendable.
func revalidateCampaignSender(ctx context.Context, q eventoutbox.DBTX, orgID, identityID, userID int64, fromEmail string) (domainID int64, err error) {
	if identityID <= 0 || userID <= 0 {
		return 0, errCampaignSenderUnauthorized
	}
	err = q.QueryRowContext(ctx, `SELECT d.id FROM identities i JOIN users u ON u.id=i.user_id JOIN domains d ON d.id=i.domain_id
		WHERE i.id=$1 AND i.user_id=$2 AND lower(i.email)=lower($4) AND `+campaignSenderPredicate, identityID, userID, orgID, fromEmail).Scan(&domainID)
	if err == sql.ErrNoRows {
		return 0, errCampaignSenderUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("failed to revalidate campaign sender: %w", err)
	}
	return domainID, nil
}

// requireFeedbackReady blocks campaign sends until SES bounce/complaint
// notifications reach Mailat for the domain. Drafts may be saved earlier.
func requireFeedbackReady(ctx context.Context, q eventoutbox.DBTX, domainID int64) error {
	var ready bool
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(sending_feedback_ready,false) FROM domains WHERE id=$1`, domainID).Scan(&ready); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to check sending feedback: %w", err)
	}
	if !ready {
		return &provider.MailValidationError{Message: "finish sending setup (bounce/complaint feedback) for this domain before sending campaigns"}
	}
	return nil
}
