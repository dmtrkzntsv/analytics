package dashboards

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// pollInterval is how often the database is stat-ed. It is deliberately not
// configurable: two stat calls a minute cost nothing, and the knob that
// matters — how often Evidence rebuilds — is DASHBOARDS_INTERVAL.
const pollInterval = time.Minute

// stamp fingerprints the database for change detection.
//
// The -wal sibling is included because a live database under traffic only has
// its main file touched at checkpoints. Stat rather than PRAGMA data_version:
// a restore job replaces the file by rename, and a held connection would keep
// reading the old inode and never notice. A missing file contributes an empty
// entry, so a database that appears later registers as a change.
func stamp(dbPath string) string {
	var sb strings.Builder
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&sb, "%d:%d|", fi.Size(), fi.ModTime().UnixNano())
		} else {
			sb.WriteString("-|")
		}
	}
	return sb.String()
}

// Run serves the dashboards until ctx is cancelled, rebuilding whenever the
// database has changed and at most once per cfg.Interval.
func Run(ctx context.Context, cfg config.DashboardsConfig, logger *slog.Logger) error {
	b := NewBuilder(cfg, logger)
	srv := &http.Server{Addr: cfg.Addr, Handler: b}

	errs := make(chan error, 1)
	go func() {
		logger.Info("dashboards: listening", "addr", cfg.Addr, "database", cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	go b.loop(ctx)

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// rebuilder holds the state that decides whether a tick is worth a build:
// the fingerprint of the database as it was when the current site was built,
// and when that happened.
type rebuilder struct {
	b     *Builder
	built string
	last  time.Time
}

// tick builds when the database has changed since the last successful build
// and cfg.Interval has elapsed. The first call always builds. A failure is
// logged and left for the next tick; the previous site keeps serving.
func (r *rebuilder) tick(ctx context.Context) bool {
	cur := stamp(r.b.cfg.DBPath)
	if r.built != "" && (cur == r.built || time.Since(r.last) < r.b.cfg.Interval) {
		return false
	}
	if err := r.b.Build(ctx); err != nil {
		r.b.log.Error("dashboards: build failed", "error", err)
		return false
	}
	r.built, r.last = cur, time.Now()
	r.b.log.Info("dashboards: rebuilt", "database", r.b.cfg.DBPath)
	return true
}

// loop rebuilds immediately, then polls for changes until ctx is cancelled.
func (b *Builder) loop(ctx context.Context) {
	r := &rebuilder{b: b}
	r.tick(ctx)

	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}
