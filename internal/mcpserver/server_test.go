package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func newHandlerFixture(t *testing.T, over map[string]string) http.Handler {
	t.Helper()
	path := seedDB(t) // from readdb_test.go: migrated DB with project 'blog'
	base := map[string]string{
		"DATABASE_URL":  "sqlite://" + path,
		"MCP_AUTH_MODE": "token",
		"MCP_TOKEN":     "ar_testtoken",
	}
	for k, v := range over {
		if v == "" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateMCP(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, cfg.Retention, logger)
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, closeDB, err := NewHandler(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })
	return h
}

func TestMCPRequires401WithChallenge(t *testing.T) {
	h := newHandlerFixture(t, map[string]string{
		"MCP_AUTH_ISSUER":  "https://idp.example.com",
		"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
	www := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(www, "resource_metadata") {
		t.Errorf("WWW-Authenticate = %q; must point at the metadata URL", www)
	}
}

func TestMCPTokenAuthPasses(t *testing.T) {
	h := newHandlerFixture(t, nil)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "analytics") {
		t.Errorf("no serverInfo in %s", rec.Body.String())
	}
}

func TestMCPWrongTokenRejected(t *testing.T) {
	h := newHandlerFixture(t, nil)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ar_wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestPRMServedWhenIssuerConfigured(t *testing.T) {
	h := newHandlerFixture(t, map[string]string{
		"MCP_AUTH_ISSUER":  "https://idp.example.com",
		"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "idp.example.com") || !strings.Contains(body, "analytics.example.com") {
		t.Errorf("metadata = %s", body)
	}
}

func TestPRM404WithoutIssuer(t *testing.T) {
	h := newHandlerFixture(t, nil) // token mode, no issuer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestTokenNeverLoggedAtInfo(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	path := seedDB(t)
	base := map[string]string{"DATABASE_URL": "sqlite://" + path,
		"MCP_AUTH_MODE": "token", "MCP_TOKEN": "ar_secrettoken"}
	cfg, _ := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	st, _ := store.Open(cfg.Database)
	defer st.Close()
	reg := manage.New(st, cfg.Retention, logger)
	reg.Reload(context.Background())
	h, closeDB, err := NewHandler(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ar_secrettoken")
	h.ServeHTTP(httptest.NewRecorder(), req)
	req2 := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer ar_wrongtoken")
	h.ServeHTTP(httptest.NewRecorder(), req2)
	for _, secret := range []string{"ar_secrettoken", "ar_wrongtoken"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("token %q appeared in info-level logs", secret)
		}
	}
}
