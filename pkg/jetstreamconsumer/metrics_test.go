package jetstreamconsumer

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type statsStub struct{ err error }

func (s statsStub) Stats(context.Context) (int64, int64, error) { return 12, 2, s.err }
func TestConsumerMetricsOmitCountsOnStorageFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		var err error
		if failed {
			err = errors.New("storage unavailable")
		}
		registry := prometheus.NewRegistry()
		registry.MustRegister(StoreMetrics(statsStub{err}))
		families, gatherErr := registry.Gather()
		if gatherErr != nil {
			t.Fatal(gatherErr)
		}
		metrics := map[string]float64{}
		for _, family := range families {
			metrics[family.GetName()] = family.Metric[0].Gauge.GetValue()
		}
		expected := 1.0
		if failed {
			expected = 0
		}
		if metrics["aat_consumer_store_collection_success"] != expected {
			t.Fatal("wrong collection status")
		}
		_, exists := metrics["aat_consumer_inbox_messages"]
		if failed && exists {
			t.Fatal("failed read exposed a misleading count")
		}
		if !failed && (metrics["aat_consumer_inbox_messages"] != 12 || metrics["aat_consumer_quarantine_messages"] != 2) {
			t.Fatal("wrong persisted counts")
		}
	}
}
