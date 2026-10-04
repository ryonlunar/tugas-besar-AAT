package jetstreamconsumer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHTTPLogsValidatedCorrelationAndStatusWithoutDemoKey(t *testing.T) {
	var logs bytes.Buffer
	runner := &Runner{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	handler := Handler(runner, inspectionStub{}, "private-demo-key", "/internal/v1/snapshots")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/snapshots", nil)
	request.Header.Set("X-Demo-Key", "private-demo-key")
	request.Header.Set("X-Correlation-ID", "demo-request-42")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["correlation_id"] != "demo-request-42" || entry["status"] != float64(200) || entry["duration_ms"] == nil || strings.Contains(logs.String(), "private-demo-key") {
		t.Fatalf("unexpected request log: %s", logs.String())
	}
	logs.Reset()
	request.Header.Set("X-Correlation-ID", "invalid id")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if !correlationPattern.MatchString(entry["correlation_id"].(string)) {
		t.Fatal("invalid correlation was logged")
	}
}
