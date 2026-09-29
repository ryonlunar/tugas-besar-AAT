package poller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

func TestBMKGClientFetchesBothFullSnapshotsWithItsCredential(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Has("since") {
			t.Errorf("full-snapshot poll unexpectedly sent since")
		}
		if r.Header.Get("X-BMKG-Key") != "bmkg-test-only" {
			t.Errorf("missing BMKG credential on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/seismic-events":
			_, _ = w.Write([]byte(`[{"event_id":"eq-1"}]`))
		case "/tsunami-warnings":
			_, _ = w.Write([]byte(`[{"warning_id":"w-1"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewBMKGClient(server.URL, "bmkg-test-only", server.Client(), 1<<20)
	events, warnings, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(warnings) != 1 || calls.Load() != 2 {
		t.Fatalf("got events=%d warnings=%d calls=%d", len(events), len(warnings), calls.Load())
	}
}

func TestBMKGClientDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	var redirectedCalls atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedCalls.Add(1)
		if r.Header.Get("X-BMKG-Key") != "bmkg-test-only" {
			t.Errorf("redirected request did not carry test credential; got %q", r.Header.Get("X-BMKG-Key"))
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer redirected.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/seismic-events" {
			http.Redirect(w, r, redirected.URL, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer upstream.Close()

	client := NewBMKGClient(upstream.URL, "bmkg-test-only", upstream.Client(), 1024)
	if _, _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("BMKG redirect was followed instead of rejected")
	}
	if got := redirectedCalls.Load(); got != 0 {
		t.Fatalf("credential-bearing request reached redirect target %d times", got)
	}
}

func TestBMKGClientForwardsPollCorrelationToBothEndpoints(t *testing.T) {
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Correlation-ID")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	client := NewBMKGClient(server.URL, "key", server.Client(), 1024)
	if _, _, err := client.Fetch(withCorrelationID(context.Background(), "11111111-1111-4111-8111-111111111111")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := <-seen; got != "11111111-1111-4111-8111-111111111111" {
			t.Fatalf("correlation header=%q", got)
		}
	}
}

func TestBMKGClientFailsWholeSnapshotWhenEitherEndpointFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/seismic-events" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.Error(w, "private upstream detail", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewBMKGClient(server.URL, "key", server.Client(), 1<<20)
	if _, _, err := client.Fetch(context.Background()); err == nil || strings.Contains(err.Error(), "private upstream detail") {
		t.Fatalf("expected sanitized endpoint failure, got %v", err)
	}
}

func TestPVMBGClientDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	var redirectedCalls atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer pvmbg-test-only" {
			t.Errorf("redirected request did not carry test credential; got %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer redirected.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirected.URL, http.StatusFound)
	}))
	defer upstream.Close()

	client := NewPVMBGClient(upstream.URL, "pvmbg-test-only", upstream.Client(), 1024)
	if _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("PVMBG redirect was followed instead of rejected")
	}
	if got := redirectedCalls.Load(); got != 0 {
		t.Fatalf("credential-bearing request reached redirect target %d times", got)
	}
}

func TestPVMBGClientUsesBearerAndRejectsOversizedResponse(t *testing.T) {
	t.Run("bearer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer pvmbg-test-only" {
				t.Errorf("wrong PVMBG credential header: %q", r.Header.Get("Authorization"))
			}
			if r.URL.Path != "/volcanic-reports" || r.URL.Query().Has("since") {
				t.Errorf("unexpected request target: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(`[{"report_id":"pv-1"}]`))
		}))
		defer server.Close()
		client := NewPVMBGClient(server.URL, "pvmbg-test-only", server.Client(), 1024)
		reports, err := client.Fetch(context.Background())
		if err != nil || len(reports) != 1 {
			t.Fatalf("reports=%d err=%v", len(reports), err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `[{"report_id":"`+strings.Repeat("x", 128)+`"}]`)
		}))
		defer server.Close()
		client := NewPVMBGClient(server.URL, "token", server.Client(), 32)
		if _, err := client.Fetch(context.Background()); err == nil {
			t.Fatal("oversized response was accepted")
		}
	})
}

type fakeStore struct {
	ids           map[string]string
	events        []storedCycle
	lookups       []string
	pendingOutbox int64
	capacityErr   error
}

type storedCycle struct {
	source  string
	events  int
	success bool
	code    string
}

func (s *fakeStore) FindHazardID(_ context.Context, source mapper.Source, ref string) (string, bool, error) {
	s.lookups = append(s.lookups, string(source)+":"+ref)
	id, ok := s.ids[string(source)+":"+ref]
	return id, ok, nil
}
func (s *fakeStore) PendingOutboxCount(context.Context) (int64, error) {
	return s.pendingOutbox, s.capacityErr
}
func (s *fakeStore) ApplyCycle(_ context.Context, source mapper.Source, events []mapper.HazardEvent, _ string, success bool, code string, _ time.Time) error {
	s.events = append(s.events, storedCycle{source: string(source), events: len(events), success: success, code: code})
	return nil
}

func TestPollerSkipsOversizedEnvelopeAndMarksSnapshotDegraded(t *testing.T) {
	raw := []byte(fmt.Sprintf(`{"report_id":"pv-large","volcano_id":"sim-tamriel-red-mountain","alert_level":"Siaga","eruption_count_24h":1,"ash_column_height_m":400,"reported_at":"2026-09-28T10:00:00Z","extra":"%s"}`, strings.Repeat("x", 2048)))
	store := &fakeStore{ids: map[string]string{}}
	p := New(store, nil, &fakePVMBG{reports: [][]byte{raw}}, nil, nil)
	p.SetMaxEnvelopeBytes(1024)
	if err := p.PollPVMBG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 || store.events[0].events != 0 || store.events[0].success || store.events[0].code != "EVENT_TOO_LARGE" {
		t.Fatalf("oversized event should be omitted and cycle marked degraded: %+v", store.events)
	}
}

func TestPollerPersistsValidRecordsAndMarksCycleDegraded(t *testing.T) {
	bmkg := &fakeBMKG{
		events:   [][]byte{[]byte(`{"event_id":"eq-1","magnitude":5.5,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Simulasi","occurred_at":"2026-09-28T10:00:00Z","potential_tsunami":false}`), []byte(`{"event_id":"eq-bad"}`)},
		warnings: [][]byte{},
	}
	store := &fakeStore{ids: map[string]string{}}
	p := New(store, bmkg, nil, nil, nil)
	if err := p.PollBMKG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 || store.events[0].source != "BMKG" || store.events[0].events != 1 || store.events[0].success || store.events[0].code == "" {
		t.Fatalf("invalid/degraded persistence result: %+v", store.events)
	}
	if len(store.lookups) != 2 || store.lookups[0] != "BMKG:eq-1" || store.lookups[1] != "BMKG:eq-bad" {
		t.Fatalf("stable IDs were not looked up for source records: %+v", store.lookups)
	}
}

func TestPollerPausesWhenOutboxCapacityIsReached(t *testing.T) {
	store := &fakeStore{ids: map[string]string{}, pendingOutbox: 10000}
	bmkg := &fakeBMKG{events: [][]byte{[]byte(`{"event_id":"eq-1"}`)}}
	pvmbg := &fakePVMBG{reports: [][]byte{[]byte(`{"report_id":"pv-1"}`)}}
	p := New(store, bmkg, pvmbg, nil, nil)
	p.SetOutboxLimit(10000)

	if err := p.PollBMKG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.PollPVMBG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bmkg.calls != 0 || pvmbg.calls != 0 || len(store.events) != 0 {
		t.Fatalf("capacity gate did not pause ingestion: bmkg_calls=%d pvmbg_calls=%d writes=%d", bmkg.calls, pvmbg.calls, len(store.events))
	}
}

func TestPollerFailsClosedWhenOutboxCapacityCannotBeChecked(t *testing.T) {
	store := &fakeStore{ids: map[string]string{}, capacityErr: fmt.Errorf("database unavailable")}
	bmkg := &fakeBMKG{}
	p := New(store, bmkg, &fakePVMBG{}, nil, nil)

	if err := p.PollBMKG(context.Background()); err == nil {
		t.Fatal("capacity query error must prevent source ingestion")
	}
	if bmkg.calls != 0 || len(store.events) != 0 {
		t.Fatalf("ingestion continued after capacity check failed: calls=%d writes=%d", bmkg.calls, len(store.events))
	}
}

type fakeBMKG struct {
	events, warnings [][]byte
	err              error
	seenIDs          []string
	calls            int
}

func (f *fakeBMKG) Fetch(context.Context) ([][]byte, [][]byte, error) {
	f.calls++
	return f.events, f.warnings, f.err
}

type fakePVMBG struct {
	reports [][]byte
	err     error
	calls   int
}

func (f *fakePVMBG) Fetch(context.Context) ([][]byte, error) { f.calls++; return f.reports, f.err }
