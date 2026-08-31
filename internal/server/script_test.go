package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
)

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
