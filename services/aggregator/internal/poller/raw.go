package poller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// ReadRecords preserves each original JSON record before mapping. If the array
// itself cannot be decoded, its complete bounded body becomes a failed input.
func (c *PVMBGClient) ReadRecords(ctx context.Context) ([]contracts.RawRecord, error) {
	if strings.TrimSpace(c.token) == "" || c.maxBody <= 0 {
		return nil, errors.New("PVMBG client is not configured")
	}
	if err := validateBaseURL(c.baseURL); err != nil {
		return nil, err
	}
	correlation, err := newCorrelationID()
	if err != nil {
		return nil, err
	}
	body, err := fetchBody(withCorrelationID(ctx, correlation), c.client, c.baseURL+"/volcanic-reports", c.maxBody, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.token) }, "/volcanic-reports")
	if err != nil {
		return nil, err
	}
	return rawRecords(body, "volcanic_report", "report_id", correlation), nil
}

func (c *BMKGClient) ReadRecords(ctx context.Context) ([]contracts.RawRecord, error) {
	if strings.TrimSpace(c.key) == "" || c.maxBody <= 0 {
		return nil, errors.New("BMKG client is not configured")
	}
	if err := validateBaseURL(c.baseURL); err != nil {
		return nil, err
	}
	correlation, err := newCorrelationID()
	if err != nil {
		return nil, err
	}
	ctx = withCorrelationID(ctx, correlation)
	type result struct {
		body []byte
		err  error
		path string
	}
	results := make(chan result, 2)
	for _, path := range []string{"/seismic-events", "/tsunami-warnings"} {
		go func(path string) {
			body, err := fetchBody(ctx, c.client, c.baseURL+path, c.maxBody, func(r *http.Request) { r.Header.Set("X-BMKG-Key", c.key) }, path)
			results <- result{body, err, path}
		}(path)
	}
	bodies := map[string][]byte{}
	for range 2 {
		r := <-results
		if r.err != nil {
			return nil, r.err
		}
		bodies[r.path] = r.body
	}
	events := rawRecords(bodies["/seismic-events"], "seismic_event", "event_id", correlation)
	warnings := rawRecords(bodies["/tsunami-warnings"], "tsunami_warning", "warning_id", correlation)
	byEvent := map[string][]json.RawMessage{}
	for _, warning := range warnings {
		if id, err := rawStringField(warning.Payload, "related_event_id"); err == nil {
			byEvent[id] = append(byEvent[id], json.RawMessage(warning.Payload))
		}
	}
	for i := range events {
		id, _ := rawStringField(events[i].Payload, "event_id")
		if matches := byEvent[id]; len(matches) > 0 {
			events[i].Auxiliary, _ = json.Marshal(matches)
		}
	}
	return append(events, warnings...), nil
}

func rawRecords(body []byte, kind, idField, correlation string) []contracts.RawRecord {
	records, err := decodeArray(body, kind)
	if err != nil {
		records = [][]byte{body}
		kind = "invalid_" + kind + "_response"
	}
	result := make([]contracts.RawRecord, 0, len(records))
	for _, raw := range records {
		ref, _ := rawStringField(raw, idField)
		key := contracts.RawKey(kind+":"+ref, raw)
		ordering := kind + ":" + ref
		// Unknown identities must not block unrelated valid entities.
		if ref == "" {
			ordering = kind + ":" + key
		}
		result = append(result, contracts.RawRecord{Key: key, Kind: kind, Payload: raw, ReceivedAt: time.Now().UTC(), CorrelationID: correlation, OrderingKey: ordering, Snapshot: true})
	}
	return result
}
