// Package telemetry provides transport metrics. It does not inspect credentials or payloads.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Monitor struct {
	Registry         *prometheus.Registry
	requests         *prometheus.CounterVec
	latency          *prometheus.HistogramVec
	operations       *prometheus.CounterVec
	operationLatency *prometheus.HistogramVec
}

func New() *Monitor {
	m := &Monitor{Registry: prometheus.NewRegistry()}
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aat_http_requests_total", Help: "Completed application HTTP requests; excludes health and readiness probes."}, []string{"method", "route", "source", "status"})
	m.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "aat_http_request_duration_seconds", Help: "Application HTTP response duration.", Buckets: []float64{.005, .01, .025, .05, .1, .2, .3, .5, 1, 2, 4, 8}}, []string{"method", "route", "source", "status"})
	m.operations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aat_operations_total", Help: "Background operation attempts, including retries."}, []string{"operation", "peer", "outcome"})
	m.operationLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "aat_operation_duration_seconds", Help: "Background operation duration.", Buckets: []float64{.005, .01, .025, .05, .1, .2, .3, .5, 1, 2, 4, 8}}, []string{"operation", "peer", "outcome"})
	m.Registry.MustRegister(m.requests, m.latency, m.operations, m.operationLatency, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Monitor) ObserveOperation(operation, peer string, err error, duration time.Duration) {
	// Keep IDs, URLs, and configured consumer names in logs, not metric labels.
	switch operation {
	case "source_poll", "jetstream_publish", "connect", "bind", "fetch", "consumer_info", "consumer_create", "ack", "nak":
	default:
		operation = "other"
	}
	switch peer {
	case "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI", "nats":
	default:
		peer = "other"
	}
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	m.operations.WithLabelValues(operation, peer, outcome).Inc()
	m.operationLatency.WithLabelValues(operation, peer, outcome).Observe(duration.Seconds())
}

// Wrap bounds route/method/source labels; IDs and query strings never become labels.
func (m *Monitor) Wrap(next http.Handler, routes ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}
		method := r.Method
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			method = "OTHER"
		}
		route := "other"
		for _, candidate := range routes {
			if candidate == r.URL.Path {
				route = candidate
				break
			}
			if strings.HasSuffix(candidate, "/{id}") {
				prefix := strings.TrimSuffix(candidate, "{id}")
				tail := strings.TrimPrefix(r.URL.Path, prefix)
				if strings.HasPrefix(r.URL.Path, prefix) && tail != "" && !strings.Contains(tail, "/") {
					route = candidate
					break
				}
			}
		}
		source := r.URL.Query().Get("source")
		switch source {
		case "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI":
		default:
			source = "all"
		}
		response := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(response, r)
		status := strconv.Itoa(response.status)
		m.requests.WithLabelValues(method, route, source, status).Inc()
		m.latency.WithLabelValues(method, route, source, status).Observe(time.Since(started).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wroteHeader {
		return
	}
	w.status, w.wroteHeader = status, true
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Listen is disabled unless METRICS_ADDR is set. The caller owns its lifetime.
// readyURL probes the application's existing readiness contract, independently of scrape success.
func (m *Monitor) Listen(readyURL string) (func(), error) {
	address := strings.TrimSpace(os.Getenv("METRICS_ADDR"))
	if address == "" {
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen metrics: %w", err)
	}
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	m.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "aat_service_ready", Help: "Whether the existing application readiness endpoint returns HTTP 200."}, func() float64 {
		response, err := client.Get(readyURL)
		if err != nil {
			return 0
		}
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			return 1
		}
		return 0
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Timeout: 3 * time.Second, MaxRequestsInFlight: 2}))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server stopped", "reason", err.Error())
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
		}
		client.CloseIdleConnections()
	}, nil
}
