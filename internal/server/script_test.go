package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScriptServed(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/script.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("content-type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("cache-control = %q", cc)
	}
	body := w.Body.String()
	// Behavioral markers the snippet must contain (spec §5.5 / §5.4a):
	for _, marker := range []string{
		"analytics_ignore",       // opt-out
		"data-project",           // project wiring
		"sendBeacon",             // transport
		"pushState",              // SPA tracking
		"popstate",               // SPA tracking
		"webdriver",              // automation filter
		"/api/hit", "/api/event", // endpoints
		"identify", // identity API
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("script.js missing %q", marker)
		}
	}
}
