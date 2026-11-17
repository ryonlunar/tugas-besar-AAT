package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

type pagedFake struct {
	fakeRepository
	page partition.Page
	err  error
}

func (f *pagedFake) Page(context.Context, string, string, int, int) (partition.Page, error) {
	return f.page, f.err
}

func TestPartialPageRetainsAvailabilityMetadata(t *testing.T) {
	repository := &pagedFake{page: partition.Page{Items: []contracts.HazardEvent{{HazardID: "healthy-item", Attributes: map[string]any{"private": "value"}}}, Partial: true, UnavailableUnits: []string{"shard-1"}}}
	handler := NewHandler(repository, "test-key", time.Now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards", nil)
	req.Header.Set("X-Internal-Key", "test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var body struct {
		Data []map[string]any
		Meta struct {
			Partial          bool     `json:"partial"`
			UnavailableUnits []string `json:"unavailable_units"`
			NextCursor       *string  `json:"next_cursor"`
		}
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || !body.Meta.Partial || len(body.Meta.UnavailableUnits) != 1 || body.Meta.NextCursor != nil || len(body.Data) != 1 {
		t.Fatalf("partial response: %d %s", response.Code, response.Body.String())
	}
	if _, leaked := body.Data[0]["attributes"]; leaked {
		t.Fatal("summary leaked raw attributes")
	}
}

func TestInvalidCursorIsAClientError(t *testing.T) {
	repository := &pagedFake{err: partition.ErrInvalidCursor}
	for _, query := range []string{"cursor=invalid", "cursor=token&offset=0", "cursor="} {
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards?"+query, nil)
		req.Header.Set("X-Internal-Key", "test-key")
		response := httptest.NewRecorder()
		NewHandler(repository, "test-key", time.Now).ServeHTTP(response, req)
		if response.Code != 400 {
			t.Fatalf("query %q: %d", query, response.Code)
		}
	}
}
