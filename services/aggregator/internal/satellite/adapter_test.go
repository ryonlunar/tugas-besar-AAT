package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// sseDetectionEvent builds one well-formed `detection` SSE record for id,
// matching the wrapper shape documented in docs/m2-satellite-feed-notes.md.
func sseDetectionEvent(t *testing.T, id int64) string {
	t.Helper()
	body := map[string]any{
		"offset": id,
		"detection": map[string]any{
			"satellite": "N20", "acq_date": "2026-01-15", "acq_time": 1234,
			"confidence": "n", "frp": 1.0, "latitude": 1.0, "longitude": 2.0,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal detection body: %v", err)
	}
	return fmt.Sprintf("id: %d\nevent: detection\ndata: %s\n\n", id, data)
}

func sseHeartbeatEvent(id int64) string {
	return fmt.Sprintf("id: %d\nevent: heartbeat\ndata: {\"offset\":%d}\n\n", id, id)
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// assertContiguousBatches checks that the committed batches' [From,Cursor)
// ranges tile [0,total) exactly once each, with no gap and no overlap --
// the invariant the adapter must hold regardless of reconnects (R2).
func assertContiguousBatches(t *testing.T, batches []contracts.Batch, total int64) {
	t.Helper()
	type span struct{ from, cursor int64 }
	spans := make([]span, 0, len(batches))
	for _, b := range batches {
		from, err := parseOffsetCursor(b.From)
		if err != nil {
			t.Fatalf("batch From %q is not an integer: %v", b.From, err)
		}
		cursor, err := strconv.ParseInt(b.Cursor, 10, 64)
		if err != nil {
			t.Fatalf("batch Cursor %q is not an integer: %v", b.Cursor, err)
		}
		spans = append(spans, span{from, cursor})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	var next int64
	for _, s := range spans {
		if s.from != next {
			t.Fatalf("gap or overlap in committed batches: expected next span to start at %d, got %d (spans=%+v)", next, s.from, spans)
		}
		next = s.cursor
	}
	if next != total {
		t.Fatalf("committed batches cover [0,%d), want [0,%d)", next, total)
	}
}

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := NewClient(serverURL, "token", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestAdapterCommitsBatchesInOffsetOrder(t *testing.T) {
	const total = 450
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for id := from; id < total; id++ {
			fmt.Fprint(w, sseDetectionEvent(t, id))
			flush(w)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	var mu sync.Mutex
	var batches []contracts.Batch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := func(_ context.Context, batch contracts.Batch) error {
		mu.Lock()
		batches = append(batches, batch)
		n := len(batches)
		mu.Unlock()
		if n >= 3 {
			cancel()
		}
		return nil
	}

	adapter := &Adapter{
		Client: client, MaxEvents: 200, MaxBytes: 10 << 20, MaxWait: 50 * time.Millisecond,
		IdleTimeout: 5 * time.Second, ReconnectMin: 10 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
	}

	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", commit) }()

	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3", len(batches))
	}
	wantRanges := [][2]string{{"", "200"}, {"200", "400"}, {"400", "450"}}
	for i, want := range wantRanges {
		if batches[i].From != want[0] || batches[i].Cursor != want[1] {
			t.Errorf("batch[%d] From/Cursor = %s/%s, want %s/%s", i, batches[i].From, batches[i].Cursor, want[0], want[1])
		}
		if len(batches[i].Records) != 1 {
			t.Fatalf("batch[%d] has %d records, want 1", i, len(batches[i].Records))
		}
		record := batches[i].Records[0]
		if record.Kind != "sse_batch" {
			t.Errorf("batch[%d] Kind = %q, want sse_batch", i, record.Kind)
		}
		if record.OrderingKey != "satellite-feed" {
			t.Errorf("batch[%d] OrderingKey = %q, want satellite-feed", i, record.OrderingKey)
		}
		if record.Key != fmt.Sprintf("offsets-%s-%s", want[0], mustPrevStr(t, want[1])) {
			t.Errorf("batch[%d] Key = %q, want offsets-%s-%s", i, record.Key, want[0], mustPrevStr(t, want[1]))
		}
	}
	for i := 1; i < len(batches); i++ {
		if batches[i].From != batches[i-1].Cursor {
			t.Errorf("batch[%d].From = %s, want %s (chained with previous Cursor)", i, batches[i].From, batches[i-1].Cursor)
		}
	}
}

func mustPrevStr(t *testing.T, s string) string {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return strconv.FormatInt(n-1, 10)
}

func TestAdapterSkipsHeartbeatRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if from <= 0 {
			fmt.Fprint(w, sseDetectionEvent(t, 0))
			flush(w)
		}
		if from <= 1 {
			fmt.Fprint(w, sseHeartbeatEvent(1))
			flush(w)
		}
		if from <= 2 {
			fmt.Fprint(w, sseDetectionEvent(t, 2))
			flush(w)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	var mu sync.Mutex
	var batches []contracts.Batch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := func(_ context.Context, batch contracts.Batch) error {
		mu.Lock()
		batches = append(batches, batch)
		mu.Unlock()
		cursor, _ := strconv.ParseInt(batch.Cursor, 10, 64)
		if cursor >= 3 {
			cancel()
		}
		return nil
	}

	adapter := &Adapter{
		Client: client, MaxEvents: 200, MaxWait: 30 * time.Millisecond,
		IdleTimeout: 5 * time.Second, ReconnectMin: 10 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
	}

	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", commit) }()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s")
	}

	mu.Lock()
	defer mu.Unlock()
	assertContiguousBatches(t, batches, 3)
	var sawEmptyHeartbeatBatch bool
	for _, b := range batches {
		if b.From == "1" && b.Cursor == "2" {
			sawEmptyHeartbeatBatch = true
			if len(b.Records) != 0 {
				t.Errorf("heartbeat batch Records = %+v, want none (heartbeat does not become a record)", b.Records)
			}
		}
	}
	if !sawEmptyHeartbeatBatch {
		t.Fatalf("no batch advanced the cursor 1->2 via an empty commit; batches=%+v", batches)
	}
}

func TestAdapterResumesAfterDisconnectWithoutGapOrDuplicate(t *testing.T) {
	const total = 300
	const disconnectAt = 123
	var attempt atomic.Int64
	var secondFrom atomic.Int64
	secondFrom.Store(-1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempt.Add(1)
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			for id := from; id < disconnectAt; id++ {
				fmt.Fprint(w, sseDetectionEvent(t, id))
				flush(w)
			}
			return // abrupt disconnect: no closing record boundary signal beyond what was sent
		}
		secondFrom.Store(from)
		for id := from; id < total; id++ {
			fmt.Fprint(w, sseDetectionEvent(t, id))
			flush(w)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	var mu sync.Mutex
	var batches []contracts.Batch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := func(_ context.Context, batch contracts.Batch) error {
		mu.Lock()
		batches = append(batches, batch)
		mu.Unlock()
		cursor, _ := strconv.ParseInt(batch.Cursor, 10, 64)
		if cursor >= total {
			cancel()
		}
		return nil
	}

	adapter := &Adapter{
		Client: client, MaxEvents: 200, MaxWait: 30 * time.Millisecond,
		IdleTimeout: 5 * time.Second, ReconnectMin: 5 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
	}

	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", commit) }()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s")
	}

	if got := secondFrom.Load(); got != disconnectAt {
		t.Errorf("second connection requested from=%d, want %d (last committed cursor, not last read offset)", got, disconnectAt)
	}
	mu.Lock()
	defer mu.Unlock()
	assertContiguousBatches(t, batches, total)
}

func TestAdapterRejectsOffsetGap(t *testing.T) {
	var attempt atomic.Int64
	var secondFrom atomic.Int64
	secondFrom.Store(-1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempt.Add(1)
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			for id := from; id < 10; id++ {
				fmt.Fprint(w, sseDetectionEvent(t, id))
				flush(w)
			}
			fmt.Fprint(w, sseDetectionEvent(t, 12)) // jump 10 -> 12, skipping 10 and 11
			flush(w)
			<-r.Context().Done()
			return
		}
		secondFrom.Store(from)
		fmt.Fprint(w, sseDetectionEvent(t, from))
		flush(w)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	var mu sync.Mutex
	var batches []contracts.Batch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := func(_ context.Context, batch contracts.Batch) error {
		mu.Lock()
		batches = append(batches, batch)
		n := len(batches)
		mu.Unlock()
		if n >= 2 {
			cancel()
		}
		return nil
	}

	adapter := &Adapter{
		Client: client, MaxEvents: 200, MaxWait: 30 * time.Millisecond,
		IdleTimeout: 5 * time.Second, ReconnectMin: 5 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
	}

	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", commit) }()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) == 0 {
		t.Fatal("no batch committed")
	}
	if batches[0].From != "" || batches[0].Cursor != "10" {
		t.Fatalf("first batch = {From:%s Cursor:%s}, want {From:\"\" Cursor:10} (never commit past the gap at 10)", batches[0].From, batches[0].Cursor)
	}
	// docs/m2-satellite-feed-notes.md section 7: Batch.Cursor is "the next
	// offset never committed" (last success + 1); after committing [0,10),
	// the reconnect must ask for exactly that offset -- not the plan
	// draft's pre-correction "11", which assumed a different cursor
	// semantic that the live observation superseded.
	if got := secondFrom.Load(); got != 10 {
		t.Errorf("second connection requested from=%d, want 10", got)
	}
}

func TestAdapterBacksOffOn429And503(t *testing.T) {
	t.Run("429 never retries faster than its jittered floor", func(t *testing.T) {
		var count atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"busy"}}`))
		}))
		defer server.Close()
		client := newTestClient(t, server.URL)

		adapter := &Adapter{Client: client, ReconnectMin: 50 * time.Millisecond, ReconnectMax: 2 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_ = adapter.Run(ctx, "", func(context.Context, contracts.Batch) error { return nil })

		if got := count.Load(); got > 2 {
			t.Fatalf("server received %d requests in 1.5s, want <=2 (429 floor is >=5s)", got)
		}
	})

	t.Run("503 backs off without hot-looping", func(t *testing.T) {
		var count atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"down"}}`))
		}))
		defer server.Close()
		client := newTestClient(t, server.URL)

		adapter := &Adapter{Client: client, ReconnectMin: 100 * time.Millisecond, ReconnectMax: 2 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_ = adapter.Run(ctx, "", func(context.Context, contracts.Batch) error { return nil })

		got := count.Load()
		if got < 2 {
			t.Fatalf("server received %d requests in 1.5s, want at least 2 (should keep retrying)", got)
		}
		if got > 15 {
			t.Fatalf("server received %d requests in 1.5s, want a small bounded number (no hot-loop)", got)
		}
	})
}

func TestAdapterStopsOnCommitError(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseDetectionEvent(t, 0))
		flush(w)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	wantErr := errors.New("boom: commit backend unavailable")
	adapter := &Adapter{
		Client: client, MaxEvents: 1, MaxWait: time.Hour, IdleTimeout: 5 * time.Second,
		ReconnectMin: 10 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := adapter.Run(ctx, "", func(context.Context, contracts.Batch) error { return wantErr })

	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want it to wrap %v", err, wantErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests, want exactly 1 (must stop immediately on a commit error, never retry)", got)
	}
}

func TestAdapterBackpressure(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseDetectionEvent(t, 0))
		flush(w)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	var allowed atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := func(context.Context, contracts.Batch) error { cancel(); return nil }
	adapter := &Adapter{
		Client: client, MaxEvents: 1, MaxWait: time.Hour, IdleTimeout: 5 * time.Second,
		ReconnectMin: 10 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
		CanRead: func(context.Context) (bool, error) { return allowed.Load(), nil },
	}

	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", commit) }()

	time.Sleep(150 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d requests while CanRead=false, want 0 (no stream request until allowed)", got)
	}
	allowed.Store(true)

	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after CanRead became true")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests after CanRead=true, want exactly 1", got)
	}
}

func TestAdapterReconnectsOnIdle(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseDetectionEvent(t, 0))
		flush(w)
		<-r.Context().Done() // never sends anything else; the adapter must time out and reconnect
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := &Adapter{
		Client: client, MaxEvents: 200, MaxWait: time.Hour, IdleTimeout: 100 * time.Millisecond,
		ReconnectMin: 10 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
	}
	go func() { _ = adapter.Run(ctx, "", func(context.Context, contracts.Batch) error { return nil }) }()

	time.Sleep(1 * time.Second)
	if got := requests.Load(); got < 2 {
		t.Fatalf("server received %d requests, want >=2 (idle timeout must trigger a reconnect)", got)
	}
}

func TestAdapterReturnsPromptlyOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseDetectionEvent(t, 0))
		flush(w)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	adapter := &Adapter{
		Client: client, MaxEvents: 200, MaxWait: time.Hour, IdleTimeout: time.Hour,
		ReconnectMin: 10 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
	}
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", func(context.Context, contracts.Batch) error { return nil }) }()

	time.Sleep(100 * time.Millisecond) // let it connect and read the one event
	started := time.Now()
	cancel()
	select {
	case err := <-runErr:
		elapsed := time.Since(started)
		if elapsed > 200*time.Millisecond {
			t.Fatalf("Run() took %v to return after ctx cancel, want <200ms", elapsed)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after ctx cancel")
	}
}

// TestAdapterEnforcesBackpressureBetweenBatchesOnOpenConnection is the
// regression for the PR #6 review finding (l0stplains, P4.4): CanRead was
// only ever checked by awaitReadable before a new connection opened. A
// long-lived SSE connection that keeps flushing batches (MaxEvents: 1 here,
// so every single event is its own batch) never revisited CanRead and
// could keep committing past the backlog threshold indefinitely. The
// adapter must now end the connection right after a flush once CanRead
// says no, and resume cleanly (no gap, no duplicate) once it says yes
// again.
func TestAdapterEnforcesBackpressureBetweenBatchesOnOpenConnection(t *testing.T) {
	const total = 20
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for id := from; id < total; id++ {
			fmt.Fprint(w, sseDetectionEvent(t, id))
			flush(w)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	var mu sync.Mutex
	var batches []contracts.Batch
	var allowed atomic.Bool
	allowed.Store(true)
	var commits atomic.Int64

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := func(_ context.Context, batch contracts.Batch) error {
		mu.Lock()
		batches = append(batches, batch)
		n := len(batches)
		mu.Unlock()
		if commits.Add(1) == 1 {
			// After the very first batch commits, backpressure kicks
			// in. If the adapter only checked CanRead before opening
			// a connection, it would keep committing every remaining
			// event on this same still-open stream without ever
			// pausing.
			allowed.Store(false)
			go func() {
				time.Sleep(100 * time.Millisecond)
				allowed.Store(true)
			}()
		}
		if n >= total {
			cancel()
		}
		return nil
	}
	adapter := &Adapter{
		Client: client, MaxEvents: 1, MaxWait: time.Hour, IdleTimeout: 5 * time.Second,
		ReconnectMin: 10 * time.Millisecond, ReconnectMax: 50 * time.Millisecond,
		CanRead: func(context.Context) (bool, error) { return allowed.Load(), nil },
	}

	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx, "", commit) }()

	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not finish within 5s")
	}

	mu.Lock()
	defer mu.Unlock()
	assertContiguousBatches(t, batches, total)
	if got := requests.Load(); got < 2 {
		t.Fatalf("server received %d requests, want >=2 (the adapter must reconnect once backpressure kicks in, not keep reading the same connection)", got)
	}
}
