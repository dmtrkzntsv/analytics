package mcpserver

// Drift tripwires: the docs resources describe internal/server's actual
// behaviour, so their load-bearing facts are asserted against that
// package's source. Change the SDK or the reserved-key set without the
// docs and these tests fail.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (run tests from the package dir): %v", path, err)
	}
	return string(b)
}

// TestDocsEventsMatchesReservedKeys extracts the reservedKeys map from
// ingest.go and requires exact two-way agreement with docs://events.
func TestDocsEventsMatchesReservedKeys(t *testing.T) {
	src := readSource(t, "../../internal/server/ingest.go")
	re := regexp.MustCompile(`"(\$[a-z_]+)":\s*func\(`)
	inCode := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		inCode[m[1]] = true
	}
	if len(inCode) < 10 {
		t.Fatalf("extracted only %d reserved keys from ingest.go — extraction regexp broken?", len(inCode))
	}
	for k := range inCode {
		if !strings.Contains(docsEvents, k) {
			t.Errorf("reserved key %s exists in ingest.go but is missing from docs://events", k)
		}
	}
	// reverse: every $key the doc's complete list names must exist in code
	listRe := regexp.MustCompile(`\$[a-z_]+`)
	section := docsEvents[strings.Index(docsEvents, "Reserved attribute keys"):]
	for _, k := range listRe.FindAllString(section, -1) {
		if !inCode[k] {
			t.Errorf("docs://events lists %s but ingest.go has no such reserved key", k)
		}
	}
	// reserved event names
	for _, name := range []string{"$pageview", "$screen_view"} {
		if !strings.Contains(src, `"`+name+`"`) {
			t.Errorf("event name %s documented but not found in ingest.go", name)
		}
		if !strings.Contains(docsEvents, name) {
			t.Errorf("event name %s missing from docs://events", name)
		}
	}
}

// TestDocsJSSDKMatchesSDK asserts every documented API symbol exists in
// the SDK source (sdk/src/twillingate.ts — the shipped bundle is compiled
// from it and separately drift-checked in CI).
func TestDocsJSSDKMatchesSDK(t *testing.T) {
	src := readSource(t, "../../sdk/src/twillingate.ts")
	for _, symbol := range []string{
		"data-key", "data-identity", "data-user", "data-group", "data-auto",
		"init", "page", "screen", "track", "attrs", "identify", "group", "reset", "flush",
		"twillingate_ignore", "analytics_ignore",
		"pushState", "popstate", "sendBeacon",
		"$pageview", "$screen_view", "$install_id",
	} {
		if !strings.Contains(src, symbol) {
			t.Errorf("docs://js-sdk documents %q but the SDK source does not contain it", symbol)
		}
		if !strings.Contains(docsJSSDK, symbol) && symbol != "popstate" && symbol != "sendBeacon" {
			t.Errorf("%q missing from docs://js-sdk text", symbol)
		}
	}
	// The legacy snippet is gone; the doc must tell a stale site what to do.
	if !strings.Contains(docsJSSDK, "script.js") {
		t.Error("docs://js-sdk must tell sites still on /js/script.js how to migrate")
	}
}
