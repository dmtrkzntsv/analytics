package manage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

// rawExec reaches the underlying *sql.DB of the sqlite store for tests
// that need to write rows the exported API cannot produce (malformed JSON
// in a registry column, in particular). Mirrors internal/mcpserver's
// seed_test.go helper of the same shape.
func rawExec(t *testing.T, st store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...); err != nil {
		t.Fatal(err)
	}
}

// ---- ops.go: zero/low-coverage operations ----

func TestArchiveAndRestoreProjectRoundTrip(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog", Name: "b", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "cli", "blog"); err != nil {
		t.Fatal(err)
	}
	if p := reg.Snapshot(ctx).Project("blog"); p == nil || !p.Archived {
		t.Fatalf("after archive, Project = %+v", p)
	}
	if err := ops.RestoreProject(ctx, "cli", "blog"); err != nil {
		t.Fatal(err)
	}
	if p := reg.Snapshot(ctx).Project("blog"); p == nil || p.Archived {
		t.Fatalf("after restore, Project = %+v", p)
	}
}

func TestDeleteProjectRemovesEverything(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog", Name: "b", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	if err := ops.DeleteProject(ctx, "cli", "blog"); err != nil {
		t.Fatal(err)
	}
	if p := reg.Snapshot(ctx).Project("blog"); p != nil {
		t.Fatalf("deleted project still present: %+v", p)
	}
	// deleting an unknown alias is an error, not a silent no-op.
	if err := ops.DeleteProject(ctx, "cli", "no-such-project"); err == nil {
		t.Fatal("delete of unknown alias = nil, want error")
	}
}

func TestIssueIngestKeyUnknownProject(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.IssueIngestKey(ctx, "cli", "no-such-project", "web"); err == nil {
		t.Fatal("want error for unknown project")
	}
}

func TestUpdateProjectAppliesRetentionAndAggregation(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog", Name: "b", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	rawDays := 90
	p, err := ops.UpdateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "b", Identity: "identified",
		Retention:   &config.RetentionOverride{Web: &config.RetentionClassOverride{RawDays: &rawDays}},
		Aggregation: &config.ProductAggregation{Enabled: true, Attributes: map[string][]string{"*": {"plan"}}, TopN: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != "identified" || p.Retention == nil || p.Retention.Web == nil || *p.Retention.Web.RawDays != 90 {
		t.Fatalf("updated project = %+v", p)
	}
	if p.Aggregation == nil || !p.Aggregation.Enabled || p.Aggregation.TopN != 10 {
		t.Fatalf("aggregation not applied: %+v", p.Aggregation)
	}
	// invalid identity is rejected before it reaches the store
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{Alias: "blog", Identity: "sometimes"}); err == nil {
		t.Fatal("want validation error for bad identity")
	}
}

// Closing the underlying store forces every downstream write to fail,
// exercising the error-propagation branch of each Ops method the same
// way internal/store/sqlite's own TestOperationsOnClosedDB does.
func TestOpsPropagateStoreErrorsOnClosedDB(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog", Name: "b", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	// snapshot already has "blog" cached; closing st only breaks writes
	// and the LoadRegistry/ConfigVersion calls Reload makes afterwards.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	for name, op := range map[string]func() error{
		"CreateProject": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "other", Name: "o", Identity: "anonymous"})
			return err
		},
		"UpdateProject": func() error {
			_, err := ops.UpdateProject(ctx, "cli", ProjectSpec{Alias: "blog", Name: "b", Identity: "anonymous"})
			return err
		},
		"ArchiveProject":   func() error { return ops.ArchiveProject(ctx, "cli", "blog") },
		"RestoreProject":   func() error { return ops.RestoreProject(ctx, "cli", "blog") },
		"DisableIngestKey": func() error { return ops.DisableIngestKey(ctx, "cli", "blog", "web") },
		"EnableIngestKey":  func() error { return ops.EnableIngestKey(ctx, "cli", "blog", "web") },
		"DeleteProject":    func() error { return ops.DeleteProject(ctx, "cli", "blog") },
		"IssueIngestKey": func() error {
			_, err := ops.IssueIngestKey(ctx, "cli", "blog", "ios")
			return err
		},
	} {
		if err := op(); err == nil {
			t.Errorf("%s on a closed store returned nil, want error", name)
		}
	}
}

// ---- ops.go: ProjectSpec.validate / .row, unexported but same package ----

func TestValidateDefaultsNameAndIdentity(t *testing.T) {
	sp := &ProjectSpec{Alias: "blog"}
	if err := sp.validate(); err != nil {
		t.Fatal(err)
	}
	if sp.Name != "blog" {
		t.Errorf("Name = %q, want default to alias", sp.Name)
	}
	if sp.Identity != config.IdentityAnonymous {
		t.Errorf("Identity = %q, want default anonymous", sp.Identity)
	}
}

func TestRowMarshalsOriginsRetentionAggregation(t *testing.T) {
	// nil origins -> "[]", no retention/aggregation
	sp := &ProjectSpec{Alias: "a", Name: "a", Identity: "anonymous"}
	row, err := sp.row()
	if err != nil {
		t.Fatal(err)
	}
	if row.AllowedOrigins != "[]" || row.Retention != "" || row.Aggregation != "" {
		t.Fatalf("row = %+v", row)
	}

	// non-nil origins, retention and aggregation all set
	rawDays := 5
	sp2 := &ProjectSpec{Alias: "b", Name: "b", Identity: "identified",
		AllowedOrigins: []string{"https://b.example.com"},
		Retention:      &config.RetentionOverride{Web: &config.RetentionClassOverride{RawDays: &rawDays}},
		Aggregation:    &config.ProductAggregation{Enabled: true, TopN: 5}}
	row2, err := sp2.row()
	if err != nil {
		t.Fatal(err)
	}
	if row2.AllowedOrigins != `["https://b.example.com"]` {
		t.Errorf("AllowedOrigins = %q", row2.AllowedOrigins)
	}
	if !strings.Contains(row2.Retention, `"raw_days":5`) {
		t.Errorf("Retention = %q", row2.Retention)
	}
	if !strings.Contains(row2.Aggregation, `"enabled":true`) {
		t.Errorf("Aggregation = %q", row2.Aggregation)
	}
}

func TestSnippetDefaultsOriginWhenEmpty(t *testing.T) {
	snip := Snippet("", "ak_x", "anonymous")
	if !strings.Contains(snip, "https://analytics.example.com/js/script.js") {
		t.Errorf("Snippet with empty origin did not fall back to the placeholder host: %s", snip)
	}
}

// ---- registry.go: previously-zero accessors and error branches ----

func TestProjectsAnyOriginAllowedAndAggregationFor(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "blog", Name: "blog", Identity: "anonymous",
		AllowedOrigins: `["https://blog.example.com/"]`, // trailing slash on this side
		Aggregation:    `{"enabled":true,"attributes":{"*":["plan"]},"top_n":25}`,
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "docs", Name: "docs", Identity: "anonymous", AllowedOrigins: "[]",
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "docs"}); err != nil {
		t.Fatal(err)
	}
	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)

	if got := s.Projects(); len(got) != 2 {
		t.Fatalf("Projects() = %d entries, want 2", len(got))
	}

	// origin allowed without the trailing slash the caller sends, matching
	// the origin stored with one — trimSlash must run on both sides.
	if !s.AnyOriginAllowed("https://blog.example.com") {
		t.Error("AnyOriginAllowed did not match across a trailing-slash difference")
	}
	if s.AnyOriginAllowed("https://evil.example.com") {
		t.Error("AnyOriginAllowed matched an origin nobody allows")
	}

	agg := s.AggregationFor("blog")
	if !agg.Enabled || agg.TopN != 25 {
		t.Fatalf("AggregationFor(blog) = %+v", agg)
	}
	if got := s.AggregationFor("docs"); got.Enabled {
		t.Fatalf("AggregationFor(docs) = %+v, want zero value (no aggregation configured)", got)
	}
	if got := s.AggregationFor("ghost"); got.Enabled {
		t.Fatalf("AggregationFor(ghost) = %+v, want zero value (unknown alias)", got)
	}
}

func TestReloadPropagatesStoreErrors(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(context.Background()); err == nil {
		t.Fatal("Reload on a closed store returned nil, want error")
	}
}

// After a successful Reload, a poll whose ConfigVersion call fails must
// not propagate the error to the caller — the previous snapshot keeps
// serving (spec §3.3: a transient read error must not take down
// ingestion). This exercises the log-and-continue branch in Snapshot.
func TestSnapshotPollErrorKeepsServingPreviousSnapshot(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	reg := New(st, defaults, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	before := reg.Snapshot(ctx)
	if before.Project("blog") == nil {
		t.Fatal("setup: blog missing before closing the store")
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// force the next Snapshot call past the poll interval
	reg.lastCheck.Store(0)
	after := reg.Snapshot(ctx)
	if after.Project("blog") == nil {
		t.Fatal("poll error dropped the previous snapshot instead of keeping it")
	}
}

// ---- importexport.go: error paths and the less-common branches ----

func TestImportRejectsUnsupportedVersion(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	_, err := ops.Import(ctx, "cli", strings.NewReader(`{"version":2,"projects":[]}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("err = %v, want unsupported version", err)
	}
}

func TestImportRejectsMalformedJSON(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.Import(ctx, "cli", strings.NewReader(`{"version":1,"unknown_field":true}`)); err == nil {
		t.Fatal("want error for an unknown field (DisallowUnknownFields)")
	}
}

func TestImportRejectsMalformedLegacyJSON(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.Import(ctx, "cli", strings.NewReader(`[{"alias": `)); err == nil {
		t.Fatal("want error for truncated legacy array")
	}
}

func TestImportEmptyDocumentIsAnError(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.Import(ctx, "cli", strings.NewReader("")); err == nil {
		t.Fatal("want error for an empty document")
	}
	if _, err := ops.Import(ctx, "cli", strings.NewReader("   \n\t ")); err == nil {
		t.Fatal("want error for a whitespace-only document")
	}
}

func TestImportSkipsExistingKeysAndAppliesDisabled(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)

	doc := `{"version":1,"projects":[{"alias":"blog","name":"blog","identity":"anonymous",
	  "allowed_origins":[],
	  "ingest_keys":[{"key":"ak_1","label":"web"},{"key":"ak_2","label":"ios","disabled":true}]}]}`
	res, err := ops.Import(ctx, "cli", strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.KeysAdded != 2 {
		t.Fatalf("first import result = %+v", res)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey("ak_1"); !ok {
		t.Fatal("ak_1 should resolve (not disabled)")
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey("ak_2"); ok {
		t.Fatal("ak_2 was imported with disabled:true and must not resolve")
	}

	// re-importing the same document must not re-add either key
	res2, err := ops.Import(ctx, "cli", strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if res2.KeysAdded != 0 {
		t.Fatalf("re-import added %d keys, want 0 (existing keys are left alone)", res2.KeysAdded)
	}
}

func TestImportRestoresAnArchivedProjectWhenDocumentSaysSo(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog", Name: "blog", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "cli", "blog"); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"projects":[{"alias":"blog","name":"blog","identity":"anonymous","allowed_origins":[],"archived":false,"ingest_keys":[]}]}`
	if _, err := ops.Import(ctx, "cli", strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	if p := reg.Snapshot(ctx).Project("blog"); p == nil || p.Archived {
		t.Fatalf("blog = %+v, want restored (archived:false in the document)", p)
	}
}

func TestExportIncludesRetentionAndAggregation(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	rawDays := 90
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "blog", Identity: "identified",
		Retention:   &config.RetentionOverride{Web: &config.RetentionClassOverride{RawDays: &rawDays}},
		Aggregation: &config.ProductAggregation{Enabled: true, TopN: 10},
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ops.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	var doc exportDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Projects) != 1 {
		t.Fatalf("doc.Projects = %+v", doc.Projects)
	}
	ep := doc.Projects[0]
	if ep.Retention == nil || ep.Retention.Web == nil || *ep.Retention.Web.RawDays != 90 {
		t.Fatalf("exported retention = %+v", ep.Retention)
	}
	if ep.ProductAggregation == nil || !ep.ProductAggregation.Enabled || ep.ProductAggregation.TopN != 10 {
		t.Fatalf("exported aggregation = %+v", ep.ProductAggregation)
	}
}

func TestExportPropagatesStoreErrors(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ops.Export(ctx, &buf); err == nil {
		t.Fatal("Export on a closed store returned nil, want error")
	}
}

// A malformed JSON blob in a registry column (which the exported API can
// never write, but a hand-edited database or a future bug could produce)
// must surface as an error from both Reload and Export rather than a
// panic or a silently empty field.
func TestReloadAndExportRejectCorruptRegistryJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column string
	}{
		{"allowed_origins", "allowed_origins"},
		{"retention", "retention"},
		{"product_aggregation", "product_aggregation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			if err := st.CreateProject(ctx, store.RegistryProject{
				Alias: "blog", Name: "blog", Identity: "anonymous", AllowedOrigins: "[]",
			}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "blog"}); err != nil {
				t.Fatal(err)
			}
			rawExec(t, st, `UPDATE projects SET `+tc.column+` = 'not json' WHERE alias = 'blog'`)

			reg := New(st, defaults, discard())
			if err := reg.Reload(ctx); err == nil {
				t.Error("Reload did not reject corrupt JSON")
			}

			ops := NewOps(reg, st)
			var buf bytes.Buffer
			if err := ops.Export(ctx, &buf); err == nil {
				t.Error("Export did not reject corrupt JSON")
			}
		})
	}
}
