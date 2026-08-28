package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
	"github.com/golang-jwt/jwt/v5"
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

// initReq builds a raw initialize JSON-RPC request body with the
// headers the StreamableHTTPHandler requires, per TestMCPTokenAuthPasses.
func initReq() *http.Request {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req
}

// TestMCPOAuthModePasses exercises oauth mode through the assembled
// handler end to end: valid fixture-signed JWTs pass, invalid ones are
// rejected with a challenge pointing at the absolute metadata URL, and
// the PRM route serves the fixture issuer.
func TestMCPOAuthModePasses(t *testing.T) {
	f := newJWKSFixture(t)
	h := newHandlerFixture(t, map[string]string{
		"MCP_AUTH_MODE":    "oauth",
		"MCP_TOKEN":        "",
		"MCP_AUTH_ISSUER":  f.issuer,
		"MCP_RESOURCE_URL": "https://analytics.example.com/mcp",
	})

	req := initReq()
	req.Header.Set("Authorization", "Bearer "+f.sign(t, f.claims(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "analytics") {
		t.Errorf("no serverInfo in %s", rec.Body.String())
	}

	bad := []struct {
		name string
		tok  string
	}{
		{"wrong signature", "not.a.jwt"},
		{"expired", f.sign(t, f.claims(jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}))},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			req := initReq()
			req.Header.Set("Authorization", "Bearer "+tc.tok)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d", rec.Code)
			}
			www := rec.Header().Get("WWW-Authenticate")
			want := "https://analytics.example.com/.well-known/oauth-protected-resource"
			if !strings.Contains(www, "resource_metadata") || !strings.Contains(www, want) {
				t.Errorf("WWW-Authenticate = %q; want absolute metadata URL %q", www, want)
			}
		})
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), f.issuer) {
		t.Errorf("metadata = %s; want issuer %s", rec2.Body.String(), f.issuer)
	}
}

// TestMCPCloudflareModePasses exercises cloudflare mode through the
// assembled handler: a valid Cf-Access-Jwt-Assertion passes, a missing
// one is rejected with no WWW-Authenticate (Access owns the edge
// challenge, not the origin), and the PRM route is never mounted.
func TestMCPCloudflareModePasses(t *testing.T) {
	f := newJWKSFixture(t)
	h := newHandlerFixture(t, map[string]string{
		"MCP_AUTH_MODE":      "cloudflare",
		"MCP_TOKEN":          "",
		"MCP_CF_TEAM_DOMAIN": f.issuer, // already has a scheme; must not be double-prefixed
		"MCP_CF_AUD":         "aud-tag-1",
	})

	assertion := f.sign(t, jwt.MapClaims{
		"iss": f.issuer, "aud": "aud-tag-1", "sub": "user@example.com",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := initReq()
	req.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec2.Code)
	}
	if www := rec2.Header().Get("WWW-Authenticate"); www != "" {
		t.Errorf("WWW-Authenticate = %q; cloudflare mode must not challenge from the origin", www)
	}

	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("code = %d; cloudflare mode must never mount the PRM route", rec3.Code)
	}
}

func TestHealthzUnauthenticated(t *testing.T) {
	h := newHandlerFixture(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

// TestRegisterOnWithoutHealthzOmitsRoute proves the shared-mux case
// (Task 21: MCP_ADDR == LISTEN_ADDR) doesn't collide with the ingest
// surface's own /healthz — RegisterOn(..., withHealthz=false) must not
// mount the route at all.
func TestRegisterOnWithoutHealthzOmitsRoute(t *testing.T) {
	path := seedDB(t)
	base := map[string]string{"DATABASE_URL": "sqlite://" + path,
		"MCP_AUTH_MODE": "token", "MCP_TOKEN": "ar_testtoken"}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
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
	protected, closeDB, err := Build(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })

	mux := http.NewServeMux()
	RegisterOn(mux, protected, cfg, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d; /healthz must not be mounted when withHealthz=false", rec.Code)
	}
}
