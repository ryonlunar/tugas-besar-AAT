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
