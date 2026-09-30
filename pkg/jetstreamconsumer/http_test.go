package jetstreamconsumer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type inspectionStub struct{}

func (inspectionStub) Ping(context.Context) error { return nil }
func (inspectionStub) Inspect(context.Context) (map[string]any, error) {
	return map[string]any{"inbox_count": 1}, nil
}

func TestDemoEndpointRequiresItsOwnKey(t *testing.T) {
	runner := &Runner{}
	handler := Handler(runner, inspectionStub{}, "demo-secret", "/internal/v1/snapshots")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/snapshots", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("without key: got HTTP %d", response.Code)
	}
	request.Header.Set("X-Demo-Key", "demo-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("with key: got HTTP %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disconnected consumer: got HTTP %d", response.Code)
	}
}
