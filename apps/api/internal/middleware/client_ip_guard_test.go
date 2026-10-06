package middleware

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GoFrame's GetClientIp trusts forwarding headers from any peer. Every caller
// must use ClientIP so the trusted-proxy boundary is applied consistently.
func TestNoDirectGetClientIp(t *testing.T) {
	root := ".." // internal/
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "GetClientIp(") {
			t.Errorf("%s calls GetClientIp; use middleware.ClientIP", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
