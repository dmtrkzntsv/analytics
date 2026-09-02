// Package archtest pins the package graph described in CLAUDE.md
// ("Layout"): leaves import only leaves, manage sits above the leaves,
// the surfaces (server, mcpserver, jobs, pipeline, dashboards) import
// leaves and manage but never each other, and internal/app is the only
// package that wires the surfaces together. cmd/ is unconstrained.
//
// Every package under internal/ must appear in the rank table; an
// unlisted one fails the test, so a new package is placed deliberately
// rather than landing wherever its first import happens to point.
package archtest

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/dmtrkzntsv/twillingate/"

// rank orders the layers. A package may import a package of strictly
// lower rank, or — for leaves only — another leaf. Nothing imports
// internal/app except cmd/.
var rank = map[string]int{
	"docs":                       0,
	"internal/civil":             0,
	"internal/version":           0,
	"internal/config":            0,
	"internal/config/configtest": 0,
	"internal/enrich":            0,
	"internal/identity":          0,
	"internal/geo":               0,
	"internal/store":             0,
	"internal/store/sqlite":      0,
	"internal/manage":            1,
	"internal/pipeline":          2,
	"internal/jobs":              2,
	"internal/server":            2,
	"internal/mcpserver":         2,
	"internal/dashboards":        2,
	"internal/app":               3,
}

// unconstrained packages may import anything: the binary, this test, and
// the smoke tooling.
func unconstrained(rel string) bool {
	return strings.HasPrefix(rel, "cmd/") || strings.HasPrefix(rel, "scripts/") ||
		rel == "internal/archtest"
}

// violations applies the rule to a production import graph keyed by
// module-relative package path. Split from the go-list plumbing so the
// rule itself is unit-tested below with a synthetic graph.
func violations(imports map[string][]string) []string {
	var out []string
	for pkg, deps := range imports {
		if unconstrained(pkg) {
			continue
		}
		r, known := rank[pkg]
		if !known {
			out = append(out, pkg+": not in the rank table; place it in a layer (see CLAUDE.md Layout)")
			continue
		}
		for _, dep := range deps {
			dr, known := rank[dep]
			switch {
			case !known:
				out = append(out, pkg+" imports "+dep+", which is not in the rank table")
			case dr < r:
			case r == 0 && dr == 0:
			default:
				out = append(out, pkg+" imports "+dep+": a layer may only import layers below it")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestRuleRejectsUpwardAndSidewaysImports(t *testing.T) {
	cases := map[string]struct {
		graph map[string][]string
		want  string
	}{
		"surface imports surface": {
			graph: map[string][]string{"internal/jobs": {"internal/mcpserver"}},
			want:  "internal/jobs imports internal/mcpserver",
		},
		"leaf imports manage": {
			graph: map[string][]string{"internal/store": {"internal/manage"}},
			want:  "internal/store imports internal/manage",
		},
		"surface imports app": {
			graph: map[string][]string{"internal/server": {"internal/app"}},
			want:  "internal/server imports internal/app",
		},
		"unlisted package": {
			graph: map[string][]string{"internal/newthing": nil},
			want:  "internal/newthing: not in the rank table",
		},
	}
	for name, c := range cases {
		got := violations(c.graph)
		if len(got) != 1 || !strings.HasPrefix(got[0], c.want) {
			t.Errorf("%s: violations = %q, want one starting %q", name, got, c.want)
		}
	}
	ok := map[string][]string{
		"internal/store/sqlite": {"internal/store"},
		"internal/manage":       {"internal/config", "internal/store"},
		"internal/jobs":         {"internal/civil", "internal/manage", "internal/store"},
		"internal/app":          {"internal/server", "internal/mcpserver", "internal/jobs"},
		"cmd/twillingate":       {"internal/app", "internal/store/sqlite"},
	}
	if got := violations(ok); len(got) != 0 {
		t.Errorf("allowed graph reported violations: %q", got)
	}
}

// listImports returns production (non-test) module-internal imports for
// every package in the module, via go list so build constraints resolve
// exactly as go build does.
func listImports(t *testing.T) map[string][]string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}|{{join .Imports \" \"}}", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	imports := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, deps, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		rel, ok := strings.CutPrefix(pkg, module)
		if !ok || strings.Contains(rel, "/node_modules/") {
			continue // some npm packages ship Go source; not ours
		}
		var internal []string
		for _, d := range strings.Fields(deps) {
			if drel, ok := strings.CutPrefix(d, module); ok {
				internal = append(internal, drel)
			}
		}
		imports[rel] = internal
	}
	if len(imports) < 15 {
		t.Fatalf("go list scanned only %d packages; the scan is broken, not the graph", len(imports))
	}
	return imports
}

func TestPackageGraph(t *testing.T) {
	for _, v := range violations(listImports(t)) {
		t.Error(v)
	}
}
