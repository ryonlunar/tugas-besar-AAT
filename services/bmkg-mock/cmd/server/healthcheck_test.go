package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthProbeRequiresHTTP200(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("health check path = %q", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		err := probeHealth(server.Client(), server.URL)
		server.Close()
		if status == http.StatusOK && err != nil {
			t.Errorf("HTTP %d health response rejected: %v", status, err)
		}
		if status != http.StatusOK && err == nil {
			t.Errorf("HTTP %d health response must be rejected", status)
		}
	}
}
