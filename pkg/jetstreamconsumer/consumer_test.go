package jetstreamconsumer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

type sinkStub struct{}

func (sinkStub) Apply(context.Context, contracts.Envelope, string, uint64) error  { return nil }
func (sinkStub) Quarantine(context.Context, []byte, string, string, uint64) error { return nil }

func TestConsumerReadinessRecoversAndCancellationInterruptsFetch(t *testing.T) {
	for _, expiry := range []time.Duration{100 * time.Millisecond, 30 * time.Second} {
		t.Run(expiry.String(), func(t *testing.T) { testConsumerLifecycle(t, expiry) })
	}
}

func testConsumerLifecycle(t *testing.T, expiry time.Duration) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := Config{URL: "nats://" + listener.Addr().String(), Token: "test-token", Stream: "HAZARD_EVENTS", Durable: "test-consumer", AckWait: time.Second, FetchExpires: expiry, NAKDelay: time.Second, MaxAckPending: 1, FetchBatch: 1, MaxDeliver: -1}
	fetching := make(chan net.Conn, 2)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveIdleBroker(conn, cfg, fetching)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &Runner{Config: cfg, Sink: sinkStub{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	first := waitForFetch(t, fetching)
	if !runner.Ready() {
		t.Fatal("bound consumer is not ready")
	}
	if expiry < time.Second {
		time.Sleep(250 * time.Millisecond)
		if !runner.Ready() {
			t.Fatal("ordinary idle fetch expiration disconnected the consumer")
		}
	}
	first.Close()
	waitUntil(t, func() bool { return !runner.Ready() })
	second := waitForFetch(t, fetching)
	defer second.Close()
	if !runner.Ready() {
		t.Fatal("consumer did not become ready after rebinding")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil || runner.Ready() {
			t.Fatalf("shutdown: error=%v ready=%t", err, runner.Ready())
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the 30-second fetch")
	}
}

func waitForFetch(t *testing.T, fetching <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case conn := <-fetching:
		return conn
	case <-time.After(4 * time.Second):
		t.Fatal("consumer did not bind and fetch")
		return nil
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

// This protocol stub permits real client connections but leaves fetches idle.
func serveIdleBroker(conn net.Conn, cfg Config, fetching chan<- net.Conn) {
	defer conn.Close()
	fmt.Fprint(conn, "INFO {\"server_id\":\"test\",\"version\":\"2.10.0\",\"proto\":1,\"max_payload\":1048576}\r\n")
	reader := bufio.NewReader(conn)
	subscriptions := map[string]string{}
	announced := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "PING":
			fmt.Fprint(conn, "PONG\r\n")
		case "SUB":
			subscriptions[parts[1]] = parts[len(parts)-1]
		case "PUB":
			length, _ := strconv.Atoi(parts[len(parts)-1])
			if _, err := io.CopyN(io.Discard, reader, int64(length+2)); err != nil {
				return
			}
			if strings.Contains(parts[1], ".MSG.NEXT.") {
				if !announced {
					fetching <- conn
					announced = true
				}
				continue
			}
			if !strings.Contains(parts[1], ".CONSUMER.INFO.") {
				return
			}
			info := nats.ConsumerInfo{Stream: cfg.Stream, Name: cfg.Durable, Created: time.Now(), Config: nats.ConsumerConfig{Durable: cfg.Durable, FilterSubject: SubjectFilter, DeliverPolicy: nats.DeliverAllPolicy, AckPolicy: nats.AckExplicitPolicy, AckWait: cfg.AckWait, MaxAckPending: cfg.MaxAckPending, MaxDeliver: cfg.MaxDeliver, ReplayPolicy: nats.ReplayInstantPolicy}}
			payload, _ := json.Marshal(info)
			reply := parts[2]
			for pattern, id := range subscriptions {
				if pattern == reply || strings.HasPrefix(reply, strings.TrimSuffix(pattern, "*")) {
					fmt.Fprintf(conn, "MSG %s %s %d\r\n%s\r\n", reply, id, len(payload), payload)
					break
				}
			}
		}
	}
}

func TestUnconfirmedACKKeepsEventCorrelationAndMessageID(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/examples/hazard-envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := contracts.ParseEnvelope(raw, "hazard.events.v1.BMKG", "")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	runner := &Runner{Config: Config{Durable: "test-consumer"}, Sink: sinkStub{}, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	// An unbound message cannot confirm an ACK, even after its side effect commits.
	runner.process(context.Background(), &nats.Msg{Subject: "hazard.events.v1.BMKG", Data: raw})
	decoder := json.NewDecoder(&logs)
	var committed, ack map[string]any
	if err := decoder.Decode(&committed); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&ack); err != nil {
		t.Fatal(err)
	}
	if committed["msg"] != "message committed" || ack["operation"] != "ack" || ack["outcome"] != "failed" || ack["duration_ms"] == nil || ack["correlation_id"] != envelope.CorrelationID || ack["message_id"] != envelope.MessageID {
		t.Fatalf("unconfirmed ACK lost its event context: %+v", ack)
	}
}
