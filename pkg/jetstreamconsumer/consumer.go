// Package jetstreamconsumer contains only the shared broker delivery mechanics.
// Each application owns its own durable name and storage side effects.
package jetstreamconsumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

const SubjectFilter = "hazard.events.v1.*"

var ErrConsumerContractMismatch = errors.New("existing durable consumer does not match declared configuration")

type Config struct {
	URL, Token, Stream, Durable           string
	AckWait, FetchExpires, NAKDelay       time.Duration
	MaxAckPending, FetchBatch, MaxDeliver int
}

type Sink interface {
	Apply(context.Context, contracts.Envelope, string, uint64) error
	Quarantine(context.Context, []byte, string, string, uint64) error
}

type Runner struct {
	Config     Config
	Sink       Sink
	Logger     *slog.Logger
	ready      atomic.Bool
	connection atomic.Pointer[nats.Conn]
}

func (r *Runner) Ready() bool {
	conn := r.connection.Load()
	return r.ready.Load() && conn != nil && conn.IsConnected()
}

func (r *Runner) Run(ctx context.Context) error {
	if r.Sink == nil || r.Config.URL == "" || r.Config.Token == "" || r.Config.Stream == "" || r.Config.Durable == "" || r.Config.AckWait <= 0 || r.Config.FetchExpires <= 0 || r.Config.NAKDelay <= 0 || r.Config.MaxAckPending < 1 || r.Config.FetchBatch < 1 || r.Config.FetchBatch > r.Config.MaxAckPending || r.Config.MaxDeliver == 0 || r.Config.MaxDeliver < -1 {
		return errors.New("consumer configuration is incomplete")
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	for ctx.Err() == nil {
		err := r.consume(ctx)
		r.ready.Store(false)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, ErrConsumerContractMismatch) {
			return err
		}
		r.Logger.Warn("consumer disconnected", "durable", r.Config.Durable, "reason", disconnectReason(err))
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (r *Runner) consume(ctx context.Context) error {
	conn, err := nats.Connect(r.Config.URL, nats.Token(r.Config.Token), nats.Name(r.Config.Durable), nats.Timeout(time.Second), nats.NoReconnect())
	if err != nil {
		return fmt.Errorf("connect NATS: %w", err)
	}
	r.connection.Store(conn)
	defer func() {
		r.ready.Store(false)
		r.connection.Store(nil)
		conn.Close()
	}()
	js, err := conn.JetStream()
	if err != nil {
		return fmt.Errorf("open JetStream: %w", err)
	}
	if err := ensureConsumer(ctx, js, r.Config); err != nil {
		return err
	}
	sub, err := js.PullSubscribe(SubjectFilter, r.Config.Durable, nats.Bind(r.Config.Stream, r.Config.Durable), nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("bind durable: %w", err)
	}
	defer sub.Unsubscribe()
	r.ready.Store(true)
	r.Logger.Info("consumer ready", "durable", r.Config.Durable, "stream", r.Config.Stream)
	for ctx.Err() == nil {
		fetchCtx, cancel := context.WithTimeout(ctx, r.Config.FetchExpires)
		messages, err := sub.Fetch(r.Config.FetchBatch, nats.Context(fetchCtx))
		cancel()
		if errors.Is(err, nats.ErrTimeout) || (errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil) {
			continue
		}
		if err != nil {
			return fmt.Errorf("fetch messages: %w", err)
		}
		for _, message := range messages {
			if ctx.Err() != nil {
				return nil
			}
			r.process(ctx, message)
		}
	}
	return nil
}

func ensureConsumer(ctx context.Context, js nats.JetStreamContext, cfg Config) error {
	desired := &nats.ConsumerConfig{Durable: cfg.Durable, FilterSubject: SubjectFilter, DeliverPolicy: nats.DeliverAllPolicy, AckPolicy: nats.AckExplicitPolicy, AckWait: cfg.AckWait, MaxAckPending: cfg.MaxAckPending, MaxDeliver: cfg.MaxDeliver, ReplayPolicy: nats.ReplayInstantPolicy}
	info, err := js.ConsumerInfo(cfg.Stream, cfg.Durable, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		_, err = js.AddConsumer(cfg.Stream, desired, nats.Context(ctx))
		return err
	}
	if err != nil {
		return err
	}
	actual := info.Config
	if actual.Durable != desired.Durable || actual.FilterSubject != desired.FilterSubject || actual.DeliverPolicy != desired.DeliverPolicy || actual.AckPolicy != desired.AckPolicy || actual.AckWait != desired.AckWait || actual.MaxAckPending != desired.MaxAckPending || actual.MaxDeliver != desired.MaxDeliver || actual.ReplayPolicy != desired.ReplayPolicy {
		return ErrConsumerContractMismatch
	}
	return nil
}

func (r *Runner) process(ctx context.Context, message *nats.Msg) {
	delivery := uint64(1)
	if meta, err := message.Metadata(); err == nil {
		delivery = meta.NumDelivered
	}
	headerID := message.Header.Get(nats.MsgIdHdr)
	envelope, err := contracts.ParseEnvelope(message.Data, message.Subject, headerID)
	if err != nil {
		// Validation errors are poison messages; the quarantine write must be durable
		// before ACK so a crash cannot silently drop the message.
		err = r.Sink.Quarantine(ctx, message.Data, message.Subject, "INVALID_ENVELOPE", delivery)
		if err == nil {
			r.Logger.Warn("message quarantined", "durable", r.Config.Durable, "subject", message.Subject, "delivery", delivery)
			r.ack(message)
		} else {
			r.nak(message, "QUARANTINE_WRITE_FAILED")
		}
		return
	}
	if err := r.Sink.Apply(ctx, envelope, message.Subject, delivery); err != nil {
		r.nak(message, "SIDE_EFFECT_FAILED")
		return
	}
	r.Logger.Info("message committed", "durable", r.Config.Durable, "message_id", envelope.MessageID, "correlation_id", envelope.CorrelationID, "revision", envelope.Revision, "delivery", delivery)
	r.ack(message)
}

func (r *Runner) ack(message *nats.Msg) {
	if err := message.AckSync(nats.AckWait(2 * time.Second)); err != nil {
		r.Logger.Warn("message ACK unconfirmed", "durable", r.Config.Durable)
	}
}

func (r *Runner) nak(message *nats.Msg, reason string) {
	r.Logger.Error("message not committed", "durable", r.Config.Durable, "reason", reason)
	if err := message.NakWithDelay(r.Config.NAKDelay); err != nil {
		r.Logger.Warn("message NAK unconfirmed", "durable", r.Config.Durable)
	}
}

func disconnectReason(err error) string {
	if errors.Is(err, nats.ErrStreamNotFound) {
		return "STREAM_NOT_FOUND"
	}
	if errors.Is(err, nats.ErrAuthorization) {
		return "NATS_AUTH_FAILED"
	}
	return "BROKER_UNAVAILABLE"
}
