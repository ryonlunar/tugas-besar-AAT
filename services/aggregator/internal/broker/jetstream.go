package broker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type JetStreamConfig struct {
	URL             string
	Token           string
	Stream          string
	Subjects        []string // concrete subjects the producer is allowed to publish
	StreamSubjects  []string // durable stream filter; may contain NATS wildcards
	MaxAge          time.Duration
	MaxBytes        int64
	MaxMsgs         int64
	MaxMsgSize      int32
	DuplicateWindow time.Duration
	ConnectTimeout  time.Duration
}

type JetStreamPublisher struct {
	mu     sync.Mutex
	config JetStreamConfig
	conn   *nats.Conn
	js     nats.JetStreamContext
}

func NewJetStreamPublisher(config JetStreamConfig) (*JetStreamPublisher, error) {
	if config.URL == "" || config.Token == "" || config.Stream == "" || len(config.Subjects) == 0 || config.MaxAge <= 0 || config.MaxBytes <= 0 || config.MaxMsgs <= 0 || config.MaxMsgSize <= 0 || config.DuplicateWindow < 0 {
		return nil, errors.New("NATS JetStream configuration is incomplete")
	}
	if len(config.StreamSubjects) == 0 {
		config.StreamSubjects = append([]string(nil), config.Subjects...)
	}
	for _, subject := range config.Subjects {
		matched := false
		for _, filter := range config.StreamSubjects {
			if subjectMatchesFilter(subject, filter) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, errors.New("NATS stream filters do not include every declared producer subject")
		}
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = time.Second
	}
	return &JetStreamPublisher{config: config}, nil
}

func (p *JetStreamPublisher) Publish(ctx context.Context, subject string, payload []byte, messageID string) error {
	if len(payload) > int(p.config.MaxMsgSize) || messageID == "" {
		return errors.New("NATS message violates configured envelope limits")
	}
	allowed := false
	for _, candidate := range p.config.Subjects {
		if candidate == subject {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("NATS subject is outside the configured producer contract")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnectedLocked(); err != nil {
		return errors.New("NATS JetStream is unavailable")
	}
	msg := nats.NewMsg(subject)
	msg.Data = append([]byte(nil), payload...)
	msg.Header.Set(nats.MsgIdHdr, messageID)
	if _, err := p.js.PublishMsg(msg, nats.Context(ctx)); err != nil {
		return errors.New("JetStream publish was not acknowledged")
	}
	return nil
}

func (p *JetStreamPublisher) ensureConnectedLocked() error {
	if p.conn != nil && !p.conn.IsClosed() && p.js != nil {
		return nil
	}
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
		p.js = nil
	}
	conn, err := nats.Connect(p.config.URL, nats.Token(p.config.Token), nats.Name("aat-aggregator-outbox"), nats.Timeout(p.config.ConnectTimeout))
	if err != nil {
		return err
	}
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return err
	}
	p.conn, p.js = conn, js
	if err := p.ensureStreamLocked(); err != nil {
		conn.Close()
		p.conn = nil
		p.js = nil
		return err
	}
	return nil
}

func (p *JetStreamPublisher) ensureStreamLocked() error {
	desired := nats.StreamConfig{
		Name:       p.config.Stream,
		Subjects:   append([]string(nil), p.config.StreamSubjects...),
		Retention:  nats.LimitsPolicy,
		Storage:    nats.FileStorage,
		Replicas:   1,
		MaxAge:     p.config.MaxAge,
		MaxBytes:   p.config.MaxBytes,
		MaxMsgs:    p.config.MaxMsgs,
		MaxMsgSize: p.config.MaxMsgSize,
		Duplicates: p.config.DuplicateWindow,
		Discard:    nats.DiscardNew,
	}
	info, err := p.js.StreamInfo(desired.Name)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = p.js.AddStream(&desired)
		if err != nil {
			return fmt.Errorf("create JetStream stream: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect JetStream stream: %w", err)
	}
	actual := info.Config
	if actual.Name != desired.Name || actual.Retention != desired.Retention || actual.Storage != desired.Storage || actual.Replicas != desired.Replicas || actual.MaxAge != desired.MaxAge || actual.MaxBytes != desired.MaxBytes || actual.MaxMsgs != desired.MaxMsgs || actual.MaxMsgSize != desired.MaxMsgSize || actual.Duplicates != desired.Duplicates || actual.Discard != desired.Discard || !sameStrings(actual.Subjects, desired.Subjects) {
		return errors.New("existing JetStream stream does not match the declared contract")
	}
	return nil
}

func subjectMatchesFilter(subject, filter string) bool {
	actualTokens := strings.Split(subject, ".")
	filterTokens := strings.Split(filter, ".")
	actualIndex := 0
	for _, token := range filterTokens {
		if token == ">" {
			return actualIndex < len(actualTokens)
		}
		if actualIndex >= len(actualTokens) {
			return false
		}
		if token != "*" && token != actualTokens[actualIndex] {
			return false
		}
		actualIndex++
	}
	return actualIndex == len(actualTokens)
}

func sameStrings(left, right []string) bool {
	l := append([]string(nil), left...)
	r := append([]string(nil), right...)
	sort.Strings(l)
	sort.Strings(r)
	if len(l) != len(r) {
		return false
	}
	for i := range l {
		if l[i] != r[i] {
			return false
		}
	}
	return true
}

func (p *JetStreamPublisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		p.conn.Drain()
		p.conn.Close()
		p.conn = nil
		p.js = nil
	}
}
