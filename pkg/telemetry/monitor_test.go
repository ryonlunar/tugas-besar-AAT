package telemetry

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestOperationMetricsBoundUntrustedLabels(t *testing.T) {
	m := New()
	for i := 0; i < 100; i++ {
		m.ObserveOperation(fmt.Sprintf("operation-%d", i), fmt.Sprintf("https://peer/%d", i), nil, 10*time.Millisecond)
	}
	m.ObserveOperation("source_poll", "BMKG", errors.New("unavailable"), time.Second)
	m.ObserveOperation("ack", "nats", nil, time.Millisecond)
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, family := range families {
		if family.GetName() != "aat_operations_total" && family.GetName() != "aat_operation_duration_seconds" {
			continue
		}
		found++
		if len(family.Metric) != 3 {
			t.Fatalf("%s: expected 3 label sets, got %d", family.GetName(), len(family.Metric))
		}
		counts := make(map[string]float64)
		for _, metric := range family.Metric {
			labels := make(map[string]string)
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			count := metric.GetCounter().GetValue()
			if family.GetName() == "aat_operation_duration_seconds" {
				count = float64(metric.GetHistogram().GetSampleCount())
			}
			counts[labels["operation"]+"/"+labels["peer"]+"/"+labels["outcome"]] = count
		}
		if counts["other/other/success"] != 100 || counts["source_poll/BMKG/failed"] != 1 || counts["ack/nats/success"] != 1 {
			t.Fatalf("%s: unexpected observations: %v", family.GetName(), counts)
		}
	}
	if found != 2 {
		t.Fatalf("expected both operation metrics, got %d", found)
	}
}

func exposition(t *testing.T, m *Monitor) string {
	t.Helper()
	response := httptest.NewRecorder()
	promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}).ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if response.Code != 200 {
		t.Fatalf("metrics: %d %s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func TestHTTPMetricsPreserveResponsesAndBoundLabels(t *testing.T) {
	m := New()
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Correlation-ID", r.Header.Get("X-Correlation-ID"))
		w.WriteHeader(401)
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "denied")
	}), "/v1/hazards/{id}")
	for _, path := range []string{"/v1/hazards/private-one?source=BMKG&token=secret", "/v1/hazards/private-two?source=PVMBG", "/random-private-path?source=unbounded-value"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Authorization", "Bearer private-token")
		request.Header.Set("X-Correlation-ID", "private-correlation")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 401 || response.Body.String() != "denied" || response.Header().Get("X-Correlation-ID") != "private-correlation" {
			t.Fatalf("response changed: %+v", response)
		}
	}
	metrics := exposition(t, m)
	for _, secret := range []string{"private-one", "private-two", "secret", "private-token", "private-correlation", "unbounded-value", "random-private-path"} {
		if strings.Contains(metrics, secret) {
			t.Fatalf("unbounded/sensitive label: %s", secret)
		}
	}
	for _, expected := range []string{`route="/v1/hazards/{id}"`, `route="other"`, `source="BMKG"`, `source="PVMBG"`, `source="all"`, `status="401"`} {
		if !strings.Contains(metrics, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
}

func TestProbesDoNotCountAndImplicitResponseIs200(t *testing.T) {
	m := New()
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }), "/read")
	for _, path := range []string{"/health", "/ready", "/read"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	metrics := exposition(t, m)
	if strings.Contains(metrics, `route="/health"`) || strings.Contains(metrics, `route="/ready"`) {
		t.Fatal("probe counted as traffic")
	}
	if !strings.Contains(metrics, `aat_http_requests_total{method="GET",route="/read",source="all",status="200"} 1`) {
		t.Fatal("implicit response was not counted correctly")
	}
}

func TestDedicatedListenerAndReadiness(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer ready.Close()
	// Reserve an available local port and release it immediately before Listen.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("METRICS_ADDR", address)
	m := New()
	closeMetrics, err := m.Listen(ready.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeMetrics()
	response, err := http.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(data), "aat_service_ready 0") {
		t.Fatal("scrape success conflated with readiness")
	}
	response, err = http.Get("http://" + address + "/v1/hazards")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("business route exposed on metrics listener")
	}
	closeMetrics()
	if connection, err := net.Dial("tcp", address); err == nil {
		connection.Close()
		t.Fatal("listener still open")
	}
}

func TestMetricsListenerDisabledByDefault(t *testing.T) {
	t.Setenv("METRICS_ADDR", "")
	closeMetrics, err := New().Listen("http://127.0.0.1:1/ready")
	if err != nil {
		t.Fatal(err)
	}
	closeMetrics()
}
