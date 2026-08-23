package server

import (
	_ "embed"
	"net/http"
)

//go:embed script.js
var trackingScript []byte

func (s *server) registerScript(mux *http.ServeMux) {
	mux.HandleFunc("GET /js/script.js", func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=86400")
		w.Write(trackingScript)
	})
}
