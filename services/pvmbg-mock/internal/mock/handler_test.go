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

func testPVMBGCatalog(t *testing.T) *PVMBGCatalog {
	t.Helper()
	c, err := NewPVMBGCatalog(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 500*time.Millisecond, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPVMBGSchemaToggleAddsConfidenceToAllExistingReports(t *testing.T) {
	c := testPVMBGCatalog(t)
	before := c.Snapshot()
	if len(before) != 20 {
		t.Fatalf("reports=%d, want 20", len(before))
	}
	if before[0].ConfidenceLevel != nil {
		t.Fatalf("v1 unexpectedly has confidence: %+v", before[0])
	}
	if err := c.SetSchemaVersion(2); err != nil {
		t.Fatal(err)
	}
	after := c.Snapshot()
	if len(after) != len(before) {
		t.Fatalf("schema toggle changed record count: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ReportID != after[i].ReportID {
			t.Fatalf("record identity changed at %d", i)
		}
		if after[i].ConfidenceLevel == nil || *after[i].ConfidenceLevel < 0 || *after[i].ConfidenceLevel > 1 {
			t.Fatalf("v2 confidence missing/invalid at %d: %+v", i, after[i])
		}
	}
	if err := c.SetSchemaVersion(1); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot()[0].ConfidenceLevel; got != nil {
		t.Fatalf("v1 must omit confidence, got %v", *got)
	}
}

func TestPVMBGCatalogRejectsSeedCountAboveSnapshotBound(t *testing.T) {
	_, err := NewPVMBGCatalogWithCount(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 1001, 0, 0)
	if err == nil {
		t.Fatal("catalog accepted more seeds than the bounded snapshot allows")
	}
}

func TestPVMBGCatalogBoundsGeneratedHistoryAndKeepsSeedRecords(t *testing.T) {
	catalog := testPVMBGCatalog(t)
	for i := 0; i < 1100; i++ {
		catalog.AddGenerated(time.Date(2026, 9, 28, 10, 0, 10, 0, time.UTC).Add(time.Duration(i) * time.Second))
	}
	reports := catalog.Snapshot()
	if len(reports) != 1020 {
		t.Fatalf("snapshot contains %d reports; want 20 seeds plus 1000 generated records", len(reports))
	}
	seen := make(map[string]bool, len(reports))
	for _, report := range reports {
		seen[report.ReportID] = true
	}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("sim-test-run-report-%03d", i)
		if !seen[id] {
			t.Fatalf("seed report %q was evicted", id)
		}
	}
	if seen["sim-test-run-report-021"] || !seen["sim-test-run-report-1120"] {
		t.Fatalf("generated retention window is wrong: oldest-present=%v newest-present=%v", seen["sim-test-run-report-021"], seen["sim-test-run-report-1120"])
	}
}

func TestPVMBGMaximumSnapshotFitsAggregatorResponseLimit(t *testing.T) {
	catalog, err := NewPVMBGCatalogWithCount(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 1000, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetSchemaVersion(2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		catalog.AddGenerated(time.Date(2026, 9, 28, 10, 0, 10, 0, time.UTC).Add(time.Duration(i) * time.Second))
	}
	sleep := func(context.Context, time.Duration) error { return nil }
	h := NewPVMBGHandler(PVMBGConfig{Token: "test-token"}, catalog, sleep, nil)
	req := httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d", resp.Code)
	}
	if len(resp.Body.Bytes()) > 1<<20 {
		t.Fatalf("maximum snapshot is %d bytes; exceeds aggregator's 1 MiB limit", len(resp.Body.Bytes()))
	}
}

func TestPVMBGCatalogHonorsConfiguredSeedCountAboveMinimum(t *testing.T) {
	catalog, err := NewPVMBGCatalogWithCount(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "test-run", 25, 500*time.Millisecond, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reports := catalog.Snapshot()
	if len(reports) != 25 {
		t.Fatalf("reports=%d, want configured 25", len(reports))
	}
	seen := map[string]bool{}
	for _, report := range reports {
		if seen[report.ReportID] {
			t.Fatalf("duplicate report id %q", report.ReportID)
		}
		seen[report.ReportID] = true
	}
}

func TestPVMBGHandlerAuthAdminSchemaAndOutage(t *testing.T) {
	c := testPVMBGCatalog(t)
	calls := 0
	sleep := func(context.Context, time.Duration) error { calls++; return nil }
	h := NewPVMBGHandler(PVMBGConfig{Token: "test-token"}, c, sleep, func(min, max time.Duration) time.Duration { return min })

	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", unauth.Code)
	}
	if calls != 0 {
		t.Fatalf("unauthorized request reached delay path: %d calls", calls)
	}

	admin := httptest.NewRequest(http.MethodPost, "/admin/schema-version", strings.NewReader(`{"version":2}`))
	admin.Header.Set("Authorization", "Bearer test-token")
	adminResp := httptest.NewRecorder()
	h.ServeHTTP(adminResp, admin)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("schema status=%d body=%s", adminResp.Code, adminResp.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil)
	get.Header.Set("Authorization", "Bearer test-token")
	getResp := httptest.NewRecorder()
	h.ServeHTTP(getResp, get)
	if getResp.Code != http.StatusOK {
		t.Fatalf("reports status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	var reports []map[string]any
	if err := json.Unmarshal(getResp.Body.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 20 {
		t.Fatalf("reports=%d", len(reports))
	}
	if _, ok := reports[0]["confidence_level"]; !ok {
		t.Fatalf("v2 field missing from old record: %+v", reports[0])
	}

	outage := httptest.NewRequest(http.MethodPost, "/admin/outage", strings.NewReader(`{"enabled":true}`))
	outage.Header.Set("Authorization", "Bearer test-token")
	outageResp := httptest.NewRecorder()
	h.ServeHTTP(outageResp, outage)
	if outageResp.Code != http.StatusOK {
		t.Fatalf("outage toggle=%d", outageResp.Code)
	}
	failed := httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil)
	failed.Header.Set("Authorization", "Bearer test-token")
	failedResp := httptest.NewRecorder()
	h.ServeHTTP(failedResp, failed)
	if failedResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("outage data status=%d", failedResp.Code)
	}
}

func TestPVMBGRejectsUnknownAdminFieldsAndExposesPublicHealth(t *testing.T) {
	h := NewPVMBGHandler(PVMBGConfig{Token: "test-token"}, testPVMBGCatalog(t), func(context.Context, time.Duration) error { return nil }, func(min, max time.Duration) time.Duration { return min })
	req := httptest.NewRequest(http.MethodPost, "/admin/schema-version", strings.NewReader(`{"version":2,"extra":true}`))
	req.Header.Set("Authorization", "Bearer test-token")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("unknown admin field status=%d", resp.Code)
	}

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || health.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health=%d %q", health.Code, health.Body.String())
	}
}

func TestPVMBGAdminDelayChangesTheResponseDelayRange(t *testing.T) {
	c := testPVMBGCatalog(t)
	var slept, pickedMin, pickedMax time.Duration
	sleep := func(_ context.Context, d time.Duration) error { slept = d; return nil }
	pick := func(min, max time.Duration) time.Duration { pickedMin, pickedMax = min, max; return max }
	h := NewPVMBGHandler(PVMBGConfig{Token: "test-token"}, c, sleep, pick)
	admin := httptest.NewRequest(http.MethodPost, "/admin/delay", strings.NewReader(`{"min_ms":3000,"max_ms":3000}`))
	admin.Header.Set("Authorization", "Bearer test-token")
	adminResp := httptest.NewRecorder()
	h.ServeHTTP(adminResp, admin)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("delay update=%d body=%s", adminResp.Code, adminResp.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil)
	get.Header.Set("Authorization", "Bearer test-token")
	getResp := httptest.NewRecorder()
	h.ServeHTTP(getResp, get)
	if getResp.Code != http.StatusOK {
		t.Fatalf("reports=%d body=%s", getResp.Code, getResp.Body.String())
	}
	want := 3 * time.Second
	if pickedMin != want || pickedMax != want || slept != want {
		t.Fatalf("range=(%s,%s), slept=%s; want all %s", pickedMin, pickedMax, slept, want)
	}
}

func TestPVMBGCatalogKeepsGeneratingDuringOutage(t *testing.T) {
	c := testPVMBGCatalog(t)
	c.SetOutage(true)
	c.AddGenerated(time.Date(2026, 9, 28, 10, 0, 10, 0, time.UTC))
	if got := len(c.Snapshot()); got != 21 {
		t.Fatalf("reports during outage=%d, want 21", got)
	}
}

func TestSchemaVersion3RenamesAlertLevel(t *testing.T) {
	c, err := NewPVMBGCatalog(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), "t", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSchemaVersion(3); err != nil {
		t.Fatal(err)
	}
	for _, raw := range c.RenderedSnapshot() {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["alert_level"]; ok {
			t.Fatal("alert_level must be renamed")
		}
		if _, ok := m["level"].(string); !ok {
			t.Fatal("level must carry the former alert_level string")
		}
	}
}

func TestSchemaVersion4ChangesEruptionCountType(t *testing.T) {
	c, err := NewPVMBGCatalog(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), "t", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	original := c.Snapshot()
	if err := c.SetSchemaVersion(4); err != nil {
		t.Fatal(err)
	}
	rendered := c.RenderedSnapshot()
	if len(rendered) != len(original) {
		t.Fatalf("rendered=%d original=%d", len(rendered), len(original))
	}
	for i, raw := range rendered {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		value, ok := m["eruption_count_24h"].(string)
		if !ok {
			t.Fatalf("eruption_count_24h must become a JSON string, got %#v", m["eruption_count_24h"])
		}
		if want := fmt.Sprint(original[i].EruptionCount24h); value != want {
			t.Fatalf("eruption_count_24h=%q, want %q (same value as v1 integer)", value, want)
		}
	}
}

func TestSchemaVersion5DropsAshHeight(t *testing.T) {
	c, err := NewPVMBGCatalog(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), "t", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSchemaVersion(5); err != nil {
		t.Fatal(err)
	}
	rendered := c.RenderedSnapshot()
	if len(rendered) == 0 {
		t.Fatal("no rendered reports")
	}
	for _, raw := range rendered {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["ash_column_height_m"]; ok {
			t.Fatal("ash_column_height_m must be absent in schema version 5")
		}
		// Everything else stays identical to v2 (confidence_level included).
		if _, ok := m["confidence_level"]; !ok {
			t.Fatal("version 5 must still carry v2's confidence_level field")
		}
		if _, ok := m["alert_level"].(string); !ok {
			t.Fatal("version 5 must keep alert_level untouched")
		}
	}
}

func TestAdminSchemaVersionAcceptsIncompatibleModes(t *testing.T) {
	c := testPVMBGCatalog(t)
	h := NewPVMBGHandler(PVMBGConfig{Token: "test-token"}, c, func(context.Context, time.Duration) error { return nil }, func(min, max time.Duration) time.Duration { return min })
	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/admin/schema-version", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		var decoded map[string]any
		_ = json.Unmarshal(resp.Body.Bytes(), &decoded)
		return resp, decoded
	}

	resp, _ := post(`{"version":3}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("version 3 admin status=%d body=%s", resp.Code, resp.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil)
	get.Header.Set("Authorization", "Bearer test-token")
	getResp := httptest.NewRecorder()
	h.ServeHTTP(getResp, get)
	var reports []map[string]any
	if err := json.Unmarshal(getResp.Body.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 {
		t.Fatal("no reports returned")
	}
	for _, r := range reports {
		if _, ok := r["alert_level"]; ok {
			t.Fatalf("alert_level still present after switching to version 3: %+v", r)
		}
		if _, ok := r["level"].(string); !ok {
			t.Fatalf("level missing after switching to version 3: %+v", r)
		}
	}

	resp, body := post(`{"version":6}`)
	if resp.Code != http.StatusBadRequest || body["error"] != "invalid_version" {
		t.Fatalf("version 6 status=%d body=%v", resp.Code, body)
	}

	resp, body = post(`{"mode":"type_change"}`)
	if resp.Code != http.StatusOK || body["version"] != float64(4) {
		t.Fatalf("mode alias type_change status=%d body=%v", resp.Code, body)
	}
	resp, body = post(`{"mode":"rename_field"}`)
	if resp.Code != http.StatusOK || body["version"] != float64(3) {
		t.Fatalf("mode alias rename_field status=%d body=%v", resp.Code, body)
	}
	resp, body = post(`{"mode":"missing_required_field"}`)
	if resp.Code != http.StatusOK || body["version"] != float64(5) {
		t.Fatalf("mode alias missing_required_field status=%d body=%v", resp.Code, body)
	}
	resp, body = post(`{"mode":"compatible_v1"}`)
	if resp.Code != http.StatusOK || body["version"] != float64(1) {
		t.Fatalf("mode alias compatible_v1 status=%d body=%v", resp.Code, body)
	}
	resp, body = post(`{"mode":"compatible_v2"}`)
	if resp.Code != http.StatusOK || body["version"] != float64(2) {
		t.Fatalf("mode alias compatible_v2 status=%d body=%v", resp.Code, body)
	}
	resp, body = post(`{"mode":"unknown_mode"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode status=%d body=%v", resp.Code, body)
	}

	resp, _ = post(`{"version":1,"mode":"compatible_v1"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("version+mode together must be rejected: status=%d", resp.Code)
	}
	resp, _ = post(`{}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("neither version nor mode must be rejected: status=%d", resp.Code)
	}
}
