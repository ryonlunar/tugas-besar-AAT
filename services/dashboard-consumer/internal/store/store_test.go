package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestDashboardPersistsUniqueInboxAndLatestRevisionAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.db")
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := contracts.Envelope{MessageID: "hazard:1", HazardID: "hazard", Revision: 1, CorrelationID: "poll-1", Data: json.RawMessage(`{"severity":"NORMAL"}`)}
	if err := state.Apply(ctx, base, "hazard.events.v1.BMKG", 1); err != nil {
		t.Fatal(err)
	}
	if err := state.Apply(ctx, base, "hazard.events.v1.BMKG", 2); err != nil {
		t.Fatal(err)
	}
	newer := base
	newer.MessageID, newer.Revision, newer.Data = "hazard:2", 2, json.RawMessage(`{"severity":"SIAGA"}`)
	if err := state.Apply(ctx, newer, "hazard.events.v1.BMKG", 1); err != nil {
		t.Fatal(err)
	}
	if err := state.Apply(ctx, base, "hazard.events.v1.BMKG", 3); err != nil {
		t.Fatal(err)
	}
	if err := state.Quarantine(ctx, []byte("bad"), "hazard.events.v1.BMKG", "INVALID_ENVELOPE", 1); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	view, err := state.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view["inbox_count"] != int64(2) || view["quarantine_count"] != int64(1) {
		t.Fatalf("unexpected persisted counts: %v", view)
	}
	var quarantined []byte
	if err := state.db.QueryRowContext(ctx, `SELECT payload FROM quarantine_payloads LIMIT 1`).Scan(&quarantined); err != nil || string(quarantined) != "bad" {
		t.Fatalf("quarantine payload was not durable: payload=%q error=%v", quarantined, err)
	}
	snapshots := view["snapshots"].([]Snapshot)
	if len(snapshots) != 1 || snapshots[0].Revision != 2 || snapshots[0].MessageID != "hazard:2" {
		t.Fatalf("snapshot regressed: %+v", snapshots)
	}
}
