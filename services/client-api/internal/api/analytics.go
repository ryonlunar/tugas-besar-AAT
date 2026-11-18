package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed analytics/*
var analyticsFiles embed.FS

func (h *Handler) registerAnalytics() {
	files, err := fs.Sub(analyticsFiles, "analytics")
	if err != nil {
		panic(err)
	}
	page := http.StripPrefix("/analytics/", http.FileServer(http.FS(files)))
	h.mux.Handle("GET /analytics/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		page.ServeHTTP(w, r)
	}))
	h.mux.Handle("GET /v1/analytics", h.admit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.authenticate(w, r); !ok {
			return
		}
		// The Analytics contract consists only of aggregates, safe for summary clients.
		h.proxy(w, r, "", "/internal/v1/analytics")
	})))
}
