package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"time"
)

// Inspection runs inside the store owner, in a read-only transaction.
func runStoreAuditCommand() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := openDB(cfg.databaseURL, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data json.RawMessage
	err = tx.QueryRowContext(ctx, `SELECT jsonb_build_object(
	  'counts', jsonb_build_object(
	    'hazards', (SELECT count(*) FROM hazard_events),
	    'versions', (SELECT count(*) FROM hazard_versions),
	    'pending_outbox', (SELECT count(*) FROM outbox WHERE published_at IS NULL),
	    'published_outbox', (SELECT count(*) FROM outbox WHERE published_at IS NOT NULL)),
	  'columns', (SELECT jsonb_agg(c) FROM (
	    SELECT table_name,column_name,data_type,is_nullable
	    FROM information_schema.columns WHERE table_schema='public'
	    ORDER BY table_name,ordinal_position) c),
	  'migrations', (SELECT jsonb_agg(m) FROM (
	    SELECT version,checksum,applied_at FROM schema_migrations ORDER BY version) m),
	  'recent_outbox', (SELECT jsonb_agg(o) FROM (
	    SELECT message_id,subject,revision,created_at,published_at,attempts,last_error_code
	    FROM outbox ORDER BY created_at DESC LIMIT 10) o),
	  'source_state', (SELECT jsonb_agg(s) FROM source_state s)
	)`).Scan(&data)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(data)
}
