package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func TestHistoryAuditOutputsOnlySelectedSnapshots(t *testing.T) {
	repo := &fakeHistoryStore{
		hazardID: "f4a741a9-3206-45d6-a733-6f2d3eb1f91c",
		versions: map[int64]json.RawMessage{
			1: json.RawMessage(`{"revision":1,"attributes":{"confidence_level":0.4}}`),
			2: json.RawMessage(`{"revision":2,"attributes":{"confidence_level":0.9}}`),
			3: json.RawMessage(`{"revision":3,"attributes":{"confidence_level":1.0}}`),
		},
	}
	var output bytes.Buffer
	if err := runHistoryAudit(context.Background(), repo, []string{"--source", "PVMBG", "--source-ref-id", "volcano-sim-01", "--revisions", "1,2"}, &output); err != nil {
		t.Fatal(err)
	}
	var got struct {
		HazardID    string                  `json:"hazard_id"`
		Source      string                  `json:"source"`
		SourceRefID string                  `json:"source_ref_id"`
		Migrations  []store.MigrationStatus `json:"migrations"`
		Versions    []struct {
			Revision int64           `json:"revision"`
			Data     json.RawMessage `json:"data"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode audit output: %v", err)
	}
	if got.HazardID != repo.hazardID || got.Source != "PVMBG" || got.SourceRefID != "volcano-sim-01" {
		t.Fatalf("unexpected audit identity: %+v", got)
	}
	if len(got.Migrations) != 1 || got.Migrations[0].Version != "001_canonical_store.sql" {
		t.Fatalf("migration ledger missing: %+v", got.Migrations)
	}
	if len(got.Versions) != 2 || got.Versions[0].Revision != 1 || got.Versions[1].Revision != 2 {
		t.Fatalf("audit returned unselected versions: %+v", got.Versions)
	}
	if repo.lookupSource != mapper.SourcePVMBG || repo.lookupRef != "volcano-sim-01" {
		t.Fatalf("lookup used %s/%s", repo.lookupSource, repo.lookupRef)
	}
}

func TestHistoryAuditRejectsInvalidSourceBeforeLookup(t *testing.T) {
	repo := &fakeHistoryStore{}
	var output bytes.Buffer
	err := runHistoryAudit(context.Background(), repo, []string{"--source", "OTHER", "--source-ref-id", "x", "--revisions", "1"}, &output)
	if err == nil {
		t.Fatal("invalid source was accepted")
	}
	if repo.lookups != 0 {
		t.Fatalf("invalid input reached store: lookups=%d", repo.lookups)
	}
}

type fakeHistoryStore struct {
	hazardID     string
	versions     map[int64]json.RawMessage
	lookupSource mapper.Source
	lookupRef    string
	lookups      int
}

func (f *fakeHistoryStore) FindHazardID(_ context.Context, source mapper.Source, ref string) (string, bool, error) {
	f.lookups++
	f.lookupSource, f.lookupRef = source, ref
	return f.hazardID, f.hazardID != "", nil
}

func (f *fakeHistoryStore) GetVersion(_ context.Context, _ string, revision int64) (json.RawMessage, bool, error) {
	data, ok := f.versions[revision]
	return data, ok, nil
}

func (f *fakeHistoryStore) MigrationStatus(context.Context) ([]store.MigrationStatus, error) {
	return []store.MigrationStatus{{Version: "001_canonical_store.sql", Checksum: "test-checksum"}}, nil
}
