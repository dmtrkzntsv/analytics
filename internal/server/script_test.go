package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
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
	// Behavioural markers the snippet must contain.
	for _, marker := range []string{
		"analytics_ignore", // opt-out
		"data-key",         // credential wiring
		"data-identity",    // identity mode, mirroring projects.json
		"data-user",        // server-rendered identity
		"data-group",       // server-rendered group
		"sendBeacon",       // transport
		"pushState",        // SPA tracking
		"popstate",         // SPA tracking
		"webdriver",        // automation filter
		"/api/events",      // the only endpoint
		"$pageview",        // reserved name
		"$user_id",         // reserved keys
		"$group_id",
		"analytics_visitor", // stored visitor id
		"identify:",         // identity API
		"reset:",            // logout, so a shared browser does not leak
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("script.js missing %q", marker)
		}
	}
	// Removed surfaces must not linger in the shipped asset.
	for _, gone := range []string{"data-project", "/api/hit", `"/api/event"`} {
		if strings.Contains(body, gone) {
			t.Errorf("script.js still references removed %q", gone)
		}
	}
}

// The snippet must not write to localStorage for an anonymous project: that
// storage is the thing that ends the consent-free posture.
func TestScriptGuardsVisitorStorageBehindIdentifiedMode(t *testing.T) {
	src := string(trackingScript)
	i := strings.Index(src, "function visitorId()")
	if i < 0 {
		t.Fatal("visitorId() not found")
	}
	body := src[i:]
	guard := strings.Index(body, `if (!identified) return null;`)
	set := strings.Index(body, "lsSet(VISITOR")
	if guard < 0 || set < 0 || guard > set {
		t.Error("visitorId() must return early for anonymous projects before writing storage")
	}
}

// The Plausible shim is served from the collector so a migrating site does
// not have to copy the file into its own static assets. The bytes served
// are the ones docs/plausible/README.md documents, so the two cannot drift.
func TestPlausibleShimServed(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/plausible-shim.js", nil)
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
	if w.Body.String() != string(docs.PlausibleShim) {
		t.Error("/js/plausible-shim.js does not serve the documented file verbatim")
	}
	// Behavioural markers, so an empty or truncated embed fails loudly.
	for _, marker := range []string{
		"plausible-event-", // the class prefix it binds to
		"window.plausible", // the manual call form
		"auxclick",         // middle-click opens links too
		"twillingate",      // the tracker global it forwards to
	} {
		if !strings.Contains(w.Body.String(), marker) {
			t.Errorf("plausible-shim.js missing %q", marker)
		}
	}
}
