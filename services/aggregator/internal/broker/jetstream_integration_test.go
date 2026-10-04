package broker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestJetStreamPublisherCreatesContractStreamAndDeduplicates(t *testing.T) {
	url, token := os.Getenv("AAT_TEST_NATS_URL"), os.Getenv("AAT_TEST_NATS_TOKEN")
	if url == "" || token == "" {
		t.Skip("AAT_TEST_NATS_URL and AAT_TEST_NATS_TOKEN are not set; JetStream integration test not run")
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	stream := "AAT_TEST_" + suffix
	subject := "aat.test." + suffix
	durable := "test-" + suffix
	defer func() {
		conn, err := nats.Connect(url, nats.Token(token), nats.Timeout(time.Second))
		if err != nil {
			return
		}
		defer conn.Close()
		js, err := conn.JetStream()
		if err != nil {
			return
		}
		_ = js.DeleteConsumer(stream, durable)
		_ = js.DeleteStream(stream)
	}()
	publisher, err := NewJetStreamPublisher(JetStreamConfig{
		URL: url, Token: token, Stream: stream, Subjects: []string{subject},
		MaxAge: time.Minute, MaxBytes: 1 << 20, MaxMsgs: 100, MaxMsgSize: 64 << 10,
		DuplicateWindow: 30 * time.Second, ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := []byte(`{"message_id":"runtime-test:1"}`)
	for range 2 {
		if err := publisher.Publish(ctx, subject, payload, "runtime-test:1"); err != nil {
			t.Fatal(err)
		}
	}

	publisher.mu.Lock()
	info, err := publisher.js.StreamInfo(stream)
	publisher.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("stream contains %d messages after duplicate publish; want 1", info.State.Msgs)
	}

	conn, err := nats.Connect(url, nats.Token(token), nats.Timeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Drain()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := js.PullSubscribe(subject, durable, nats.BindStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Unsubscribe()
	messages, err := subscription.Fetch(1, nats.MaxWait(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || string(messages[0].Data) != string(payload) || messages[0].Header.Get(nats.MsgIdHdr) != "runtime-test:1" {
		t.Fatalf("unexpected JetStream delivery: count=%d subject=%s payload_matches=%t", len(messages), subject, len(messages) == 1 && string(messages[0].Data) == string(payload))
	}
	if err := messages[0].Ack(); err != nil {
		t.Fatal(fmt.Errorf("ack test delivery: %w", err))
	}
	if err := conn.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestJetStreamMessageLimitIncludesHeaders(t *testing.T) {
	url, token := os.Getenv("AAT_TEST_NATS_URL"), os.Getenv("AAT_TEST_NATS_TOKEN")
	if url == "" || token == "" {
		t.Skip("disposable NATS is required")
	}
	stream := "AAT_SIZE_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	subject := "aat.size." + stream
	const limit = 1024
	const id = "size-boundary:1"
	// NATS/1.0 CRLF + Nats-Msg-Id: <id> CRLF + final CRLF.
	headerBytes := len("NATS/1.0\r\nNats-Msg-Id: " + id + "\r\n\r\n")
	payload := make([]byte, limit-headerBytes)
	publisher, err := NewJetStreamPublisher(JetStreamConfig{URL: url, Token: token, Stream: stream, Subjects: []string{subject}, MaxAge: time.Minute, MaxBytes: 1 << 20, MaxMsgs: 10, MaxMsgSize: limit, DuplicateWindow: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := publisher.Publish(ctx, subject, payload, id); err != nil {
		t.Fatalf("message at the exact wire limit was rejected: %v", err)
	}
	defer publisher.js.DeleteStream(stream)
	if err := publisher.Publish(ctx, subject, append(payload, 0), id); err == nil || err.Error() != "NATS message violates configured envelope limits" {
		t.Fatalf("header overflow was not rejected locally: %v", err)
	}
	info, err := publisher.js.StreamInfo(stream)
	if err != nil || info.State.Msgs != 1 {
		t.Fatalf("unexpected broker contents: info=%+v error=%v", info, err)
	}
}
