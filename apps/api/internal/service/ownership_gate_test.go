package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	ownershipIdentities = regexp.MustCompile(`(?i)\bidentities\b`)
	// user_id unqualified or qualified with the identities table or its usual alias i.
	ownershipUserID   = regexp.MustCompile(`(?i)(?:^|[^\w.])(?:(?:i|identities)\.)?user_id\b`)
	ownershipPersonal = regexp.MustCompile(`(?i)kind\s*=\s*'personal'`)
)

// ownershipGateAllowlist lists SQL literals that read identities.user_id without
// kind='personal' on purpose. Keys are "<file>: <unique substring of the literal>".
var ownershipGateAllowlist = map[string]string{
	"identity.go: INSERT INTO identities (uuid, user_id":                                                  "creates the row; kind defaults to personal",
	"identity.go: SELECT count(*) FROM identities i JOIN users u":                                         "org-wide identity count includes shared identities",
	"org_members.go: kind='shared' AND user_id=$1":                                                        "hands the shared-identity steward over on member removal",
	"org_members.go: UPDATE identities SET user_id=$2,is_default=false,updated_at=now() WHERE id=ANY($1)": "transfers ids selected with kind='personal'",
	"org_members.go: UPDATE identities SET user_id=$2,is_default=false,updated_at=now() WHERE id=$1":      "admin transfer of an identity locked and checked as personal",
	"org_members.go: SELECT i.id,i.user_id,i.kind FROM identities i":                                      "admin transfer reads the kind and rejects shared identities in Go",
	"org_members.go: FROM identities i JOIN users u ON u.id=i.user_id WHERE i.id=$1":                      "admin identity view shows the steward as owner",
	"org_members.go: FROM identities i JOIN domains d ON d.id=i.domain_id JOIN users u ON u.id=i.user_id": "admin org identity listing shows the steward as owner",
	"receiving_events.go: COALESCE(t.system_user_id,i.user_id)":                                           "transactional API sends use personal identities only; system sends carry their acting user",
}

// TestIdentityOwnershipGate enforces that identity ownership predicates in the
// service and outbox packages are restricted to personal identities, either
// inline or through identityAccessSQL (which also admits shared members).
func TestIdentityOwnershipGate(t *testing.T) {
	var files []string
	for _, dir := range []string{".", "../eventoutbox"} {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range matches {
			if !strings.HasSuffix(f, "_test.go") {
				files = append(files, f)
			}
		}
	}
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(path)
		guarded := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if bin, ok := n.(*ast.BinaryExpr); ok && bin.Op == token.ADD && callsIdentityAccess(bin) {
				ast.Inspect(bin, func(m ast.Node) bool {
					if lit, ok := m.(*ast.BasicLit); ok {
						guarded[lit] = true
					}
					return true
				})
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || guarded[lit] {
				return true
			}
			sql, err := strconv.Unquote(lit.Value)
			if err != nil || !ownershipIdentities.MatchString(sql) || !ownershipUserID.MatchString(sql) || ownershipPersonal.MatchString(sql) {
				return true
			}
			for key := range ownershipGateAllowlist {
				file, needle, _ := strings.Cut(key, ": ")
				if file == base && strings.Contains(sql, needle) {
					used[key] = true
					return true
				}
			}
			t.Errorf("%s: identity ownership predicate without kind='personal': %s", fset.Position(lit.Pos()), strings.Join(strings.Fields(sql), " "))
			return true
		})
	}
	for key := range ownershipGateAllowlist {
		if !used[key] {
			t.Errorf("stale ownership allowlist entry: %s", key)
		}
	}
}

func callsIdentityAccess(n ast.Node) (found bool) {
	ast.Inspect(n, func(m ast.Node) bool {
		if call, ok := m.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "identityAccessSQL" {
				found = true
			}
		}
		return !found
	})
	return found
}
