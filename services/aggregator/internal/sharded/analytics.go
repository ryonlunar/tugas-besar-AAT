package sharded

import (
	"context"
	"sort"
	"time"
)

type AnalyticsPoint struct {
	Bucket   time.Time `json:"bucket"`
	Category string    `json:"category"`
	Count    int64     `json:"count"`
}

type Analytics struct {
	Series           []AnalyticsPoint `json:"series"`
	BySeverity       map[string]int64 `json:"by_severity"`
	ByArea           map[string]int64 `json:"by_area"`
	Partial          bool             `json:"partial"`
	UnavailableUnits []string         `json:"unavailable_units"`
}

type analyticsGroup struct {
	kind, label string
	bucket      time.Time
}

// Analytics queries each primary once for a consistent view within that unit.
func (s *Store) Analytics(ctx context.Context, from, to time.Time, step string) Analytics {
	ctx, cancel := context.WithTimeout(ctx, 450*time.Millisecond)
	defer cancel()
	const statement = `
 WITH active AS MATERIALIZED (
  SELECT occurred_at,hazard_type,severity,area_name,attributes ? 'tsunami_warnings' AS tsunami
  FROM hazard_events WHERE NOT retracted AND occurred_at >= $1 AND occurred_at < $2
 )
 SELECT 'series', category, date_bin($3::interval,occurred_at,'1970-01-01 00:00:00+00'::timestamptz),count(*)
 FROM active CROSS JOIN LATERAL (
  SELECT hazard_type AS category UNION ALL SELECT 'TSUNAMI' WHERE hazard_type='SEISMIC' AND tsunami
 ) categories GROUP BY 2,3
 UNION ALL SELECT 'severity',severity,'1970-01-01 00:00:00+00'::timestamptz,count(*) FROM active GROUP BY severity
 UNION ALL SELECT 'area',area_name,'1970-01-01 00:00:00+00'::timestamptz,count(*) FROM active GROUP BY area_name
 `
	groups, missing := fanout(ctx, s, func(ctx context.Context, u unit) (map[analyticsGroup]int64, error) {
		rows, err := u.readDB.QueryContext(ctx, statement, from, to, step)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		groups := map[analyticsGroup]int64{}
		for rows.Next() {
			var group analyticsGroup
			var count int64
			if err := rows.Scan(&group.kind, &group.label, &group.bucket, &count); err != nil {
				return nil, err
			}
			group.bucket = group.bucket.UTC()
			groups[group] = count
		}
		return groups, rows.Err()
	})
	totals := map[analyticsGroup]int64{}
	for _, group := range groups {
		for key, count := range group {
			totals[key] += count
		}
	}
	result := Analytics{Series: []AnalyticsPoint{}, BySeverity: map[string]int64{}, ByArea: map[string]int64{}, Partial: len(missing) > 0, UnavailableUnits: missing}
	for key, count := range totals {
		switch key.kind {
		case "series":
			result.Series = append(result.Series, AnalyticsPoint{key.bucket, key.label, count})
		case "severity":
			result.BySeverity[key.label] = count
		case "area":
			result.ByArea[key.label] = count
		}
	}
	sort.Slice(result.Series, func(i, j int) bool {
		a, b := result.Series[i], result.Series[j]
		if a.Bucket.Equal(b.Bucket) {
			return a.Category < b.Category
		}
		return a.Bucket.Before(b.Bucket)
	})
	return result
}
