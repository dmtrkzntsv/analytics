package manage

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
	// the shape projects.example.json documents today: a bare array
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
