package manage

import (
	"context"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestCreateProjectValidatesAndReloads(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	ctx := context.Background()

	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "My blog", Identity: "anonymous",
		AllowedOrigins: []string{"https://blog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Alias != "blog" {
		t.Fatalf("created = %+v", p)
	}
	// snapshot rebuilt synchronously after an in-process write
	if reg.Snapshot(ctx).Project("blog") == nil {
		t.Fatal("snapshot not reloaded")
	}
	// validation
	for _, bad := range []ProjectSpec{
		{Alias: "", Name: "x", Identity: "anonymous"},
		{Alias: "x", Name: "x", Identity: "sometimes"},
		{Alias: "x", Name: "x", Identity: "anonymous", AllowedOrigins: []string{""}},
	} {
		if _, err := ops.CreateProject(ctx, "cli", bad); err == nil {
			t.Errorf("spec %+v did not fail", bad)
		}
	}
}

func TestCreateRejectsBadAlias(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	ctx := context.Background()

	for _, bad := range []string{"My-Blog", "my_app", "shop uk", "blog!", ""} {
		if _, err := ops.CreateProject(ctx, "test",
			ProjectSpec{Alias: bad, Identity: "anonymous"}); err == nil {
			t.Errorf("CreateProject(%q) = nil error, want rejection", bad)
		}
	}
	for _, ok := range []string{"blog", "blog2", "2048"} {
		if _, err := ops.CreateProject(ctx, "test",
			ProjectSpec{Alias: ok, Identity: "anonymous"}); err != nil {
			t.Errorf("CreateProject(%q) = %v, want nil", ok, err)
		}
	}
}

func TestUpdateDoesNotCharsetCheck(t *testing.T) {
	// A legacy row predating the rule must stay editable, or `config
	// export | config import` locks its owner out of the fix.
	st := testStore(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "my_app", Name: "my_app", Identity: "anonymous",
		AllowedOrigins: "[]",
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "my_app"}); err != nil {
		t.Fatal(err)
	}
	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)

	if _, err := ops.UpdateProject(ctx, "test",
		ProjectSpec{Alias: "my_app", Name: "renamed", Identity: "anonymous"}); err != nil {
		t.Fatalf("UpdateProject on legacy alias = %v, want nil", err)
	}
	if got := reg.Snapshot(ctx).Project("my_app"); got == nil || got.Name != "renamed" {
		t.Fatalf("update did not apply: %+v", got)
	}
}

// TestRenameProjectAllowsLegacyAlias pins the command's primary use case:
// a legacy alias that predates the ^[a-z0-9]+$ rule (created directly
// through the store, the way real legacy rows are — see
// TestUpdateDoesNotCharsetCheck above) must still be renamable to a
// conforming alias through Ops.RenameProject. validateNew runs against the
// PROPOSED new alias only; if a future change made it also cover the OLD
// alias, this command would silently stop doing the one thing it exists
// for, and nothing else would catch it.
func TestRenameProjectAllowsLegacyAlias(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "my_app", Name: "my_app", Identity: "anonymous", AllowedOrigins: "[]",
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "my_app"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_legacy", Project: "my_app", Label: "web"},
		store.AuditEntry{Actor: "test", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)

	if err := ops.RenameProject(ctx, "test", "my_app", "myapp"); err != nil {
		t.Fatalf("RenameProject off a legacy source alias = %v, want nil", err)
	}

	if got := reg.Snapshot(ctx).Project("my_app"); got != nil {
		t.Fatal("legacy alias still present after rename")
	}
	if got := reg.Snapshot(ctx).Project("myapp"); got == nil {
		t.Fatal("new alias not present after rename")
	}
	_, ks, err := st.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range ks {
		if k.Project == "my_app" {
			t.Errorf("ingest key still under the legacy alias after rename: %+v", k)
		}
		if k.Project == "myapp" && k.Key == "ak_legacy" {
			found = true
		}
	}
	if !found {
		t.Fatal("ingest key did not follow the rename off a legacy alias")
	}
}

// TestRenameProjectRejectsInvalidNewAlias pins the other direction of the
// charset rule: the source alias is exempt from validateNew (it is
// TestRenameProjectAllowsLegacyAlias's whole point), but the alias being
// proposed still is not. An invalid -to must be rejected, leaving the
// source untouched.
func TestRenameProjectRejectsInvalidNewAlias(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	seedProject(t, st, "blog")
	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)

	if err := ops.RenameProject(ctx, "test", "blog", "My_App"); err == nil {
		t.Fatal("RenameProject accepted a -to alias outside ^[a-z0-9]+$")
	}
	if got := reg.Snapshot(ctx).Project("blog"); got == nil {
		t.Fatal("source project vanished despite the rejected target alias")
	}
}

func TestIssueKeyMintsAndResolves(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "b", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	key, err := ops.IssueIngestKey(ctx, "mcp", "blog", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "ak_") || len(key) != 3+32 {
		t.Fatalf("key = %q", key)
	}
	if p, label, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok || p.Alias != "blog" || label != "web" {
		t.Fatalf("minted key does not resolve: %v %q %v", p, label, ok)
	}
	if err := ops.DisableIngestKey(ctx, "mcp", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); ok {
		t.Fatal("disabled key still resolves")
	}
	if err := ops.EnableIngestKey(ctx, "mcp", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok {
		t.Fatal("re-enabled key does not resolve")
	}
	// duplicate label on the same project is rejected (retire-by-label
	// depends on label uniqueness within a project)
	if _, err := ops.IssueIngestKey(ctx, "mcp", "blog", "web"); err == nil {
		t.Fatal("duplicate label did not fail")
	}
}

func TestMintersAndSnippet(t *testing.T) {
	tok, err := MintMCPToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "ar_") || len(tok) != 3+64 {
		t.Fatalf("token = %q", tok)
	}
	snip := Snippet("https://blog.example.com", "ak_x", "anonymous")
	for _, want := range []string{"twillingate.js", `data-key="ak_x"`, `data-identity="anonymous"`} {
		if !strings.Contains(snip, want) {
			t.Errorf("snippet missing %q:\n%s", want, snip)
		}
	}
}
