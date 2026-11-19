package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fieldReportBody = `{"observed_at":"2026-11-12T03:00:00Z","latitude":-0.9492,"longitude":100.3543,"description":"Air naik"}`

const (
	fieldTeamScope = "hazards:summary hazards:raw reports:write"
	internalScope  = "hazards:summary hazards:raw ops:failed-messages"
)

type upstreamCall struct {
	method, path, query, body string
	header                    http.Header
}

func newRouteHandler(t *testing.T, maxInflight int, respond http.HandlerFunc) (*Handler, *rsa.PrivateKey, time.Time, *[]upstreamCall) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	calls := &[]upstreamCall{}
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*calls = append(*calls, upstreamCall{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()})
		mu.Unlock()
		if respond != nil {
			respond(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(upstream.Close)
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	h := NewHandler(Config{PublicKey: &key.PublicKey, Issuer: "aat-auth", Audience: "aat-client-api", AggregatorURL: upstream.URL, AggregatorKey: "internal-secret", MaxInflight: maxInflight, Timeout: 2 * time.Second, Now: func() time.Time { return now }})
	return h, key, now, calls
}

func call(h *Handler, method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func TestEndpointPermissionsAreServerSide(t *testing.T) {
	h, key, now, calls := newRouteHandler(t, 16, nil)
	media := signed(t, key, "media", "hazards:summary", now)
	field := signed(t, key, "field-team", fieldTeamScope, now)
	internal := signed(t, key, "bnpb-internal", internalScope, now)
	idem := map[string]string{"Idempotency-Key": "r-1"}
	cases := []struct {
		name, method, path, token string
		want                      int
	}{
		{"media cannot submit reports", http.MethodPost, "/field-reports", media, http.StatusForbidden},
		{"internal cannot submit reports", http.MethodPost, "/field-reports", internal, http.StatusForbidden},
		{"no token", http.MethodPost, "/field-reports", "", http.StatusUnauthorized},
		{"media cannot list failed", http.MethodGet, "/failed-messages", media, http.StatusForbidden},
		{"field team cannot list failed", http.MethodGet, "/failed-messages", field, http.StatusForbidden},
		{"field team cannot inspect failed", http.MethodGet, "/failed-messages/PVMBG:abc", field, http.StatusForbidden},
		{"field team cannot replay", http.MethodPost, "/failed-messages/PVMBG:abc/replay", field, http.StatusForbidden},
		{"media escalated scope", http.MethodPost, "/field-reports", signed(t, key, "media", "hazards:summary reports:write", now), http.StatusUnauthorized},
		{"field team claims ops", http.MethodGet, "/failed-messages", signed(t, key, "field-team", "hazards:summary hazards:raw ops:failed-messages", now), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if res := call(h, tc.method, tc.path, tc.token, fieldReportBody, idem); res.Code != tc.want {
			t.Errorf("%s: status=%d want %d (%s)", tc.name, res.Code, tc.want, res.Body.String())
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("rejected requests reached Aggregator: %+v", *calls)
	}
	// Tokens issued before the new scopes existed keep their hazard reads.
	if res := call(h, http.MethodGet, "/v1/hazards", signed(t, key, "field-team", "hazards:summary hazards:raw", now), "", nil); res.Code != 200 {
		t.Fatalf("legacy field-team read status=%d", res.Code)
	}
}

func TestOpsRoutesForwardToAggregatorOwnerAPI(t *testing.T) {
	h, key, now, calls := newRouteHandler(t, 16, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/replay") {
			w.WriteHeader(http.StatusAccepted)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	internal := signed(t, key, "bnpb-internal", internalScope, now)
	if res := call(h, http.MethodGet, "/failed-messages?source=PVMBG&limit=5", internal, "", nil); res.Code != 200 {
		t.Fatalf("list status=%d %s", res.Code, res.Body.String())
	}
	if res := call(h, http.MethodGet, "/failed-messages/PVMBG:abc", internal, "", nil); res.Code != 200 {
		t.Fatalf("inspect status=%d", res.Code)
	}
	if res := call(h, http.MethodPost, "/failed-messages/PVMBG:abc/replay", internal, "", nil); res.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d", res.Code)
	}
	if res := call(h, http.MethodGet, "/failed-messages?role=admin", internal, "", nil); res.Code != 400 {
		t.Fatalf("unknown query status=%d", res.Code)
	}
	want := []struct{ method, path string }{
		{http.MethodGet, "/internal/v1/failed-messages"},
		{http.MethodGet, "/internal/v1/failed-messages/PVMBG:abc"},
		{http.MethodPost, "/internal/v1/failed-messages/PVMBG:abc/replay"},
	}
	if len(*calls) != len(want) {
		t.Fatalf("upstream calls=%+v", *calls)
	}
	for i, w := range want {
		c := (*calls)[i]
		if c.method != w.method || c.path != w.path || c.header.Get("X-Internal-Key") != "internal-secret" || c.header.Get("X-Client-ID") != "bnpb-internal" {
			t.Errorf("call %d = %s %s headers=%v", i, c.method, c.path, c.header)
		}
	}
	if !strings.Contains((*calls)[0].query, "source=PVMBG") || !strings.Contains((*calls)[0].query, "limit=5") {
		t.Fatalf("list query=%q", (*calls)[0].query)
	}
}

func TestFieldReportForwardsOriginalBytesWithIdentity(t *testing.T) {
	h, key, now, calls := newRouteHandler(t, 16, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"report_id":"00000000-0000-4000-8000-000000000001","status":"accepted"}`))
	})
	field := signed(t, key, "field-team", fieldTeamScope, now)
	if res := call(h, http.MethodPost, "/field-reports", field, fieldReportBody, nil); res.Code != 400 || !strings.Contains(res.Body.String(), "INVALID_IDEMPOTENCY_KEY") {
		t.Fatalf("missing key status=%d %s", res.Code, res.Body.String())
	}
	if res := call(h, http.MethodPost, "/field-reports", field, `{"observed_at":"x"`, map[string]string{"Idempotency-Key": "k"}); res.Code != 400 {
		t.Fatalf("truncated body status=%d", res.Code)
	}
	if res := call(h, http.MethodPost, "/field-reports", field, strings.Repeat("a", 40<<10), map[string]string{"Idempotency-Key": "k"}); res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d", res.Code)
	}
	if len(*calls) != 0 {
		t.Fatal("invalid reports reached Aggregator")
	}
	res := call(h, http.MethodPost, "/field-reports", field, fieldReportBody, map[string]string{"Idempotency-Key": "report:2026-11-18:01", "X-Correlation-ID": "corr-1"})
	if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"status":"accepted"`) || res.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("submit status=%d body=%s", res.Code, res.Body.String())
	}
	c := (*calls)[0]
	if c.method != http.MethodPost || c.path != "/internal/v1/field-reports" || c.body != fieldReportBody {
		t.Fatalf("forwarded %s %s %q", c.method, c.path, c.body)
	}
	if c.header.Get("Idempotency-Key") != "report:2026-11-18:01" || c.header.Get("X-Client-ID") != "field-team" || c.header.Get("X-Request-Priority") != "high" || c.header.Get("X-Correlation-ID") != "corr-1" {
		t.Fatalf("forwarded headers=%v", c.header)
	}
}

func TestMediaOverloadIsShedWhileFieldTeamIsServed(t *testing.T) {
	release := make(chan struct{})
	var entered atomic.Int64
	h, key, now, calls := newRouteHandler(t, 4, func(w http.ResponseWriter, r *http.Request) {
		entered.Add(1)
		if r.Method == http.MethodGet {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"report_id":"00000000-0000-4000-8000-000000000001","status":"accepted"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	})
	_ = calls
	media := signed(t, key, "media", "hazards:summary", now)
	field := signed(t, key, "field-team", fieldTeamScope, now)
	general, _ := splitCapacity(4)
	var wg sync.WaitGroup
	for i := 0; i < general; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); call(h, http.MethodGet, "/v1/hazards", media, "", nil) }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < int64(general) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if res := call(h, http.MethodGet, "/v1/hazards", media, "", nil); res.Code != http.StatusTooManyRequests || res.Header().Get("Retry-After") == "" {
		t.Fatalf("excess media status=%d", res.Code)
	}
	res := call(h, http.MethodPost, "/field-reports", field, fieldReportBody, map[string]string{"Idempotency-Key": "under-load"})
	if res.Code != http.StatusAccepted {
		t.Fatalf("field team under media overload status=%d %s", res.Code, res.Body.String())
	}
	close(release)
	wg.Wait()
}

func TestIdempotentReportFailsOverWithSameBody(t *testing.T) {
	h := NewHandler(Config{DiscoveryHost: "aggregator-peers", Timeout: time.Second})
	h.discovery.addresses = []string{"10.0.0.1", "10.0.0.2"}
	h.discovery.expires = time.Now().Add(time.Minute)
	bodies := []string{}
	h.client = &http.Client{Transport: backendTransport(func(r *http.Request) (*http.Response, error) {
		body := []byte{}
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		bodies = append(bodies, string(body))
		if r.URL.Hostname() == "10.0.0.1" {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://aggregator:8083/internal/v1/field-reports", strings.NewReader(fieldReportBody))
	req.Header.Set("Idempotency-Key", "k")
	response, err := h.readUpstream(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(bodies) != 2 || bodies[0] != fieldReportBody || bodies[1] != fieldReportBody {
		t.Fatalf("attempt bodies=%q", bodies)
	}
	// Without an idempotency key a write is never repeated on another replica.
	attempts := len(bodies)
	req, _ = http.NewRequest(http.MethodPost, "http://10.0.0.1:8083/internal/v1/failed-messages/x/replay", nil)
	if _, err := h.readUpstream(req); err == nil {
		t.Fatal("non-idempotent request unexpectedly succeeded")
	}
	if len(bodies) != attempts+1 {
		t.Fatalf("non-idempotent request attempts=%d", len(bodies)-attempts)
	}
}

func TestFailedMessageInspectionRelaysLargePayloadIntact(t *testing.T) {
	// The journal retains source payloads up to SOURCE_RESPONSE_MAX_BYTES
	// (1 MiB by default). Base64 plus the auxiliary bytes push the detail past
	// the buffered response limit, so inspection must still return all of it.
	payload := make([]byte, 1<<20)
	auxiliary := make([]byte, 256<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(auxiliary); err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]any{"data": map[string]any{"message_id": "m-1", "payload_base64": payload, "auxiliary_base64": auxiliary}})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail) <= maxBufferedResponse {
		t.Fatalf("fixture is %d bytes; it must exceed the buffered limit", len(detail))
	}
	h, key, now, _ := newRouteHandler(t, 16, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(detail)
	})
	internal := signed(t, key, "bnpb-internal", internalScope, now)
	res := call(h, http.MethodGet, "/failed-messages/m-1", internal, "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%.200s", res.Code, res.Body.String())
	}
	var got struct {
		Data struct {
			Payload   []byte `json:"payload_base64"`
			Auxiliary []byte `json:"auxiliary_base64"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not complete JSON: %v", err)
	}
	if !bytes.Equal(got.Data.Payload, payload) || !bytes.Equal(got.Data.Auxiliary, auxiliary) {
		t.Fatal("decoded inspection bytes differ from the original payload")
	}
}
