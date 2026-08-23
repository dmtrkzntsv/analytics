// Package server implements the ingestion HTTP API (spec §5). It never
// logs request bodies, IPs, or User-Agents.
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/store"
)

const maxBody = 16 << 10

type Enqueuer interface {
	EnqueueHit(h store.WebHit)
	EnqueueEvent(e store.ProductEvent)
}

type Salt interface {
	Current(ctx context.Context) (string, error)
}

type server struct {
	cfg    *config.Config
	queue  Enqueuer
	geo    geo.Provider
	salt   Salt
	logger *slog.Logger
	// origin -> project index for O(1) allowed-origin checks
	originOK map[string]map[string]bool // project -> set of origins
}

func New(cfg *config.Config, q Enqueuer, g geo.Provider, salt Salt, logger *slog.Logger) http.Handler {
	s := &server{cfg: cfg, queue: q, geo: g, salt: salt, logger: logger,
		originOK: map[string]map[string]bool{}}
	for _, p := range cfg.Projects {
		set := map[string]bool{}
		for _, o := range p.AllowedOrigins {
			set[strings.TrimSuffix(o, "/")] = true
		}
		s.originOK[p.Alias] = set
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hit", s.handleHit)
	mux.HandleFunc("POST /api/event", s.handleEvent)
	mux.HandleFunc("OPTIONS /api/hit", s.handlePreflight)
	mux.HandleFunc("OPTIONS /api/event", s.handlePreflight)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	s.registerScript(mux) // Task 14; no-op stub until then
	return mux
}

// registerScript wires GET /js/script.js. Task 14 replaces this stub with
// the real handler; until then the route is unregistered (404).
func (s *server) registerScript(mux *http.ServeMux) {}

// originAllowed reports whether the request origin is allowed for the
// project and emits CORS headers when it is.
func (s *server) originAllowed(w http.ResponseWriter, r *http.Request, project string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	set, ok := s.originOK[project]
	if !ok || !set[strings.TrimSuffix(origin, "/")] {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	return true
}

// anyOriginAllowed is used by preflight, where no body (and thus no
// project id) is available: allow iff the origin belongs to any project.
func (s *server) anyOriginAllowed(origin string) bool {
	o := strings.TrimSuffix(origin, "/")
	for _, set := range s.originOK {
		if set[o] {
			return true
		}
	}
	return false
}

func (s *server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && s.anyOriginAllowed(origin) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "86400")
	}
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
