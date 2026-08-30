package manage

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestExportImportRoundTrip(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "My blog", Identity: "identified",
		AllowedOrigins: []string{"https://blog.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ops.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	exported := buf.String()

	// import into a fresh database
	st2 := testStore(t)
	reg2 := New(st2, defaults, discard())
	reg2.Reload(ctx)
	ops2 := NewOps(reg2, st2)
	res, err := ops2.Import(ctx, "cli", strings.NewReader(exported))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.KeysAdded != 1 {
		t.Fatalf("result = %+v", res)
	}
	var buf2 bytes.Buffer
	if err := ops2.Export(ctx, &buf2); err != nil {
		t.Fatal(err)
	}
	if buf2.String() != exported {
		t.Errorf("round trip changed the document:\n%s\nvs\n%s", exported, buf2.String())
	}
}

func TestImportNeverArchivesOrDisables(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	for _, alias := range []string{"keep", "listed"} {
		if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: alias, Name: alias, Identity: "anonymous"}); err != nil {
			t.Fatal(err)
		}
	}
	// a document naming only "listed" must leave "keep" untouched
	doc := `{"version":1,"projects":[{"alias":"listed","name":"renamed","identity":"anonymous","allowed_origins":[],"ingest_keys":[]}]}`
	if _, err := ops.Import(ctx, "cli", strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)
	if p := s.Project("keep"); p == nil || p.Archived {
		t.Fatalf("keep = %+v; import must never archive the unlisted", p)
	}
	if p := s.Project("listed"); p.Name != "renamed" {
		t.Fatalf("listed = %+v; listed fields must update", p)
	}
}

func TestImportLegacyProjectsJSON(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	// the legacy projects.json shape (retired file, now import-only): a bare array
	legacy := `[{"alias":"blog","name":"My blog","identity":"anonymous",
	  "ingest_keys":[{"key":"ak_legacy1","label":"web"}],
	  "allowed_origins":["https://blog.example.com"]}]`
	res, err := ops.Import(ctx, "cli", strings.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.KeysAdded != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey("ak_legacy1"); !ok {
		t.Fatal("legacy key not imported")
	}
}

// TestImportV1DocumentFoldsLegacyProductAggregation exercises the
// DisallowUnknownFields compatibility guarantee the brief singled out: a
// v1 export document (not the legacy bare-array projects.json) that still
// carries the pre-2026-08 product_aggregation block — because it was
// exported before this change, or hand-edited — must still decode (the
// field is kept, just no longer written) and its event-keyed map must
// fold into the new flat attributes list on import. The bare-array legacy
// path is covered by TestImportLegacyProjectsJSON; this is the other of
// the two places exportProject.declaredAttributes's fold branch is
// reachable from, and until now neither test exercised it.
func TestImportV1DocumentFoldsLegacyProductAggregation(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	doc := `{"version":1,"projects":[{"alias":"blog","name":"My blog","identity":"anonymous",
	  "allowed_origins":[],"ingest_keys":[],
	  "product_aggregation":{"enabled":true,"attributes":{"*":["plan"],"subscribed":["tier","plan"]},"top_n":50}}]}`
	res, err := ops.Import(ctx, "cli", strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 {
		t.Fatalf("result = %+v", res)
	}
	p := reg.Snapshot(ctx).Project("blog")
	if p == nil {
		t.Fatal("blog not imported")
	}
	if len(p.Attributes) != 2 || p.Attributes[0] != "plan" || p.Attributes[1] != "tier" {
		t.Fatalf("Attributes = %v, want [plan tier] (sorted DISTINCT union of the legacy map)", p.Attributes)
	}
}

// findKey looks up a RegistryKey by key value directly from the store,
// bypassing the snapshot (which drops disabled keys — see the "list"
// comment in cmd/twillingate/key.go).
func findKey(t *testing.T, st store.Store, ctx context.Context, key string) store.RegistryKey {
	t.Helper()
	_, ks, err := st.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		if k.Key == key {
			return k
		}
	}
	t.Fatalf("key %q not found", key)
	return store.RegistryKey{}
}

func TestImportReEnablesADisabledKeyWhenTheDocumentSaysSo(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "My blog", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	key, err := ops.IssueIngestKey(ctx, "cli", "blog", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.DisableIngestKey(ctx, "cli", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	if !findKey(t, st, ctx, key).Disabled {
		t.Fatal("setup: key must be disabled before import")
	}

	// The document lists the same key with disabled:false — an explicit
	// re-enable, not silence, so it must take effect.
	doc := `{"version":1,"projects":[{"alias":"blog","name":"My blog","identity":"anonymous","allowed_origins":[],
	  "ingest_keys":[{"key":"` + key + `","label":"web","disabled":false}]}]}`
	if _, err := ops.Import(ctx, "cli", strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	if findKey(t, st, ctx, key).Disabled {
		t.Error("key still disabled after import listed it with disabled:false")
	}
}

func TestImportDisablesAnEnabledKeyWhenTheDocumentSaysSo(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "My blog", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	key, err := ops.IssueIngestKey(ctx, "cli", "blog", "web")
	if err != nil {
		t.Fatal(err)
	}
	if findKey(t, st, ctx, key).Disabled {
		t.Fatal("setup: key must start enabled")
	}

	doc := `{"version":1,"projects":[{"alias":"blog","name":"My blog","identity":"anonymous","allowed_origins":[],
	  "ingest_keys":[{"key":"` + key + `","label":"web","disabled":true}]}]}`
	if _, err := ops.Import(ctx, "cli", strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	if !findKey(t, st, ctx, key).Disabled {
		t.Error("key still enabled after import listed it with disabled:true")
	}
}

func TestExportImportArchivedState(t *testing.T) {
	// Create and archive a project in store A
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "archivedblog", Name: "Archived blog", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "cli", "archivedblog"); err != nil {
		t.Fatal(err)
	}

	// Export from store A
	var buf bytes.Buffer
	if err := ops.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	exported := buf.String()

	// Import into a fresh store B
	st2 := testStore(t)
	reg2 := New(st2, defaults, discard())
	reg2.Reload(ctx)
	ops2 := NewOps(reg2, st2)
	res, err := ops2.Import(ctx, "cli", strings.NewReader(exported))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 {
		t.Fatalf("result.Created = %d, want 1", res.Created)
	}

	// Verify archived state is preserved in store B
	p := reg2.Snapshot(ctx).Project("archivedblog")
	if p == nil || !p.Archived {
		t.Fatalf("archivedblog = %+v; must be archived", p)
	}

	// Re-export from store B and verify byte equality
	var buf2 bytes.Buffer
	if err := ops2.Export(ctx, &buf2); err != nil {
		t.Fatal(err)
	}
	if buf2.String() != exported {
		t.Errorf("round trip changed archived state:\n%s\nvs\n%s", exported, buf2.String())
	}
}

// TestImportRejectsWholeDocumentOnABadAlias proves import is all-or-nothing
// on alias validation: a document with a bad alias in the middle
// ([blog, my_app, shop]) must write NOTHING, including "shop" which is
// valid and listed AFTER the offender. Before this fix, Import applied
// projects one at a time and returned on the first error, so "blog" would
// already exist and "shop" would never be reached — a half-migrated
// registry. See internal/manage/importexport.go's pre-validation pass.
func TestImportRejectsWholeDocumentOnABadAlias(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)

	doc := `{"version":1,"projects":[
	  {"alias":"blog","name":"Blog","identity":"anonymous","allowed_origins":[],"ingest_keys":[]},
	  {"alias":"my_app","name":"My app","identity":"anonymous","allowed_origins":[],"ingest_keys":[]},
	  {"alias":"shop","name":"Shop","identity":"anonymous","allowed_origins":[],"ingest_keys":[]}
	]}`
	res, err := ops.Import(ctx, "cli", strings.NewReader(doc))
	if err == nil {
		t.Fatal("expected an error naming the bad alias, got nil")
	}
	if !strings.Contains(err.Error(), "my_app") {
		t.Errorf("error = %v; want it to name my_app", err)
	}
	if res.Created != 0 || res.Updated != 0 {
		t.Fatalf("result = %+v; want nothing applied", res)
	}
	if reg.Snapshot(ctx).Project("blog") != nil {
		t.Error("blog was created despite the document being rejected")
	}
	if reg.Snapshot(ctx).Project("shop") != nil {
		t.Error("shop (listed after the bad alias) was created despite the document being rejected")
	}
}
