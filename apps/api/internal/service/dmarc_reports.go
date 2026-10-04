package service

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dublyo/mailat/api/internal/model"
	"golang.org/x/net/idna"
)

const DMARCReportsFolder = "dmarc-reports"
const maxDMARCCompressed = 2 << 20
const maxDMARCExpanded = 10 << 20
const maxDMARCArchiveEntries = 32
const maxDMARCXMLDepth = 64
const maxDMARCRecords = 10000

// Verdicts come exclusively from the authenticated SES receipt or its persisted
// columns. MIME Authentication-Results headers are deliberately never consulted.
type dmarcVerdicts struct{ DMARC, SPF, DKIM, Spam, Virus string }

func (v dmarcVerdicts) trusted() bool {
	pass := func(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "PASS") }
	fail := func(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "FAIL") }
	return pass(v.DMARC) && (pass(v.SPF) || pass(v.DKIM)) && !fail(v.Spam) && !fail(v.Virus)
}
func receiptDMARCVerdicts(receipt *model.SESReceipt) dmarcVerdicts {
	if receipt == nil {
		return dmarcVerdicts{}
	}
	return dmarcVerdicts{receipt.DMARCVerdict.Status, receipt.SPFVerdict.Status, receipt.DKIMVerdict.Status, receipt.SpamVerdict.Status, receipt.VirusVerdict.Status}
}

var dmarcConversationPrefix = regexp.MustCompile(`(?i)^\s*(re|fw|fwd)\s*(\[[0-9]+\])?\s*:`)

func dmarcConversation(parsed *parsedIncoming) bool {
	return parsed.Header.Get("In-Reply-To") != "" || parsed.Header.Get("References") != "" || parsed.Header.Get("Resent-From") != "" || parsed.Header.Get("Resent-Date") != "" || dmarcConversationPrefix.MatchString(decodeMIMEHeader(parsed.Header.Get("Subject")))
}
func normalizeReportDomain(raw string) string {
	name, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if err != nil || len(name) > 253 || !strings.Contains(name, ".") {
		return ""
	}
	name = strings.ToLower(name)
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return ""
			}
		}
	}
	return name
}

type dmarcBudget struct{ compressed, expanded, entries, records int }

func (b *dmarcBudget) read(r io.Reader) ([]byte, error) {
	left := maxDMARCExpanded - b.expanded
	if left < 0 {
		return nil, fmt.Errorf("expanded limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(left)+1))
	b.expanded += len(data)
	if err != nil || b.expanded > maxDMARCExpanded {
		return nil, fmt.Errorf("invalid or oversized attachment")
	}
	return data, nil
}
func archiveKind(data []byte) string {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return "gzip"
	}
	if len(data) >= 4 && bytes.Equal(data[:2], []byte("PK")) {
		return "zip"
	}
	return ""
}
func archiveName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".zip" || ext == ".gz" || ext == ".gzip"
}
func xmlCandidate(a AttachmentInfo) bool {
	typ := strings.ToLower(strings.SplitN(a.ContentType, ";", 2)[0])
	ext := strings.ToLower(filepath.Ext(a.Filename))
	return ext == ".xml" || typ == "application/xml" || typ == "text/xml" || strings.HasSuffix(typ, "+xml")
}

// Report classification never controls acceptance. Any ambiguity returns a
// non-match; original bytes still follow the normal receiving/storage path.
func dmarcReportDomains(parsed *parsedIncoming, verdicts dmarcVerdicts) ([]string, string) {
	if parsed == nil || !verdicts.trusted() {
		return nil, "authentication"
	}
	if dmarcConversation(parsed) {
		return nil, "conversation"
	}
	budget := &dmarcBudget{}
	domains := []string{}
	parse := func(data []byte) (string, error) {
		if archiveKind(data) != "" {
			return "", fmt.Errorf("nested archive")
		}
		return parseDMARCAggregate(data, budget)
	}
	for _, a := range parsed.Attachments {
		kind := archiveKind(a.Data)
		if kind == "" && archiveName(a.Filename) {
			return nil, "invalid_archive"
		}
		if kind == "" && !xmlCandidate(a) {
			continue
		}
		if kind != "" {
			budget.compressed += len(a.Data)
			if budget.compressed > maxDMARCCompressed {
				return nil, "compressed_limit"
			}
		}
		switch kind {
		case "gzip":
			budget.entries++
			if budget.entries > maxDMARCArchiveEntries {
				return nil, "archive_entry_limit"
			}
			input := bytes.NewReader(a.Data)
			reader, err := gzip.NewReader(input)
			if err != nil {
				return nil, "invalid_gzip"
			}
			reader.Multistream(false)
			data, readErr := budget.read(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || input.Len() != 0 {
				return nil, "invalid_gzip"
			}
			domain, err := parse(data)
			if err != nil {
				return nil, "invalid_report"
			}
			domains = append(domains, domain)
		case "zip":
			reader, err := zip.NewReader(bytes.NewReader(a.Data), int64(len(a.Data)))
			if err != nil {
				return nil, "invalid_zip"
			}
			budget.entries += len(reader.File)
			if budget.entries > maxDMARCArchiveEntries {
				return nil, "archive_entry_limit"
			}
			found := false
			for _, file := range reader.File {
				if file.FileInfo().IsDir() {
					continue
				}
				if archiveName(file.Name) {
					return nil, "nested_archive"
				}
				// All expanded entries count, including non-XML entries; no path is ever extracted.
				if file.UncompressedSize64 > uint64(maxDMARCExpanded-budget.expanded) {
					return nil, "expanded_limit"
				}
				input, err := file.Open()
				if err != nil {
					return nil, "invalid_zip"
				}
				data, readErr := budget.read(input)
				closeErr := input.Close()
				if readErr != nil || closeErr != nil {
					return nil, "invalid_zip"
				}
				if archiveKind(data) != "" {
					return nil, "nested_archive"
				}
				if !strings.EqualFold(filepath.Ext(file.Name), ".xml") {
					continue
				}
				domain, err := parse(data)
				if err != nil {
					return nil, "invalid_report"
				}
				domains = append(domains, domain)
				found = true
			}
			if !found {
				return nil, "no_report"
			}
		default:
			data, err := budget.read(bytes.NewReader(a.Data))
			if err != nil {
				return nil, "expanded_limit"
			}
			domain, err := parse(data)
			if err != nil {
				return nil, "invalid_report"
			}
			domains = append(domains, domain)
		}
	}
	if len(domains) == 0 {
		return nil, "no_report"
	}
	return domains, "aggregate"
}

// A streaming structural check avoids unbounded record allocation and accepts
// namespace prefixes by local element name without resolving any XML references.
func parseDMARCAggregate(data []byte, budget *dmarcBudget) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	path := []string{}
	children := []bool{}
	text := strings.Builder{}
	values := map[string]string{}
	seen := map[string]bool{}
	records := 0
	roots := 0
	metadata := 0
	policies := 0
	rowFields := map[string]string{}
	recordHasRow := false
	recordHasIdentifiers := false
	recordHasAuth := false
	recordHasPolicy := false
	invalid := func() (string, error) { return "", fmt.Errorf("invalid aggregate structure") }
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid()
		}
		switch node := token.(type) {
		case xml.Directive:
			return invalid() // DTD/entity declarations are not report data.
		case xml.StartElement:
			if len(path) == 0 {
				roots++
				if roots != 1 || node.Name.Local != "feedback" {
					return invalid()
				}
			}
			if len(children) > 0 {
				children[len(children)-1] = true
			}
			children = append(children, false)
			path = append(path, node.Name.Local)
			if len(path) > maxDMARCXMLDepth {
				return invalid()
			}
			text.Reset()
			key := strings.Join(path, "/")
			if key == "feedback/report_metadata" {
				metadata++
				if metadata > 1 {
					return invalid()
				}
			}
			if key == "feedback/policy_published" {
				policies++
				if policies > 1 {
					return invalid()
				}
			}
			if key == "feedback/record" {
				records++
				budget.records++
				if budget.records > maxDMARCRecords {
					return invalid()
				}
				rowFields = map[string]string{}
				recordHasRow = false
				recordHasIdentifiers = false
				recordHasAuth = false
				recordHasPolicy = false
			}
			if key == "feedback/report_metadata/date_range" {
				if seen[key] {
					return invalid()
				}
				seen[key] = true
			}
			if key == "feedback/record/row/policy_evaluated" {
				if recordHasPolicy {
					return invalid()
				}
				recordHasPolicy = true
			}
			if key == "feedback/record/row" {
				if recordHasRow {
					return invalid()
				}
				recordHasRow = true
			}
			if key == "feedback/record/identifiers" {
				if recordHasIdentifiers {
					return invalid()
				}
				recordHasIdentifiers = true
			}
			if key == "feedback/record/auth_results" {
				if recordHasAuth {
					return invalid()
				}
				recordHasAuth = true
			}
		case xml.CharData:
			if len(path) == 0 && strings.TrimSpace(string(node)) != "" {
				return invalid()
			}
			if text.Len()+len(node) > 65536 {
				return invalid()
			}
			text.Write(node)
		case xml.EndElement:
			if len(path) == 0 {
				return invalid()
			}
			key := strings.Join(path, "/")
			value := strings.TrimSpace(text.String())
			switch key {
			case "feedback/report_metadata/org_name", "feedback/report_metadata/report_id", "feedback/report_metadata/date_range/begin", "feedback/report_metadata/date_range/end", "feedback/policy_published/domain", "feedback/policy_published/p":
				if seen[key] || children[len(children)-1] {
					return invalid()
				}
				seen[key] = true
				values[key] = value
			case "feedback/record/row/source_ip", "feedback/record/row/count", "feedback/record/row/policy_evaluated/disposition", "feedback/record/row/policy_evaluated/dkim", "feedback/record/row/policy_evaluated/spf", "feedback/record/identifiers/header_from":
				if _, ok := rowFields[key]; ok || children[len(children)-1] {
					return invalid()
				}
				rowFields[key] = value
			case "feedback/record":
				if !recordHasRow || !recordHasIdentifiers || !recordHasAuth {
					return invalid()
				}
				if _, err := netip.ParseAddr(rowFields["feedback/record/row/source_ip"]); err != nil {
					return invalid()
				}
				count, err := strconv.ParseUint(rowFields["feedback/record/row/count"], 10, 64)
				if err != nil || count == 0 {
					return invalid()
				}
				if normalizeReportDomain(rowFields["feedback/record/identifiers/header_from"]) == "" {
					return invalid()
				}
				for _, name := range []string{"dkim", "spf"} {
					v := rowFields["feedback/record/row/policy_evaluated/"+name]
					if v != "pass" && v != "fail" {
						return invalid()
					}
				}
				disposition := rowFields["feedback/record/row/policy_evaluated/disposition"]
				if disposition != "none" && disposition != "quarantine" && disposition != "reject" {
					return invalid()
				}
			}
			path = path[:len(path)-1]
			children = children[:len(children)-1]
			text.Reset()
		}
	}
	if roots != 1 || len(path) != 0 || metadata != 1 || policies != 1 || records == 0 {
		return invalid()
	}
	for _, name := range []string{"org_name", "report_id"} {
		if values["feedback/report_metadata/"+name] == "" {
			return invalid()
		}
	}
	begin, e1 := strconv.ParseInt(values["feedback/report_metadata/date_range/begin"], 10, 64)
	end, e2 := strconv.ParseInt(values["feedback/report_metadata/date_range/end"], 10, 64)
	if e1 != nil || e2 != nil || begin < 0 || end <= begin {
		return invalid()
	}
	policy := values["feedback/policy_published/p"]
	if policy != "none" && policy != "quarantine" && policy != "reject" {
		return invalid()
	}
	domain := normalizeReportDomain(values["feedback/policy_published/domain"])
	if domain == "" {
		return invalid()
	}
	return domain, nil
}

func ownedDMARCReport(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, orgID int64, domains []string) (bool, error) {
	if len(domains) == 0 {
		return false, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM domains WHERE org_id=$1`, orgID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	owned := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		owned[normalizeReportDomain(name)] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	// Mixed-organization report bundles stay in the normal mailbox destination.
	for _, domain := range domains {
		if !owned[domain] {
			return false, nil
		}
	}
	return true, nil
}
func autoOrganizeDMARC(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT auto_organize_dmarc_reports FROM user_settings WHERE user_id=$1),true)`, userID).Scan(&enabled)
	return enabled, err
}
