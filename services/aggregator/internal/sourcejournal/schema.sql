CREATE TABLE IF NOT EXISTS ingest_sources (
    source text PRIMARY KEY CHECK (source IN ('BMKG','PVMBG','SATELIT','SENSOR_PANTAI')),
    generation bigint NOT NULL CHECK (generation > 0),
    cursor text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS ingest_records (
    source text NOT NULL REFERENCES ingest_sources(source),
    record_key text NOT NULL,
    kind text NOT NULL,
    payload bytea NOT NULL,
    received_at timestamptz NOT NULL,
    correlation_id text NOT NULL,
    PRIMARY KEY (source, record_key)
);

-- Additive upgrade of the initial raw-journal prototype.
CREATE SEQUENCE IF NOT EXISTS ingest_records_sequence;
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS sequence bigint NOT NULL DEFAULT nextval('ingest_records_sequence');
ALTER SEQUENCE ingest_records_sequence OWNED BY ingest_records.sequence;
CREATE UNIQUE INDEX IF NOT EXISTS ingest_records_sequence_idx ON ingest_records(sequence);
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS ordering_key text NOT NULL DEFAULT '';
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS auxiliary bytea NOT NULL DEFAULT '';
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'retrying' CHECK (status IN ('retrying','parked','done'));
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0;
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT 'awaiting_processing';
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS traceparent text NOT NULL DEFAULT '';
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS tracestate text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS ingest_records_pending_idx ON ingest_records(source,sequence) WHERE status <> 'done';
CREATE TABLE IF NOT EXISTS ingest_snapshot_heads (
    source text NOT NULL,
    ordering_key text NOT NULL,
    digest text NOT NULL,
    PRIMARY KEY(source,ordering_key)
);
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS replay_requested boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS ingest_records_entity_pending_idx ON ingest_records(source,ordering_key,sequence) WHERE status<>'done' AND ordering_key<>'';
CREATE SEQUENCE IF NOT EXISTS ingest_plan_sequence;
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS normalized_plan jsonb;
ALTER TABLE ingest_records ADD COLUMN IF NOT EXISTS plan_sequence bigint;
