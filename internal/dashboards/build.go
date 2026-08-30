package dashboards

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// execCommand is a seam so tests can stand in for npm.
var execCommand = exec.CommandContext

// Builder owns one Evidence project: it rebuilds the site and serves the
// most recent successful build.
//
// Builds land in one of two slots inside the project directory and the
// handler is switched over atomically. Serving Evidence's own build/
// directory would expose a half-written tree for the length of every
// rebuild; the two slots also mean a failed build keeps the previous site up.
type Builder struct {
	cfg  config.DashboardsConfig
	log  *slog.Logger
	site atomic.Pointer[string]
}

func NewBuilder(cfg config.DashboardsConfig, log *slog.Logger) *Builder {
	return &Builder{cfg: cfg, log: log}
}

// Build snapshots the database, runs Evidence, and swaps the result in.
func (b *Builder) Build(ctx context.Context) error {
	snap := filepath.Join(b.cfg.WorkDir, "snapshot.db")
	if err := snapshot(ctx, b.cfg.DBPath, snap); err != nil {
		return fmt.Errorf("dashboards: snapshot: %w", err)
	}
	filename, err := sourceFilename(b.cfg.ProjectDir, snap)
	if err != nil {
		return fmt.Errorf("dashboards: source path: %w", err)
	}
	for _, script := range []string{"sources", "build"} {
		cmd := execCommand(ctx, "npm", "run", script)
		cmd.Dir = b.cfg.ProjectDir
		cmd.Env = append(os.Environ(), "EVIDENCE_SOURCE__twillingate__filename="+filename)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("dashboards: npm run %s: %w (%s)", script, err, out)
		}
	}
	return b.rotate()
}

func (b *Builder) slots() (string, string) {
	return filepath.Join(b.cfg.ProjectDir, "site.a"), filepath.Join(b.cfg.ProjectDir, "site.b")
}

// rotate moves Evidence's build/ output into the slot that is not currently
// being served, switches the handler over, and drops the old slot. Both
// slots live inside the project directory, so the rename never crosses a
// filesystem boundary.
func (b *Builder) rotate() error {
	a, c := b.slots()
	next, prev := a, ""
	if cur := b.site.Load(); cur != nil {
		prev = *cur
		if *cur == a {
			next = c
		}
	}
	if err := os.RemoveAll(next); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(b.cfg.ProjectDir, "build"), next); err != nil {
		return err
	}
	b.site.Store(&next)
	if prev != "" {
		if err := os.RemoveAll(prev); err != nil {
			b.log.Warn("dashboards: removing the previous build failed", "path", prev, "error", err)
		}
	}
	return nil
}

func (b *Builder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	root := b.site.Load()
	if root == nil {
		http.Error(w, "dashboards: building", http.StatusServiceUnavailable)
		return
	}
	http.FileServer(http.Dir(*root)).ServeHTTP(w, r)
}
