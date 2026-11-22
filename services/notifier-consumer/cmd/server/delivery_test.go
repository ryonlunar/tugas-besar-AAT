package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/jetstreamconsumer"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
)

type observedSink struct{ delivered atomic.Int32 }

func (s *observedSink) Apply(context.Context, contracts.Envelope, string, uint64) error {
	s.delivered.Add(1)
	return nil
}
func (s *observedSink) Quarantine(context.Context, []byte, string, string, uint64) error {
	return fmt.Errorf("unexpected quarantine")
}

// Use a disposable broker because the runner's subject filter is shared with
// the deployed system. Never run this against the application's JetStream.
func TestCriticalDeliveryRetriesBeforeAck(t *testing.T) {
	fixture := os.Getenv("AAT_DISPOSABLE_ALERT_NATS")
	if fixture == "" {
		t.Skip("AAT_DISPOSABLE_ALERT_NATS must address an isolated fixture broker")
	}
	conn, err := nats.Connect(fixture, nats.Token("fixture"), nats.Timeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	stream := "ALERT_TEST"
	if _, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{jetstreamconsumer.SubjectFilter}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	defer js.DeleteStream(stream)
	requested := make(chan struct{}, 1)
	var available atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer alert-fixture" || r.Header.Get("Idempotency-Key") == "" {
			t.Error("receiver request missing headers")
			http.Error(w, "bad request", 400)
			return
		}
		if !available.Load() {
			select {
			case requested <- struct{}{}:
			default:
			}
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	t.Setenv("FIELD_ALERT_URL", receiver.URL)
	t.Setenv("FIELD_ALERT_TOKEN", "alert-fixture")
	observed := &observedSink{}
	sink, err := newDeliverySink(observed, telemetry.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runner := &jetstreamconsumer.Runner{Config: jetstreamconsumer.Config{URL: fixture, Token: "fixture", Stream: stream, Durable: "alerts-test", AckWait: time.Second, FetchExpires: 250 * time.Millisecond, NAKDelay: 100 * time.Millisecond, MaxAckPending: 1, FetchBatch: 1, MaxDeliver: -1}, Sink: sink}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	const id = "139d202a-8385-50ef-a40f-dea40af07c1c"
	data, _ := json.Marshal(contracts.HazardEvent{HazardID: id, Source: contracts.SourceCoastal, SourceRefID: "arbitrary-reading", HazardType: contracts.HazardCoastal, Severity: contracts.SeverityAwas, AreaName: "Padang", OccurredAt: stamp, IngestedAt: stamp, Attributes: map[string]any{"critical": true}})
	payload, _ := json.Marshal(contracts.Envelope{SchemaVersion: 1, EventType: "hazard.created", MessageID: id + ":1", CorrelationID: "fixture", PublishedAt: stamp, Producer: "aggregator", Revision: 1, Data: data})
	if _, err = js.Publish("hazard.events.v1.SENSOR_PANTAI", payload); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("critical event did not reach receiver")
	}
	if observed.delivered.Load() != 0 {
		t.Fatal("failed receiver request was treated as committed")
	}
	available.Store(true)
	for ctx.Err() == nil {
		info, err := js.ConsumerInfo(stream, "alerts-test")
		if err == nil && info.AckFloor.Stream == 1 && observed.delivered.Load() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("no acknowledged redelivery after receiver recovered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
