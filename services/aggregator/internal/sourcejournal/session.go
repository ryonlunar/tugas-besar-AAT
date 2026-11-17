// Package sourcejournal fences source intake using a dedicated PostgreSQL
// session. Its raw journal is separate from canonical HazardEvent processing.
package sourcejournal

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
)

//go:embed schema.sql
var schema string

var (
	ErrCapacity       = errors.New("unfinished raw journal capacity reached")
	ErrCursorConflict = errors.New("source cursor changed; reacquire ownership and resume")
	ErrRecordConflict = errors.New("source record key reused with different content")
)

// Install is called by migrations, not on every source acquisition.
func Install(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, schema)
	return err
}

type Session struct {
	mu          sync.Mutex
	conn        *pgx.Conn
	source      contracts.Source
	generation  int64
	cursor      string
	idleTimeout time.Duration
}

// Acquire opens its own connection: returning a session lock to a pool would
// leave ownership attached to an unrelated borrower. No client timestamp is
// used to decide ownership. An idle/frozen owner loses its server-side session.
func Acquire(ctx context.Context, dsn string, source contracts.Source, idleTimeout time.Duration) (*Session, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	keys := map[contracts.Source]int{contracts.SourceBMKG: 1, contracts.SourcePVMBG: 2, contracts.SourceSatellite: 3, contracts.SourceCoastal: 4}
	key, ok := keys[source]
	if !ok || idleTimeout < 100*time.Millisecond {
		return nil, false, errors.New("invalid source or idle timeout")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, false, err
	}
	session := &Session{conn: conn, source: source, idleTimeout: idleTimeout}
	owned := false
	defer func() {
		if !owned {
			_ = session.Close(context.Background())
		}
	}()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(4032,$1)`, key).Scan(&locked); err != nil || !locked {
		return nil, false, err
	}
	timeout := fmt.Sprintf("%dms", idleTimeout.Milliseconds())
	if _, err := conn.Exec(ctx, `SELECT set_config('idle_session_timeout',$1,false),set_config('idle_in_transaction_session_timeout',$1,false)`, timeout); err != nil {
		return nil, false, err
	}
	err = conn.QueryRow(ctx, `INSERT INTO ingest_sources(source,generation) VALUES($1,1)
		ON CONFLICT(source) DO UPDATE SET generation=ingest_sources.generation+1
		RETURNING generation,cursor`, source).Scan(&session.generation, &session.cursor)
	if err != nil {
		return nil, false, err
	}
	owned = true
	return session, true, nil
}

func (s *Session) Cursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *Session) Generation() int64 { return s.generation }

// Heartbeat must run more frequently than the idle timeout while the adapter is
// waiting for input. A failure ends the adapter run; the caller must reacquire a
// new Session. Never reconnect and reuse this Session's cached cursor or token.
func (s *Session) Heartbeat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	// A readable former primary is insufficient proof of ownership. Require a
	// synchronous commit on this session, without changing the generation/cursor.
	var generation int64
	err := s.conn.QueryRow(ctx, `UPDATE ingest_sources SET generation=generation
		WHERE source=$1 AND generation=$2 RETURNING generation`, s.source, s.generation).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCursorConflict
	}
	return err
}

// Append commits bytes and the cursor in the same transaction and on the same
// connection that owns the lock. A resumed owner cannot bypass a lost session
// by borrowing a fresh pooled connection.
func (s *Session) Append(ctx context.Context, batch contracts.Batch) (failure error) {
	ctx, span := otel.Tracer("aat/store").Start(ctx, "postgres.append_raw")
	span.SetAttributes(attribute.String("source", string(s.source)))
	defer func() {
		if failure != nil {
			span.SetStatus(codes.Error, "storage operation failed")
		}
		span.End()
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range batch.Records {
		if record.Key == "" || len(record.Key) > 512 || record.Kind == "" || len(record.Kind) > 64 || len(record.OrderingKey) > 256 || record.ReceivedAt.IsZero() || record.CorrelationID == "" || len(record.CorrelationID) > 64 {
			return errors.New("raw record identity, kind, timestamp, and correlation ID are required")
		}
	}
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var generation int64
	var cursor string
	if err := tx.QueryRow(ctx, `SELECT generation,cursor FROM ingest_sources WHERE source=$1 FOR UPDATE`, s.source).Scan(&generation, &cursor); err != nil {
		return err
	}
	if generation != s.generation || cursor != batch.From {
		return ErrCursorConflict
	}
	for _, record := range batch.Records {
		key := record.Key
		if record.Snapshot {
			if record.OrderingKey == "" {
				return errors.New("snapshot ordering key is required")
			}
			digest := contracts.RawKey(record.Key, record.Auxiliary)
			var previous string
			err := tx.QueryRow(ctx, `SELECT digest FROM ingest_snapshot_heads WHERE source=$1 AND ordering_key=$2`, s.source, record.OrderingKey).Scan(&previous)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if previous == digest {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ingest_snapshot_heads(source,ordering_key,digest) VALUES($1,$2,$3) ON CONFLICT(source,ordering_key) DO UPDATE SET digest=EXCLUDED.digest`, s.source, record.OrderingKey, digest); err != nil {
				return err
			}
			key = contracts.RawKey(batch.From+":"+record.OrderingKey, []byte(digest))
		}
		payload := record.Payload
		if payload == nil {
			payload = []byte{}
		}
		auxiliary := record.Auxiliary
		if auxiliary == nil {
			auxiliary = []byte{}
		}
		headers := telemetry.TraceHeaders(ctx)
		result, err := tx.Exec(ctx, `INSERT INTO ingest_records(source,record_key,kind,payload,received_at,correlation_id,ordering_key,auxiliary,traceparent,tracestate)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(source,record_key) DO NOTHING`, s.source, key, record.Kind, payload, record.ReceivedAt, record.CorrelationID, record.OrderingKey, auxiliary, headers["traceparent"], headers["tracestate"])
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			var payload, auxiliary []byte
			var kind string
			var ordering string
			if err := tx.QueryRow(ctx, `SELECT kind,payload,auxiliary,ordering_key FROM ingest_records WHERE source=$1 AND record_key=$2`, s.source, key).Scan(&kind, &payload, &auxiliary, &ordering); err != nil {
				return err
			}
			if kind != record.Kind || ordering != record.OrderingKey || !bytes.Equal(payload, record.Payload) || !bytes.Equal(auxiliary, record.Auxiliary) {
				return ErrRecordConflict
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ingest_sources SET cursor=$2 WHERE source=$1`, s.source, batch.Cursor); err != nil {
		return err
	}
	var pending int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ingest_records WHERE source=$1 AND status<>'done'`, s.source).Scan(&pending); err != nil {
		return err
	}
	if pending > 10000 {
		return ErrCapacity
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.cursor = batch.Cursor
	return nil
}

// Run keeps the source session alive between batches and cancels the adapter
// when ownership is lost. All source HTTP/stream reads must honor this context.
func (s *Session) Run(ctx context.Context, adapter contracts.SourceAdapter) error {
	if adapter == nil {
		return errors.New("source adapter is required")
	}
	return s.WithHeartbeat(ctx, func(ctx context.Context) error {
		return adapter.Run(ctx, s.Cursor(), s.Append)
	})
}

// WithHeartbeat runs work only while this dedicated ownership session is alive.
func (s *Session) WithHeartbeat(ctx context.Context, work func(context.Context) error) error {
	if work == nil {
		return errors.New("source work is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeat := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(s.idleTimeout / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				heartbeat <- nil
				return
			case <-ticker.C:
				probeCtx, stop := context.WithTimeout(ctx, time.Second)
				err := s.Heartbeat(probeCtx)
				stop()
				if err != nil {
					if ctx.Err() != nil {
						heartbeat <- nil
						return
					}
					cancel()
					heartbeat <- err
					return
				}
			}
		}
	}()
	err := work(ctx)
	cancel()
	return errors.Join(err, <-heartbeat)
}

func (s *Session) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.Close(ctx)
}
