package server

import (
	_ "embed"
	"net/http"
)

// script.js is the frozen legacy snippet: deployed websites load it, so it
// is served byte-for-byte forever. twillingate.js is the current SDK,
// compiled from sdk/ (`npm run build` there rewrites the committed bundle).
//
//go:embed script.js
var trackingScript []byte

//go:embed twillingate.js
var sdkScript []byte

func (s *Server) registerScript(mux *http.ServeMux) {
	serve := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			h.Set("Content-Type", "text/javascript; charset=utf-8")
			h.Set("Cache-Control", "public, max-age=86400")
			w.Write(body)
		}
	}
	mux.HandleFunc("GET /js/script.js", serve(trackingScript))
	mux.HandleFunc("GET /js/twillingate.js", serve(sdkScript))
}
