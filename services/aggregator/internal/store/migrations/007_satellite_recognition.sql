CREATE TABLE satellite_detections (
  detection_key text PRIMARY KEY,
  satellite text NOT NULL,
  detected_at timestamptz NOT NULL,
  acq_date text NOT NULL,
  acq_time_text text NOT NULL,
  lat_text text NOT NULL,
  lon_text text NOT NULL,
  latitude double precision NOT NULL,
  longitude double precision NOT NULL,
  cell_lat integer NOT NULL,
  cell_lon integer NOT NULL,
  confidence text NOT NULL,
  frp_text text NOT NULL,
  extra jsonb NOT NULL DEFAULT '{}'::jsonb,
  attributes_digest text NOT NULL,
  status text NOT NULL CHECK (status IN ('active','low','late','retracted')),
  fire_id text,
  first_offset text NOT NULL,
  last_offset text NOT NULL
);
CREATE INDEX satellite_detections_grid_idx ON satellite_detections(cell_lat,cell_lon,detected_at) WHERE status='active';
CREATE INDEX satellite_detections_fire_idx ON satellite_detections(fire_id) WHERE fire_id IS NOT NULL;

CREATE TABLE satellite_fires (
  fire_id text PRIMARY KEY,
  anchor_key text NOT NULL,
  anchor_at timestamptz NOT NULL,
  active boolean NOT NULL,
  member_count integer NOT NULL,
  content_digest text NOT NULL,
  settled boolean NOT NULL DEFAULT false
);

CREATE TABLE satellite_watermarks (
  satellite text PRIMARY KEY,
  max_detected_at timestamptz NOT NULL
);

CREATE TABLE satellite_rejects (
  event_offset text PRIMARY KEY,
  batch_key text NOT NULL,
  reason text NOT NULL,
  raw bytea NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE satellite_counters (
  name text PRIMARY KEY,
  value bigint NOT NULL
);
