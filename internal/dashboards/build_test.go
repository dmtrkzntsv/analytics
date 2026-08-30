package dashboards

import (
	"context"
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
