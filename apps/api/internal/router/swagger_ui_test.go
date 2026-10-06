package router

import (
	"regexp"
	"testing"
)

// Every third-party Swagger UI asset must be an exact version with an SRI hash,
// so a compromised or moved CDN tag cannot run script on the docs origin.
func TestSwaggerUIAssetsArePinnedWithSRI(t *testing.T) {
	tags := regexp.MustCompile(`<(?:script|link)[^>]+(?:src|href)="https://[^"]+"[^>]*>`).FindAllString(swaggerUIHTML, -1)
	if len(tags) != 3 {
		t.Fatalf("expected 3 CDN assets, found %d", len(tags))
	}
	pinned := regexp.MustCompile(`swagger-ui-dist@\d+\.\d+\.\d+/`)
	sri := regexp.MustCompile(`integrity="sha384-[A-Za-z0-9+/]{64}"`)
	for _, tag := range tags {
		if !pinned.MatchString(tag) || !sri.MatchString(tag) || !regexp.MustCompile(`crossorigin="anonymous"`).MatchString(tag) {
			t.Errorf("asset is not pinned with SRI: %s", tag)
		}
	}
}
