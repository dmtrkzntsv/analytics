# Single `twillingate.md` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse seven prose docs and two hand-written Go string constants into one embedded `docs/twillingate.md`, served as `docs://twillingate`.

**Architecture:** `docs/twillingate.md` is authored as one document in a deliberate reading order, embedded via `//go:embed` in the existing `docs` package, and exposed as a single MCP resource. The absorbed files become short pointers so existing links and source comments still resolve. `docs_sync_test.go` is repointed to bind the document to `reservedKeys`, and `CLAUDE.md` gains the maintenance rule no test can enforce.

**Tech Stack:** Go `//go:embed`, MCP Go SDK resources, Markdown.

**Spec:** `docs/superpowers/specs/2026-08-30-host-path-split-design.md` (§7)

## Global Constraints

- **Run this plan LAST**, after `2026-08-30-host-path-wire.md` and `2026-08-30-sdk-masking-routing.md` have landed. The document must describe the finished contract, not be written twice (spec §7.1).
- **`docs/plausible/README.md` is NOT absorbed.** It documents the bytes served at `/js/plausible-shim.js` and `TestPlausibleShimServed` binds the two.
- **`schema://views` stays a separate resource.** It is the hot path for agents writing SQL; making them fetch a 1,400-line manual for a column list is a regression.
- **Absorbed files become pointers, not deletions.** `internal/server/twillingate.ts` cites `docs/ingest-api.md` as the normative wire format, and README links must keep resolving.
- **A `docs`-only push publishes nothing** (`paths-ignore` on `docs/**`), so same-commit doc updates cost no release. Commit type `docs`.

---

### Task 1: write `docs/twillingate.md`

**Files:**
- Create: `docs/twillingate.md`
- Read (as sources): `docs/deployment.md`, `docs/litestream.md`, `docs/configuration.md`, `docs/sdk.md`, `docs/ingest-api.md`, `docs/mcp-auth.md`, `docs/mcp-clients.md`, `internal/mcpserver/docs_content.go` (`docsEvents`)

**Interfaces:**
- Consumes: nothing.
- Produces: `docs/twillingate.md`.

- [ ] **Step 1: Assemble in the spec's reading order**

The document is one path, not a stapled bundle. Top-level `##` sections, in this order:

| section | source |
| --- | --- |
| What twillingate is | new — 2 paragraphs: cookieless web + product + app analytics, one SQLite file, one binary |
| Install | `deployment.md` §2 |
| Configure | `configuration.md` (env vars, projects, ingest keys, low-resource hosts) |
| Instrument a website | `sdk.md` (snippet mode, masking, routing, `util`) |
| Instrument anything else | `ingest-api.md` (the normative wire format) |
| The event model | `docsEvents` (families, reserved names, reserved keys) |
| Connect an MCP client | `mcp-auth.md` then `mcp-clients.md` |
| Query the data | how to use `query`, pointing at `schema://views` for columns |
| Dashboards | `deployment.md` §3 |
| Operate and recover | `deployment.md` §4–§7, `litestream.md` |

- [ ] **Step 2: Reconcile against the shipped code**

The absorbed docs predate the wire change. Every one of these must describe the *current* contract, not the old one:

- `$pageview` takes `$host` and `$path`; `$url` does not exist.
- UTMs travel as `$utm_source`, `$utm_medium`, `$utm_campaign`.
- There is no `?ref=` referrer fallback.
- `/js/script.js` returns 404.
- Examples carry `data-identity` explicitly, and no `data-user`.
- `data-mask-url` and `data-routing` are documented with the three spec forms.

Verify each claim against the source rather than trusting the old prose:

Run: `grep -n '"\$' internal/server/ingest.go`
Expected: exactly the reserved keys the document lists.

- [ ] **Step 3: Verify every command in the document actually runs**

Any shell command copied from `deployment.md` or `litestream.md` that can be run safely here, run. Mark anything requiring R2 credentials or a live VPS as untested rather than asserting it works.

- [ ] **Step 4: Commit**

```bash
git add docs/twillingate.md
git commit -m "docs: write the single normative twillingate.md"
```

---

### Task 2: embed and serve it

**Files:**
- Modify: `docs/embed.go`
- Modify: `internal/mcpserver/resources.go:60-72`
- Modify: `internal/mcpserver/docs_content.go`
- Test: `internal/mcpserver/resources_test.go`

**Interfaces:**
- Consumes: `docs/twillingate.md` (Task 1).
- Produces: `docs.Twillingate string`; MCP resource `docs://twillingate`. Removes `docs://events`, `docs://js-sdk`, `docs://ingest-api`, and the constants `docsEvents`, `docsJSSDK`.

- [ ] **Step 1: Write the failing test**

Append to `internal/mcpserver/resources_test.go` (read a neighbouring resource test for the read idiom):

```go
func TestTwillingateResourceServed(t *testing.T) {
	h := newTestHost(t)
	s := h.mcpServer(t)
	got := readResource(t, s, "docs://twillingate")
	for _, want := range []string{
		"$pageview", "$path", "$host", "data-mask-url", "/api/events",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("docs://twillingate missing %q", want)
		}
	}
	if strings.Contains(got, "$url") {
		t.Error("docs://twillingate must not document the removed $url")
	}
}

// The three resources it replaces must be gone, so a client cannot read a
// stale contract from a URI that still resolves.
func TestOldDocResourcesRemoved(t *testing.T) {
	h := newTestHost(t)
	s := h.mcpServer(t)
	for _, uri := range []string{"docs://events", "docs://js-sdk", "docs://ingest-api"} {
		if _, err := readResourceErr(t, s, uri); err == nil {
			t.Errorf("%s still resolves", uri)
		}
	}
}
```

If `readResource`/`readResourceErr`/`mcpServer` do not exist, write them once as local helpers over the existing test patterns in that file.

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/mcpserver/ -run TestTwillingateResource -v`
Expected: FAIL — resource not found.

- [ ] **Step 3: Embed the document**

`docs/embed.go`:

```go
//go:embed twillingate.md
var Twillingate string
```

Keep `IngestAPI` for now — Task 3 removes it once nothing reads it.

- [ ] **Step 4: Register one resource, remove three**

`internal/mcpserver/resources.go`, in `registerResources`, replace the three `textResource` calls with:

```go
	textResource(s, "docs://twillingate", "twillingate",
		"Everything needed to run twillingate: install, configure, instrument a site or app, connect an MCP client, query, operate and recover. The normative wire format and event model live here too. Read this before instrumenting or integrating anything.",
		docs.Twillingate)
```

Leave the `schema://views` registration untouched.

- [ ] **Step 5: Delete the superseded constants**

`internal/mcpserver/docs_content.go` — delete `docsEvents` and `docsJSSDK` and the file's provenance comment describing them. If nothing is left, delete the file.

- [ ] **Step 6: Run the tests to make sure they pass**

Run: `go test ./internal/mcpserver/`
Expected: FAIL in `docs_sync_test.go`, which still references the deleted constants. That is Task 4 — leave it failing and proceed; do not stub the constants back.

- [ ] **Step 7: Commit**

```bash
git add docs/embed.go internal/mcpserver/
git commit -m "docs: serve one docs://twillingate resource"
```

---

### Task 3: turn the absorbed files into pointers

**Files:**
- Modify: `docs/deployment.md`, `docs/litestream.md`, `docs/configuration.md`, `docs/sdk.md`, `docs/ingest-api.md`, `docs/mcp-auth.md`, `docs/mcp-clients.md`
- Modify: `docs/embed.go`
- Modify: `README.md`

- [ ] **Step 1: Replace each file's body with a pointer**

Each of the seven becomes exactly this, with the section name filled in:

```markdown
# Deployment

This document has moved. Installing, upgrading and recovering a
twillingate deployment is covered in [twillingate.md](twillingate.md),
under **Install** and **Operate and recover**.

`docs/twillingate.md` is the single normative document — do not add content
here.
```

- [ ] **Step 2: Drop the now-unused `IngestAPI` embed**

`docs/embed.go` — `IngestAPI` embedded `ingest-api.md`, which is now a pointer. Delete the variable and its `//go:embed` line.

Run: `grep -rn 'docs.IngestAPI' --include=*.go .`
Expected: no matches. If any remain, they are stale readers — remove them.

- [ ] **Step 3: Update README links**

Run: `grep -n 'docs/' README.md` and repoint every link to `docs/twillingate.md`, keeping `docs/plausible/` as-is.

- [ ] **Step 4: Verify nothing references a moved doc as a source**

Run: `grep -rn 'docs/sdk.md\|docs/ingest-api.md\|docs/mcp-auth.md\|docs/mcp-clients.md\|docs/configuration.md\|docs/litestream.md\|docs/deployment.md' --include=*.go --include=*.ts --include=*.md . | grep -v 'docs/superpowers'`
Expected: only pointer files and README links, no source comment claiming one of them is normative. Update `sdk/src/twillingate.ts`'s header comment, which cites `docs/ingest-api.md` as the normative wire format.

- [ ] **Step 5: Build and commit**

Run: `go build ./... && go test ./internal/mcpserver/ -run TestTwillingate`

```bash
git add docs/ README.md sdk/
git commit -m "docs: point the absorbed docs at twillingate.md"
```

---

### Task 4: bind the document to `reservedKeys`

**Files:**
- Modify: `internal/mcpserver/docs_sync_test.go`

**Interfaces:**
- Consumes: `docs.Twillingate` (Task 2).
- Produces: a test that fails when a reserved key is added without documenting it.

- [ ] **Step 1: Replace the file's contents**

The old tests bound `docsEvents`/`docsJSSDK` to source files. Bind the document to the map instead — a stronger check, and the one that would have caught `$host`/`$path` going undocumented:

```go
package mcpserver

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
)

// reservedKeys in internal/server/ingest.go is the machine-readable truth
// for the wire contract. docs/twillingate.md is what an agent reads before
// instrumenting anything. Bind them in both directions: an undocumented key
// is a contract an agent cannot discover, and a documented key that does not
// exist is one it will waste a round trip on.
func TestTwillingateDocumentsEveryReservedKey(t *testing.T) {
	src, err := os.ReadFile("../server/ingest.go")
	if err != nil {
		t.Fatal(err)
	}
	keys := regexp.MustCompile(`"(\$[a-z_]+)":\s*func`).FindAllStringSubmatch(string(src), -1)
	if len(keys) == 0 {
		t.Fatal("no reserved keys found in ingest.go; the pattern needs updating")
	}
	for _, m := range keys {
		if !strings.Contains(docs.Twillingate, m[1]) {
			t.Errorf("reserved key %s is not documented in docs/twillingate.md", m[1])
		}
	}
}

// The reverse direction: a $-key named in the doc must exist in the map.
// Catches a key that was renamed or removed in code and left in prose.
func TestTwillingateDocumentsNoStaleKey(t *testing.T) {
	src, err := os.ReadFile("../server/ingest.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	// Names, not attributes: $pageview and $screen_view are event names and
	// live in constants rather than the reservedKeys map.
	names := map[string]bool{"$pageview": true, "$screen_view": true}
	for _, m := range regexp.MustCompile(`\$[a-z_]+`).FindAllString(docs.Twillingate, -1) {
		if names[m] || strings.Contains(code, `"`+m+`"`) {
			continue
		}
		t.Errorf("docs/twillingate.md documents %s, which ingest.go does not define", m)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/mcpserver/ -run TestTwillingateDocuments -v`
Expected: PASS. A failure here means Task 1's document is genuinely missing a key — fix the document, not the test.

- [ ] **Step 3: Prove the test bites**

Temporarily delete one reserved key's mention from `docs/twillingate.md`, rerun, confirm FAIL, then restore it. A guard that cannot fail is not a guard.

- [ ] **Step 4: Commit**

```bash
git add internal/mcpserver/docs_sync_test.go
git commit -m "docs: bind twillingate.md to the reserved key map"
```

---

### Task 5: the CLAUDE.md maintenance rule

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add the section**

Append to `CLAUDE.md`:

```markdown
## Documentation

`docs/twillingate.md` is the single normative document and the only prose
the MCP endpoint serves to agents. It is not a summary of the code — it is
the contract. Update it **in the same commit** as any change to:

- reserved attribute keys or event names (`internal/server/ingest.go`)
- the ingest wire format or its responses (`internal/server/handlers.go`)
- the JS SDK's public API, `data-` attributes or defaults
  (`sdk/src/twillingate.ts`)
- config keys, env vars or project fields (`internal/config/`)
- install, upgrade or restore procedure (`deploy/`, `Makefile`)
- MCP auth modes, client setup, or the set of tools offered
  (`internal/mcpserver/`)
- the Evidence dashboards (`internal/dashboards/`, `evidence/`)

Update `schemaViews` in `internal/mcpserver/resources.go` in the same commit
as any migration that adds or changes a queryable view.

Everything else under `docs/` that it absorbed is a pointer, not a source —
never add content there. `docs/plausible/README.md` is the exception: it
documents bytes the collector serves and a test binds it to them.

`docs_sync_test.go` enforces the reserved-key half of this. The rest is on
you: a `docs`-only push publishes nothing, so a same-commit update costs no
release.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: require twillingate.md to be updated in the same commit"
```

---

### Task 6: full gate

- [ ] **Step 1: Run everything**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 2: Confirm the resource surface**

Run: `grep -n 'textResource\|AddResource' internal/mcpserver/resources.go`
Expected: exactly two registrations — `docs://twillingate` and `schema://views`.

- [ ] **Step 3: Confirm no orphaned prose**

Run: `wc -l docs/*.md`
Expected: the seven absorbed files are ~8 lines each (pointers); `twillingate.md` carries the content.
