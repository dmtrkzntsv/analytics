package server

import (
	"bytes"
	_ "embed"
	"net/http"

	"github.com/dmtrkzntsv/twillingate/internal/version"
)

// twillingate.js is the only served client, compiled from sdk/ (`npm run
// build` there rewrites the committed bundle). The legacy /js/script.js
// snippet was removed once every project had migrated.
//
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
	mux.HandleFunc("GET /js/twillingate.js", serve(versioned))
}
