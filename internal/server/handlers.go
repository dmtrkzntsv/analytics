package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/dmitry/analytics/internal/enrich"
	"github.com/dmitry/analytics/internal/identity"
	"github.com/dmitry/analytics/internal/store"
	"github.com/google/uuid"
)

type hitPayload struct {
	Project  string `json:"project"`
	URL      string `json:"url"`
	Referrer string `json:"referrer"`
}

type eventPayload struct {
	Project    string         `json:"project"`
	Name       string         `json:"name"`
	UserID     string         `json:"user_id"`
	Attributes map[string]any `json:"attributes"`
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil { // only on entropy exhaustion; fall back to v4
		return uuid.NewString()
	}
	return id.String()
}

func (s *server) handleHit(w http.ResponseWriter, r *http.Request) {
	var p hitPayload
	if !decode(w, r, &p) {
		return
	}
	if p.Project == "" || p.URL == "" {
		http.Error(w, "project and url required", http.StatusBadRequest)
		return
	}
	if s.cfg.Project(p.Project) == nil {
		w.WriteHeader(http.StatusNoContent) // silent drop, no oracle
		return
	}
	if !s.originAllowed(w, r, p.Project) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	page, err := enrich.ParsePageURL(p.URL)
	if err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	ua := r.Header.Get("User-Agent")
	if enrich.IsBot(ua) {
		w.WriteHeader(http.StatusAccepted) // accepted, silently ignored
		return
	}
	salt, err := s.salt.Current(r.Context())
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	device, browser, osName := enrich.ParseUserAgent(ua)
	source := enrich.CleanReferrer(p.Referrer, page.Host)
	if source == "" && page.Ref != "" {
		source = page.Ref // ?ref= fallback (spec §5.4)
	}
	s.queue.EnqueueHit(store.WebHit{
		ID:             newID(),
		Project:        p.Project,
		TS:             time.Now().UTC(),
		ActorID:        identity.VisitorHash(salt, clientIP(r), ua, p.Project),
		Path:           page.Path,
		ReferrerSource: source,
		UTMSource:      page.UTMSource,
		UTMMedium:      page.UTMMedium,
		UTMCampaign:    page.UTMCampaign,
		Country:        s.geo.Country(r, clientIP(r)),
		Device:         device,
		Browser:        browser,
		OS:             osName,
	})
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) handleEvent(w http.ResponseWriter, r *http.Request) {
	var p eventPayload
	if !decode(w, r, &p) {
		return
	}
	if p.Project == "" || p.Name == "" || p.UserID == "" {
		http.Error(w, "project, name, user_id required", http.StatusBadRequest)
		return
	}
	if s.cfg.Project(p.Project) == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Origin, when present, must be allowed; absence is permitted here
	// (server-side SDKs, spec §5.2).
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(w, r, p.Project) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	s.queue.EnqueueEvent(store.ProductEvent{
		ID:         newID(),
		Project:    p.Project,
		EventName:  p.Name,
		UserID:     p.UserID,
		TS:         time.Now().UTC(),
		Attributes: sanitizeAttributes(p.Attributes),
	})
	w.WriteHeader(http.StatusAccepted)
}

// sanitizeAttributes applies spec §5.1 limits: ≤50 keys (sorted-key order
// keeps the outcome deterministic), keys ≤64 chars, values stringified and
// truncated to 512 chars.
func sanitizeAttributes(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		if len(k) > 0 && len(k) <= 64 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 50 {
		keys = keys[:50]
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		var v string
		switch t := in[k].(type) {
		case string:
			v = t
		default:
			b, err := json.Marshal(t)
			if err != nil {
				continue
			}
			v = string(b)
		}
		if len(v) > 512 {
			v = v[:512]
		}
		out[k] = v
	}
	return out
}
