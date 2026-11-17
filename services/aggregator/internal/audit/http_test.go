package audit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuditAndOwnerCredentialsAreSeparated(t *testing.T) {
	handler := NewHandler(nil, "assistant-token", "owner-key")
	for _, tc := range []struct {
		path, header, value string
		status              int
	}{
		{"/internal/audit/events", "", "", 401},
		{"/internal/audit/events", "Authorization", "Bearer owner-key", 401},
		{"/internal/v1/failed-messages", "Authorization", "Bearer assistant-token", 401},
		{"/internal/v1/failed-messages", "X-Internal-Key", "assistant-token", 401},
		{"/internal/audit/events?source=UNKNOWN", "Authorization", "Bearer assistant-token", 400},
		{"/internal/audit/events?limit=1001", "Authorization", "Bearer assistant-token", 400},
		{"/internal/audit/events?limit=1&limit=2", "Authorization", "Bearer assistant-token", 400},
		{"/internal/audit/events?since=yesterday", "Authorization", "Bearer assistant-token", 400},
		{"/internal/audit/missing", "Authorization", "Bearer assistant-token", 404},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.header != "" {
			request.Header.Set(tc.header, tc.value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
	}
}
