package store

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

type metricsCollector struct {
	store                                        *Store
	success, state, lastSuccess, pending, oldest *prometheus.Desc
}

// MetricsCollector reads aggregate state through the owning service's read pool.
// Failed collections omit state rather than reporting an empty queue or fresh data.
func (s *Store) MetricsCollector() prometheus.Collector {
	return &metricsCollector{s,
		prometheus.NewDesc("aat_store_collection_success", "Whether aggregate storage metrics could be read.", nil, nil),
		prometheus.NewDesc("aat_source_state", "One-hot source state: fresh, stale, or unavailable.", []string{"source", "state"}, nil),
		prometheus.NewDesc("aat_source_last_success_timestamp_seconds", "Last successful ingestion cycle; absent before the first success.", []string{"source"}, nil),
		prometheus.NewDesc("aat_outbox_pending", "Events awaiting publication, including scheduled retries.", nil, nil),
		prometheus.NewDesc("aat_outbox_oldest_pending_timestamp_seconds", "Creation time of oldest pending event; zero when empty.", nil, nil),
	}
}
func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.success, c.state, c.lastSuccess, c.pending, c.oldest} {
		ch <- d
	}
}
func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sources := []mapper.Source{mapper.SourceBMKG, mapper.SourcePVMBG}
	if c.store.unit == "shard-3" {
		sources = append(sources, contracts.SourceCoastal)
	}
	states, err := c.store.SourceStates(ctx, sources, time.Now().UTC())
	var pending int64
	var oldest float64
	if err == nil {
		err = c.store.readDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM min(created_at)),0) FROM outbox WHERE published_at IS NULL`).Scan(&pending, &oldest)
	}
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(pending))
	ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, oldest)
	for _, source := range sources {
		state := states[source]
		for _, name := range []string{"fresh", "stale", "unavailable"} {
			value := 0.0
			if state.Status == name {
				value = 1
			}
			ch <- prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, value, string(source), name)
		}
		if state.LastSuccessAt != nil {
			ch <- prometheus.MustNewConstMetric(c.lastSuccess, prometheus.GaugeValue, float64(state.LastSuccessAt.UnixNano())/1e9, string(source))
		}
	}
}
