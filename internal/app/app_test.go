package app

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/config/configtest"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }

func testConfig(t *testing.T, addr, dbPath string) *config.Config {
	t.Helper()
	return configtest.Load(t, map[string]string{
		"LISTEN_ADDR":             addr,
		"DATABASE_URL":            "sqlite://" + dbPath,
		"BUFFER_FLUSH_MAX_EVENTS": "2",
		"BUFFER_FLUSH_INTERVAL":   "50ms",
		"BUFFER_CAPACITY":         "100",
	}, `[{"alias": "app", "name": "App", "ingest_keys": [{"key": "ak_test", "label": "web"}], "allowed_origins": ["https://app.com"]}]`)
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}

func TestServeEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, slog.Default()) }()

	base := "http://" + addr
	waitHealthy(t, base)

	post := func(path, origin, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("POST", base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("User-Agent", chromeUA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := post("/api/hit", "https://app.com",
		`{"project":"app","url":"https://app.com/pricing","referrer":""}`); r.StatusCode != 202 {
		t.Fatalf("hit: %d", r.StatusCode)
	}
	if r := post("/api/event", "",
		`{"project":"app","name":"signup","user_id":"u1","attributes":{"plan":"pro"}}`); r.StatusCode != 202 {
		t.Fatalf("event: %d", r.StatusCode)
	}
	if r := post("/api/hit", "https://evil.com",
		`{"project":"app","url":"https://app.com/"}`); r.StatusCode != 403 {
		t.Fatalf("evil origin: %d", r.StatusCode)
	}

	// The tracking snippet must be served by the same process.
	resp, err := http.Get(base + "/js/script.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("script.js: %d", resp.StatusCode)
	}

	// Graceful shutdown must flush the buffer.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}

	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bg := context.Background()
	days, err := st.WebDaysBefore(bg, "app", mustDay("2100-01-01"))
	if err != nil || len(days) != 1 {
		t.Fatalf("web hit not persisted: %v %v", days, err)
	}
	pdays, err := st.ProductDaysBefore(bg, "app", mustDay("2100-01-01"))
	if err != nil || len(pdays) != 1 {
		t.Fatalf("event not persisted: %v %v", pdays, err)
	}
	// The project must have been registered from config on boot.
	aliases, err := st.ProjectAliases(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0] != "app" {
		t.Fatalf("aliases = %v, want [app]", aliases)
	}
}

// Restarting against an existing database must re-run migrations harmlessly
// and keep prior data.
func TestServeRestartsOnExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	bg := context.Background()

	run := func(body string) {
		t.Helper()
		addr := freePort(t)
		ctx, cancel := context.WithCancel(bg)
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, testConfig(t, addr, dbPath), slog.Default()) }()
		base := "http://" + addr
		waitHealthy(t, base)
		req, err := http.NewRequest("POST", base+"/api/hit", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://app.com")
		req.Header.Set("User-Agent", chromeUA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 202 {
			t.Fatalf("hit: %d", resp.StatusCode)
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
	run(`{"project":"app","url":"https://app.com/one"}`)
	run(`{"project":"app","url":"https://app.com/two"}`)

	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	days, err := st.WebDaysBefore(bg, "app", mustDay("2100-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("days = %v, want one day holding both hits", days)
	}
}

// A listen address already in use must surface as an error, not a hang.
func TestServeReportsListenFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := testConfig(t, l.Addr().String(), filepath.Join(t.TempDir(), "busy.db"))
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(context.Background(), cfg, slog.Default()) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("want an error binding an occupied port, got nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve hung instead of reporting the bind failure")
	}
}

func TestServeRejectsBadDatabaseDSN(t *testing.T) {
	cfg := testConfig(t, freePort(t), filepath.Join(t.TempDir(), "x.db"))
	cfg.Database = "bogus://nope"
	if err := Serve(context.Background(), cfg, slog.Default()); err == nil {
		t.Fatal("want an error for an unknown DSN scheme")
	}
}

func TestNewLogger(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", ""} {
		if NewLogger(config.LogConfig{Level: level}) == nil {
			t.Errorf("level %q returned nil", level)
		}
	}
	if NewLogger(config.LogConfig{Format: "text"}) == nil {
		t.Error("text format returned nil")
	}
	// A configured file must actually receive the output.
	path := filepath.Join(t.TempDir(), "log.json")
	NewLogger(config.LogConfig{File: path}).Info("hello")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("log file = %q, want it to contain the message", body)
	}
}

// Spec §5.4a: an IP address or a full User-Agent must never reach the
// database or the logs. They may only feed the one-way visitor hash and the
// device/browser classification.
func TestServeNeverPersistsIPOrUserAgent(t *testing.T) {
	const (
		secretIP = "203.0.113.77"
		secretUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 UNIQUEMARKERSTRING"
	)
	dbPath := filepath.Join(t.TempDir(), "privacy.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, logger) }()
	base := "http://" + addr
	waitHealthy(t, base)

	req, err := http.NewRequest("POST", base+"/api/hit",
		strings.NewReader(`{"project":"app","url":"https://app.com/pricing"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.com")
	req.Header.Set("User-Agent", secretUA)
	req.Header.Set("X-Forwarded-For", secretIP)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("hit: %d", resp.StatusCode)
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

	// Scan the raw database bytes, not just the columns: this catches an IP
	// or UA smuggled into any field, index or free page.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretIP, "UNIQUEMARKERSTRING", secretUA} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("database contains %q; it must never be persisted", secret)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain %q; it must never be logged", secret)
		}
	}
	// Sanity: the hit really was recorded, so the assertions above are not
	// passing merely because nothing was written.
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	days, err := st.WebDaysBefore(context.Background(), "app", mustDay("2100-01-01"))
	if err != nil || len(days) != 1 {
		t.Fatalf("hit not persisted: %v %v", days, err)
	}
}
