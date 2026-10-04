package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/broker"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

func publicationTestStore(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	db, err := sql.Open("pgx", requireTestDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repository := New(db, 15*time.Second)
	if err := repository.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`TRUNCATE outbox, hazard_versions, hazard_events, source_state CASCADE`); err != nil {
		t.Fatal(err)
	}
	return db, repository
}

func TestStoredEnvelopePublishesAfterJSONBRoundTrip(t *testing.T) {
	url, token := os.Getenv("AAT_TEST_NATS_URL"), os.Getenv("AAT_TEST_NATS_TOKEN")
	if url == "" || token == "" {
		t.Skip("disposable PostgreSQL and NATS are required for the wire boundary test")
	}
	db, repository := publicationTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	at := time.Now().UTC()
	extra := make(map[string]int, 6000)
	for i := 0; i < 6000; i++ {
		extra[fmt.Sprintf("k%04d", i)] = 0
	}
	raw, err := json.Marshal(map[string]any{"report_id": "wire-boundary", "volcano_id": "sim-tamriel-red-mountain", "alert_level": "Normal", "eruption_count_24h": 0, "ash_column_height_m": 0, "reported_at": at.Format(time.RFC3339Nano), "extra": extra})
	if err != nil {
		t.Fatal(err)
	}
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	event, err := mapper.New(mapper.DefaultVolcanoLocations()).MapVolcanic(raw, id, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{event}, "wire-boundary", true, "", at, at); err != nil {
		t.Fatal(err)
	}
	var storedBytes int
	if err := db.QueryRowContext(ctx, `SELECT octet_length(envelope::text) FROM outbox WHERE hazard_id=$1`, id).Scan(&storedBytes); err != nil {
		t.Fatal(err)
	}
	if storedBytes <= 64<<10 {
		t.Fatalf("fixture must exercise JSONB expansion, got %d bytes", storedBytes)
	}
	pending, err := repository.PendingOutbox(ctx, 1, at.Add(time.Second))
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending count=%d error=%v", len(pending), err)
	}
	stream := "AAT_WIRE_" + strings.ReplaceAll(id, "-", "")
	publisher, err := broker.NewJetStreamPublisher(broker.JetStreamConfig{URL: url, Token: token, Stream: stream, Subjects: []string{SubjectPVMBG}, MaxAge: time.Minute, MaxBytes: 1 << 20, MaxMsgs: 10, MaxMsgSize: 64 << 10, DuplicateWindow: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	msg := pending[0]
	if err := publisher.Publish(ctx, msg.Subject, msg.Envelope, msg.MessageID); err != nil {
		t.Fatal(err)
	}
	conn, err := nats.Connect(url, nats.Token(token))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	defer js.DeleteStream(stream)
	sub, err := js.PullSubscribe(SubjectPVMBG, "wire-test", nats.BindStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := sub.Fetch(1, nats.MaxWait(time.Second))
	if err != nil || len(messages) != 1 {
		t.Fatalf("received count=%d error=%v", len(messages), err)
	}
	envelope, err := contracts.ParseEnvelope(messages[0].Data, messages[0].Subject, messages[0].Header.Get(nats.MsgIdHdr))
	if err != nil || envelope.HazardID != id {
		t.Fatalf("received invalid envelope: hazard=%s error=%v", envelope.HazardID, err)
	}
	var data struct {
		Attributes struct {
			Extra map[string]int `json:"extra"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil || len(data.Attributes.Extra) != len(extra) {
		t.Fatalf("dynamic object was not preserved: fields=%d error=%v", len(data.Attributes.Extra), err)
	}
	if err := messages[0].AckSync(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizedOversizedRecordsDoNotWriteOrBlockValidRecords(t *testing.T) {
	for _, test := range []struct {
		name     string
		limit    int
		exponent string
	}{
		{"default limit", 64 << 10, "1e70000"},
		{"configured limit", 1024, "1e1000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, repository := publicationTestStore(t)
			repository.SetMaxEnvelopeBytes(test.limit)
			ctx := context.Background()
			at := time.Now().UTC()
			existing := testHazard("00000000-0000-4000-8000-000000000001", "PVMBG", "existing", at, map[string]any{"value": "before"})
			if err := repository.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{existing}, "initial", true, "", at, at); err != nil {
				t.Fatal(err)
			}
			before := testHazard("00000000-0000-4000-8000-000000000002", "PVMBG", "before", at, map[string]any{})
			oversized := testHazard("00000000-0000-4000-8000-000000000003", "PVMBG", "oversized", at, map[string]any{"reading": json.Number(test.exponent)})
			update := existing
			update.Attributes = oversized.Attributes
			after := testHazard("00000000-0000-4000-8000-000000000004", "PVMBG", "after", at, map[string]any{})
			if err := repository.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{before, oversized, update, after}, "mixed", true, "", at, at); err != nil {
				t.Fatal(err)
			}
			var events, versions, pending int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM hazard_events), (SELECT count(*) FROM hazard_versions), (SELECT count(*) FROM outbox)`).Scan(&events, &versions, &pending); err != nil {
				t.Fatal(err)
			}
			if events != 3 || versions != 3 || pending != 3 {
				t.Fatalf("partial or blocked writes: events=%d versions=%d outbox=%d", events, versions, pending)
			}
			if _, found, err := repository.FindHazardID(ctx, mapper.SourcePVMBG, "oversized"); err != nil || found {
				t.Fatalf("oversized record persisted: found=%t error=%v", found, err)
			}
			old, found, err := repository.Get(ctx, existing.HazardID)
			if err != nil || !found || old.Attributes["value"] != "before" {
				t.Fatalf("rejected update changed existing record: %+v found=%t error=%v", old, found, err)
			}
			var code string
			if err := db.QueryRow(`SELECT last_error_code FROM source_state WHERE source='PVMBG'`).Scan(&code); err != nil || code != "EVENT_TOO_LARGE" {
				t.Fatalf("rejection was not recorded: code=%s error=%v", code, err)
			}
		})
	}
}
