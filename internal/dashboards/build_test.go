package dashboards

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// stubNPM replaces the npm exec. The success form writes the build directory
// Evidence would produce, recording the source path it was handed so tests
// can assert on it.
func stubNPM(t *testing.T, fail bool) *int {
	t.Helper()
	calls := 0
	old := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls++
		if name != "npm" {
			t.Errorf("exec %q, want npm", name)
		}
		if fail {
			return exec.CommandContext(ctx, "false")
		}
		script := "mkdir -p build && printf '%s' \"$EVIDENCE_SOURCE__twillingate__filename\" > build/index.html"
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	t.Cleanup(func() { execCommand = old })
	return &calls
}

// stubNPMReportingABrokenSource replaces the npm exec with one that exits 0
// while printing what Evidence prints for a source it could not read. The
// marker is the point of the fixture, so it is written out as the character
// Evidence emits rather than as an escape.
func stubNPMReportingABrokenSource(t *testing.T) {
	t.Helper()
	old := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		const line = `  projects ✖ Error: "SQLITE_ERROR: no such table: projects"`
		return exec.CommandContext(ctx, "sh", "-c",
			"mkdir -p build && touch build/index.html && printf '%s' '"+line+"'")
	}
	t.Cleanup(func() { execCommand = old })
}

func newTestBuilder(t *testing.T) *Builder {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "a.db")
	seedDB(t, db)
	return NewBuilder(config.DashboardsConfig{
		DBPath:     db,
		ProjectDir: dir,
		WorkDir:    filepath.Join(dir, "work"),
	}, slog.New(slog.DiscardHandler))
}

func TestBuildRotatesSlots(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	if err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := filepath.Base(*b.site.Load()); got != "site.a" {
		t.Errorf("first slot = %q, want site.a", got)
	}
	if err := b.Build(context.Background()); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if got := filepath.Base(*b.site.Load()); got != "site.b" {
		t.Errorf("second slot = %q, want site.b", got)
	}
	if _, err := os.Stat(filepath.Join(b.cfg.ProjectDir, "site.a")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the superseded slot should have been removed")
	}
	if _, err := os.Stat(filepath.Join(b.cfg.ProjectDir, "build")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("build/ should have been renamed into a slot, not copied")
	}
}

func TestBuildFailureKeepsServingPreviousSlot(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	if err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	good := *b.site.Load()

	stubNPM(t, true)
	if err := b.Build(context.Background()); err == nil {
		t.Fatal("want an error from a failing npm")
	}
	if now := *b.site.Load(); now != good {
		t.Errorf("site = %q, want the previous good build %q", now, good)
	}
	if _, err := os.Stat(filepath.Join(good, "index.html")); err != nil {
		t.Errorf("previous build no longer readable: %v", err)
	}
}

func TestBuildReportsAMissingDatabase(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	b.cfg.DBPath = filepath.Join(t.TempDir(), "gone.db")
	if err := b.Build(context.Background()); err == nil {
		t.Fatal("want an error when the database is missing")
	}
}

func TestBuildPassesTheComputedSourcePathToEvidence(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	if err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(*b.site.Load(), "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := sourceFilename(b.cfg.ProjectDir, filepath.Join(b.cfg.WorkDir, "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != want {
		t.Errorf("EVIDENCE_SOURCE__twillingate__filename = %q, want %q", body, want)
	}
}

func TestServeHTTPBeforeTheFirstBuild(t *testing.T) {
	b := newTestBuilder(t)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestServeHTTPServesTheBuild(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	if err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	// "/" rather than "/index.html": FileServer redirects the explicit form.
	b.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "snapshot.db") {
		t.Errorf("body = %q, want the recorded Evidence source path", rec.Body.String())
	}
}

// A replica that never received the schema is the failure the builder cannot
// see from Evidence's exit status: every source query fails, npm still exits
// 0, and the site is rebuilt from the previous pass's data.
func TestBuildRejectsADatabaseWithoutTheTwillingateSchema(t *testing.T) {
	calls := stubNPM(t, false)
	b := newTestBuilder(t)
	other := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table unrelated(x)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	b.cfg.DBPath = other

	err = b.Build(context.Background())
	if err == nil {
		t.Fatal("want an error for a database with no twillingate schema")
	}
	if !strings.Contains(err.Error(), "not a twillingate database") {
		t.Errorf("error = %v, want it to name the database", err)
	}
	if !strings.Contains(err.Error(), other) {
		t.Errorf("error = %v, want it to name %s", err, other)
	}
	if *calls != 0 {
		t.Errorf("ran npm %d times; the check belongs before the build", *calls)
	}
}

// A database that has the table but no rows in it has been created by
// something other than a migration run — an empty file the driver made.
func TestCheckSchemaRejectsAnUnmigratedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table schema_migrations(version integer primary key)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := checkSchema(context.Background(), path); err == nil {
		t.Fatal("want an error when no migration has been applied")
	}
}

func TestBuildRejectsASourceEvidenceCouldNotRead(t *testing.T) {
	stubNPMReportingABrokenSource(t)
	b := newTestBuilder(t)
	err := b.Build(context.Background())
	if err == nil {
		t.Fatal("want an error when Evidence could not read a source")
	}
	if !strings.Contains(err.Error(), "could not read a source") {
		t.Errorf("error = %v, want it to name the failed source step", err)
	}
	if b.site.Load() != nil {
		t.Error("a build that could not read its sources was published")
	}
}
