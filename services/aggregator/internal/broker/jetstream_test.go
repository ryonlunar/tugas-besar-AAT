package broker

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestJetStreamStreamFilterCoversOnlyDeclaredProducerSubjects(t *testing.T) {
	config := JetStreamConfig{
		URL: "nats://127.0.0.1:4222", Token: "test-token", Stream: "HAZARD_EVENTS",
		Subjects:       []string{"hazard.events.v1.BMKG", "hazard.events.v1.PVMBG"},
		StreamSubjects: []string{"hazard.events.v1.*"},
		MaxAge:         24 * time.Hour, MaxBytes: 1 << 20, MaxMsgs: 100, MaxMsgSize: 65536,
		DuplicateWindow: 2 * time.Minute,
	}
	publisher, err := NewJetStreamPublisher(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.config.StreamSubjects) != 1 || publisher.config.StreamSubjects[0] != "hazard.events.v1.*" {
		t.Fatalf("stream subjects=%v", publisher.config.StreamSubjects)
	}
	if err := publisher.Publish(context.Background(), "hazard.events.v1.OTHER", []byte(`{}`), "message-1"); err == nil {
		t.Fatal("publisher accepted a subject outside its producer contract")
	}
}

func TestJetStreamRejectsProducerSubjectOutsideStreamFilter(t *testing.T) {
	config := JetStreamConfig{
		URL: "nats://127.0.0.1:4222", Token: "test-token", Stream: "HAZARD_EVENTS",
		Subjects:       []string{"hazard.events.v1.BMKG"},
		StreamSubjects: []string{"hazard.events.*"},
		MaxAge:         time.Hour, MaxBytes: 1 << 20, MaxMsgs: 100, MaxMsgSize: 65536,
		DuplicateWindow: time.Minute,
	}
	if _, err := NewJetStreamPublisher(config); err == nil {
		t.Fatal("invalid stream filter was accepted")
	}
}

func TestJetStreamPublishConnectionSetupRespectsCallerDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-release // emulate a connected but unresponsive NATS server
	}()
	defer func() {
		close(release)
		_ = listener.Close()
		<-serverDone
	}()

	publisher, err := NewJetStreamPublisher(JetStreamConfig{
		URL: "nats://" + listener.Addr().String(), Token: "synthetic-test-token", Stream: "HAZARD_EVENTS",
		Subjects: []string{"hazard.events.v1.BMKG"},
		MaxAge:   time.Hour, MaxBytes: 1 << 20, MaxMsgs: 100, MaxMsgSize: 65536,
		DuplicateWindow: time.Minute, ConnectTimeout: 750 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = publisher.Publish(ctx, "hazard.events.v1.BMKG", []byte(`{}`), "event-1")
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("publish to an unresponsive NATS server unexpectedly succeeded")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("connection setup ignored caller deadline: elapsed=%s, err=%v", elapsed, err)
	}
}
