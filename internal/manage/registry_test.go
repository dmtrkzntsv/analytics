package manage

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

var defaults = config.Retention{
	Web:     config.RetentionClass{RawDays: 7, AggregateDays: 365},
	Product: config.RetentionClass{RawDays: 30, AggregateDays: 365},
	App:     config.RetentionClass{RawDays: 30, AggregateDays: 365},
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open("sqlite://" + t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func seedProject(t *testing.T, st store.Store, alias string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: alias, Name: alias, Identity: "identified",
		AllowedOrigins: `["https://` + alias + `.example.com"]`,
		Retention:      `{"web":{"raw_days":90}}`,
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: alias}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIngestKey(ctx, store.RegistryKey{
		Key: "ak_" + alias, Project: alias, Label: "web",
	}, store.AuditEntry{Actor: "test", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotLookups(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	reg := New(st, defaults, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(context.Background())

	p, label, ok := s.ProjectByKey("ak_blog")
	if !ok || p.Alias != "blog" || label != "web" {
		t.Fatalf("ProjectByKey = %v %q %v", p, label, ok)
	}
	if _, _, ok := s.ProjectByKey("ak_nope"); ok {
		t.Fatal("unknown key resolved")
	}
	if !s.OriginAllowed("blog", "https://blog.example.com") {
		t.Fatal("origin not allowed")
	}
	if s.OriginAllowed("blog", "https://evil.example.com") {
		t.Fatal("wrong origin allowed")
	}
	r := s.RetentionFor("blog")
	if r.Web.RawDays != 90 || r.Web.AggregateDays != 365 || r.Product.RawDays != 30 {
		t.Fatalf("RetentionFor merged wrong: %+v", r)
	}
	// Unknown alias falls back to defaults, matching the archived-project
	// behaviour jobs relies on.
	if r := s.RetentionFor("ghost"); r != defaults {
		t.Fatalf("RetentionFor(ghost) = %+v", r)
	}
	if p := s.Project("blog"); p == nil || p.Identity != "identified" {
		t.Fatalf("Project = %+v", p)
	}
}

func TestArchivedProjectKeysDoNotResolve(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	ctx := context.Background()
	if err := st.SetProjectArchived(ctx, "blog", true,
		store.AuditEntry{Actor: "test", Action: "project.archive", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)
	if _, _, ok := s.ProjectByKey("ak_blog"); ok {
		t.Fatal("archived project's key resolved")
	}
	// but the project itself is still visible (retention overrides survive)
	if p := s.Project("blog"); p == nil || !p.Archived {
		t.Fatalf("Project = %+v", p)
	}
}

func TestSnapshotPicksUpOutOfProcessWrite(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	reg := New(st, defaults, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a CLI in another process: write via the store directly.
	seedProject(t, st, "docs")
	if p := reg.Snapshot(ctx).Project("docs"); p != nil {
		t.Fatal("saw the write before the poll interval elapsed")
	}
	reg.lastCheck.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if p := reg.Snapshot(ctx).Project("docs"); p == nil {
		t.Fatal("poll did not pick up the write")
	}
}

// KeylessProjects drives the boot-time warning in internal/app: a project
// with no active key can receive nothing, which is a legitimate retired
// state (not an error), and an archived project must not be flagged twice.
func TestKeylessProjects(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	seedProject(t, st, "blog") // has an active key
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "retired", Name: "retired", Identity: "anonymous", AllowedOrigins: "[]",
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "retired"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "gone", Name: "gone", Identity: "anonymous", AllowedOrigins: "[]",
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectArchived(ctx, "gone", true,
		store.AuditEntry{Actor: "test", Action: "project.archive", Subject: "gone"}); err != nil {
		t.Fatal(err)
	}

	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	got := reg.Snapshot(ctx).KeylessProjects()
	if len(got) != 1 || got[0] != "retired" {
		t.Fatalf("KeylessProjects() = %v, want [retired] (blog has a key, gone is archived)", got)
	}
}

func TestMatchOrigin(t *testing.T) {
	cases := []struct {
		pattern, origin string
		want            bool
	}{
		{"*", "https://anything.example.com", true},
		{"*", "tauri://localhost", true},
		{"https://*.example.com", "https://blog.example.com", true},
		{"https://*.example.com", "https://a.b.example.com", true},
		{"https://*.example.com", "https://example.com", false},
		{"https://*.example.com", "http://blog.example.com", false},
		{"https://*.example.com", "https://blog.example.com.evil.net", false},
		{"https://*.example.com", "https://evil.net/x.example.com", false},
		{"https://*.example.com:*", "https://blog.example.com:8443", true},
		{"https://example.com", "https://example.com", true},
		{"https://example.com", "https://evil.com", false},
	}
	for _, c := range cases {
		if got := matchOrigin(c.pattern, c.origin); got != c.want {
			t.Errorf("matchOrigin(%q, %q) = %v, want %v", c.pattern, c.origin, got, c.want)
		}
	}
}

// A wildcard entry has to work through the whole reload path — split into
// the pattern list, trailing slash trimmed — not just in the matcher.
func TestSnapshotWildcardOrigins(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: "blog", Name: "blog", Identity: "anonymous",
		AllowedOrigins: `["https://*.example.com/", "https://fixed.test"]`,
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	reg := New(st, defaults, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)
	for _, o := range []string{"https://blog.example.com", "https://shop.example.com/", "https://fixed.test"} {
		if !s.OriginAllowed("blog", o) {
			t.Errorf("OriginAllowed(blog, %q) = false", o)
		}
		if !s.AnyOriginAllowed(o) {
			t.Errorf("AnyOriginAllowed(%q) = false", o)
		}
	}
	if s.OriginAllowed("blog", "https://example.com.evil.net") {
		t.Error("wildcard matched a suffix-spoofing origin")
	}
	if s.AnyOriginAllowed("https://evil.net") {
		t.Error("AnyOriginAllowed matched an origin nobody allows")
	}
}
