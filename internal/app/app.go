// Package app wires the serve subcommand (spec §1, §5, §9).
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/jobs"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/mcpserver"
	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
	"github.com/dmtrkzntsv/twillingate/internal/server"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
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

// httpSurface pairs a listen address with the handler serving it, so
// Serve can start/shut down an arbitrary number of listeners (one for the
// shared -api/-mcp case, up to two when the surfaces use different
// addresses) with the same loop.
type httpSurface struct {
	addr    string
	handler http.Handler
}

// Serve runs the requested surfaces (api: ingestion, mcpOn: the MCP
// endpoint) until ctx is cancelled or a listener fails. At least one of
// api/mcpOn must be true; the caller (cmd/twillingate) enforces that as a
// usage error before reaching here.
//
// Store/registry/geo/pipeline/jobs setup runs regardless of which surfaces
// are requested: jobs and pipeline are harmless when only MCP runs, and
// the MCP surface itself needs store+registry. Only the HTTP listeners and
// the ingest-summary goroutine are conditional.
//
// Shutdown order matters: HTTP drains first so no new events/requests
// arrive, then the ingest summary logger, then the jobs runner stops, and
// only then is the pipeline cancelled — its cancellation is what triggers
// the final flush, so it must come last or buffered events would be lost.
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, api, mcpOn bool) error {
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	// v_product_attrs' live half reads the cardinality cap from meta with a
	// scalar subquery -- SQL cannot see the environment. Written before
	// anything queries the view so the first read uses the configured cap
	// rather than the view's built-in fallback.
	if err := st.SetMeta(ctx, "product_attributes_top_n",
		strconv.Itoa(cfg.ProductAttributesTopN)); err != nil {
		return err
	}

	reg := manage.New(st, cfg.Retention, logger)
	if err := reg.Reload(ctx); err != nil {
		return err
	}
	if len(reg.Snapshot(ctx).Projects()) == 0 {
		logger.Warn("no projects configured; create one with `twillingate project create` or an MCP management tool")
	}
	warnLegacyProjectsFile(cfg, logger)

	// Seed the flat view so it exists before the first daily pass.
	if err := st.RebuildFlatView(ctx, reg.Snapshot(ctx).DeclaredAttributeKeys()); err != nil {
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

	// stopBackground shuts down jobs then pipeline, in that order, for use
	// on early-return error paths before the HTTP surfaces exist.
	stopBackground := func() {
		stopJobs()
		<-jobsDone
		stopPipe()
		<-pipeDone
	}

	var ingestHandler *server.Server
	if api {
		ingestHandler = server.New(cfg, reg, buf, geoProvider, salter, st, logger)
	}

	// Assemble the HTTP surface(s). When both -api and -mcp target the
	// same address, they share one listener/mux (Build, not NewHandler,
	// so the /mcp route mounts alongside the ingest routes without a
	// double /healthz registration); otherwise the MCP endpoint gets its
	// own standalone mux via NewHandler.
	var surfaces []httpSurface
	var mcpClose func() error
	if mcpOn {
		ops := manage.NewOps(reg, st)
		if api && cfg.MCP.Addr == cfg.Listen {
			protected, closeDB, err := mcpserver.Build(ctx, cfg, reg, ops, logger)
			if err != nil {
				stopBackground()
				return err
			}
			mcpClose = closeDB
			mux := http.NewServeMux()
			mux.Handle("/", ingestHandler) // ingest keeps its own /healthz and /js/*
			mcpserver.RegisterOn(mux, protected, cfg, false)
			surfaces = append(surfaces, httpSurface{cfg.Listen, mux})
		} else {
			mcpHandler, closeDB, err := mcpserver.NewHandler(ctx, cfg, reg, ops, logger)
			if err != nil {
				stopBackground()
				return err
			}
			mcpClose = closeDB
			if api {
				surfaces = append(surfaces, httpSurface{cfg.Listen, ingestHandler})
			}
			surfaces = append(surfaces, httpSurface{cfg.MCP.Addr, mcpHandler})
		}
	} else {
		surfaces = append(surfaces, httpSurface{cfg.Listen, ingestHandler})
	}
	if mcpClose != nil {
		defer mcpClose()
	}

	summaryDone := make(chan struct{})
	stopSummary := func() {}
	if api {
		var sumCtx context.Context
		sumCtx, stopSummary = context.WithCancel(context.Background())
		// ingestSummaryInterval is read here, synchronously, rather than
		// inside the goroutine: it is a package var a test shrinks to avoid
		// waiting a real minute, and reading it lazily from the background
		// goroutine would race against a later test's write to it.
		interval := ingestSummaryInterval
		go func() { logIngestSummary(sumCtx, ingestHandler, logger, interval); close(summaryDone) }()
	} else {
		close(summaryDone)
	}

	srvs := make([]*http.Server, len(surfaces))
	errCh := make(chan error, len(surfaces))
	for i, s := range surfaces {
		sv := &http.Server{
			Addr:              s.addr,
			Handler:           s.handler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		srvs[i] = sv
		go func(sv *http.Server) { errCh <- sv.ListenAndServe() }(sv)
		logger.Info("serving", "addr", s.addr, "projects", len(reg.Snapshot(ctx).Projects()))
	}

	remaining := len(srvs)
	var listenErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		listenErr = err
		remaining--
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, sv := range srvs {
		if err := sv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http shutdown", "error", err, "addr", sv.Addr)
		}
	}
	stopSummary()
	<-summaryDone
	stopJobs()
	<-jobsDone
	stopPipe() // triggers final flush
	<-pipeDone

	for i := 0; i < remaining; i++ {
		if err := <-errCh; listenErr == nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr = err
		}
	}
	return listenErr
}

// databasePath extracts the filesystem path from a sqlite DSN for use as
// the data dir (GeoLite2 DB lives next to the database).
func databasePath(dsn string) string {
	return strings.TrimPrefix(dsn, "sqlite://")
}

// warnLegacyProjectsFile warns once at boot when a pre-upgrade PROJECTS_FILE
// (or the installer's default projects.json path) is still present: the
// registry is now the sole source of project config, so the file is no
// longer read and needs a one-time `twillingate config import`. cfg is
// unused today but kept in the signature so a future per-install override
// does not need to change every call site; split out from Serve so the
// warning is unit-testable without booting a full server.
func warnLegacyProjectsFile(cfg *config.Config, logger *slog.Logger) {
	if legacy := os.Getenv("PROJECTS_FILE"); legacy != "" {
		logger.Warn("PROJECTS_FILE is no longer read; import it once with `twillingate config import`", "file", legacy)
		return
	}
	if _, err := os.Stat("/etc/analytics/projects.json"); err == nil {
		logger.Warn("projects.json found but no longer read; import it once with `twillingate config import /etc/analytics/projects.json`")
	}
}

// ingestSummaryInterval is how often logIngestSummary drains the counters.
// Package var (mirroring internal/geo's refreshInterval) so a test can
// shrink it instead of waiting a real minute for the ticker to fire.
var ingestSummaryInterval = time.Minute

// logIngestSummary emits one line per active key label each minute instead
// of logging per request. Labels, never keys — and this is what tells an
// operator an old key has gone idle and is safe to retire.
func logIngestSummary(ctx context.Context, srv *server.Server, logger *slog.Logger, interval time.Duration) {
	t := time.NewTicker(interval)
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
