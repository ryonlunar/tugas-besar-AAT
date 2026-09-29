package outbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

type fakeQueue struct {
	pending []store.OutboxMessage
	marked  []string
	failed  []string
	next    time.Time
}

func (q *fakeQueue) PendingOutbox(context.Context, int, time.Time) ([]store.OutboxMessage, error) {
	return q.pending, nil
}
func (q *fakeQueue) MarkPublished(_ context.Context, id string, _ time.Time) error {
	q.marked = append(q.marked, id)
	return nil
}
func (q *fakeQueue) RecordPublishFailure(_ context.Context, id, _ string, next time.Time) error {
	q.failed = append(q.failed, id)
	q.next = next
	return nil
}

type fakePublisher struct {
	err      error
	ids      []string
	subjects []string
}

func (p *fakePublisher) Publish(_ context.Context, subject string, _ []byte, id string) error {
	p.subjects = append(p.subjects, subject)
	p.ids = append(p.ids, id)
	return p.err
}

func TestRelayPublishesAndMarksOnlyAfterPublisherAck(t *testing.T) {
	queue := &fakeQueue{pending: []store.OutboxMessage{{MessageID: "event-1:1", Subject: "hazard.events.v1.PVMBG", Envelope: []byte(`{"message_id":"event-1:1"}`)}}}
	publisher := &fakePublisher{}
	relay := New(queue, publisher, 10, 500*time.Millisecond, time.Second, 2*time.Second)
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.ids) != 1 || publisher.ids[0] != "event-1:1" || len(queue.marked) != 1 || queue.marked[0] != "event-1:1" || len(queue.failed) != 0 {
		t.Fatalf("publisher=%+v queue=%+v", publisher, queue)
	}
}

func TestRelayRetainsPendingMessageAndSchedulesRetryOnPublishFailure(t *testing.T) {
	queue := &fakeQueue{pending: []store.OutboxMessage{{MessageID: "event-2:2", Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`), Attempts: 1}}}
	publisher := &fakePublisher{err: errors.New("broker unavailable")}
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	relay := New(queue, publisher, 10, 500*time.Millisecond, time.Second, 8*time.Second)
	relay.SetClock(func() time.Time { return now })
	if err := relay.DrainOnce(context.Background()); err == nil {
		t.Fatal("publish failure must be reported")
	}
	if len(queue.marked) != 0 || len(queue.failed) != 1 || queue.failed[0] != "event-2:2" {
		t.Fatalf("pending message was incorrectly marked: %+v", queue)
	}
	if queue.next.Before(now.Add(500*time.Millisecond)) || queue.next.After(now.Add(2*time.Second)) {
		t.Fatalf("retry time outside bounded backoff: %s", queue.next)
	}
}

func TestRelayLogsPublishOperationWithCorrelationAndDuration(t *testing.T) {
	queue := &fakeQueue{pending: []store.OutboxMessage{{
		MessageID: "event-3:1", Subject: "hazard.events.v1.BMKG",
		Envelope: []byte(`{"message_id":"event-3:1","correlation_id":"poll-123"}`),
	}}}
	var logs bytes.Buffer
	relay := New(queue, &fakePublisher{}, 10, 500*time.Millisecond, time.Second, 2*time.Second)
	relay.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"operation":"jetstream_publish"`, `"correlation_id":"poll-123"`, `"duration_ms"`, `"status":"success"`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("structured publish log is missing %s: %s", field, logs.String())
		}
	}
}
