package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTwillingateSDKServed(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/twillingate.js", nil)
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
	if !strings.Contains(body, "twillingate.js v") {
		t.Error("bundle is missing its version banner; rebuild with `npm run build` in sdk/")
	}
	// Behavioural markers the committed bundle must contain. The full
	// behaviour is covered by the vitest suite in sdk/; this guards against
	// an empty or stale artifact being embedded.
	for _, marker := range []string{
		"twillingate",         // the global
		"twillingate_ignore",  // opt-out
		"analytics_ignore",    // legacy opt-out still honoured
		"twillingate_visitor", // stored visitor id
		"analytics_visitor",   // legacy storage migration
		"twillingate_queue",   // offline queue
		"data-key",            // snippet-mode credential wiring
		"sendBeacon",          // unload transport
		"pushState",           // SPA tracking
		"webdriver",           // automation filter
		"/api/events",         // the only endpoint
		"$pageview",           // web analytics
		"$screen_view",        // app analytics
		"$install_id",         // app/identity batch attribute
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("twillingate.js missing %q", marker)
		}
	}
}

// The legacy snippet must keep serving byte-for-byte: deployed websites
// load it, and the cut-over promise is that they never break.
func TestLegacyScriptStillServed(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/script.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if w.Body.String() != string(trackingScript) {
		t.Error("/js/script.js does not serve the embedded legacy snippet verbatim")
	}
}
