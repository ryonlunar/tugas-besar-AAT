package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

type fakeRepository struct {
	items   []mapper.HazardEvent
	states  map[mapper.Source]store.SourceState
	pingErr error
	listErr error
	getErr  error
	listHit int
}

func (f *fakeRepository) Ping(context.Context) error { return f.pingErr }
func (f *fakeRepository) List(_ context.Context, source string, limit, offset int) ([]mapper.HazardEvent, bool, error) {
	f.listHit++
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	filtered := make([]mapper.HazardEvent, 0)
	for _, item := range f.items {
		if source == "" || string(item.Source) == source {
			filtered = append(filtered, item)
		}
	}
	if offset >= len(filtered) {
		return []mapper.HazardEvent{}, false, nil
	}
	end := offset + limit
	hasMore := end < len(filtered)
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], hasMore, nil
}
func (f *fakeRepository) Get(_ context.Context, id string) (mapper.HazardEvent, bool, error) {
	if f.getErr != nil {
		return mapper.HazardEvent{}, false, f.getErr
	}
	for _, item := range f.items {
		if item.HazardID == id {
			return item, true, nil
		}
	}
	return mapper.HazardEvent{}, false, nil
}
func (f *fakeRepository) SourceStates(_ context.Context, sources []mapper.Source, _ time.Time) (map[mapper.Source]store.SourceState, error) {
	result := make(map[mapper.Source]store.SourceState, len(sources))
	for _, source := range sources {
		result[source] = f.states[source]
	}
	return result, nil
}

func TestAdmissionReturnsRetryable429ButHealthBypassesLimit(t *testing.T) {
	repo := &fakeRepository{}
	h := NewHandlerWithLimit(repo, "internal-test-only", time.Now, 1)
	h.inflight <- struct{}{}

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards", nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" || !strings.Contains(response.Body.String(), "RATE_LIMITED") {
		t.Fatalf("over-capacity response status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health should bypass data admission, got %d", health.Code)
	}
}

func TestInvalidCorrelationIDIsReplacedWithUUIDv4(t *testing.T) {
	h := NewHandler(&fakeRepository{}, "internal-test-only", time.Now)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("X-Correlation-ID", "spaces are invalid")
	h.ServeHTTP(response, request)
	correlationID := response.Header().Get("X-Correlation-ID")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(correlationID) {
		t.Fatalf("expected generated UUIDv4, got %q", correlationID)
	}
}

func TestInternalAPIRequiresItsOwnKey(t *testing.T) {
	repo := &fakeRepository{}
	h := NewHandler(repo, "internal-test-only", time.Now)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/hazards", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.listHit != 0 {
		t.Fatal("unauthenticated request reached repository")
	}
}

func TestListSummaryDoesNotExposeRawFieldsAndReturnsPaginationMetadata(t *testing.T) {
	item := mapper.HazardEvent{HazardID: "11111111-1111-4111-8111-111111111111", Source: mapper.SourcePVMBG, SourceRefID: "pv-1", HazardType: mapper.HazardVolcanic, Severity: mapper.SeveritySiaga, AreaName: "Gunung Simulasi", Latitude: -7, Longitude: 108, OccurredAt: "2026-09-28T03:00:00Z", IngestedAt: "2026-09-28T03:01:00Z", Attributes: map[string]any{"confidence_level": 0.9}}
	repo := &fakeRepository{items: []mapper.HazardEvent{item, item}, states: map[mapper.Source]store.SourceState{mapper.SourcePVMBG: {Status: "fresh"}}}
	h := NewHandler(repo, "internal-test-only", time.Now)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards?source=PVMBG&limit=1&offset=0", nil)
	request.Header.Set("X-Internal-Key", "internal-test-only")
	request.Header.Set("X-Correlation-ID", "poll-42")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Correlation-ID") != "poll-42" {
		t.Fatalf("correlation id not echoed: %q", response.Header().Get("X-Correlation-ID"))
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data count=%d", len(data))
	}
	encoded, _ := json.Marshal(data[0])
	if strings.Contains(string(encoded), "attributes") || strings.Contains(string(encoded), "latitude") || strings.Contains(string(encoded), "source_ref_id") {
		t.Fatalf("summary leaked raw fields: %s", encoded)
	}
	meta := body["meta"].(map[string]any)
	if meta["has_more"] != true || meta["limit"] != float64(1) {
		t.Fatalf("bad page metadata: %+v", meta)
	}
}

func TestInternalAPIRejectsInvalidQueriesAndUnavailableSource(t *testing.T) {
	t.Run("invalid query", func(t *testing.T) {
		repo := &fakeRepository{}
		h := NewHandler(repo, "key", time.Now)
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards?limit=0", nil)
		req.Header.Set("X-Internal-Key", "key")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("all unavailable", func(t *testing.T) {
		repo := &fakeRepository{states: map[mapper.Source]store.SourceState{mapper.SourceBMKG: {Status: "unavailable"}, mapper.SourcePVMBG: {Status: "unavailable"}}}
		h := NewHandler(repo, "key", time.Now)
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards", nil)
		req.Header.Set("X-Internal-Key", "key")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "SOURCE_UNAVAILABLE") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestRequestLogIncludesCorrelationTimingAndRejectedStatus(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	handler := NewHandler(&fakeRepository{}, "private-internal-key", time.Now)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards", nil)
	request.Header.Set("X-Correlation-ID", "request-42")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["correlation_id"] != "request-42" || entry["status"] != float64(401) || entry["duration_ms"] == nil || strings.Contains(logs.String(), "private-internal-key") {
		t.Fatalf("unexpected log: %s", logs.String())
	}
}
