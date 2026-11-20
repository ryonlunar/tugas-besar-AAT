// Field Team notification receiver. A successful reply means the alert is
// durably visible in the receiver's inbox.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	_ "modernc.org/sqlite"
)

type inbox struct {
	db      *sql.DB
	key     [32]byte
	latency prometheus.Observer
}

func (i inbox) handler() http.Handler {
	mux := http.NewServeMux()
	ready := func(w http.ResponseWriter, r *http.Request) {
		if i.db.PingContext(r.Context()) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}
	mux.HandleFunc("GET /health", ready)
	mux.HandleFunc("GET /ready", ready)
	mux.HandleFunc("POST /v1/notifications", i.authorize(i.receive))
	mux.HandleFunc("GET /v1/notifications", i.authorize(i.list))
	return mux
}

func (i inbox) authorize(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(key[:], i.key[:]) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		fn(w, r)
	}
}

func (i inbox) receive(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	e, err := contracts.ParseEnvelope(raw, "hazard.events.v1.SENSOR_PANTAI", r.Header.Get("Idempotency-Key"))
	var data struct {
		Attributes struct {
			Critical   bool      `json:"critical"`
			ReceivedAt time.Time `json:"received_at"`
		} `json:"attributes"`
	}
	if err != nil || e.EventType == "hazard.retracted" || r.Header.Get("Idempotency-Key") != e.MessageID || json.Unmarshal(e.Data, &data) != nil || !data.Attributes.Critical {
		http.Error(w, "invalid critical alert", 400)
		return
	}
	// One alert per observation, including replays of a later revision.
	at := time.Now().UTC()
	result, err := i.db.ExecContext(r.Context(), `INSERT INTO notifications(hazard_id,message_id,payload,received_at) VALUES(?,?,?,?) ON CONFLICT(hazard_id) DO NOTHING`, e.HazardID, e.MessageID, raw, at.Format(time.RFC3339Nano))
	if err != nil {
		http.Error(w, "inbox unavailable", 503)
		return
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		http.Error(w, "inbox unavailable", 503)
		return
	}
	if inserted == 1 {
		if delay := at.Sub(data.Attributes.ReceivedAt); i.latency != nil && !data.Attributes.ReceivedAt.IsZero() && delay >= 0 {
			i.latency.Observe(delay.Seconds())
		}
		slog.InfoContext(r.Context(), "Field Team alert committed", "message_id", e.MessageID, "hazard_id", e.HazardID, "correlation_id", e.CorrelationID, "trace_id", telemetry.TraceID(r.Context()))
	}
	w.WriteHeader(http.StatusNoContent)
}

type receipt struct {
	Notification json.RawMessage `json:"notification"`
	ReceivedAt   string          `json:"received_at"`
}

func (i inbox) list(w http.ResponseWriter, r *http.Request) {
	values, paginated := r.URL.Query()["after"]
	query := `SELECT rowid,payload,received_at FROM notifications ORDER BY rowid DESC LIMIT 100`
	var params []any
	if paginated {
		if len(values) != 1 {
			http.Error(w, "invalid cursor", 400)
			return
		}
		after, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || after < 0 {
			http.Error(w, "invalid cursor", 400)
			return
		}
		query = `SELECT rowid,payload,received_at FROM notifications WHERE rowid > ? ORDER BY rowid LIMIT 101`
		params = []any{after}
	}
	rows, err := i.db.QueryContext(r.Context(), query, params...)
	if err != nil {
		http.Error(w, "inbox unavailable", 503)
		return
	}
	defer rows.Close()
	result := []receipt{}
	var last int64
	var next string
	for rows.Next() {
		var item receipt
		if len(result) == 100 {
			next = strconv.FormatInt(last, 10)
			break
		}
		var payload []byte
		if err := rows.Scan(&last, &payload, &item.ReceivedAt); err != nil {
			http.Error(w, "inbox unavailable", 503)
			return
		}
		item.Notification = payload
		result = append(result, item)
	}
	if rows.Err() != nil {
		http.Error(w, "inbox unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if paginated {
		_ = json.NewEncoder(w).Encode(struct {
			Items      []receipt `json:"items"`
			NextCursor string    `json:"next_cursor,omitempty"`
		}{result, next})
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func openInbox(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(500)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS notifications(hazard_id TEXT PRIMARY KEY,message_id TEXT NOT NULL,payload BLOB NOT NULL,received_at TEXT NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func run() error {
	token := os.Getenv("FIELD_ALERT_TOKEN")
	if token == "" {
		return errors.New("FIELD_ALERT_TOKEN is required")
	}
	state := os.Getenv("FIELD_ALERT_STATE_PATH")
	if state == "" {
		state = "/data/field-alerts.db"
	}
	db, err := openInbox(state)
	if err != nil {
		return err
	}
	defer db.Close()
	closeTrace, err := telemetry.StartTracing("field-team-receiver")
	if err != nil {
		return err
	}
	defer closeTrace()
	monitor := telemetry.New()
	latency := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "aat_field_alert_latency_seconds", Help: "Elapsed time from Aggregator raw intake to the first durable Field Team receipt.", Buckets: []float64{.1, .25, .5, 1, 2, 3, 4, 5, 8, 15, 30, 60}})
	monitor.Registry.MustRegister(latency)
	closeMetrics, err := monitor.Listen("http://127.0.0.1:8087/ready")
	if err != nil {
		return err
	}
	defer closeMetrics()
	i := inbox{db: db, key: sha256.Sum256([]byte("Bearer " + token)), latency: latency}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	server := http.Server{Addr: ":8087", Handler: monitor.Wrap(i.handler(), "/v1/notifications"), ReadHeaderTimeout: time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "field-team-receiver"))
	if err := run(); err != nil {
		slog.Error("Field Team receiver stopped", "reason", err.Error())
		os.Exit(1)
	}
}
