package jetstreamconsumer

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

type StatsReader interface {
	Stats(context.Context) (int64, int64, error)
}
type storeMetrics struct {
	state                      StatsReader
	success, inbox, quarantine *prometheus.Desc
}

func StoreMetrics(state StatsReader) prometheus.Collector {
	return &storeMetrics{state,
		prometheus.NewDesc("aat_consumer_store_collection_success", "Whether aggregate consumer storage metrics could be read.", nil, nil),
		prometheus.NewDesc("aat_consumer_inbox_messages", "Unique committed valid messages retained in the consumer inbox.", nil, nil),
		prometheus.NewDesc("aat_consumer_quarantine_messages", "Unique invalid messages durably quarantined.", nil, nil),
	}
}
func (c *storeMetrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.success
	ch <- c.inbox
	ch <- c.quarantine
}
func (c *storeMetrics) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	inbox, quarantine, err := c.state.Stats(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.inbox, prometheus.GaugeValue, float64(inbox))
	ch <- prometheus.MustNewConstMetric(c.quarantine, prometheus.GaugeValue, float64(quarantine))
}
