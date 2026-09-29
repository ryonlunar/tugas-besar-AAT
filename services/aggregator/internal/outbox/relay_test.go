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
	pending     []store.OutboxMessage
	marked      []string
	failed      []string
	failureCode []string
	next        time.Time
	nextByID    map[string]time.Time
	markErr     error
	recordErr   error
}

func (q *fakeQueue) PendingOutbox(_ context.Context, limit int, now time.Time) ([]store.OutboxMessage, error) {
	messages := make([]store.OutboxMessage, 0, len(q.pending))
	for _, message := range q.pending {
		if next, ok := q.nextByID[message.MessageID]; ok && now.Before(next) {
			continue
		}
		messages = append(messages, message)
		if limit > 0 && len(messages) >= limit {
			break
		}
	}
	return messages, nil
}
func (q *fakeQueue) MarkPublished(_ context.Context, id string, _ time.Time) error {
	if q.markErr != nil {
		return q.markErr
	}
	q.marked = append(q.marked, id)
	return nil
}
func (q *fakeQueue) RecordPublishFailure(_ context.Context, id, code string, next time.Time) error {
	if q.recordErr != nil {
		return q.recordErr
	}
	q.failed = append(q.failed, id)
	q.failureCode = append(q.failureCode, code)
	q.next = next
	if q.nextByID == nil {
		q.nextByID = make(map[string]time.Time)
	}
	q.nextByID[id] = next
	return nil
}

type fakePublisher struct {
	err              error
	errorsByID       map[string]error
	ids              []string
	subjects         []string
	timeoutRemaining time.Duration
}

func (p *fakePublisher) Publish(ctx context.Context, subject string, _ []byte, id string) error {
	p.subjects = append(p.subjects, subject)
	p.ids = append(p.ids, id)
	if deadline, ok := ctx.Deadline(); ok {
		p.timeoutRemaining = time.Until(deadline)
	}
	if err := p.errorsByID[id]; err != nil {
		return err
	}
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

func TestRelayUsesConfiguredPublishTimeout(t *testing.T) {
	const configured = 1500 * time.Millisecond
	queue := &fakeQueue{pending: []store.OutboxMessage{{MessageID: "event-timeout:1", Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`)}}}
	publisher := &fakePublisher{}
	relay := New(queue, publisher, 10, 500*time.Millisecond, time.Second, 2*time.Second)
	if err := relay.SetPublishTimeout(configured); err != nil {
		t.Fatal(err)
	}
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.timeoutRemaining <= 0 || publisher.timeoutRemaining > configured || publisher.timeoutRemaining < configured-250*time.Millisecond {
		t.Fatalf("publisher context has %s remaining; configured timeout is %s", publisher.timeoutRemaining, configured)
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

func TestRelayRetriesAckPersistenceFailureOnlyAfterBackoffAndRecovers(t *testing.T) {
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	queue := &fakeQueue{
		pending: []store.OutboxMessage{{MessageID: "event-ack:1", HazardID: "event-ack", Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`), Attempts: 1}},
		markErr: errors.New("transient store failure"),
	}
	publisher := &fakePublisher{}
	relay := New(queue, publisher, 10, 500*time.Millisecond, time.Second, 8*time.Second)
	relay.SetClock(func() time.Time { return now })
	if err := relay.DrainOnce(context.Background()); err == nil {
		t.Fatal("ack persistence failure must be reported")
	}
	if len(queue.failed) != 1 || queue.failed[0] != "event-ack:1" || queue.failureCode[0] != "ACK_PERSIST_FAILED" || !queue.next.After(now) {
		t.Fatalf("ack failure retry was not durably scheduled: %+v", queue)
	}
	if len(publisher.ids) != 1 {
		t.Fatalf("initial publish count=%d; want 1", len(publisher.ids))
	}
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("not-yet-due message should be skipped without error: %v", err)
	}
	if len(publisher.ids) != 1 {
		t.Fatalf("message was hot-replayed before backoff: publish count=%d", len(publisher.ids))
	}
	now = queue.next.Add(time.Nanosecond)
	queue.markErr = nil
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("retry should recover after ack persistence works: %v", err)
	}
	if len(publisher.ids) != 2 || len(queue.marked) != 1 || queue.marked[0] != "event-ack:1" {
		t.Fatalf("retry did not recover: publishes=%v marked=%v", publisher.ids, queue.marked)
	}
}

func TestRelayLocallyThrottlesWhenAckRetryMetadataCannotPersist(t *testing.T) {
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	queue := &fakeQueue{
		pending:   []store.OutboxMessage{{MessageID: "event-ack-local:1", HazardID: "event-ack-local", Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`)}},
		markErr:   errors.New("store unavailable"),
		recordErr: errors.New("store unavailable"),
	}
	publisher := &fakePublisher{}
	relay := New(queue, publisher, 10, 500*time.Millisecond, time.Second, 8*time.Second)
	relay.SetClock(func() time.Time { return now })
	if err := relay.DrainOnce(context.Background()); err == nil {
		t.Fatal("ack and retry-metadata failures must be reported")
	}
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("locally deferred message should be skipped without error: %v", err)
	}
	if len(publisher.ids) != 1 {
		t.Fatalf("message was hot-replayed when retry metadata could not persist: publishes=%v", publisher.ids)
	}
	now = now.Add(time.Hour)
	queue.markErr = nil
	queue.recordErr = nil
	if err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("message should retry after local throttle expires: %v", err)
	}
	if len(publisher.ids) != 2 || len(queue.marked) != 1 {
		t.Fatalf("message did not resume after local throttle: publishes=%v marked=%v", publisher.ids, queue.marked)
	}
}

type contextWaitPublisher struct{}

func (contextWaitPublisher) Publish(ctx context.Context, _ string, _ []byte, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRelaySchedulesPublishRetryFromObservedFailureTime(t *testing.T) {
	queue := &fakeQueue{pending: []store.OutboxMessage{{MessageID: "event-timeout:1", Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`)}}}
	relay := New(queue, contextWaitPublisher{}, 10, 500*time.Millisecond, 200*time.Millisecond, 2*time.Second)
	if err := relay.SetPublishTimeout(250 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := relay.DrainOnce(context.Background()); err == nil {
		t.Fatal("deadline failure must be reported")
	}
	finishedAt := time.Now().UTC()
	if !queue.next.After(finishedAt.Add(50 * time.Millisecond)) {
		t.Fatalf("retry time %s is not based on the observed publish failure at %s", queue.next, finishedAt)
	}
}

func TestRelayDoesNotPublishLaterHazardRevisionAfterEarlierFailure(t *testing.T) {
	queue := &fakeQueue{pending: []store.OutboxMessage{
		{MessageID: "hazard-a:1", HazardID: "hazard-a", Revision: 1, Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`)},
		{MessageID: "hazard-a:2", HazardID: "hazard-a", Revision: 2, Subject: "hazard.events.v1.BMKG", Envelope: []byte(`{}`)},
		{MessageID: "hazard-b:1", HazardID: "hazard-b", Revision: 1, Subject: "hazard.events.v1.PVMBG", Envelope: []byte(`{}`)},
	}}
	publisher := &fakePublisher{errorsByID: map[string]error{"hazard-a:1": errors.New("broker unavailable")}}
	relay := New(queue, publisher, 10, 500*time.Millisecond, time.Second, 2*time.Second)

	if err := relay.DrainOnce(context.Background()); err == nil {
		t.Fatal("publish failure must be reported")
	}
	if strings.Join(publisher.ids, ",") != "hazard-a:1,hazard-b:1" {
		t.Fatalf("published IDs=%v; later revision of blocked hazard must be skipped while unrelated hazard proceeds", publisher.ids)
	}
	if strings.Join(queue.marked, ",") != "hazard-b:1" || strings.Join(queue.failed, ",") != "hazard-a:1" {
		t.Fatalf("unexpected outbox transitions: %+v", queue)
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
