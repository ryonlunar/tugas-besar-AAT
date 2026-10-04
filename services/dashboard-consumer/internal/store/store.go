package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Snapshot struct {
	HazardID  string          `json:"hazard_id"`
	Revision  int64           `json:"revision"`
	MessageID string          `json:"message_id"`
	Data      json.RawMessage `json:"data"`
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("dashboard state path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS inbox (message_id TEXT PRIMARY KEY, hazard_id TEXT NOT NULL, revision INTEGER NOT NULL, correlation_id TEXT NOT NULL, subject TEXT NOT NULL, delivery_count INTEGER NOT NULL, received_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS snapshots (hazard_id TEXT PRIMARY KEY, revision INTEGER NOT NULL, message_id TEXT NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS quarantine (digest TEXT PRIMARY KEY, subject TEXT NOT NULL, reason TEXT NOT NULL, delivery_count INTEGER NOT NULL, received_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS quarantine_payloads (digest TEXT PRIMARY KEY, payload BLOB NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Apply(ctx context.Context, event contracts.Envelope, subject string, delivery uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO inbox(message_id,hazard_id,revision,correlation_id,subject,delivery_count,received_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(message_id) DO NOTHING`, event.MessageID, event.HazardID, event.Revision, event.CorrelationID, subject, delivery, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		_, err = tx.ExecContext(ctx, `INSERT INTO snapshots(hazard_id,revision,message_id,data) VALUES(?,?,?,?) ON CONFLICT(hazard_id) DO UPDATE SET revision=excluded.revision,message_id=excluded.message_id,data=excluded.data WHERE excluded.revision > snapshots.revision`, event.HazardID, event.Revision, event.MessageID, string(event.Data))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE inbox SET delivery_count=MAX(delivery_count,?) WHERE message_id=?`, delivery, event.MessageID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Quarantine(ctx context.Context, payload []byte, subject, reason string, delivery uint64) error {
	digest := sha256.Sum256(append(append([]byte(subject), 0), payload...))
	key := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO quarantine(digest,subject,reason,delivery_count,received_at) VALUES(?,?,?,?,?) ON CONFLICT(digest) DO UPDATE SET delivery_count=MAX(quarantine.delivery_count,excluded.delivery_count)`, key, subject, reason, delivery, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quarantine_payloads(digest,payload) VALUES(?,?) ON CONFLICT(digest) DO NOTHING`, key, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Inspect(ctx context.Context) (map[string]any, error) {
	var inboxCount, quarantineCount int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM inbox`).Scan(&inboxCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM quarantine`).Scan(&quarantineCount); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT hazard_id,revision,message_id,data FROM snapshots ORDER BY hazard_id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshots := make([]Snapshot, 0)
	for rows.Next() {
		var item Snapshot
		var raw string
		if err := rows.Scan(&item.HazardID, &item.Revision, &item.MessageID, &raw); err != nil {
			return nil, err
		}
		item.Data = json.RawMessage(raw)
		snapshots = append(snapshots, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"inbox_count": inboxCount, "quarantine_count": quarantineCount, "snapshots": snapshots}, nil
}
