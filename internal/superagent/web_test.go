package superagent_test

import (
	"strings"
	"testing"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

func TestNormalizeWebURLAndContentHash(t *testing.T) {
	canonical, err := superagent.CanonicalWebURL("HTTPS://Example.COM:443/a/../b/?b=2&a=1#frag")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "https://example.com/b/?a=1&b=2" {
		t.Fatalf("canonical=%q", canonical)
	}
	origin, err := superagent.WebOrigin(canonical)
	if err != nil || origin != "https://example.com" {
		t.Fatalf("origin=%q err=%v", origin, err)
	}
	first := superagent.WebContentHash("  第一行\n\n第二行  ")
	second := superagent.WebContentHash("第一行 第二行")
	if first != second {
		t.Fatalf("hash must ignore whitespace: %s %s", first, second)
	}
}

func TestCheckWebURLEnforcesOriginAndPath(t *testing.T) {
	origins := []string{"https://example.com"}
	prefixes := []string{"/items/"}
	if _, err := superagent.CheckWebURL("https://example.com/items/42", origins, prefixes); err != nil {
		t.Fatalf("allowed url rejected: %v", err)
	}
	if _, err := superagent.CheckWebURL("https://evil.example.com/items/42", origins, prefixes); err == nil {
		t.Fatal("foreign origin accepted")
	}
	if _, err := superagent.CheckWebURL("https://example.com/private/42", origins, prefixes); err == nil {
		t.Fatal("foreign path accepted")
	}
	if _, err := superagent.CheckWebURL("https://example.com/items-other/42", origins, []string{"/items"}); err == nil {
		t.Fatal("path boundary not enforced")
	}
}

func TestWebThreadExternalIDStaysWithinLimit(t *testing.T) {
	canonical := "https://example.com/items/" + strings.Repeat("x", 400)
	key := superagent.WebThreadExternalID("site", "account", canonical)
	if len(key) > 300 || !strings.HasPrefix(key, "web:site:account:") {
		t.Fatalf("key=%q len=%d", key, len(key))
	}
}

func TestWebSiteIDFromTriggerPayload(t *testing.T) {
	trigger := &store.Trigger{Payload: []byte(`{"site_id":"qqmail","account":"me"}`)}
	if got := superagent.WebSiteID(trigger); got != "qqmail" {
		t.Fatalf("site=%q", got)
	}
	if got := superagent.WebAccount(trigger); got != "me" {
		t.Fatalf("account=%q", got)
	}
}
