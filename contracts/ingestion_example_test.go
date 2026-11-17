package contracts_test

import (
	"context"
	"fmt"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// A finite fixture uses the same interface as a live source adapter. Offset 8
// identifies the message; updates to the same detection receive different keys.
type fixtureAdapter struct{}

func (fixtureAdapter) Run(ctx context.Context, resume string, commit func(context.Context, contracts.Batch) error) error {
	return commit(ctx, contracts.Batch{From: resume, Cursor: "9", Records: []contracts.RawRecord{{
		Key: "offset-8", Kind: "detection", Payload: []byte(`{"latitude":-2.5,"longitude":113.8}`),
		ReceivedAt: time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC), CorrelationID: "fixture-8",
	}}})
}

func ExampleSourceAdapter() {
	var source contracts.SourceAdapter = fixtureAdapter{}
	err := source.Run(context.Background(), "8", func(_ context.Context, batch contracts.Batch) error {
		fmt.Printf("persist %s; advance cursor %s -> %s only after persistence succeeds\n", batch.Records[0].Key, batch.From, batch.Cursor)
		return nil
	})
	if err != nil {
		panic(err)
	}
	// Output: persist offset-8; advance cursor 8 -> 9 only after persistence succeeds
}
