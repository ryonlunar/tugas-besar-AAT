package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

const DefaultOutboxLimit int64 = 10000

var ErrOutboxCapacity = errors.New("outbox capacity reached")

type Store struct {
	db                                                      *sql.DB // shared-pool compatibility constructor for integration tests
	readDB, bmkgWriteDB, pvmbgWriteDB, relayDB, migrationDB *sql.DB
	staleAfter                                              time.Duration
	readTimeout, writeTimeout                               time.Duration
	outboxLimit                                             int64
}

type SourceState struct {
	Status        string     `json:"status"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	StaleSince    *time.Time `json:"stale_since"`
}

type OutboxMessage struct {
	MessageID string
	Subject   string
	Envelope  []byte
	Attempts  int
}

func New(db *sql.DB, staleAfter time.Duration) *Store {
	return NewWithPools(db, db, db, db, staleAfter)
}

func NewWithPools(readDB, bmkgWriteDB, pvmbgWriteDB, relayDB *sql.DB, staleAfter time.Duration) *Store {
	if staleAfter <= 0 {
		staleAfter = 15 * time.Second
	}
	return &Store{db: bmkgWriteDB, readDB: readDB, bmkgWriteDB: bmkgWriteDB, pvmbgWriteDB: pvmbgWriteDB, relayDB: relayDB, migrationDB: bmkgWriteDB, staleAfter: staleAfter, readTimeout: 200 * time.Millisecond, writeTimeout: 2 * time.Second, outboxLimit: DefaultOutboxLimit}
}

func (s *Store) SetOutboxLimit(limit int64) {
	if limit <= 0 {
		limit = DefaultOutboxLimit
	}
	s.outboxLimit = limit
}

func (s *Store) SetTimeouts(readTimeout, writeTimeout time.Duration) {
	if readTimeout > 0 {
		s.readTimeout = readTimeout
	}
	if writeTimeout > 0 {
		s.writeTimeout = writeTimeout
	}
}

func (s *Store) readContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.readTimeout)
}
func (s *Store) writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.writeTimeout)
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.readDB == nil {
		return errors.New("database is not configured")
	}
	return s.readDB.PingContext(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.migrationDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(4031, 1)`); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(4031, 1)`)
	}()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		body, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		err = conn.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version=$1`, name).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("migration checksum mismatch for %s", name)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read migration ledger %s: %w", name, err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,checksum,applied_at) VALUES($1,$2,now())`, name, checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) FindHazardID(ctx context.Context, source mapper.Source, refID string) (string, bool, error) {
	if !sourceIDPattern.MatchString(refID) {
		return "", false, errors.New("source reference id is invalid")
	}
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	var id string
	err := s.readDB.QueryRowContext(ctx, `SELECT hazard_id::text FROM hazard_events WHERE source=$1 AND source_ref_id=$2`, string(source), refID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (s *Store) ApplyCycle(ctx context.Context, source mapper.Source, events []mapper.HazardEvent, correlationID string, success bool, errorCode string, at time.Time) error {
	if source != mapper.SourceBMKG && source != mapper.SourcePVMBG {
		return errors.New("unknown source")
	}
	if at.IsZero() {
		return errors.New("cycle timestamp is required")
	}
	if success && errorCode != "" {
		return errors.New("successful cycle cannot contain an error code")
	}
	if !success && errorCode == "" {
		errorCode = "SOURCE_DEGRADED"
	}
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	db := s.bmkgWriteDB
	if source == mapper.SourcePVMBG {
		db = s.pvmbgWriteDB
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin source cycle: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4031, 2)`); err != nil {
		return fmt.Errorf("acquire outbox capacity lock: %w", err)
	}
	var pending int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
		return fmt.Errorf("count pending outbox messages: %w", err)
	}
	if pending >= s.outboxLimit {
		return ErrOutboxCapacity
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event.Source != source || !sourceIDPattern.MatchString(event.SourceRefID) {
			return errors.New("event source or source reference id is invalid")
		}
		if _, duplicate := seen[event.SourceRefID]; duplicate {
			return fmt.Errorf("duplicate source record %q in snapshot", event.SourceRefID)
		}
		seen[event.SourceRefID] = struct{}{}
		if err := s.upsertEvent(ctx, tx, event, correlationID, at.UTC(), &pending); err != nil {
			return fmt.Errorf("upsert source record %s: %w", event.SourceRefID, err)
		}
	}
	if err := updateSourceState(ctx, tx, source, at.UTC(), success, errorCode); err != nil {
		return fmt.Errorf("update source state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source cycle: %w", err)
	}
	return nil
}

func (s *Store) upsertEvent(ctx context.Context, tx *sql.Tx, event mapper.HazardEvent, correlationID string, at time.Time, pending *int64) error {
	if event.HazardID == "" {
		id, err := newUUID()
		if err != nil {
			return err
		}
		event.HazardID = id
	}
	if !validUUID(event.HazardID) {
		return errors.New("hazard id must be UUID")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil {
		return errors.New("event occurred_at must be RFC3339")
	}
	occurredAt = occurredAt.UTC().Truncate(time.Microsecond)
	event.OccurredAt = occurredAt.Format(time.RFC3339Nano)
	if event.AreaName == "" || event.Attributes == nil || event.Latitude < -90 || event.Latitude > 90 || event.Longitude < -180 || event.Longitude > 180 {
		return errors.New("canonical event fields are invalid")
	}
	var hazardID, oldHash string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT hazard_id::text, revision, content_hash FROM hazard_events WHERE source=$1 AND source_ref_id=$2 FOR UPDATE`, string(event.Source), event.SourceRefID).Scan(&hazardID, &revision, &oldHash)
	isNew := errors.Is(err, sql.ErrNoRows)
	if err != nil && !isNew {
		return err
	}
	if isNew {
		revision = 0
	} else {
		event.HazardID = hazardID
	}
	event.IngestedAt = at.Format(time.RFC3339Nano)
	content, hash, err := canonicalContent(event)
	if err != nil {
		return err
	}
	if !isNew && oldHash == hash {
		return nil
	}
	if !isNew {
		var equivalent bool
		err := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('source',source,'source_ref_id',source_ref_id,'hazard_type',hazard_type,'severity',severity,'area_name',area_name,'latitude',latitude,'longitude',longitude,'attributes',attributes) = ($1::jsonb - 'occurred_at') AND occurred_at=$4::timestamptz FROM hazard_events WHERE source=$2 AND source_ref_id=$3`, content, string(event.Source), event.SourceRefID, occurredAt).Scan(&equivalent)
		if err != nil {
			return fmt.Errorf("compare canonical event content: %w", err)
		}
		if equivalent {
			return nil
		}
	}
	if *pending >= s.outboxLimit {
		return ErrOutboxCapacity
	}
	revision++
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode canonical event: %w", err)
	}
	if isNew {
		_, err = tx.ExecContext(ctx, `INSERT INTO hazard_events(hazard_id,source,source_ref_id,hazard_type,severity,area_name,latitude,longitude,occurred_at,ingested_at,attributes,revision,content_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13)`, event.HazardID, event.Source, event.SourceRefID, event.HazardType, event.Severity, event.AreaName, event.Latitude, event.Longitude, occurredAt.UTC(), at, jsonBytes(event.Attributes), revision, hash)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE hazard_events SET hazard_type=$2,severity=$3,area_name=$4,latitude=$5,longitude=$6,occurred_at=$7,ingested_at=$8,attributes=$9::jsonb,revision=$10,content_hash=$11 WHERE hazard_id=$1`, event.HazardID, event.HazardType, event.Severity, event.AreaName, event.Latitude, event.Longitude, occurredAt.UTC(), at, jsonBytes(event.Attributes), revision, hash)
	}
	if err != nil {
		return fmt.Errorf("write current event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hazard_versions(hazard_id,revision,data,recorded_at) VALUES($1,$2,$3::jsonb,$4)`, event.HazardID, revision, data, at); err != nil {
		return fmt.Errorf("append event version: %w", err)
	}
	eventType := "hazard.updated"
	if isNew {
		eventType = "hazard.created"
	}
	if !sourceIDPattern.MatchString(correlationID) {
		return errors.New("correlation id is invalid")
	}
	envelope := map[string]any{
		"schema_version": 1,
		"event_type":     eventType,
		"message_id":     fmt.Sprintf("%s:%d", event.HazardID, revision),
		"correlation_id": correlationID,
		"published_at":   at.Format(time.RFC3339Nano),
		"producer":       "aggregator",
		"revision":       revision,
		"data":           json.RawMessage(data),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode outbox envelope: %w", err)
	}
	subject := "hazard.events.v1." + string(event.Source)
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox(message_id,hazard_id,revision,subject,envelope,created_at,next_attempt_at) VALUES($1,$2,$3,$4,$5::jsonb,$6,$6)`, envelope["message_id"], event.HazardID, revision, subject, payload, at)
	if err != nil {
		return fmt.Errorf("insert outbox message: %w", err)
	}
	(*pending)++
	_ = content
	return nil
}

func updateSourceState(ctx context.Context, tx *sql.Tx, source mapper.Source, at time.Time, success bool, code string) error {
	if success {
		_, err := tx.ExecContext(ctx, `INSERT INTO source_state(source,last_attempt_at,last_success_at,last_failure_at,failure_since,last_error_code) VALUES($1,$2,$2,NULL,NULL,NULL) ON CONFLICT(source) DO UPDATE SET last_attempt_at=EXCLUDED.last_attempt_at,last_success_at=EXCLUDED.last_success_at,failure_since=NULL,last_error_code=NULL`, string(source), at)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO source_state(source,last_attempt_at,last_success_at,last_failure_at,failure_since,last_error_code) VALUES($1,$2,NULL,$2,$2,$3) ON CONFLICT(source) DO UPDATE SET last_attempt_at=EXCLUDED.last_attempt_at,last_failure_at=EXCLUDED.last_failure_at,failure_since=COALESCE(source_state.failure_since,EXCLUDED.failure_since),last_error_code=EXCLUDED.last_error_code`, string(source), at, code)
	return err
}

func canonicalContent(event mapper.HazardEvent) ([]byte, string, error) {
	occurredAt, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil {
		return nil, "", errors.New("event occurred_at must be RFC3339")
	}
	event.OccurredAt = occurredAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	content := map[string]any{
		"source":        event.Source,
		"source_ref_id": event.SourceRefID,
		"hazard_type":   event.HazardType,
		"severity":      event.Severity,
		"area_name":     event.AreaName,
		"latitude":      event.Latitude,
		"longitude":     event.Longitude,
		"occurred_at":   event.OccurredAt,
		"attributes":    event.Attributes,
	}
	data, err := json.Marshal(content)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func jsonBytes(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func (s *Store) List(ctx context.Context, source string, limit, offset int) ([]mapper.HazardEvent, bool, error) {
	if limit < 1 || offset < 0 || (source != "" && source != string(mapper.SourceBMKG) && source != string(mapper.SourcePVMBG)) {
		return nil, false, errors.New("invalid hazard query")
	}
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	rows, err := s.readDB.QueryContext(ctx, `SELECT jsonb_build_object('hazard_id',hazard_id,'source',source,'source_ref_id',source_ref_id,'hazard_type',hazard_type,'severity',severity,'area_name',area_name,'latitude',latitude,'longitude',longitude,'occurred_at',occurred_at,'ingested_at',ingested_at,'attributes',attributes) FROM hazard_events WHERE ($1='' OR source=$1) ORDER BY occurred_at DESC, hazard_id ASC LIMIT $2 OFFSET $3`, source, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]mapper.HazardEvent, 0, limit)
	for rows.Next() {
		var raw []byte
		var event mapper.HazardEvent
		if err := rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, false, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (s *Store) Get(ctx context.Context, hazardID string) (mapper.HazardEvent, bool, error) {
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	var raw []byte
	err := s.readDB.QueryRowContext(ctx, `SELECT data FROM hazard_versions WHERE hazard_id::text=$1 ORDER BY revision DESC LIMIT 1`, hazardID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return mapper.HazardEvent{}, false, nil
	}
	if err != nil {
		return mapper.HazardEvent{}, false, err
	}
	var event mapper.HazardEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return mapper.HazardEvent{}, false, err
	}
	return event, true, nil
}

func (s *Store) GetVersion(ctx context.Context, hazardID string, revision int64) (json.RawMessage, bool, error) {
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	var data []byte
	err := s.readDB.QueryRowContext(ctx, `SELECT data FROM hazard_versions WHERE hazard_id::text=$1 AND revision=$2`, hazardID, revision).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append(json.RawMessage(nil), data...), true, nil
}

func (s *Store) SourceStates(ctx context.Context, sources []mapper.Source, now time.Time) (map[mapper.Source]SourceState, error) {
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	result := make(map[mapper.Source]SourceState, len(sources))
	for _, source := range sources {
		var lastSuccess, failureSince sql.NullTime
		err := s.readDB.QueryRowContext(ctx, `SELECT last_success_at,failure_since FROM source_state WHERE source=$1`, string(source)).Scan(&lastSuccess, &failureSince)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !lastSuccess.Valid) {
			result[source] = SourceState{Status: "unavailable"}
			continue
		}
		if err != nil {
			return nil, err
		}
		state := SourceState{Status: "fresh", LastSuccessAt: timePointer(lastSuccess.Time)}
		threshold := lastSuccess.Time.Add(s.staleAfter)
		if failureSince.Valid || now.Sub(lastSuccess.Time) > s.staleAfter {
			state.Status = "stale"
			staleAt := threshold
			if failureSince.Valid && failureSince.Time.Before(staleAt) {
				staleAt = failureSince.Time
			}
			state.StaleSince = timePointer(staleAt)
		}
		result[source] = state
	}
	return result, nil
}

func (s *Store) PendingOutboxCount(ctx context.Context) (int64, error) {
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	var count int64
	if err := s.relayDB.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) PendingOutbox(ctx context.Context, limit int, now time.Time) ([]OutboxMessage, error) {
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	rows, err := s.relayDB.QueryContext(ctx, `SELECT message_id,subject,envelope,attempts FROM outbox WHERE published_at IS NULL AND next_attempt_at <= $1 ORDER BY created_at,message_id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]OutboxMessage, 0, limit)
	for rows.Next() {
		var msg OutboxMessage
		if err := rows.Scan(&msg.MessageID, &msg.Subject, &msg.Envelope, &msg.Attempts); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

func (s *Store) MarkPublished(ctx context.Context, messageID string, at time.Time) error {
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	result, err := s.relayDB.ExecContext(ctx, `UPDATE outbox SET published_at=$2,last_error_code=NULL WHERE message_id=$1 AND published_at IS NULL`, messageID, at.UTC())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errors.New("outbox message was not pending")
	}
	return err
}

func (s *Store) RecordPublishFailure(ctx context.Context, messageID, code string, next time.Time) error {
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	result, err := s.relayDB.ExecContext(ctx, `UPDATE outbox SET attempts=attempts+1,next_attempt_at=$3,last_error_code=$2 WHERE message_id=$1 AND published_at IS NULL`, messageID, code, next.UTC())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errors.New("outbox message was not pending")
	}
	return err
}

func timePointer(value time.Time) *time.Time { copied := value.UTC(); return &copied }

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
