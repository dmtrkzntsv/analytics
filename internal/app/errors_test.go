package app

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/dmitry/analytics/internal/config/configtest"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

// syncLogBuffer is a concurrency-safe io.Writer: Serve logs from several
// background goroutines while the test polls the buffer from its own.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncLogBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncLogBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// A bad GEO_URL must surface as a boot error rather than a silent fallback.
func TestServeFailsOnGeoProviderError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "geo-err.db")
	seedProject(t, dbPath,
		manage.ProjectSpec{Alias: "app", Name: "App", AllowedOrigins: []string{"https://app.com"}},
		"ak_test", "web")
	cfg := configtest.Load(t, map[string]string{
		"LISTEN_ADDR":  freePort(t),
		"DATABASE_URL": "sqlite://" + dbPath,
		"GEO_URL":      "unsupported-provider://x",
	})
	err := Serve(context.Background(), cfg, slog.Default(), true, false)
	if err == nil || !strings.Contains(err.Error(), "geo:") {
		t.Fatalf("Serve = %v, want a geo provider error", err)
	}
}

// An empty registry must warn the operator rather than boot silently with
// nothing configured to ingest.
func TestServeWarnsWhenNoProjectsConfigured(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	cfg := configtest.Load(t, map[string]string{
		"LISTEN_ADDR":  freePort(t),
		"DATABASE_URL": "sqlite://" + dbPath,
	})
	logs := runServeAndCollectLogs(t, cfg)
	if !strings.Contains(logs, "no projects configured") {
		t.Errorf("logs = %q, want a warning about no projects configured", logs)
	}
}

// A project with no active ingest key can receive nothing; boot must warn
// rather than silently accept a config that can never ingest anything for
// that project.
func TestServeWarnsAboutKeylessProjects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "keyless.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, configtest.Load(t, nil).Retention, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{
		Alias: "nokey", Name: "No Key", AllowedOrigins: []string{"https://nokey.example"}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	cfg := configtest.Load(t, map[string]string{
		"LISTEN_ADDR":  freePort(t),
		"DATABASE_URL": "sqlite://" + dbPath,
	})
	logs := runServeAndCollectLogs(t, cfg)
	if !strings.Contains(logs, "no active ingest keys") || !strings.Contains(logs, "nokey") {
		t.Errorf("logs = %q, want a warning naming the keyless project", logs)
	}
}

// A database whose schema collides with migration 001 (simulating a corrupt
// or foreign file at the configured path) must fail Serve's boot, not panic
// or silently proceed on a half-migrated schema.
func TestServeFailsOnMigrateError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collide.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// agg_web_daily is created by migration 001; pre-creating it with an
	// incompatible schema makes that migration fail partway.
	if _, err := raw.Exec(`CREATE TABLE agg_web_daily (nonsense TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	cfg := configtest.Load(t, map[string]string{
		"LISTEN_ADDR":  freePort(t),
		"DATABASE_URL": "sqlite://" + dbPath,
	})
	if err := Serve(context.Background(), cfg, slog.Default(), true, false); err == nil {
		t.Fatal("Serve = nil, want a migration error from the colliding schema")
	}
}

// An MCP auth mode that fails eagerly (oauth against an unreachable issuer)
// must fail Serve's boot on the standalone-listener path (api=false,
// mcpOn=true always goes through mcpserver.NewHandler).
func TestServeFailsOnMCPHandlerError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcp-err.db")
	seedProject(t, dbPath,
		manage.ProjectSpec{Alias: "app", Name: "App", AllowedOrigins: []string{"https://app.com"}},
		"ak_test", "web")
	cfg := configtest.Load(t, map[string]string{
		"LISTEN_ADDR":      freePort(t),
		"DATABASE_URL":     "sqlite://" + dbPath,
		"MCP_AUTH_MODE":    "oauth",
		"MCP_AUTH_ISSUER":  "http://127.0.0.1:1", // nothing listens on port 1: fails fast
		"MCP_RESOURCE_URL": "https://mcp.example.com",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := Serve(ctx, cfg, slog.Default(), false, true)
	if err == nil {
		t.Fatal("Serve = nil, want an error from an unreachable oauth issuer")
	}
}

// Same failure, but on the shared-listener path (api=true with MCP sharing
// the ingest address), which goes through mcpserver.Build directly instead
// of NewHandler.
func TestServeFailsOnMCPBuildErrorSharedListener(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcp-shared-err.db")
	seedProject(t, dbPath,
		manage.ProjectSpec{Alias: "app", Name: "App", AllowedOrigins: []string{"https://app.com"}},
		"ak_test", "web")
	addr := freePort(t)
	cfg := configtest.Load(t, map[string]string{
		"LISTEN_ADDR":      addr,
		"DATABASE_URL":     "sqlite://" + dbPath,
		"MCP_ADDR":         addr, // same as LISTEN_ADDR: shared-listener path
		"MCP_AUTH_MODE":    "oauth",
		"MCP_AUTH_ISSUER":  "http://127.0.0.1:1",
		"MCP_RESOURCE_URL": "https://mcp.example.com",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := Serve(ctx, cfg, slog.Default(), true, true)
	if err == nil {
		t.Fatal("Serve = nil, want an error from an unreachable oauth issuer")
	}
}

// The periodic ingest-summary logger must actually drain and log the
// counters it accumulates, not just exist. Shrinks the package-level ticker
// interval (mirroring internal/geo's refreshInterval) instead of waiting a
// real minute for the default ticker to fire.
func TestServeLogsIngestSummary(t *testing.T) {
	oldInterval := ingestSummaryInterval
	ingestSummaryInterval = 10 * time.Millisecond
	t.Cleanup(func() { ingestSummaryInterval = oldInterval })

	dbPath := filepath.Join(t.TempDir(), "summary.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	buf := &syncLogBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, logger, true, false) }()
	base := "http://" + addr
	waitHealthy(t, base)

	resp, err := http.Post(base+"/api/events", "application/json",
		strings.NewReader(`{"key":"ak_test","events":[{"name":"$pageview"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.After(5 * time.Second)
	for !strings.Contains(buf.String(), "ingest summary") {
		select {
		case <-deadline:
			t.Fatalf("log output = %q, want an ingest summary line", buf.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !strings.Contains(buf.String(), `key_label=web`) {
		t.Errorf("log output = %q, want key_label=web", buf.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}
}
