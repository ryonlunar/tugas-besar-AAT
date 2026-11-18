package satellite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Adapter implements contracts.SourceAdapter for the Feed Satelit SSE
// stream. It never advances its durable cursor past a detected offset gap,
// it archives each micro-batch's exact wire bytes (D4) rather than
// re-encoding parsed events, and it never retries after the caller's commit
// callback itself fails -- a commit error is a terminal condition for Run,
// not a transport fault to back off from.
type Adapter struct {
	Client *Client

	// MaxEvents, MaxBytes, and MaxWait bound how large/how long a
	// micro-batch may grow before it is flushed via commit. MaxBytes <=0
	// disables the byte bound; MaxWait<=0 disables the time bound (a
	// batch then only flushes on MaxEvents, a heartbeat boundary, or
	// stream teardown).
	MaxEvents int
	MaxBytes  int
	MaxWait   time.Duration

	// IdleTimeout reconnects the stream after this long without any byte
	// read at all (a stalled or silently-dropped connection); it is
	// independent of MaxWait, which only bounds how long an in-progress
	// batch may stay unflushed while events keep arriving.
	IdleTimeout time.Duration

	// ReconnectMin/ReconnectMax bound the backoff between a failed (or
	// idle-timed-out) connection attempt and the next one.
	ReconnectMin, ReconnectMax time.Duration

	// MaxLine bounds a single physical SSE line; 0 uses a sane default
	// (see maxLine()).
	MaxLine int

	// CanRead reports whether the adapter may open or keep reading the
	// stream right now (backpressure, e.g. journal.Session.Unfinished
	// margin). A nil CanRead always allows reading.
	CanRead func(context.Context) (bool, error)

	// Received, when set, is called once per event successfully folded
	// into a batch (not per flushed batch), for latency instrumentation
	// keyed by the detection/event's own id-derived offset key.
	Received func(key string, at time.Time)

	// Observe, when set, is called after every connection attempt
	// (success or failure) with a coarse outcome, a reason, the error
	// (nil on success), and how long the attempt's read loop ran.
	Observe func(outcome, reason string, err error, elapsed time.Duration)

	// Now returns the current time; defaults to time.Now. Tests can
	// override it, though none currently do.
	Now func() time.Time
}

// ErrOffsetGap is the internal signal that the feed sent an id strictly
// greater than the adapter's expected next offset. Run never surfaces this
// as a hard failure: it flushes whatever was already accumulated (durably
// advancing the cursor up to, but never past, the gap) and reconnects from
// that same cursor, giving the feed another chance to supply the missing
// offsets in order.
var ErrOffsetGap = errors.New("satellite: offset gap detected")

var errIdleTimeout = errors.New("satellite: idle timeout")

// commitErr marks a failure returned by the caller's commit callback.
// Unlike every other error Run can encounter, this one is never retried:
// the batch is not known to be durable, and the adapter has no way to tell
// whether retrying would duplicate a partially-applied effect.
type commitErr struct{ err error }

func (e *commitErr) Error() string { return fmt.Sprintf("satellite adapter commit failed: %v", e.err) }
func (e *commitErr) Unwrap() error { return e.err }

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Adapter) maxLine() int {
	if a.MaxLine > 0 {
		return a.MaxLine
	}
	return 1 << 20
}

func (a *Adapter) observe(outcome, reason string, err error, elapsed time.Duration) {
	if a.Observe != nil {
		a.Observe(outcome, reason, err, elapsed)
	}
}

// Run implements contracts.SourceAdapter. It blocks until ctx is cancelled,
// the commit callback fails, or a malformed cursor is supplied.
func (a *Adapter) Run(ctx context.Context, cursor string, commit func(context.Context, contracts.Batch) error) error {
	expected, err := parseOffsetCursor(cursor)
	if err != nil {
		return fmt.Errorf("satellite adapter: invalid resume cursor %q: %w", cursor, err)
	}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.awaitReadable(ctx); err != nil {
			return err
		}
		started := a.now()
		next, outcome, runErr := a.runOnce(ctx, expected, commit)
		elapsed := a.now().Sub(started)
		expected = next
		if runErr == nil {
			attempt = 0
			a.observe("reconnect", outcome, nil, elapsed)
			continue
		}
		var ce *commitErr
		if errors.As(runErr, &ce) {
			a.observe("stopped", "commit_failed", ce.err, elapsed)
			return ce
		}
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return runErr
		}
		attempt++
		delay := a.backoffFor(runErr, attempt)
		a.observe("retry", outcome, runErr, elapsed)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// awaitReadable blocks until CanRead allows the adapter to proceed, or ctx
// is cancelled. It is checked before every connection attempt so an adapter
// backing off from backpressure never opens a new stream in the meantime.
func (a *Adapter) awaitReadable(ctx context.Context) error {
	if a.CanRead == nil {
		return nil
	}
	for {
		allowed, err := a.CanRead(ctx)
		if err != nil {
			return fmt.Errorf("satellite adapter: CanRead: %w", err)
		}
		if allowed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// backoffFor picks a reconnect delay. Feed errors classify by their HTTP
// status (ClassifyFeedError); any other error (a mid-stream read failure,
// a plain transport error from OpenStream) is treated as transient.
func (a *Adapter) backoffFor(err error, attempt int) time.Duration {
	min, max := a.ReconnectMin, a.ReconnectMax
	if min <= 0 {
		min = 200 * time.Millisecond
	}
	if max <= 0 || max < min {
		max = min
	}
	class := ClassifyFeedError(err)
	switch class {
	case ClassBusy:
		// The feed may still be tearing down a prior owner's connection
		// after a takeover; back off with a long floor plus jitter
		// rather than retrying quickly (D-level decision, client.go).
		floor := 5 * time.Second
		return floor + jitter(max)
	case ClassAuth:
		// A bad token will not fix itself; back off far longer than a
		// transient failure so a misconfigured deployment does not
		// hammer the feed, while still eventually retrying in case the
		// Secret is rotated live.
		floor := 5 * time.Second
		if floor < max {
			floor = max
		}
		return floor + jitter(max)
	case ClassPermanent:
		return max
	default: // ClassTransient, and anything unclassified
		backoff := min << uint(minInt(attempt-1, 20))
		if backoff <= 0 || backoff > max {
			backoff = max
		}
		return backoff/2 + jitter(backoff/2+1)
	}
}

func jitter(bound time.Duration) time.Duration {
	if bound <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(bound)))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// pendingBatch accumulates one in-flight micro-batch: the raw wire bytes of
// every event folded into it so far, and whether any of them was a
// non-heartbeat record. A batch containing only heartbeats commits with
// Records:nil (the durable cursor still advances); any other non-empty
// batch commits with exactly one RawRecord whose payload is every folded
// event's raw bytes concatenated in offset order (D4) -- not one record per
// event.
type pendingBatch struct {
	from            int64
	count           int64
	raw             bytes.Buffer
	hasNonHeartbeat bool
	firstReceivedAt time.Time
}

func (b *pendingBatch) reset(from int64) {
	b.from = from
	b.count = 0
	b.raw.Reset()
	b.hasNonHeartbeat = false
}

func (b *pendingBatch) append(ev Event, receivedAt time.Time) {
	if b.count == 0 {
		b.firstReceivedAt = receivedAt
	}
	b.raw.Write(ev.Raw)
	b.count++
	if ev.Type != "heartbeat" {
		b.hasNonHeartbeat = true
	}
}

func (b *pendingBatch) empty() bool { return b.count == 0 }

// flush commits the pending batch, if non-empty, and resets it to start
// fresh at the resulting cursor. A commit failure is wrapped in commitErr
// so Run treats it as terminal rather than retrying.
func (a *Adapter) flush(ctx context.Context, b *pendingBatch, commit func(context.Context, contracts.Batch) error) error {
	if b.empty() {
		return nil
	}
	from, cursor := b.from, b.from+b.count
	var records []contracts.RawRecord
	if b.hasNonHeartbeat {
		records = []contracts.RawRecord{{
			Key:           fmt.Sprintf("offsets-%d-%d", from, cursor-1),
			Kind:          "sse_batch",
			Payload:       append([]byte(nil), b.raw.Bytes()...),
			ReceivedAt:    b.firstReceivedAt,
			CorrelationID: fmt.Sprintf("sat-%d", from),
			OrderingKey:   "satellite-feed",
		}}
	}
	batch := contracts.Batch{From: strconv.FormatInt(from, 10), Cursor: strconv.FormatInt(cursor, 10), Records: records}
	if err := commit(ctx, batch); err != nil {
		return &commitErr{err: err}
	}
	for _, rec := range records {
		if a.Received != nil {
			a.Received(rec.Key, a.now())
		}
	}
	b.reset(cursor)
	return nil
}

// runOnce opens one connection at the given cursor and reads events from it
// until the connection ends (cleanly, on error, on an offset gap, or on
// idle timeout) or ctx is cancelled. It returns the cursor to resume from on
// the next attempt, a short outcome label for Observe, and an error: nil
// for an ordinary disconnect worth retrying immediately, a *commitErr if
// the caller's commit callback failed, or ctx's own error if ctx ended the
// attempt.
func (a *Adapter) runOnce(ctx context.Context, cursor int64, commit func(context.Context, contracts.Batch) error) (next int64, outcome string, err error) {
	body, openErr := a.Client.OpenStream(ctx, strconv.FormatInt(cursor, 10))
	if openErr != nil {
		return cursor, "open_failed", openErr
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			body.Close()
		case <-done:
		}
	}()
	defer body.Close()

	type readResult struct {
		ev  Event
		err error
	}
	results := make(chan readResult, 1)
	go func() {
		reader := NewReader(body, a.maxLine())
		for {
			ev, err := reader.Next()
			select {
			case results <- readResult{ev, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	idleTimeout := a.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Second
	}
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	var batch pendingBatch
	batch.reset(cursor)
	expected := cursor

	var waitTimer *time.Timer
	defer func() {
		if waitTimer != nil {
			waitTimer.Stop()
		}
	}()
	var waitC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return expected, "context_done", ctx.Err()

		case <-idleTimer.C:
			if err := a.flush(ctx, &batch, commit); err != nil {
				return expected, "idle_flush_failed", err
			}
			return expected, "idle_timeout", errIdleTimeout

		case <-waitC:
			if err := a.flush(ctx, &batch, commit); err != nil {
				return expected, "max_wait_flush_failed", err
			}
			waitTimer = nil
			waitC = nil

		case r := <-results:
			idleTimer.Reset(idleTimeout)
			if r.err != nil {
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "disconnect_flush_failed", flushErr
				}
				if errors.Is(r.err, io.EOF) {
					return expected, "eof", nil
				}
				return expected, "read_error", r.err
			}
			ev := r.ev
			if ev.ID == "" && ev.Type == "" {
				// A bare comment record carries no id/type; it never
				// advances the cursor or joins a batch on its own.
				continue
			}
			id, convErr := strconv.ParseInt(ev.ID, 10, 64)
			if convErr != nil {
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "bad_id_flush_failed", flushErr
				}
				return expected, "bad_id", fmt.Errorf("satellite adapter: SSE id %q is not an integer", ev.ID)
			}
			if id < expected {
				continue // overlap from a prior connection's unacknowledged tail; already committed
			}
			if id > expected {
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "gap_flush_failed", flushErr
				}
				return expected, "offset_gap", ErrOffsetGap
			}

			isHeartbeat := ev.Type == "heartbeat"
			if isHeartbeat && batch.hasNonHeartbeat {
				// Close off the real-record batch exactly at this
				// boundary so the heartbeat gets its own dedicated,
				// empty-Records commit instead of being folded in.
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "pre_heartbeat_flush_failed", flushErr
				}
				waitTimer, waitC = nil, nil
			}
			batch.append(ev, a.now())
			expected = id + 1
			if a.MaxBytes > 0 && batch.raw.Len() >= a.MaxBytes {
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "max_bytes_flush_failed", flushErr
				}
				waitTimer, waitC = nil, nil
				continue
			}
			if a.MaxEvents > 0 && batch.count >= int64(a.MaxEvents) {
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "max_events_flush_failed", flushErr
				}
				waitTimer, waitC = nil, nil
				continue
			}
			if isHeartbeat {
				// A heartbeat always forms (and immediately flushes)
				// its own single-event batch, regardless of MaxWait.
				if flushErr := a.flush(ctx, &batch, commit); flushErr != nil {
					return expected, "heartbeat_flush_failed", flushErr
				}
				waitTimer, waitC = nil, nil
				continue
			}
			if waitTimer == nil && a.MaxWait > 0 {
				waitTimer = time.NewTimer(a.MaxWait)
				waitC = waitTimer.C
			}
		}
	}
}

func parseOffsetCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	return strconv.ParseInt(cursor, 10, 64)
}
