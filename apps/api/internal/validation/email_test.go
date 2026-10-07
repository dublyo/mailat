package validation

import (
	"context"
	"testing"

	"github.com/gogf/gf/v2/util/gvalid"
)

func TestEmailRuleAcceptsPlusAndRejectsJunk(t *testing.T) {
	type req struct {
		Email string `v:"required|email"`
		Opt   string `v:"email"`
	}
	for _, ok := range []string{"name+tag@gmail.com", "a.b-c_d@example.co.uk", "o'neil@example.com", "x@sub.example.com"} {
		if err := gvalid.New().Data(req{Email: ok}).Run(context.Background()); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"plain", "a@b", "a b@example.com", "Name <a@example.com>", "a@example.com.", "@example.com", "a@.example.com"} {
		if err := gvalid.New().Data(req{Email: bad}).Run(context.Background()); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := gvalid.New().Data(req{Email: "a@example.com"}).Run(context.Background()); err != nil {
		t.Errorf("empty optional email rejected: %v", err)
	}
	if err := gvalid.New().Data(req{}).Run(context.Background()); err == nil {
		t.Error("missing required email accepted")
	}
}
