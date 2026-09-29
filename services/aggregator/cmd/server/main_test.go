package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessProbeRequiresHTTP200(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		if err := probeReadiness(server.Client(), server.URL); err != nil {
			t.Fatalf("ready server rejected: %v", err)
		}
	})

	t.Run("not ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		if err := probeReadiness(server.Client(), server.URL); err == nil {
			t.Fatal("non-200 readiness response must fail the container probe")
		}
	})
}
