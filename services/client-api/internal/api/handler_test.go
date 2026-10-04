package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestMediaGetsSummaryAndCannotRequestFull(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var internalKey, correlation string
	aggregator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalKey, correlation = r.Header.Get("X-Internal-Key"), r.Header.Get("X-Correlation-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"hazard_id":"id-1","source":"BMKG","source_ref_id":"raw-id","hazard_type":"SEISMIC","severity":"WASPADA","area_name":"Simulasi","latitude":1,"longitude":2,"occurred_at":"2026-10-01T00:00:00Z","ingested_at":"2026-10-01T00:00:01Z","attributes":{"secret":true}}],"meta":{"correlation_id":"request-1","sources":{"BMKG":{"status":"fresh"}}}}`))
	}))
	defer aggregator.Close()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	h := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: aggregator.URL, AggregatorKey: "internal-secret", Now: func() time.Time { return now }})
	token := signed(t, key, "media", "hazards:summary", now)
	req := httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-ID", "request-1")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "source_ref_id") || strings.Contains(res.Body.String(), "attributes") || strings.Contains(res.Body.String(), "latitude") {
		t.Fatalf("raw fields leaked: %s", res.Body.String())
	}
	if internalKey != "internal-secret" || correlation != "request-1" {
		t.Fatalf("upstream headers = %q / %q", internalKey, correlation)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/hazards?view=full", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatalf("media full status = %d", res.Code)
	}
}

func TestConcurrentReadsReuseUpstreamConnections(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const readers = 8
	var opened, arrived atomic.Int64
	firstWave := make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if arrived.Add(1) == readers {
			close(firstWave)
		}
		<-firstWave
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	upstream.Start()
	defer upstream.Close()
	now := time.Now()
	h := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: upstream.URL, AggregatorKey: "key", MaxInflight: readers, Now: func() time.Time { return now }})
	defer h.client.CloseIdleConnections()
	token := signed(t, key, "field-team", "hazards:summary hazards:raw", now)
	for wave := 0; wave < 2; wave++ {
		var workers sync.WaitGroup
		for worker := 0; worker < readers; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				req := httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				res := httptest.NewRecorder()
				h.ServeHTTP(res, req)
				if res.Code != 200 {
					t.Errorf("read status=%d", res.Code)
				}
			}()
		}
		workers.Wait()
	}
	if opened.Load() != readers {
		t.Fatalf("opened %d connections for two waves of %d reads", opened.Load(), readers)
	}
}

func TestProxyDoesNotSendInternalCredentialAcrossRedirect(t *testing.T) {
	var redirected atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer upstream.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	h := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: upstream.URL, AggregatorKey: "test-internal-key", Now: func() time.Time { return now }})
	defer h.client.CloseIdleConnections()
	req := httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
	req.Header.Set("Authorization", "Bearer "+signed(t, key, "field-team", "hazards:summary hazards:raw", now))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable || redirected.Load() != 0 {
		t.Fatalf("status=%d redirected requests=%d", response.Code, redirected.Load())
	}
}

func TestProxyPreservesRetryGuidanceWithoutCopyingOtherHeaders(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, tc := range []struct {
		status       int
		header, want string
	}{
		{429, "7", "7"},
		{503, "Fri, 02 Oct 2026 12:00:00 GMT", "Fri, 02 Oct 2026 12:00:00 GMT"},
		{400, "7", ""},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", tc.header)
				w.Header().Set("X-Internal-Debug", "private")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"code":"test"}}`))
			}))
			defer upstream.Close()
			h := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: upstream.URL, AggregatorKey: "key", Now: func() time.Time { return now }})
			req := httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
			req.Header.Set("Authorization", "Bearer "+signed(t, key, "field-team", "hazards:summary hazards:raw", now))
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tc.status || res.Header().Get("Retry-After") != tc.want {
				t.Fatalf("status=%d Retry-After=%q", res.Code, res.Header().Get("Retry-After"))
			}
			if res.Header().Get("X-Internal-Debug") != "" {
				t.Fatal("unrelated upstream header forwarded")
			}
		})
	}
}

func TestRejectsInvalidIssuerAndScope(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	h := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: "http://127.0.0.1:1", AggregatorKey: "key", Now: func() time.Time { return now }})
	badIssuer := signedWith(t, key, "other", "media", "hazards:summary", now)
	req := httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
	req.Header.Set("Authorization", "Bearer "+badIssuer)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 401 {
		t.Fatalf("issuer status = %d", res.Code)
	}
	badScope := signed(t, key, "media", "hazards:summary hazards:raw", now)
	req = httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
	req.Header.Set("Authorization", "Bearer "+badScope)
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 401 {
		t.Fatalf("scope status = %d", res.Code)
	}
}

func signed(t *testing.T, key *rsa.PrivateKey, sub, scope string, now time.Time) string {
	return signedWith(t, key, "aat-auth", sub, scope, now)
}
func signedWith(t *testing.T, key *rsa.PrivateKey, issuer, sub, scope string, now time.Time) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": issuer, "aud": "aat-client-api", "sub": sub, "scope": scope, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestReadinessForwardsAndLogsCorrelationWithoutCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Correlation-ID") != "ready-42" {
			t.Error("readiness correlation not forwarded")
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	handler := NewHandler(Config{AggregatorURL: upstream.URL, AggregatorKey: "private-key"})
	defer handler.client.CloseIdleConnections()
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	request.Header.Set("X-Correlation-ID", "ready-42")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("readiness status: %d", response.Code)
	}
	decoder := json.NewDecoder(&logs)
	found := false
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		if entry["operation"] == "aggregator_ready" {
			found = entry["correlation_id"] == "ready-42" && entry["status"] == float64(200) && entry["duration_ms"] != nil
		}
	}
	if !found {
		t.Fatal("correlated readiness timing not logged")
	}
}

func TestTokenRejectionLogsDistinguishClockSkewFromExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		issuedAt   time.Time
		wantReason string
	}{
		{"API clock behind Auth", now.Add(time.Second), "TOKEN_ISSUED_IN_FUTURE"},
		{"natural expiry", now.Add(-time.Minute), "TOKEN_EXPIRED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := signed(t, key, "field-team", "hazards:summary hazards:raw", test.issuedAt)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			handler := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: "http://unused.invalid", Now: func() time.Time { return now }})
			defer handler.client.CloseIdleConnections()
			request := httptest.NewRequest(http.MethodGet, "/v1/hazards", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-Correlation-ID", "401-reproduction")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 401 {
				t.Fatalf("got HTTP %d", response.Code)
			}
			decoder := json.NewDecoder(&logs)
			var rejection map[string]any
			if err := decoder.Decode(&rejection); err != nil {
				t.Fatal(err)
			}
			if rejection["reason"] != test.wantReason || rejection["correlation_id"] != "401-reproduction" {
				t.Fatalf("rejection context: %+v", rejection)
			}
			if strings.Contains(logs.String(), token) {
				t.Fatal("token leaked into logs")
			}
		})
	}
}
