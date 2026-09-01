package dashboards

// Drift tripwire for the Evidence project's prerender entries.
//
// SvelteKit only prerenders a templated route that something names: its
// crawler cannot reach /web/<project> or /web/<project>/page, because the
// links to them come out of queries Evidence resolves in the browser. Every
// templated route therefore needs an entry in evidence/svelte.config.js, and
// a route without one fails `evidence build` outright — for every project and
// for none, so no amount of data hides it. The config derives the entries by
// walking pages/; this checks that what it derives still covers the tree.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const evidenceDir = "../../evidence"

// templatedPageRoutes walks the pages directory the way SvelteKit maps it to
// routes, and keeps the ones that take a parameter.
func templatedPageRoutes(t *testing.T) []string {
	t.Helper()
	pages := filepath.Join(evidenceDir, "pages")
	var routes []string
	err := filepath.WalkDir(pages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		rel, err := filepath.Rel(pages, path)
		if err != nil {
			return err
		}
		route := "/" + filepath.ToSlash(strings.TrimSuffix(rel, ".md"))
		route = strings.TrimSuffix(route, "/index")
		if strings.Contains(route, "[") {
			routes = append(routes, route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", pages, err)
	}
	if len(routes) == 0 {
		t.Fatalf("no templated pages under %s — did the tree move?", pages)
	}
	return routes
}

// prerenderEntries evaluates svelte.config.js and returns kit.prerender.entries.
// The config imports nothing but node builtins, so this needs a node binary
// and not an npm install.
func prerenderEntries(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; skipping the prerender entry check")
	}
	// Read the config here as well as in node. `go test` keys its cache on the
	// files the test itself opens and cannot see through a subprocess, so
	// without this read an edit to svelte.config.js replays a stale pass — the
	// tripwire would go on reporting the config it checked yesterday.
	config := filepath.Join(evidenceDir, "svelte.config.js")
	if _, err := os.ReadFile(config); err != nil {
		t.Fatalf("reading %s: %v", config, err)
	}
	const script = `import c from './svelte.config.js';
		process.stdout.write(JSON.stringify(c.kit.prerender.entries))`
	cmd := exec.Command("node", "--input-type=module", "-e", script)
	cmd.Dir = evidenceDir
	out, err := cmd.Output()
	if err != nil {
		// The config throws for a route it cannot fill, and that message is
		// the whole diagnosis — it goes to stderr, which Output keeps here.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("evaluating svelte.config.js: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("evaluating svelte.config.js: %v", err)
	}
	var entries []string
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("entries are not a string array (%s): %v", out, err)
	}
	return entries
}

func TestEveryTemplatedRouteHasAPrerenderEntry(t *testing.T) {
	entries := prerenderEntries(t)
	for _, route := range templatedPageRoutes(t) {
		// /web/[project]/page is satisfied by an entry for any one project.
		pattern := regexp.MustCompile(`\\\[[^\]]*\\\]`).ReplaceAllString(regexp.QuoteMeta(route), `[^/]+`)
		match := regexp.MustCompile("^" + pattern + "$")
		found := false
		for _, entry := range entries {
			if match.MatchString(entry) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no prerender entry for %s; evidence build will fail on it\nentries: %v", route, entries)
		}
	}
}

// The static routes ride on '*', which SvelteKit expands to every route that
// takes no parameter. Dropping it would silently stop prerendering index.
func TestPrerenderEntriesKeepTheWildcard(t *testing.T) {
	for _, entry := range prerenderEntries(t) {
		if entry == "*" {
			return
		}
	}
	t.Error("svelte.config.js dropped the '*' entry; static pages will not prerender")
}
