package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestNotifierRecordsOneReceiptPerMessageAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifier.db")
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	event := contracts.Envelope{MessageID: "hazard:1", HazardID: "hazard", Revision: 1, CorrelationID: "poll-1"}
	if err := state.Apply(ctx, event, "hazard.events.v1.BMKG", 1); err != nil {
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
	if err := state.Apply(ctx, event, "hazard.events.v1.BMKG", 2); err != nil {
		t.Fatal(err)
	}
	if err := state.Quarantine(ctx, []byte("bad"), "hazard.events.v1.BMKG", "INVALID_ENVELOPE", 1); err != nil {
		t.Fatal(err)
	}
	view, err := state.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view["inbox_count"] != int64(1) {
		t.Fatalf("duplicate inbox row: %v", view)
	}
	var quarantined []byte
	if err := state.db.QueryRowContext(ctx, `SELECT payload FROM quarantine_payloads LIMIT 1`).Scan(&quarantined); err != nil || string(quarantined) != "bad" {
		t.Fatalf("quarantine payload was not durable: payload=%q error=%v", quarantined, err)
	}
	receipts := view["notifications"].([]Receipt)
	if len(receipts) != 1 || receipts[0].MessageID != event.MessageID || receipts[0].Status != "recorded" {
		t.Fatalf("unexpected receipts: %+v", receipts)
	}
}
