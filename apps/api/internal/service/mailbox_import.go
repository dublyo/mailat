package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"
)

// CSV import limits. bcrypt DefaultCost takes about 70 ms per password, so
// 200 rows keep a commit under about 20 s.
const (
	MailboxImportMaxBytes = 1 << 20
	mailboxImportMaxRows  = 200
)

var mailboxImportColumns = map[string]bool{"local_part": true, "address": true, "name": true, "invite_email": true, "password": true, "may_send": true, "may_receive": true}

type MailboxImportRow struct {
	Line    int    `json:"line"`    // line in the file (the header is line 1)
	Address string `json:"address"` // the mailbox address, or the raw value when it is invalid
	Result  string `json:"result"`  // ok (dry run), created or error
	Message string `json:"message,omitempty"`
}

type MailboxImportResult struct {
	DryRun bool               `json:"dryRun"`
	Rows   []MailboxImportRow `json:"rows"`
}

type mailboxImportSpec struct {
	row                   *MailboxImportRow
	local, name           string
	inviteEmail, password string
	maySend, mayReceive   bool
}

func orgMessage(err error) string {
	var orgErr *OrgError
	if errors.As(err, &orgErr) {
		return orgErr.Message
	}
	return err.Error()
}

func importBool(v, column string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false", column)
}

// parseMailboxCSV reads the header and rows. A bad file (encoding, header,
// size) fails as a whole; bad rows are reported per row.
func parseMailboxCSV(body []byte, domain string) ([]*mailboxImportSpec, error) {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(body) {
		return nil, orgError(http.StatusBadRequest, "The file must be UTF-8 text")
	}
	r := csv.NewReader(bytes.NewReader(body))
	header, err := r.Read()
	if err == io.EOF {
		return nil, orgError(http.StatusBadRequest, "The file is empty; the first line must be a header row")
	}
	if err != nil {
		return nil, orgError(http.StatusBadRequest, "The file is not valid CSV: "+err.Error())
	}
	cols := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if !mailboxImportColumns[h] {
			return nil, orgError(http.StatusBadRequest, fmt.Sprintf("Unknown column %q; use local_part or address, name, invite_email, password, may_send, may_receive", h))
		}
		if _, dup := cols[h]; dup {
			return nil, orgError(http.StatusBadRequest, fmt.Sprintf("Column %q appears twice", h))
		}
		cols[h] = i
	}
	_, hasLocal := cols["local_part"]
	_, hasAddress := cols["address"]
	if _, hasName := cols["name"]; !hasName || (!hasLocal && !hasAddress) {
		return nil, orgError(http.StatusBadRequest, "The header needs local_part (or address) and name")
	}
	var specs []*mailboxImportSpec
	seen := map[string]int{}
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, orgError(http.StatusBadRequest, "The file is not valid CSV: "+err.Error())
		}
		if len(specs) == mailboxImportMaxRows {
			return nil, orgError(http.StatusBadRequest, fmt.Sprintf("Import at most %d mailboxes per file", mailboxImportMaxRows))
		}
		line, _ := r.FieldPos(0)
		get := func(name string) string {
			if i, ok := cols[name]; ok {
				if name == "password" {
					return record[i] // a password is taken exactly as written
				}
				return strings.TrimSpace(record[i])
			}
			return ""
		}
		spec := &mailboxImportSpec{row: &MailboxImportRow{Line: line, Result: "error"}}
		specs = append(specs, spec)
		if err = spec.parse(get, domain); err != nil {
			spec.row.Message = orgMessage(err)
			continue
		}
		if first, dup := seen[spec.row.Address]; dup {
			spec.row.Message = fmt.Sprintf("Duplicate of line %d", first)
			continue
		}
		seen[spec.row.Address] = line
		spec.row.Result = "ok"
	}
	if len(specs) == 0 {
		return nil, orgError(http.StatusBadRequest, "The file has no mailbox rows")
	}
	return specs, nil
}

func (spec *mailboxImportSpec) parse(get func(string) string, domain string) error {
	local, address := strings.ToLower(get("local_part")), strings.ToLower(get("address"))
	spec.row.Address = address
	if address != "" {
		at := strings.LastIndex(address, "@")
		if at < 0 || address[at+1:] != strings.ToLower(domain) {
			return fmt.Errorf("The address must be on %s", domain)
		}
		if local != "" && local != address[:at] {
			return fmt.Errorf("local_part and address disagree")
		}
		local = address[:at]
	}
	if spec.row.Address == "" {
		spec.row.Address = local
	}
	full, err := mailboxAddress(local, domain)
	if err != nil {
		return err
	}
	spec.local, spec.row.Address = local, full
	if spec.name, err = validMailboxName(get("name")); err != nil {
		return err
	}
	invite, pw := get("invite_email"), get("password")
	if (invite == "") == (pw == "") {
		return fmt.Errorf("Give exactly one of invite_email or password")
	}
	if invite != "" {
		if spec.inviteEmail, err = normalizeAccountEmail(invite); err != nil {
			return fmt.Errorf("invite_email is not a valid email address")
		}
		if spec.inviteEmail == full {
			return fmt.Errorf("invite_email must be outside the new mailbox")
		}
	} else if len(pw) < 8 || len(pw) > 72 {
		return fmt.Errorf("password must be 8 to 72 bytes long")
	}
	spec.password = pw
	if spec.maySend, err = importBool(get("may_send"), "may_send"); err != nil {
		return err
	}
	spec.mayReceive, err = importBool(get("may_receive"), "may_receive")
	return err
}

// ImportCSV creates mailboxes from a CSV file. A dry run checks every row
// against the database and the rest of the file, writes nothing and hashes no
// password. A commit creates each valid row in its own transaction (the same
// as CreateMailbox) and writes one mailbox_import audit summary.
func (s *MailboxService) ImportCSV(ctx context.Context, a OrgActor, domainUUID string, body []byte, dryRun bool) (*MailboxImportResult, error) {
	if !validUUID(domainUUID) {
		return nil, orgError(http.StatusNotFound, "Domain not found")
	}
	var domainID int64
	var domain, status string
	var verified bool
	err := s.db.QueryRowContext(ctx, `SELECT id,name,COALESCE(status,''),COALESCE(ses_verified,false) FROM domains WHERE uuid=$1 AND org_id=$2`, domainUUID, a.OrgID).
		Scan(&domainID, &domain, &status, &verified)
	if err == sql.ErrNoRows {
		return nil, orgError(http.StatusNotFound, "Domain not found")
	}
	if err != nil {
		return nil, err
	}
	if status != "active" || !verified {
		return nil, orgError(http.StatusBadRequest, "Verify the domain with SES first")
	}
	specs, err := parseMailboxCSV(body, domain)
	if err != nil {
		return nil, err
	}
	out := &MailboxImportResult{DryRun: dryRun, Rows: make([]MailboxImportRow, 0, len(specs))}
	if dryRun {
		if err = s.checkImport(ctx, a, domainID, specs); err != nil {
			return nil, err
		}
	} else {
		created := 0
		for _, spec := range specs {
			if spec.row.Result != "ok" {
				continue
			}
			req := &CreateMailboxRequest{LocalPart: spec.local, Name: spec.name, MaySend: &spec.maySend, MayReceive: &spec.mayReceive,
				Access: MailboxAccessRequest{Mode: "password", Password: spec.password}}
			if spec.inviteEmail != "" {
				req.Access = MailboxAccessRequest{Mode: "invite", InviteEmail: spec.inviteEmail}
			}
			if _, err := s.CreateMailbox(ctx, a, domainUUID, req); err != nil {
				var orgErr *OrgError
				spec.row.Result, spec.row.Message = "error", "Unable to create this mailbox"
				if errors.As(err, &orgErr) {
					spec.row.Message = orgErr.Message
				} else {
					log.Printf("mailbox import: line %d failed: %v", spec.row.Line, err)
				}
				continue
			}
			spec.row.Result = "created"
			created++
		}
		if err = s.auditImport(ctx, a, domainUUID, domain, len(specs), created); err != nil {
			return nil, err
		}
	}
	for _, spec := range specs {
		out.Rows = append(out.Rows, *spec.row)
	}
	return out, nil
}

// checkImport runs the CreateMailbox checks read-only, counting new
// identities against the limit across the whole file.
func (s *MailboxService) checkImport(ctx context.Context, a OrgActor, domainID int64, specs []*mailboxImportSpec) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var limit, count int
	if err = tx.QueryRowContext(ctx, `SELECT max_identities,(SELECT count(*) FROM identities i JOIN users u ON u.id=i.user_id WHERE u.org_id=o.id) FROM organizations o WHERE o.id=$1`, a.OrgID).Scan(&limit, &count); err != nil {
		return err
	}
	var senderErr error
	senderChecked := false
	for _, spec := range specs {
		if spec.row.Result != "ok" {
			continue
		}
		fail := func(message string) { spec.row.Result, spec.row.Message = "error", message }
		reuseID, _, err := mailboxAddressFree(ctx, tx, a.OrgID, domainID, spec.row.Address, false)
		var orgErr *OrgError
		if errors.As(err, &orgErr) {
			fail(orgErr.Message)
			continue
		}
		if err != nil {
			return err
		}
		if spec.inviteEmail != "" {
			if !senderChecked {
				_, senderErr = inviteSender(ctx, tx, a, "")
				senderChecked = true
				if senderErr != nil && !errors.As(senderErr, &orgErr) {
					return senderErr
				}
			}
			if senderErr != nil {
				fail(orgMessage(senderErr))
				continue
			}
		}
		if reuseID == 0 && !s.members.cfg.DisableAppLimits {
			if limit > 0 && count >= limit {
				fail("The organization identity limit is reached")
				continue
			}
			count++
		}
	}
	return nil
}

func (s *MailboxService) auditImport(ctx context.Context, a OrgActor, domainUUID, domain string, rows, created int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = auditTx(ctx, tx, a, "mailbox_import", "domain", domainUUID, fmt.Sprintf("Imported %d of %d mailboxes to %s", created, rows, domain),
		map[string]any{"rows": rows, "created": created, "errors": rows - created}); err != nil {
		return err
	}
	return tx.Commit()
}
