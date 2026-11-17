package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPTracingPreservesParentAndSkipsProbes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	originalProvider, originalPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(originalProvider)
		otel.SetTextMapPropagator(originalPropagator)
		_ = provider.Shutdown(context.Background())
	})
	monitor := New()
	var received trace.SpanContext
	server := httptest.NewServer(monitor.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = trace.SpanContextFromContext(r.Context())
		_, _ = io.WriteString(w, "ok")
	}), "/events/{id}"))
	defer server.Close()
	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	client := http.Client{Transport: HTTPTransport(http.DefaultTransport)}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events/private-id", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	parent.End()
	if !received.IsValid() || received.TraceID() != parent.SpanContext().TraceID() {
		t.Fatal("HTTP parent was not propagated")
	}
	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("spans=%d expected parent, client, server", len(spans))
	}
	found := false
	for _, span := range spans {
		if span.Name == "GET /events/{id}" {
			found = true
		}
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), "secret-token") {
				t.Fatal("credential leaked into trace")
			}
		}
	}
	if !found {
		t.Fatal("server trace name must use the route template")
	}
	for _, endpoint := range []string{"/health", "/ready"} {
		response, err := client.Get(server.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	if len(exporter.GetSpans()) != len(spans) {
		t.Fatal("health/readiness produced trace noise")
	}
}

func TestTraceSamplingRejectsInvalidRatios(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	for _, value := range []string{"NaN", "+Inf", "-0.1", "1.1", "invalid"} {
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", value)
		if stop, err := StartTracing("test"); err == nil {
			stop()
			t.Fatalf("accepted invalid sampler ratio %q", value)
		}
	}
}
