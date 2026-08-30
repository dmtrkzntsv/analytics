package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Build assembles the tool host, streamable transport and auth
// middleware, returning the auth-wrapped handler for the /mcp route
// (endpoint spec §3, §5). It mounts nothing itself: NewHandler wraps
// it with its own mux for the standalone-listener case, and Task 21's
// shared-mux case calls it directly to mount alongside /mcp on the
// ingest surface's mux via RegisterOn.
func Build(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error) {
	db, err := OpenReadDB(cfg.MCP.DBPath)
	if err != nil {
		return nil, nil, err
	}
	h := &host{db: db, reg: reg, ops: ops,
		timeout: cfg.MCP.QueryTimeout, maxRows: cfg.MCP.QueryMaxRows,
		publicURL: cfg.PublicURL, logger: logger}
	srv := mcp.NewServer(&mcp.Implementation{Name: "twillingate", Version: "1.0.0"}, nil)
	h.register(srv)
	h.registerResources(srv)
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, nil)

	protected, err := wrapAuth(ctx, cfg.MCP, streamable)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return protected, db.Close, nil
}

// NewHandler assembles the MCP endpoint: tool host, streamable
// transport, auth middleware, and (mode-dependent) the RFC 9728
// metadata route, mounted on its own mux. Routes registered on the
// returned mux: /mcp, /healthz, and
// /.well-known/oauth-protected-resource in oauth mode.
// The func() error closes the read DB.
func NewHandler(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error) {
	protected, closeDB, err := Build(ctx, cfg, reg, ops, logger)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	RegisterOn(mux, protected, cfg, true)
	return mux, closeDB, nil
}

// RegisterOn mounts the MCP routes on a mux. withHealthz=false when the
// mux is shared with the ingest surface, whose /healthz already exists
// (ServeMux panics on duplicate patterns).
func RegisterOn(mux *http.ServeMux, protected http.Handler, cfg *config.Config, withHealthz bool) {
	mux.Handle("/mcp", protected)
	if cfg.MCP.AuthMode == "oauth" {
		meta := &oauthex.ProtectedResourceMetadata{
			Resource:             cfg.MCP.ResourceURL,
			AuthorizationServers: []string{cfg.MCP.Issuer},
		}
		mux.Handle("GET /.well-known/oauth-protected-resource",
			auth.ProtectedResourceMetadataHandler(meta))
	}
	if withHealthz {
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})
	}
}

// wrapAuth builds the mode's verifier and middleware (endpoint spec §5.2).
func wrapAuth(ctx context.Context, m config.MCPConfig, next http.Handler) (http.Handler, error) {
	switch m.AuthMode {
	case "token":
		opts := &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
		return auth.RequireBearerToken(StaticVerifier(m.Token), opts)(next), nil
	case "oauth":
		jwksURL, err := DiscoverJWKSURL(ctx, m.Issuer, nil)
		if err != nil {
			return nil, fmt.Errorf("mcp oauth mode: %w (is the MCP_AUTH_DSN issuer correct and reachable?)", err)
		}
		v := OAuthVerifier(m.Issuer, m.Audience, NewJWKSCache(jwksURL, nil))
		return auth.RequireBearerToken(v, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: metadataURLFor(m.ResourceURL),
		})(next), nil
	case "cloudflare":
		// Access owns discovery and the 401 challenge at the edge; the
		// origin's only job is validating the assertion header. A thin
		// middleware instead of RequireBearerToken so a request whose
		// Authorization header Access did not populate is still judged
		// by the assertion alone (endpoint spec §5.2).
		//
		// CFTeamDomain is normally a bare team domain and gets an
		// "https://" prefix here; scheme-tolerant like CloudflareVerifier
		// itself so a caller (or test) that already has a full issuer URL
		// isn't double-prefixed into a malformed one.
		certsHost := m.CFTeamDomain
		if !strings.HasPrefix(certsHost, "http://") && !strings.HasPrefix(certsHost, "https://") {
			certsHost = "https://" + certsHost
		}
		cache := NewJWKSCache(certsHost+"/cdn-cgi/access/certs", nil)
		v := CloudflareVerifier(m.CFTeamDomain, m.CFAud, cache)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info, err := v(r.Context(), "", r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = info
			next.ServeHTTP(w, r)
		}), nil
	default:
		return nil, fmt.Errorf("mcpserver: unknown auth mode %q", m.AuthMode)
	}
}

func metadataURLFor(resourceURL string) string {
	// RFC 9728: the well-known path is host-rooted; the resource URL's
	// origin carries it. Good enough for the single-origin deployments
	// this server targets; revisit if a path-scoped resource needs the
	// path-suffix form.
	u := resourceURL
	for i := len("https://"); i < len(u); i++ {
		if u[i] == '/' {
			u = u[:i]
			break
		}
	}
	return u + "/.well-known/oauth-protected-resource"
}
