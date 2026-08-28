// Package app wires the serve subcommand (spec §1, §5, §9).
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/identity"
	"github.com/dmitry/analytics/internal/jobs"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/pipeline"
	"github.com/dmitry/analytics/internal/server"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func NewLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	out := os.Stdout
	if cfg.File != "" {
		if f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640); err == nil {
			out = f
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}

// Serve runs the ingestion server until ctx is cancelled or the listener
// fails.
//
// Shutdown order matters: HTTP drains first so no new events arrive, then
// the jobs runner stops, and only then is the pipeline cancelled — its
// cancellation is what triggers the final flush, so it must come last or
// buffered events would be lost.
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	reg := manage.New(st, cfg.Retention, logger)
	if err := reg.Reload(ctx); err != nil {
		return err
	}
	if len(reg.Snapshot(ctx).Projects()) == 0 {
		logger.Warn("no projects configured; create one with `analytics project create` or an MCP management tool")
	}
	warnLegacyProjectsFile(cfg, logger)

	// Seed the flat view so it exists before the first daily pass.
	if keys, err := st.KnownAttributeKeys(ctx); err != nil {
		logger.Warn("flat view seed: attribute scan", "error", err)
	} else if err := st.RebuildFlatView(ctx, keys); err != nil {
		logger.Warn("flat view seed", "error", err)
	}

	salter := identity.NewSalter(st, time.Now)
	dataDir := filepath.Dir(databasePath(cfg.Database))
	geoProvider, err := geo.New(cfg.Geo, dataDir, logger)
	if err != nil {
		return err
	}
	defer geoProvider.Close()

	// The pipeline and jobs runner get their own contexts rather than ctx,
	// so cancelling ctx does not tear them down before HTTP has drained.
	buf := pipeline.New(cfg.Buffer, st, logger)
	pipeCtx, stopPipe := context.WithCancel(context.Background())
	pipeDone := make(chan struct{})
	go func() { buf.Run(pipeCtx); close(pipeDone) }()

	// A project with no active ingest key can receive nothing. That is a
	// legitimate retired state, so warn rather than refuse to start.
	for _, alias := range reg.Snapshot(ctx).KeylessProjects() {
		logger.Warn("project has no active ingest keys and can receive nothing", "project", alias)
	}

	runner := jobs.New(st, cfg, reg, salter, logger, time.Now)
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	jobsDone := make(chan struct{})
	go func() { runner.Run(jobsCtx); close(jobsDone) }()

	handler := server.New(cfg, reg, buf, geoProvider, salter, st, logger)
	sumCtx, stopSummary := context.WithCancel(context.Background())
	summaryDone := make(chan struct{})
	go func() { logIngestSummary(sumCtx, handler, logger); close(summaryDone) }()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("serving", "addr", cfg.Listen, "projects", len(reg.Snapshot(ctx).Projects()))

	select {
	case <-ctx.Done():
	case err := <-errCh:
		stopSummary()
		<-summaryDone
		stopJobs()
		<-jobsDone
		stopPipe()
		<-pipeDone
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
	stopSummary()
	<-summaryDone
	stopJobs()
	<-jobsDone
	stopPipe() // triggers final flush
	<-pipeDone
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// databasePath extracts the filesystem path from a sqlite DSN for use as
// the data dir (GeoLite2 DB lives next to the database).
func databasePath(dsn string) string {
	return strings.TrimPrefix(dsn, "sqlite://")
}

// warnLegacyProjectsFile warns once at boot when a pre-upgrade PROJECTS_FILE
// (or the installer's default projects.json path) is still present: the
// registry is now the sole source of project config, so the file is no
// longer read and needs a one-time `analytics config import`. cfg is
// unused today but kept in the signature so a future per-install override
// does not need to change every call site; split out from Serve so the
// warning is unit-testable without booting a full server.
func warnLegacyProjectsFile(cfg *config.Config, logger *slog.Logger) {
	if legacy := os.Getenv("PROJECTS_FILE"); legacy != "" {
		logger.Warn("PROJECTS_FILE is no longer read; import it once with `analytics config import`", "file", legacy)
		return
	}
	if _, err := os.Stat("/etc/analytics/projects.json"); err == nil {
		logger.Warn("projects.json found but no longer read; import it once with `analytics config import /etc/analytics/projects.json`")
	}
}

// logIngestSummary emits one line per active key label each minute instead
// of logging per request. Labels, never keys — and this is what tells an
// operator an old key has gone idle and is safe to retire.
func logIngestSummary(ctx context.Context, srv *server.Server, logger *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for label, c := range srv.Counters().Drain() {
				logger.Info("ingest summary", "key_label", label,
					"accepted", c[0], "rejected", c[1])
			}
		}
	}
}
