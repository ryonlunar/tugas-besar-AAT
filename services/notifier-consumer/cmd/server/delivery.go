package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/jetstreamconsumer"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
)

type deliverySink struct {
	jetstreamconsumer.Sink
	endpoint, token string
	client          http.Client
	observe         func(string, string, error, time.Duration)
}

func newDeliverySink(sink jetstreamconsumer.Sink, monitor *telemetry.Monitor) (jetstreamconsumer.Sink, error) {
	endpoint := os.Getenv("FIELD_ALERT_URL")
	if endpoint == "" {
		return sink, nil
	}
	base, err := url.Parse(endpoint)
	token := os.Getenv("FIELD_ALERT_TOKEN")
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" {
		return nil, errors.New("invalid Field Team receiver URL or token")
	}
	return &deliverySink{Sink: sink, endpoint: base.JoinPath("/v1/notifications").String(), token: token, observe: monitor.ObserveOperation, client: http.Client{
		Timeout: 750 * time.Millisecond, Transport: telemetry.HTTPTransport(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (s *deliverySink) Apply(ctx context.Context, event contracts.Envelope, subject string, delivery uint64) error {
	if event.Source == string(contracts.SourceCoastal) && event.EventType != "hazard.retracted" {
		var data struct {
			Attributes struct {
				Critical bool `json:"critical"`
			} `json:"attributes"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		if data.Attributes.Critical {
			started := time.Now()
			err := s.send(ctx, event)
			s.observe("field_alert_delivery", "field-team-receiver", err, time.Since(started))
			if err != nil {
				slog.WarnContext(ctx, "Field Team delivery failed", "message_id", event.MessageID, "correlation_id", event.CorrelationID, "duration_ms", time.Since(started).Milliseconds(), "reason", err.Error())
				return err
			}
		}
	}
	return s.Sink.Apply(ctx, event, subject, delivery)
}

func (s *deliverySink) send(ctx context.Context, event contracts.Envelope) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Idempotency-Key", event.MessageID)
	req.Header.Set("X-Correlation-ID", event.CorrelationID)
	reply, err := s.client.Do(req)
	if err != nil {
		return errors.New("receiver request failed")
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusNoContent {
		return fmt.Errorf("receiver HTTP %d", reply.StatusCode)
	}
	return nil
}
