package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

func TestUnsafeInputsAreParkedWithoutPanicking(t *testing.T) {
	processor := DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations()))
	for _, raw := range [][]byte{[]byte(`{"report_id":`), {0xff}, []byte(strings.Repeat("[", 129) + strings.Repeat("]", 129)), []byte(strings.Repeat("x", (1<<20)+1))} {
		m := sourcejournal.Message{RawRecord: contracts.RawRecord{Kind: "volcanic_report", Payload: raw, ReceivedAt: time.Now()}}
		if _, err := processor(context.Background(), nil, m); err == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	if excessiveDepth([]byte(`{"quoted":"[[[\\\"[[["}`), 2) {
		t.Fatal("brackets inside strings counted as nesting")
	}
}
