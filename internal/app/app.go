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
	infos := make([]store.ProjectInfo, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		infos = append(infos, store.ProjectInfo{Alias: p.Alias, Name: p.Name})
	}
	if err := st.SyncProjects(ctx, infos); err != nil {
		return err
	}
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

	runner := jobs.New(st, cfg, salter, logger, time.Now)
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	jobsDone := make(chan struct{})
	go func() { runner.Run(jobsCtx); close(jobsDone) }()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, buf, geoProvider, salter, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("serving", "addr", cfg.Listen, "projects", len(cfg.Projects))

	select {
	case <-ctx.Done():
	case err := <-errCh:
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
