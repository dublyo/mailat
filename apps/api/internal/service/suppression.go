package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"
)

// emailSHA256 is the suppression match key. It must normalize exactly like
// the SQL in suppressedSQL and the suppressions_fill_sha256 trigger (012).
func emailSHA256(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// suppressedSQL returns a boolean SQL expression that is true when the address
// is on the org's marketing suppression list. Matching uses the hash column so
// GDPR-erased rows (which keep only the hash) still block sends.
// worker/campaign_handler.go keeps an identical copy (import cycle); a test
// asserts the two stay the same.
func suppressedSQL(orgExpr, emailExpr string) string {
	return "EXISTS(SELECT 1 FROM suppressions s WHERE s.org_id=" + orgExpr +
		" AND s.email_sha256=encode(sha256(convert_to(lower(trim(" + emailExpr + ")),'UTF8')),'hex'))"
}

// ContactError is a contacts/compliance failure with a fixed HTTP status and
// machine-readable code; the message is safe to show to the caller.
type ContactError struct {
	Status  int
	Code    string
	Message string
}

func (e *ContactError) Error() string { return e.Message }

var (
	ErrUnknownList         = &ContactError{400, "unknown_list", "One or more lists don't exist in this organization."}
	ErrSystemStatus        = &ContactError{400, "system_status", "status is managed by the system"}
	ErrContactNotFound     = &ContactError{404, "not_found", "Contact not found"}
	ErrReactivationBlocked = &ContactError{409, "reactivation_blocked", "This address has unsubscribed and can't be re-added here."}
	ErrContactSuppressed   = &ContactError{409, "suppressed", "This address is unsubscribed or suppressed and can't be added."}
	ErrDuplicateEmail      = &ContactError{409, "duplicate_email", "Another contact already uses this email address."}
	ErrContactExists       = &ContactError{409, "contact_exists", "contact with this email already exists"}
)

func invalidContact(msg string) error { return &ContactError{400, "invalid_contact", msg} }

// ContactActor identifies who made an admin change, for consent and audit rows.
type ContactActor struct {
	UserID int64
	IP     string
	UA     string
}

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func isSuppressedTx(ctx context.Context, q queryer, orgID int64, email string) (bool, error) {
	var suppressed bool
	if err := q.QueryRowContext(ctx, `SELECT `+suppressedSQL("$1", "$2::text"), orgID, email).Scan(&suppressed); err != nil {
		return false, fmt.Errorf("failed to check suppression: %w", err)
	}
	return suppressed, nil
}

// validateOrgLists returns ErrUnknownList unless every id is a list in the org.
// This is the guard against attaching contacts to another org's lists.
func validateOrgLists(ctx context.Context, q queryer, orgID int64, ids []int) error {
	ids = uniqueInts(ids)
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM lists WHERE org_id = $1 AND id = ANY($2::int[])`, orgID, pq.Array(ids)).Scan(&n); err != nil {
		return fmt.Errorf("failed to validate lists: %w", err)
	}
	if n != len(ids) {
		return ErrUnknownList
	}
	return nil
}

func recountLists(ctx context.Context, q queryer, orgID int64, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := q.ExecContext(ctx, `
		UPDATE lists SET contact_count = (SELECT COUNT(*) FROM list_contacts WHERE list_id = lists.id), updated_at = NOW()
		WHERE id = ANY($1::int[]) AND org_id = $2`, pq.Array(uniqueInts(ids)), orgID); err != nil {
		return fmt.Errorf("failed to update list counts: %w", err)
	}
	return nil
}

func uniqueInts(ids []int) []int {
	seen := make(map[int]bool, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// normalizeContactEmail lowercases, trims and validates a contact address.
func normalizeContactEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 255 {
		return "", invalidContact("email must be 1-255 characters")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", invalidContact("invalid email address")
	}
	return email, nil
}

func validateContactNames(first, last string) error {
	if utf8.RuneCountInString(first) > 100 || utf8.RuneCountInString(last) > 100 {
		return invalidContact("names must be at most 100 characters")
	}
	return nil
}

// likePattern escapes LIKE metacharacters so user input matches literally
// (used with ESCAPE '\').
func likePattern(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}
