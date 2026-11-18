package satellite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// pgState implements State over a pgx.Tx belonging to one
// sourcejournal.Session.Prepare call (see processor.go). Every method
// scopes its queries to the current transaction only -- there is no
// separate read path, so recognition state is always read consistently
// with whatever this same transaction has already written earlier in the
// same ProcessBatch call.
type pgState struct{ tx pgx.Tx }

// NewPGState wraps tx. Exported so main.go's wiring (Fase 5) can construct
// it without reaching into package-private details, even though Processor
// is the only expected caller in this package.
func NewPGState(tx pgx.Tx) State { return &pgState{tx: tx} }

func (s *pgState) Detection(ctx context.Context, key string) (StoredDetection, bool, error) {
	row := s.tx.QueryRow(ctx, `SELECT detection_key,satellite,acq_date,acq_time_text,lat_text,lon_text,latitude,longitude,
		confidence,frp_text,extra,attributes_digest,detected_at,status,coalesce(fire_id,''),first_offset,last_offset
		FROM satellite_detections WHERE detection_key=$1`, key)
	d, err := scanStoredDetection(row)
	if err == pgx.ErrNoRows {
		return StoredDetection{}, false, nil
	}
	if err != nil {
		return StoredDetection{}, false, err
	}
	return d, true, nil
}

func (s *pgState) Neighbors(ctx context.Context, d StoredDetection) ([]StoredDetection, error) {
	cellLat, cellLon := Cell(d.Latitude, d.Longitude)
	span := NeighborLonSpan(d.Latitude)
	rows, err := s.tx.Query(ctx, `SELECT detection_key,satellite,acq_date,acq_time_text,lat_text,lon_text,latitude,longitude,
		confidence,frp_text,extra,attributes_digest,detected_at,status,coalesce(fire_id,''),first_offset,last_offset
		FROM satellite_detections
		WHERE status='active' AND detection_key<>$1
		AND cell_lat BETWEEN $2 AND $3 AND cell_lon BETWEEN $4 AND $5
		AND detected_at BETWEEN $6 AND $7`,
		d.Key, cellLat-1, cellLat+1, cellLon-span, cellLon+span,
		d.DetectedAt.Add(-360*time.Minute), d.DetectedAt.Add(360*time.Minute))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStoredDetections(rows)
}

func (s *pgState) Members(ctx context.Context, fireIDs []string) ([]StoredDetection, error) {
	if len(fireIDs) == 0 {
		return nil, nil
	}
	rows, err := s.tx.Query(ctx, `SELECT detection_key,satellite,acq_date,acq_time_text,lat_text,lon_text,latitude,longitude,
		confidence,frp_text,extra,attributes_digest,detected_at,status,coalesce(fire_id,''),first_offset,last_offset
		FROM satellite_detections WHERE fire_id = ANY($1)`, fireIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStoredDetections(rows)
}

func (s *pgState) Fires(ctx context.Context, ids []string) (map[string]FireMeta, error) {
	out := map[string]FireMeta{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.tx.Query(ctx, `SELECT fire_id,anchor_key,anchor_at,active FROM satellite_fires WHERE fire_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, anchorKey string
		var anchorAt time.Time
		var active bool
		if err := rows.Scan(&id, &anchorKey, &anchorAt, &active); err != nil {
			return nil, err
		}
		out[id] = FireMeta{Anchor: Anchor{Key: anchorKey, At: anchorAt}, Active: active}
	}
	return out, rows.Err()
}

func (s *pgState) FireRecords(ctx context.Context, ids []string) (map[string]FireRecord, error) {
	out := map[string]FireRecord{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.tx.Query(ctx, `SELECT fire_id,anchor_key,anchor_at,active,member_count,content_digest,settled FROM satellite_fires WHERE fire_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rec FireRecord
		var anchorKey string
		var anchorAt time.Time
		if err := rows.Scan(&rec.FireID, &anchorKey, &anchorAt, &rec.Active, &rec.MemberCount, &rec.ContentDigest, &rec.Settled); err != nil {
			return nil, err
		}
		rec.Anchor = Anchor{Key: anchorKey, At: anchorAt}
		out[rec.FireID] = rec
	}
	return out, rows.Err()
}

func (s *pgState) Watermark(ctx context.Context, satellite string) (time.Time, bool, error) {
	var t time.Time
	err := s.tx.QueryRow(ctx, `SELECT max_detected_at FROM satellite_watermarks WHERE satellite=$1`, satellite).Scan(&t)
	if err == pgx.ErrNoRows {
		return time.Time{}, false, nil
	}
	return t, err == nil, err
}

func (s *pgState) Apply(ctx context.Context, c Changes) error {
	for _, d := range c.Detections {
		extra, err := json.Marshal(d.Extra)
		if err != nil {
			return err
		}
		cellLat, cellLon := Cell(d.Latitude, d.Longitude)
		fireID := &d.FireID
		if d.FireID == "" {
			fireID = nil
		}
		_, err = s.tx.Exec(ctx, `INSERT INTO satellite_detections(detection_key,satellite,acq_date,acq_time_text,lat_text,lon_text,
			latitude,longitude,cell_lat,cell_lon,confidence,frp_text,extra,attributes_digest,detected_at,status,fire_id,first_offset,last_offset)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
			ON CONFLICT (detection_key) DO UPDATE SET
			confidence=excluded.confidence, frp_text=excluded.frp_text, extra=excluded.extra,
			attributes_digest=excluded.attributes_digest, status=excluded.status, fire_id=excluded.fire_id,
			last_offset=excluded.last_offset`,
			d.Key, d.Satellite, d.AcqDate, d.AcqTimeText, d.LatText, d.LonText, d.Latitude, d.Longitude,
			cellLat, cellLon, d.Confidence, d.FRPText, extra, d.AttributesDigest, d.DetectedAt, d.Status, fireID, d.FirstOffset, d.LastOffset)
		if err != nil {
			return err
		}
	}
	for _, f := range c.Fires {
		_, err := s.tx.Exec(ctx, `INSERT INTO satellite_fires(fire_id,anchor_key,anchor_at,active,member_count,content_digest,settled)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (fire_id) DO UPDATE SET active=excluded.active, member_count=excluded.member_count,
			content_digest=excluded.content_digest, settled=excluded.settled`,
			f.FireID, f.Anchor.Key, f.Anchor.At, f.Active, f.MemberCount, f.ContentDigest, f.Settled)
		if err != nil {
			return err
		}
	}
	for satellite, w := range c.Watermarks {
		_, err := s.tx.Exec(ctx, `INSERT INTO satellite_watermarks(satellite,max_detected_at) VALUES($1,$2)
			ON CONFLICT (satellite) DO UPDATE SET max_detected_at=excluded.max_detected_at WHERE excluded.max_detected_at>satellite_watermarks.max_detected_at`,
			satellite, w)
		if err != nil {
			return err
		}
	}
	for _, r := range c.Rejects {
		_, err := s.tx.Exec(ctx, `INSERT INTO satellite_rejects(event_offset,batch_key,reason,raw) VALUES($1,$2,$3,$4)
			ON CONFLICT (event_offset) DO NOTHING`, r.Offset, r.BatchKey, r.Reason, r.Raw)
		if err != nil {
			return err
		}
	}
	for name, delta := range c.CounterDeltas {
		_, err := s.tx.Exec(ctx, `INSERT INTO satellite_counters(name,value) VALUES($1,$2)
			ON CONFLICT (name) DO UPDATE SET value=satellite_counters.value+excluded.value`, name, delta)
		if err != nil {
			return err
		}
	}
	return nil
}

// rowScanner covers both pgx.Row (QueryRow) and pgx.Rows (Query) so
// scanStoredDetection can be shared between Detection's single-row lookup
// and the multi-row Neighbors/Members queries.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanStoredDetection(row rowScanner) (StoredDetection, error) {
	var d StoredDetection
	var extra []byte
	if err := row.Scan(&d.Key, &d.Satellite, &d.AcqDate, &d.AcqTimeText, &d.LatText, &d.LonText, &d.Latitude, &d.Longitude,
		&d.Confidence, &d.FRPText, &extra, &d.AttributesDigest, &d.DetectedAt, &d.Status, &d.FireID, &d.FirstOffset, &d.LastOffset); err != nil {
		return StoredDetection{}, err
	}
	if len(extra) > 0 {
		if err := json.Unmarshal(extra, &d.Extra); err != nil {
			return StoredDetection{}, err
		}
	}
	return d, nil
}

func scanStoredDetections(rows pgx.Rows) ([]StoredDetection, error) {
	var out []StoredDetection
	for rows.Next() {
		d, err := scanStoredDetection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
