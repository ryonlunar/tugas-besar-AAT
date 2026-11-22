package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestReceiptSurvivesRestartAndDeduplicatesRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receiver.db")
	db, err := openInbox(path)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("Bearer fixture-token"))
	const id = "139d202a-8385-50ef-a40f-dea40af07c1c"
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	data, _ := json.Marshal(contracts.HazardEvent{HazardID: id, Source: contracts.SourceCoastal, SourceRefID: "obs-1", HazardType: contracts.HazardCoastal, Severity: contracts.SeverityAwas, AreaName: "Padang", OccurredAt: stamp, IngestedAt: stamp, Attributes: map[string]any{"critical": true}})
	post := func(handler http.Handler, token, message string, revision int64, kind string) int {
		body, _ := json.Marshal(contracts.Envelope{SchemaVersion: 1, EventType: kind, MessageID: message, CorrelationID: "corr-1", PublishedAt: stamp, Producer: "aggregator", Revision: revision, Data: data})
		req := httptest.NewRequest(http.MethodPost, "/v1/notifications", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", message)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	handler := inbox{db: db, key: key}.handler()
	if status := post(handler, "wrong", id+":1", 1, "hazard.created"); status != 401 {
		t.Fatalf("unexpected auth result %d", status)
	}
	if status := post(handler, "fixture-token", id+":1", 1, "hazard.created"); status != 204 {
		t.Fatalf("unexpected delivery result %d", status)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openInbox(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler = inbox{db: db, key: key}.handler()
	if post(handler, "fixture-token", id+":1", 1, "hazard.created") != 204 || post(handler, "fixture-token", id+":2", 2, "hazard.updated") != 204 {
		t.Fatal("idempotent deliveries failed")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable receipts %d, %v", count, err)
	}
}

func TestReceiptScanIncludesMoreThanOneHundredAlerts(t *testing.T) {
	db, err := openInbox(filepath.Join(t.TempDir(), "receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const stamp = "2026-11-20T00:00:00Z"
	_, err = db.Exec(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<201)
		INSERT INTO notifications(hazard_id,message_id,payload,received_at)
		SELECT n,n,json_object('id',n),? FROM seq`, stamp)
	if err != nil {
		t.Fatal(err)
	}
	handler := inbox{db: db, key: sha256.Sum256([]byte("Bearer fixture-token"))}.handler()
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/notifications"+query, nil)
		req.Header.Set("Authorization", "Bearer fixture-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	last := 0
	for _, query := range []string{"?after=0", "?after=100", "?after=200"} {
		var page struct {
			Items      []receipt `json:"items"`
			NextCursor string    `json:"next_cursor"`
		}
		w := get(query)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
		}
		for _, row := range page.Items {
			var payload struct{ ID int }
			if err := json.Unmarshal(row.Notification, &payload); err != nil || payload.ID != last+1 {
				t.Fatalf("receipt order: %d after %d (%v)", payload.ID, last, err)
			}
			last = payload.ID
		}
		if (last < 201) != (page.NextCursor != "") {
			t.Fatalf("unexpected cursor %q after %d", page.NextCursor, last)
		}
	}
	if last != 201 || get("?after=-1").Code != 400 {
		t.Fatal("receipt scan missed entries or accepted an invalid cursor")
	}
	var sample []receipt
	if w := get(""); json.Unmarshal(w.Body.Bytes(), &sample) != nil || len(sample) != 100 {
		t.Fatal("legacy receipt sample changed")
	}
}
