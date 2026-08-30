package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateProjectToolReturnsSnippet(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"alias": "shop", "name": "My shop",
		"allowed_origins": []string{"https://shop.example.com"}})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"twillingate.js", "ak_", "data-identity"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	// create_project with issue_key=true mints a first key; verify the
	// key resolves in the registry
	list := callTool(t, cs, "list_ingest_keys", map[string]any{"project": "shop"})
	if !strings.Contains(textOf(list), "default") {
		t.Errorf("no default key listed: %s", textOf(list))
	}
}

func TestArchiveRestoreTools(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "archive_project", map[string]any{"alias": "docs"}); res.IsError {
		t.Fatalf("archive: %s", textOf(res))
	}
	res := callTool(t, cs, "list_projects", nil)
	if !strings.Contains(textOf(res), "archived") {
		t.Errorf("archive not visible: %s", textOf(res))
	}
	if res := callTool(t, cs, "restore_project", map[string]any{"alias": "docs"}); res.IsError {
		t.Fatalf("restore: %s", textOf(res))
	}
}

func TestKeyToolsLifecycle(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project": "blog", "label": "ios"})
	if res.IsError {
		t.Fatalf("issue: %s", textOf(res))
	}
	if res := callTool(t, cs, "disable_ingest_key", map[string]any{
		"project": "blog", "label": "ios"}); res.IsError {
		t.Fatalf("disable: %s", textOf(res))
	}
	if res := callTool(t, cs, "enable_ingest_key", map[string]any{
		"project": "blog", "label": "ios"}); res.IsError {
		t.Fatalf("enable: %s", textOf(res))
	}
}

func TestNoDeleteToolExists(t *testing.T) {
	_, cs := newTestHost(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "delete") {
			t.Errorf("irreversible tool exposed over MCP: %s", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("tool %s has no annotations", tool.Name)
		}
	}
}

// TestManagementToolsAnnotatedNonReadOnly checks the annotation contract:
// writers must not be marked ReadOnlyHint, and every other tool must be.
// "query" is included in the read-only side (not exempted): it already
// carries ReadOnlyHint: true (Task 18), so there is no reason to carve it
// out — doing so would hide a regression if it ever lost the hint.
//
// It also checks the two guardrails the write annotations exist to
// encode (spec §6): every writer explicitly asserts DestructiveHint ==
// false (never left at the SDK's true default, which would tell a client
// this tool might destroy data), and the reversible-but-not-idempotent-
// by-nature ones (archive/restore/disable/enable) assert IdempotentHint
// == true, since calling them twice with the same arguments is a no-op.
func TestManagementToolsAnnotatedNonReadOnly(t *testing.T) {
	_, cs := newTestHost(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]bool{"create_project": true, "update_project": true,
		"archive_project": true, "restore_project": true, "issue_ingest_key": true,
		"disable_ingest_key": true, "enable_ingest_key": true}
	idempotent := map[string]bool{"archive_project": true, "restore_project": true,
		"disable_ingest_key": true, "enable_ingest_key": true}
	for _, tool := range tools.Tools {
		if writers[tool.Name] && tool.Annotations.ReadOnlyHint {
			t.Errorf("%s marked read-only", tool.Name)
		}
		if !writers[tool.Name] && !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s not marked read-only", tool.Name)
		}
		if writers[tool.Name] {
			d := tool.Annotations.DestructiveHint
			if d == nil || *d != false {
				t.Errorf("%s: DestructiveHint must be explicitly false, got %v", tool.Name, d)
			}
		}
		if idempotent[tool.Name] && !tool.Annotations.IdempotentHint {
			t.Errorf("%s: IdempotentHint must be true", tool.Name)
		}
	}
}

// TestUpdateProjectMerges is the binding ruling from Task 9: Ops.UpdateProject
// is full-replace, but the MCP tool must merge — fields the caller omits
// must be preserved from the current project, not zeroed out. Origins are
// the one exception (replaced wholesale when provided, never partially
// merged), which is why they cannot be cleared through this tool.
func TestUpdateProjectMerges(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	// blog is seeded identified with empty allowed_origins; give it an
	// origin first so we can prove it survives a name-only update.
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "allowed_origins": []string{"https://blog.example.com"}}); res.IsError {
		t.Fatalf("seed origin: %s", textOf(res))
	}
	res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "name": "Blog Renamed"})
	if res.IsError {
		t.Fatalf("update: %s", textOf(res))
	}
	// list_projects doesn't surface origins, so check the registry
	// directly: alias+name-only update must not wipe allowed_origins.
	p := h.reg.Snapshot(ctx).Project("blog")
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.AllowedOrigins) != 1 || p.AllowedOrigins[0] != "https://blog.example.com" {
		t.Errorf("allowed_origins not preserved by name-only update, got %v", p.AllowedOrigins)
	}
	list := callTool(t, cs, "list_projects", nil)
	out := textOf(list)
	var parsed struct {
		Projects []struct {
			Alias, Name, Identity string
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("decode list_projects: %v\n%s", err, out)
	}
	var blog *struct{ Alias, Name, Identity string }
	for i := range parsed.Projects {
		if parsed.Projects[i].Alias == "blog" {
			blog = &parsed.Projects[i]
		}
	}
	if blog == nil {
		t.Fatalf("blog missing from list_projects: %s", out)
	}
	// list_projects also lists "docs" (seeded anonymous); asserting on
	// the decoded blog entry specifically proves this was a merge, not a
	// blind overwrite that would have reset identity to its zero value
	// (anonymous) and name to "" -> "blog".
	if blog.Identity != "identified" {
		t.Errorf("blog identity not preserved by merge, got %q", blog.Identity)
	}
	if blog.Name != "Blog Renamed" {
		t.Errorf("name update did not apply, got %q", blog.Name)
	}
}

// TestUpdateProjectEmptyOriginsPreservesExisting is the High-severity fix:
// an explicit `allowed_origins: []` must not wipe existing origins. JSON
// decoding gives an empty-but-non-nil slice here, so the merge guard must
// check len(...) > 0, not != nil — otherwise a caller passing an empty
// array (rather than omitting the field) silently clears origins, which
// directly contradicts the tool description's claim that this tool
// cannot clear origins.
func TestUpdateProjectEmptyOriginsPreservesExisting(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "allowed_origins": []string{"https://blog.example.com"}}); res.IsError {
		t.Fatalf("seed origin: %s", textOf(res))
	}
	res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "allowed_origins": []string{}})
	if res.IsError {
		t.Fatalf("update: %s", textOf(res))
	}
	p := h.reg.Snapshot(ctx).Project("blog")
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.AllowedOrigins) != 1 || p.AllowedOrigins[0] != "https://blog.example.com" {
		t.Errorf("explicit empty allowed_origins wiped existing origins, got %v", p.AllowedOrigins)
	}
}

// TestCreateProjectSetsAttributes proves create_project's Attributes field
// reaches manage.ProjectSpec and is stored, not just accepted and dropped.
func TestCreateProjectSetsAttributes(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	res := callTool(t, cs, "create_project", map[string]any{
		"alias": "shop", "attributes": []string{"plan", "tier"}})
	if res.IsError {
		t.Fatalf("create: %s", textOf(res))
	}
	p := h.reg.Snapshot(ctx).Project("shop")
	if p == nil {
		t.Fatalf("shop missing from registry")
	}
	if len(p.Attributes) != 2 || p.Attributes[0] != "plan" || p.Attributes[1] != "tier" {
		t.Fatalf("Attributes = %v, want [plan tier]", p.Attributes)
	}
}

// TestUpdateProjectAttributesMergeSemantics is the merge rule for
// Attributes, matching AllowedOrigins: omitting the field on update_project
// leaves the current declared attributes untouched, and supplying a
// non-nil list replaces the whole thing (never merges element-wise).
func TestUpdateProjectAttributesMergeSemantics(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "attributes": []string{"plan", "tier"}}); res.IsError {
		t.Fatalf("seed attributes: %s", textOf(res))
	}

	// name-only update must not wipe attributes
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "name": "Blog Renamed"}); res.IsError {
		t.Fatalf("update: %s", textOf(res))
	}
	p := h.reg.Snapshot(ctx).Project("blog")
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.Attributes) != 2 || p.Attributes[0] != "plan" || p.Attributes[1] != "tier" {
		t.Fatalf("attributes not preserved by name-only update, got %v", p.Attributes)
	}

	// supplying attributes replaces the whole list
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "attributes": []string{"solo"}}); res.IsError {
		t.Fatalf("update attributes: %s", textOf(res))
	}
	p = h.reg.Snapshot(ctx).Project("blog")
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.Attributes) != 1 || p.Attributes[0] != "solo" {
		t.Fatalf("attributes not replaced wholesale, got %v", p.Attributes)
	}
}

// TestUpdateProjectUnknownAlias checks the tool names the bad alias
// rather than surfacing a nil-pointer or an opaque error.
func TestUpdateProjectUnknownAlias(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{
		"alias": "nope", "name": "x"})
	if !res.IsError {
		t.Fatalf("expected error for unknown alias")
	}
	if !strings.Contains(textOf(res), "nope") {
		t.Errorf("error does not name the alias: %s", textOf(res))
	}
}
