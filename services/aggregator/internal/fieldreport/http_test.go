package fieldreport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const body = `{"observed_at":"2026-11-12T03:00:00Z","latitude":-0.9492,"longitude":100.3543,"description":"Air naik"}`

type fakeRepository struct {
	accept   func(Submission) (Receipt, error)
	received []Submission
	block    chan struct{}
	entered  chan struct{}
}

func (f *fakeRepository) AcceptFieldReport(_ context.Context, s Submission) (Receipt, error) {
	f.received = append(f.received, s)
	if f.block != nil {
		f.entered <- struct{}{}
		<-f.block
	}
	return f.accept(s)
}
func (f *fakeRepository) FieldReport(context.Context, string, string) (Status, bool, error) {
	return Status{}, false, nil
}
func (f *fakeRepository) ProcessFieldReports(context.Context, string, int, Processor) (int, error) {
	return 0, nil
}

func post(h http.Handler, key, client, idem, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/field-reports", strings.NewReader(payload))
	req.Header.Set("X-Internal-Key", key)
	req.Header.Set("X-Client-ID", client)
	req.Header.Set("Idempotency-Key", idem)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func TestIntakeResponses(t *testing.T) {
	repo := &fakeRepository{accept: func(s Submission) (Receipt, error) {
		switch s.IdempotencyKey {
		case "conflict":
			return Receipt{}, ErrConflict
		case "down":
			return Receipt{}, ErrUnavailable
		case "retry":
			return Receipt{ReportID: "id-1", Status: StatusAccepted, Replayed: true}, nil
		}
		return Receipt{ReportID: "id-1", Status: StatusAccepted}, nil
	}}
	h := NewHandler(repo, "secret", 4)
	cases := []struct {
		name, key, client, idem, payload string
		want                             int
	}{
		{"wrong credential", "wrong", "field-team", "k", body, 401},
		{"missing client", "secret", "", "k", body, 400},
		{"missing key", "secret", "field-team", "", body, 400},
		{"invalid body", "secret", "field-team", "k", `{"latitude":1}`, 400},
		{"oversized", "secret", "field-team", "k", strings.Repeat(" ", 40<<10), 413},
		{"conflict", "secret", "field-team", "conflict", body, 409},
		{"storage down", "secret", "field-team", "down", body, 503},
		{"accepted", "secret", "field-team", "ok", body, 202},
	}
	for _, tc := range cases {
		if res := post(h, tc.key, tc.client, tc.idem, tc.payload); res.Code != tc.want {
			t.Errorf("%s: status=%d want %d %s", tc.name, res.Code, tc.want, res.Body.String())
		}
	}
	res := post(h, "secret", "field-team", "retry", body)
	if res.Code != 202 || res.Header().Get("Idempotent-Replayed") != "true" || !strings.Contains(res.Body.String(), `"report_id":"id-1"`) {
		t.Fatalf("replayed response=%d %v %s", res.Code, res.Header(), res.Body.String())
	}
	last := repo.received[len(repo.received)-1]
	if string(last.Payload) != body || last.ClientID != "field-team" || last.CorrelationID == "" {
		t.Fatalf("stored submission=%+v", last)
	}
	if res := post(h, "secret", "field-team", "down", body); res.Header().Get("Retry-After") == "" {
		t.Fatal("storage failure gave no retry guidance")
	}
}

func TestIntakeHasItsOwnAdmissionLimit(t *testing.T) {
	repo := &fakeRepository{block: make(chan struct{}), entered: make(chan struct{}), accept: func(Submission) (Receipt, error) {
		return Receipt{ReportID: "id", Status: StatusAccepted}, nil
	}}
	h := NewHandler(repo, "secret", 1)
	done := make(chan int)
	go func() { done <- post(h, "secret", "field-team", "a", body).Code }()
	<-repo.entered
	if res := post(h, "secret", "field-team", "b", body); res.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status=%d", res.Code)
	}
	close(repo.block)
	if code := <-done; code != 202 {
		t.Fatalf("first request status=%d", code)
	}
}

func TestProcessRejectsInvalidStoredBytes(t *testing.T) {
	if outcome := Process(Pending{Payload: []byte(body)}); outcome.Status != StatusProcessed || outcome.Report.Description != "Air naik" {
		t.Fatalf("valid outcome=%+v", outcome)
	}
	if outcome := Process(Pending{Payload: []byte(`{"x":1}`)}); outcome.Status != StatusRejected || outcome.Reason == "" {
		t.Fatalf("invalid outcome=%+v", outcome)
	}
}
