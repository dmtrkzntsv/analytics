package mcpserver

// Drift tripwires: docs/twillingate.md is the single normative document and
// the only prose the MCP endpoint serves, so its load-bearing facts are
// asserted against the source they describe. Change the reserved-key set or
// the SDK's public surface without the document and these tests fail.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
)

// documentedKeys reads the reserved attribute key table out of the
// document: the rows between its heading and the next one.
func documentedKeys(t *testing.T) []string {
	t.Helper()
	const heading = "### Reserved attribute keys"
	i := strings.Index(docs.Twillingate, heading)
	if i < 0 {
		t.Fatal("docs/twillingate.md has no '### Reserved attribute keys' section")
	}
	section := docs.Twillingate[i+len(heading):]
	if j := strings.Index(section, "\n### "); j >= 0 {
		section = section[:j]
	}
	var keys []string
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		keys = append(keys, regexp.MustCompile("`(\\$[a-z_]+)`").FindAllString(line, -1)...)
	}
	if len(keys) < 10 {
		t.Fatalf("found only %d keys in the reserved-key table — did its shape change?", len(keys))
	}
	for i, k := range keys {
		keys[i] = strings.Trim(k, "`")
	}
	return keys
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (run tests from the package dir): %v", path, err)
	}
	return string(b)
}

// removedKeys are documented deliberately as NO LONGER existing, so the
// reverse check below must not treat them as stale. Keeping the note is the
// point: a stale client sending $url needs to find out why it is rejected.
var removedKeys = map[string]bool{"$url": true}

// TestDocumentMatchesReservedKeys extracts the reservedKeys map from
// ingest.go and requires two-way agreement with docs/twillingate.md. An
// undocumented key is a contract an agent cannot discover; a documented key
// that does not exist is one it will waste a round trip on.
func TestDocumentMatchesReservedKeys(t *testing.T) {
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
		if !strings.Contains(docs.Twillingate, k) {
			t.Errorf("reserved key %s exists in ingest.go but is missing from docs/twillingate.md", k)
		}
	}
	// Reverse: every $key the document's authoritative table lists must
	// exist in code. Scoped to that table's rows rather than the whole
	// document, which also contains shell variables ($base), illustrative
	// warnings ($app_ver) and hypothetical future names ($session_start).
	for _, k := range documentedKeys(t) {
		if inCode[k] || removedKeys[k] {
			continue
		}
		t.Errorf("the reserved-key table lists %s, which ingest.go does not define", k)
	}
	for _, name := range []string{"$pageview", "$screen_view"} {
		if !strings.Contains(src, `"`+name+`"`) {
			t.Errorf("event name %s documented but not found in ingest.go", name)
		}
		if !strings.Contains(docs.Twillingate, name) {
			t.Errorf("event name %s missing from docs/twillingate.md", name)
		}
	}
	// A key the server removed must stay documented as removed, so a stale
	// client can find out why its pageviews are rejected.
	for k := range removedKeys {
		if inCode[k] {
			t.Errorf("%s is in reservedKeys again — drop it from removedKeys", k)
		}
		if !strings.Contains(docs.Twillingate, k) {
			t.Errorf("docs/twillingate.md must keep explaining that %s was removed", k)
		}
	}
}

// TestDocumentMatchesSDK asserts every documented SDK symbol exists in the
// source (sdk/src/twillingate.ts — the shipped bundle is compiled from it
// and separately drift-checked in CI).
func TestDocumentMatchesSDK(t *testing.T) {
	src := readSource(t, "../../sdk/src/twillingate.ts")
	for _, symbol := range []string{
		"data-key", "data-identity", "data-user", "data-group", "data-auto",
		"data-mask-url", "data-routing",
		"init", "page", "screen", "track", "attrs", "identify", "group", "reset", "flush",
		"twillingate_ignore", "analytics_ignore",
		"pushState", "popstate", "hashchange",
		"$pageview", "$screen_view", "$install_id",
	} {
		if !strings.Contains(src, symbol) {
			t.Errorf("docs/twillingate.md documents %q but the SDK source does not contain it", symbol)
		}
		if !strings.Contains(docs.Twillingate, symbol) {
			t.Errorf("%q missing from docs/twillingate.md", symbol)
		}
	}
	// util is the documented namespace for the path helpers.
	for _, symbol := range []string{"maskIds", "withQuery"} {
		if !strings.Contains(readSource(t, "../../sdk/src/util.ts"), symbol) {
			t.Errorf("util.%s documented but missing from sdk/src/util.ts", symbol)
		}
		if !strings.Contains(docs.Twillingate, symbol) {
			t.Errorf("util.%s missing from docs/twillingate.md", symbol)
		}
	}
	// The legacy snippet is gone; the document must tell a stale site what
	// to do rather than leaving it to guess.
	if !strings.Contains(docs.Twillingate, "script.js") {
		t.Error("docs/twillingate.md must tell sites still on /js/script.js how to migrate")
	}
}

// TestDocumentCoversEveryWebDimension keeps the query guidance honest: a new
// breakdown dimension that nobody documents is one an agent never uses.
func TestDocumentCoversEveryWebDimension(t *testing.T) {
	for _, d := range webDimensions {
		if !strings.Contains(docs.Twillingate, d.view) {
			t.Errorf("view %s is queryable but not named in docs/twillingate.md", d.view)
		}
		if !strings.Contains(schemaViews, d.view) {
			t.Errorf("view %s is queryable but not named in schema://views", d.view)
		}
	}
}
