package sharded

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

// ImportLegacy copies a stopped M1 writer's canonical state without changing
// identities or revisions. Repeated imports accept identical rows only. Outbox
// records are republished into the new broker; consumers deduplicate their IDs.
func (s *Store) ImportLegacy(ctx context.Context, legacy *sql.DB) error {
	var tables int
	if err := legacy.QueryRowContext(ctx, `SELECT count(*) FROM unnest(
    ARRAY['hazard_events','hazard_versions','outbox','source_state']) AS name
    WHERE to_regclass(name) IS NOT NULL`).Scan(&tables); err != nil {
		return fmt.Errorf("inspect legacy schema: %w", err)
	}
	if tables == 0 {
		return nil // A fresh installation has no M1 state to copy.
	}
	if tables != 4 {
		return fmt.Errorf("incomplete legacy schema: found %d of 4 required tables", tables)
	}
	transactions := map[string]*sql.Tx{}
	defer func() {
		for _, tx := range transactions {
			_ = tx.Rollback()
		}
	}()
	for _, u := range s.units {
		tx, err := u.store.BeginImport(ctx)
		if err != nil {
			return err
		}
		transactions[u.name] = tx
	}
	queries := []struct{ table, query, key string }{
		{"hazard_events", `SELECT row_to_json(e),e.source,e.source_ref_id FROM hazard_events e`, "hazard_id"},
		{"hazard_versions", `SELECT row_to_json(v),e.source,e.source_ref_id FROM hazard_versions v JOIN hazard_events e USING(hazard_id)`, "hazard_id,revision"},
		{"outbox", `SELECT row_to_json(o),e.source,e.source_ref_id FROM outbox o JOIN hazard_events e USING(hazard_id)`, "message_id"},
	}
	for _, q := range queries {
		rows, err := legacy.QueryContext(ctx, q.query)
		if err != nil {
			return err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var raw []byte
				var source, ref string
				if err := rows.Scan(&raw, &source, &ref); err != nil {
					return err
				}
				name, err := partition.Unit(contracts.Source(source), ref)
				if err != nil {
					return err
				}
				statement := fmt.Sprintf(`INSERT INTO %s SELECT * FROM json_populate_record(NULL::%s,$1::json)
     ON CONFLICT (%s) DO UPDATE SET hazard_id=EXCLUDED.hazard_id WHERE to_jsonb(%s)=to_jsonb(EXCLUDED)`, q.table, q.table, q.key, q.table)
				if q.table == "hazard_events" {
					statement = strings.Replace(statement, "$1::json)", "(jsonb_build_object('retracted',false) || $1::jsonb)::json)", 1)
				}
				if q.table == "outbox" {
					statement = `INSERT INTO outbox SELECT * FROM json_populate_record(NULL::outbox,
      (jsonb_build_object('traceparent','','tracestate','') || $1::jsonb || jsonb_build_object('published_at',NULL,'attempts',0,'next_attempt_at',now(),'last_error_code',NULL))::json)
      ON CONFLICT (message_id) DO NOTHING`
				}
				result, err := transactions[name].ExecContext(ctx, statement, raw)
				if err != nil {
					return fmt.Errorf("import %s on %s: %w", q.table, name, err)
				}
				if q.table != "outbox" {
					n, err := result.RowsAffected()
					if err != nil || n != 1 {
						return fmt.Errorf("conflicting %s already exists on %s", q.table, name)
					}
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return err
		}
	}
	rows, err := legacy.QueryContext(ctx, `SELECT row_to_json(s) FROM source_state s`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		for _, tx := range transactions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO source_state SELECT * FROM json_populate_record(NULL::source_state,$1::json) ON CONFLICT(source) DO NOTHING`, raw); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range s.units {
		if err := transactions[u.name].Commit(); err != nil {
			return fmt.Errorf("commit import on %s: %w", u.name, err)
		}
	}
	return nil
}
