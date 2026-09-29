CREATE TABLE IF NOT EXISTS hazard_events (
    hazard_id uuid PRIMARY KEY,
    source text NOT NULL CHECK (source IN ('BMKG', 'PVMBG')),
    source_ref_id text NOT NULL CHECK (length(source_ref_id) > 0),
    hazard_type text NOT NULL CHECK (hazard_type IN ('SEISMIC', 'VOLCANIC')),
    severity text NOT NULL CHECK (severity IN ('NORMAL', 'WASPADA', 'SIAGA', 'AWAS')),
    area_name text NOT NULL CHECK (length(area_name) > 0),
    latitude double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    occurred_at timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL,
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(attributes) = 'object'),
    revision bigint NOT NULL CHECK (revision >= 1),
    content_hash text NOT NULL CHECK (length(content_hash) = 64),
    UNIQUE (source, source_ref_id),
    CHECK ((source = 'BMKG' AND hazard_type = 'SEISMIC') OR (source = 'PVMBG' AND hazard_type = 'VOLCANIC'))
);

CREATE INDEX IF NOT EXISTS hazard_events_source_time_idx ON hazard_events (source, occurred_at DESC, hazard_id ASC);
CREATE INDEX IF NOT EXISTS hazard_events_time_idx ON hazard_events (occurred_at DESC, hazard_id ASC);

CREATE TABLE IF NOT EXISTS hazard_versions (
    hazard_id uuid NOT NULL REFERENCES hazard_events(hazard_id),
    revision bigint NOT NULL CHECK (revision >= 1),
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (hazard_id, revision)
);

CREATE TABLE IF NOT EXISTS source_state (
    source text PRIMARY KEY CHECK (source IN ('BMKG', 'PVMBG')),
    last_attempt_at timestamptz,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    failure_since timestamptz,
    last_error_code text
);

CREATE TABLE IF NOT EXISTS outbox (
    message_id text PRIMARY KEY,
    hazard_id uuid NOT NULL REFERENCES hazard_events(hazard_id),
    revision bigint NOT NULL CHECK (revision >= 1),
    subject text NOT NULL CHECK (subject IN ('hazard.events.v1.BMKG', 'hazard.events.v1.PVMBG')),
    envelope jsonb NOT NULL CHECK (jsonb_typeof(envelope) = 'object'),
    created_at timestamptz NOT NULL,
    published_at timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    last_error_code text,
    UNIQUE (hazard_id, revision)
);

CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox (next_attempt_at, created_at) WHERE published_at IS NULL;
