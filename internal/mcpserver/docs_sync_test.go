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

// TestDocumentNamesEveryTool binds the tool tables in docs/twillingate.md to
// the tools actually registered. A tool nobody documents is one an agent
// never reaches for; a documented tool that does not exist is a failed call.
func TestDocumentNamesEveryTool(t *testing.T) {
	registered := map[string]bool{}
	for _, f := range []string{
		"tools_read.go", "tools_product.go", "tools_manage.go",
		"query.go", "guide.go",
	} {
		src := readSource(t, f)
		for _, m := range regexp.MustCompile(`mcp\.Tool\{Name:\s*"([a-z_]+)"`).FindAllStringSubmatch(src, -1) {
			registered[m[1]] = true
		}
	}
	if len(registered) < 15 {
		t.Fatalf("extracted only %d tools — extraction regexp broken?", len(registered))
	}
	// Scoped to the tool tables and the "Managing" list, not the whole
	// document: `identities` is also a table name in the views prose, so a
	// document-wide Contains would pass for the wrong reason.
	documented := documentedTools()
	for name := range registered {
		if !documented[name] {
			t.Errorf("tool %s is registered but not listed in a tool table in docs/twillingate.md", name)
		}
	}
	// No reverse check: these table rows also carry parameter names and
	// dimension values, so "documented but not registered" is noise. The
	// count assertion below is what catches a tool that quietly went away.
	// The document states a count; keep it honest.
	if !strings.Contains(docs.Twillingate, spellOut(len(registered))+" tools") {
		t.Errorf("docs/twillingate.md does not say %q tools (there are %d)",
			spellOut(len(registered)), len(registered))
	}
}

// documentedTools reads tool names out of the document's tables and its
// "Managing" list — the places that claim a tool exists, as opposed to
// prose that may mention the same word for something else.
func documentedTools() map[string]bool {
	out := map[string]bool{}
	tick := regexp.MustCompile("`([a-z][a-z_]+)`")
	for _, line := range strings.Split(docs.Twillingate, "\n") {
		managing := strings.HasPrefix(line, "**Managing**") ||
			strings.HasPrefix(line, "`restore_project`") ||
			strings.HasPrefix(line, "`enable_ingest_key`")
		if !strings.HasPrefix(line, "|") && !managing {
			continue
		}
		for _, m := range tick.FindAllStringSubmatch(line, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// spellOut covers the range the tool count plausibly moves through. A count
// outside it returns "" so the assertion above fails loudly rather than
// silently passing on a substring that happens to match.
func spellOut(n int) string {
	words := map[int]string{
		15: "fifteen", 16: "sixteen", 17: "seventeen", 18: "eighteen",
		19: "nineteen", 20: "twenty", 21: "twenty-one", 22: "twenty-two",
		23: "twenty-three", 24: "twenty-four", 25: "twenty-five",
	}
	return words[n]
}
