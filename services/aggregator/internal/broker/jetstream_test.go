package broker

import (
	"context"
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
