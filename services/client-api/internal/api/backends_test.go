package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type backendTransport func(*http.Request) (*http.Response, error)

func (f backendTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadFailoverUsesDistinctReplicasAndKeepsBodyAlive(t *testing.T) {
	h := NewHandler(Config{DiscoveryHost: "aggregator-peers", Timeout: 300 * time.Millisecond})
	h.discovery.addresses = []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	h.discovery.expires = time.Now().Add(time.Minute)
	attempts := []string{}
	h.client = &http.Client{Transport: backendTransport(func(r *http.Request) (*http.Response, error) {
		attempts = append(attempts, r.URL.Hostname())
		if r.URL.Hostname() == "10.0.0.1" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		if r.Header.Get("X-Internal-Key") != "secret" || r.Host != "aggregator:8083" {
			t.Fatal("failover changed authentication or the logical host")
		}
		if r.Context().Err() != nil {
			t.Fatal("successful response context was cancelled before its body could be read")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: r}, nil
	})}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://aggregator:8083/internal/v1/hazards", nil)
	req.Header.Set("X-Internal-Key", "secret")
	started := time.Now()
	response, err := h.readUpstream(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if response.Request.Context().Err() != nil {
		t.Fatal("response body context was cancelled early")
	}
	response.Body.Close()
	if response.Request.Context().Err() == nil {
		t.Fatal("response body close did not release attempt context")
	}
	if len(attempts) != 2 || attempts[0] != "10.0.0.1" || attempts[1] != "10.0.0.2" || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("failover attempts=%v duration=%s", attempts, time.Since(started))
	}
}

func TestFailoverPreservesControlledRejections(t *testing.T) {
	h := NewHandler(Config{DiscoveryHost: "aggregator-peers", Timeout: time.Second})
	h.discovery.addresses = []string{"10.0.0.1", "10.0.0.2"}
	h.discovery.expires = time.Now().Add(time.Minute)
	attempts := 0
	h.client = &http.Client{Transport: backendTransport(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"1"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})}
	req, _ := http.NewRequest(http.MethodGet, "http://aggregator:8083/internal/v1/hazards", nil)
	response, err := h.readUpstream(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 429 || attempts != 1 || response.Header.Get("Retry-After") != "1" {
		t.Fatal("controlled rejection was retried or changed")
	}
}

func TestFailoverReservesTimeInsideShortReadinessDeadline(t *testing.T) {
	h := NewHandler(Config{DiscoveryHost: "aggregator-peers", Timeout: time.Second})
	h.discovery.addresses = []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	h.discovery.expires = time.Now().Add(time.Minute)
	attempts := 0
	h.client = &http.Client{Transport: backendTransport(func(r *http.Request) (*http.Response, error) {
		attempts++
		if r.URL.Hostname() == "10.0.0.1" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://aggregator:8083/ready", nil)
	response, err := h.readUpstream(request)
	if err != nil {
		t.Fatalf("frozen peer exhausted the readiness deadline: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || attempts != 2 || ctx.Err() != nil {
		t.Fatal("did not reach a distinct healthy peer within the parent deadline")
	}
}
