package manage

import (
	"context"
	"strings"
	"testing"
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
	for _, want := range []string{"script.js", `data-key="ak_x"`, `data-identity="anonymous"`} {
		if !strings.Contains(snip, want) {
			t.Errorf("snippet missing %q:\n%s", want, snip)
		}
	}
}
