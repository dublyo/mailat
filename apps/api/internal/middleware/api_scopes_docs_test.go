package middleware

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/apidocs"
)

const scopeDocPath = "../../../../docs/api-automation.md"

// scopeTableScopes returns every backticked scope in the "| Work | Scopes |"
// table of docs/api-automation.md (the rows up to the first blank line) and
// the prose that follows the table up to the next heading.
func scopeTableScopes(doc string) (map[string]bool, string, bool) {
	start := strings.Index(doc, "| Work | Scopes |")
	if start < 0 {
		return nil, "", false
	}
	rest := doc[start:]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		end = len(rest)
	}
	scopes := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z]+:[a-z]+)`").FindAllStringSubmatch(rest[:end], -1) {
		scopes[m[1]] = true
	}
	after := rest[end:]
	if h := strings.Index(after, "\n#"); h >= 0 {
		after = after[:h]
	}
	return scopes, after, true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestAPIScopeDocsTableMatchesAPIKeyPermissions(t *testing.T) {
	b, err := os.ReadFile(filepath.FromSlash(scopeDocPath))
	if err != nil {
		t.Fatal(err)
	}
	documented, _, ok := scopeTableScopes(string(b))
	if !ok {
		t.Fatal("docs/api-automation.md has no | Work | Scopes | table")
	}
	if got, want := sortedKeys(documented), sortedKeys(APIKeyPermissions); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scope table lists %v; APIKeyPermissions has %v", got, want)
	}
}

// A deleted row must fail the comparison above.
func TestAPIScopeDocsTableMutation(t *testing.T) {
	b, err := os.ReadFile(filepath.FromSlash(scopeDocPath))
	if err != nil {
		t.Fatal(err)
	}
	var mutated []string
	removed := false
	for _, line := range strings.Split(string(b), "\n") {
		if !removed && strings.Contains(line, "`campaigns:manage`") && strings.HasPrefix(line, "|") {
			removed = true
			continue
		}
		mutated = append(mutated, line)
	}
	if !removed {
		t.Fatal("no campaigns:manage row to delete")
	}
	documented, _, _ := scopeTableScopes(strings.Join(mutated, "\n"))
	if len(documented) == len(APIKeyPermissions) {
		t.Fatal("deleting a row did not change the documented scope set")
	}
}

// Every top-level route group that no API key scope reaches must be named in
// the prose after the table, so readers know an API key gets 403 there.
func TestAPIScopeDocsNameScopelessRouteGroups(t *testing.T) {
	b, err := os.ReadFile(filepath.FromSlash(scopeDocPath))
	if err != nil {
		t.Fatal(err)
	}
	_, prose, ok := scopeTableScopes(string(b))
	if !ok {
		t.Fatal("no scope table")
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Security []interface{} `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(apidocs.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	param := regexp.MustCompile(`\{(\w+)\}`)
	scoped, unscoped := map[string]bool{}, map[string]bool{}
	for path, ops := range spec.Paths {
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
		group := "/" + segments[0]
		route := param.ReplaceAllString(path, ":$1")
		for method, op := range ops {
			if len(op.Security) == 0 {
				continue // public route
			}
			if _, ok := APIKeyScope(strings.ToUpper(method), route); ok {
				scoped[group] = true
			} else {
				unscoped[group] = true
			}
		}
	}
	if len(unscoped) == 0 {
		t.Fatal("no scopeless route groups found; check the OpenAPI parse")
	}
	for group := range unscoped {
		if scoped[group] {
			continue // mixed groups are described route by route
		}
		if !strings.Contains(prose, "`"+group+"`") && !strings.Contains(prose, "`"+group+"/") {
			t.Errorf("scopeless route group %s is not named after the scope table", group)
		}
	}
}
