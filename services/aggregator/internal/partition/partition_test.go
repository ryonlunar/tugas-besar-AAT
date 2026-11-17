package partition

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestPageOrderingUsesInstantsIncludingFractionsAndOffsets(t *testing.T) {
	events := []contracts.HazardEvent{
		{HazardID: "newer", OccurredAt: "2026-11-12T03:00:00.1Z"},
		{HazardID: "offset-a", OccurredAt: "2026-11-12T10:00:00+07:00"},
		{HazardID: "utc-b", OccurredAt: "2026-11-12T03:00:00Z"},
		{HazardID: "older", OccurredAt: "2026-11-12T02:59:59.999Z"},
	}
	readers := make([]Reader, UnitCount)
	for i, event := range events {
		readers[i] = Reader{Unit: fmt.Sprintf("shard-%d", i), Read: func(_ context.Context, _ contracts.Source, after *Position, _ int) ([]contracts.HazardEvent, error) {
			at, _ := time.Parse(time.RFC3339Nano, event.OccurredAt)
			if after != nil && (at.After(after.OccurredAt) || (at.Equal(after.OccurredAt) && event.HazardID <= after.HazardID)) {
				return nil, nil
			}
			return []contracts.HazardEvent{event}, nil
		}}
	}
	got := []string{}
	token := ""
	for i := 0; i < 10; i++ {
		page, err := ReadPage(context.Background(), readers, Query{Cursor: token, Limit: 1}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Items {
			got = append(got, event.HazardID)
		}
		if page.NextCursor == nil {
			break
		}
		token = *page.NextCursor
	}
	if strings.Join(got, ",") != "newer,offset-a,utc-b,older" {
		t.Fatalf("wrong chronological pagination: %v", got)
	}
}

func TestRegionalSkewDoesNotConcentratePlacement(t *testing.T) {
	counts := map[string]int{}
	for i := 0; i < 12000; i++ {
		unit, err := Unit(contracts.SourceBMKG, fmt.Sprintf("same-region-event-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		counts[unit]++
	}
	if len(counts) != UnitCount {
		t.Fatalf("not all units used: %v", counts)
	}
	for _, count := range counts {
		if count < 2700 || count > 3300 {
			t.Fatalf("uneven identity distribution: %v", counts)
		}
	}
}

func TestPersistedPlacementVectors(t *testing.T) {
	// These assignments are storage compatibility data. A change needs migration.
	for _, test := range []struct {
		source             contracts.Source
		ref, unit, journal string
	}{
		{contracts.SourceBMKG, "quake-1", "shard-3", "shard-0"},
		{contracts.SourcePVMBG, "volcano-1", "shard-2", "shard-1"},
		{contracts.SourceSatellite, "fire-1", "shard-2", "shard-2"},
		{contracts.SourceCoastal, "obs-1", "shard-1", "shard-3"},
	} {
		unit, err := Unit(test.source, test.ref)
		if err != nil || unit != test.unit {
			t.Fatalf("placement changed for %s/%s: %s %v", test.source, test.ref, unit, err)
		}
		journal, err := JournalUnit(test.source)
		if err != nil || journal != test.journal {
			t.Fatalf("journal home changed for %s: %s %v", test.source, journal, err)
		}
	}
}

func TestReadDeadlineAndPartialPageDoNotSkipRecoveredData(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	readers := make([]Reader, UnitCount)
	for i := range readers {
		readers[i] = Reader{Unit: fmt.Sprintf("shard-%d", i), Read: func(ctx context.Context, _ contracts.Source, _ *Position, _ int) ([]contracts.HazardEvent, error) {
			return []contracts.HazardEvent{{HazardID: fmt.Sprintf("healthy-%d", i), OccurredAt: "2026-11-12T00:00:00Z"}}, nil
		}}
	}
	readers[1].Read = func(context.Context, contracts.Source, *Position, int) ([]contracts.HazardEvent, error) {
		<-release
		return nil, nil
	}
	started := time.Now()
	page, err := ReadPage(context.Background(), readers, Query{Limit: 1}, 30*time.Millisecond)
	if err != nil || !page.Partial || page.NextCursor != nil || len(page.UnavailableUnits) != 1 || page.UnavailableUnits[0] != "shard-1" || len(page.Items) != 1 {
		t.Fatalf("incorrect partial page: %+v err=%v", page, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("slow reader blocked the whole request")
	}
}
