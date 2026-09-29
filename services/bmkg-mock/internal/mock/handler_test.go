package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testBMKGCatalog(t *testing.T) *BMKGCatalog {
	t.Helper()
	c, err := NewBMKGCatalog(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestBMKGCatalogSeedsTwentyRecordsAndRelatedWarnings(t *testing.T) {
	c := testBMKGCatalog(t)
	events, warnings := c.Snapshot()
	if len(events) != 20 {
		t.Fatalf("events=%d, want 20", len(events))
	}
	if len(warnings) == 0 {
		t.Fatal("expected at least one synthetic tsunami warning")
	}
	byID := make(map[string]SeismicEvent, len(events))
	for _, event := range events {
		byID[event.EventID] = event
		if event.RegionName == "" || event.OccurredAt.IsZero() {
			t.Fatalf("incomplete event: %+v", event)
		}
		if event.EventID[:len("sim-test-run-")] != "sim-test-run-" {
			t.Fatalf("event id lacks run id: %q", event.EventID)
		}
	}
	for _, warning := range warnings {
		event, ok := byID[warning.RelatedEventID]
		if !ok || !event.PotentialTsunami {
			t.Fatalf("warning is not attached to a tsunami-potential event: %+v", warning)
		}
	}
}

func TestBMKGCatalogRejectsSeedCountAboveSnapshotBound(t *testing.T) {
	_, err := NewBMKGCatalogWithCount(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 1001)
	if err == nil {
		t.Fatal("catalog accepted more seeds than the bounded snapshot allows")
	}
}

func TestBMKGCatalogBoundsGeneratedHistoryAndKeepsSeedRecords(t *testing.T) {
	catalog := testBMKGCatalog(t)
	for i := 0; i < 1100; i++ {
		catalog.AddGenerated(time.Date(2026, 9, 28, 10, 0, 10, 0, time.UTC).Add(time.Duration(i) * time.Second))
	}
	events, _ := catalog.Snapshot()
	if len(events) != 1020 {
		t.Fatalf("snapshot contains %d events; want 20 seeds plus 1000 generated records", len(events))
	}
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		seen[event.EventID] = true
	}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("sim-test-run-event-%03d", i)
		if !seen[id] {
			t.Fatalf("seed event %q was evicted", id)
		}
	}
	if seen["sim-test-run-event-021"] || !seen["sim-test-run-event-1120"] {
		t.Fatalf("generated retention window is wrong: oldest-present=%v newest-present=%v", seen["sim-test-run-event-021"], seen["sim-test-run-event-1120"])
	}
}

func TestBMKGMaximumSnapshotFitsAggregatorResponseLimit(t *testing.T) {
	catalog, err := NewBMKGCatalogWithCount(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		catalog.AddGenerated(time.Date(2026, 9, 28, 10, 0, 10, 0, time.UTC).Add(time.Duration(i) * time.Second))
	}
	h := NewBMKGHandler(BMKGConfig{APIKey: "test-key"}, catalog, noSleep)
	req := httptest.NewRequest(http.MethodGet, "/seismic-events", nil)
	req.Header.Set("X-BMKG-Key", "test-key")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d", resp.Code)
	}
	if len(resp.Body.Bytes()) > 1<<20 {
		t.Fatalf("maximum snapshot is %d bytes; exceeds aggregator's 1 MiB limit", len(resp.Body.Bytes()))
	}
}

func TestBMKGCatalogHonorsConfiguredSeedCountAboveMinimum(t *testing.T) {
	catalog, err := NewBMKGCatalogWithCount(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 24)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := catalog.Snapshot()
	if len(events) != 24 {
		t.Fatalf("events=%d, want configured 24", len(events))
	}
	seen := map[string]bool{}
	for _, event := range events {
		if seen[event.EventID] {
			t.Fatalf("duplicate event id %q", event.EventID)
		}
		seen[event.EventID] = true
	}
}

func TestBMKGDataEndpointAuthenticatesAndFiltersInclusively(t *testing.T) {
	c := testBMKGCatalog(t)
	events, _ := c.Snapshot()
	cutoff := events[10].OccurredAt.Format(time.RFC3339Nano)
	h := NewBMKGHandler(BMKGConfig{APIKey: "test-key"}, c, noSleep)

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/seismic-events", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/seismic-events?since="+cutoff, nil)
	req.Header.Set("X-BMKG-Key", "test-key")
	got := httptest.NewRecorder()
	h.ServeHTTP(got, req)
	if got.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
	var filtered []SeismicEvent
	if err := json.Unmarshal(got.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != len(events)-10 {
		t.Fatalf("filtered=%d, want %d", len(filtered), len(events)-10)
	}
	if !filtered[0].OccurredAt.Equal(events[10].OccurredAt) {
		t.Fatalf("since must be inclusive: first=%s cutoff=%s", filtered[0].OccurredAt, cutoff)
	}
}

func TestBMKGHealthIsPublicAndUnknownQueryIsRejected(t *testing.T) {
	h := NewBMKGHandler(BMKGConfig{APIKey: "test-key"}, testBMKGCatalog(t), noSleep)
	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || health.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health=%d %q", health.Code, health.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/seismic-events?unexpected=1", nil)
	req.Header.Set("X-BMKG-Key", "test-key")
	got := httptest.NewRecorder()
	h.ServeHTTP(got, req)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestBMKGReadinessIsPublicAndMethodChecked(t *testing.T) {
	h := NewBMKGHandler(BMKGConfig{APIKey: "test-key"}, testBMKGCatalog(t), noSleep)
	ready := httptest.NewRecorder()
	h.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("readiness=%d %q", ready.Code, ready.Body.String())
	}
	wrongMethod := httptest.NewRecorder()
	h.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/ready", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("readiness method guard=%d Allow=%q", wrongMethod.Code, wrongMethod.Header().Get("Allow"))
	}
}

func TestBMKGCatalogAddsOneGeneratedRecord(t *testing.T) {
	c := testBMKGCatalog(t)
	before, _ := c.Snapshot()
	c.AddGenerated(time.Date(2026, 9, 28, 10, 0, 10, 0, time.UTC))
	after, _ := c.Snapshot()
	if len(after) != len(before)+1 {
		t.Fatalf("after=%d before=%d", len(after), len(before))
	}
	if after[len(after)-1].EventID == before[len(before)-1].EventID {
		t.Fatal("generated event must have a new id")
	}
}

func TestBMKGTsunamiSinceUsesInternalPublicationTime(t *testing.T) {
	c := testBMKGCatalog(t)
	_, warnings := c.Snapshot()
	if len(warnings) == 0 {
		t.Fatal("fixture requires a warning")
	}
	since := warnings[len(warnings)-1].PublishedAt.Add(time.Minute).Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodGet, "/tsunami-warnings?since="+since, nil)
	req.Header.Set("X-BMKG-Key", "test-key")
	resp := httptest.NewRecorder()
	NewBMKGHandler(BMKGConfig{APIKey: "test-key"}, c, noSleep).ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var got []TsunamiWarning
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("warning should be filtered by publication time, got %d", len(got))
	}
	if strings.Contains(resp.Body.String(), "published_at") {
		t.Fatalf("internal publication time leaked: %s", resp.Body.String())
	}
}
