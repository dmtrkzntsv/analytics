package server

import (
	"bytes"
	_ "embed"
	"net/http"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/version"
)

// script.js is the frozen legacy snippet: deployed websites load it, so it
// is served byte-for-byte forever. twillingate.js is the current SDK,
// compiled from sdk/ (`npm run build` there rewrites the committed bundle).
//
//go:embed script.js
var trackingScript []byte

//go:embed twillingate.js
var sdkScript []byte

// The committed bundle carries this placeholder (in its banner and in
// twillingate.VERSION) so the artifact stays deterministic for CI's drift
// check; the served copy names the release the binary shipped in.
const sdkVersionPlaceholder = "__TWILLINGATE_VERSION__"

func (s *Server) registerScript(mux *http.ServeMux) {
	serve := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			h.Set("Content-Type", "text/javascript; charset=utf-8")
			h.Set("Cache-Control", "public, max-age=86400")
			w.Write(body)
		}
	}
	versioned := bytes.ReplaceAll(sdkScript,
		[]byte(sdkVersionPlaceholder), []byte(version.Version))
	// Helpers are served from the same table so a site loads them rather
	// than copying them into its own static assets. plausible-shim.js is
	// embedded from docs/, where the README documenting it lives, so the
	// hosted copy and the documented one are the same bytes.
	for path, body := range map[string][]byte{
		"GET /js/script.js":         trackingScript,
		"GET /js/twillingate.js":    versioned,
		"GET /js/plausible-shim.js": docs.PlausibleShim,
	} {
		mux.HandleFunc(path, serve(body))
	}
}
