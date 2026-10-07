// Package validation adjusts GoFrame's request validation rules.
package validation

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/gogf/gf/v2/util/gvalid"
)

// GoFrame's built-in `email` rule only allows [A-Za-z0-9_.-] before the @, so
// valid addresses such as name+tag@example.com were rejected. A custom rule
// with the same name takes precedence for every `v:"email"` tag.
func init() {
	gvalid.RegisterRule("email", func(_ context.Context, in gvalid.RuleFuncInput) error {
		value := in.Value.String()
		// Custom rules also run for empty values; presence is `required`'s job.
		if value == "" || ValidEmail(value) {
			return nil
		}
		msg := in.Message
		if msg == "" {
			msg = "The value `" + value + "` is not a valid email address"
		}
		return errors.New(msg)
	})
}

// ValidEmail reports whether s is a bare RFC 5322 addr-spec with a dotted domain.
func ValidEmail(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, " \t\r\n<>") {
		return false
	}
	parsed, err := mail.ParseAddress(s)
	if err != nil || parsed.Name != "" || parsed.Address != s {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	if at < 1 || at > 64 {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}
