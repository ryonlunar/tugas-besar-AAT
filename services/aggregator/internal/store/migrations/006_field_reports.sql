CREATE TABLE field_reports (
    report_id uuid PRIMARY KEY,
    client_id text NOT NULL,
    idempotency_key text NOT NULL,
    request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    payload bytea NOT NULL,
    correlation_id text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    status text NOT NULL DEFAULT 'accepted' CHECK (status IN ('accepted','processed','rejected')),
    attempts integer NOT NULL DEFAULT 0,
    reason text,
    observed_at timestamptz,
    latitude double precision,
    longitude double precision,
    description text,
    processed_at timestamptz,
    processed_by text,
    UNIQUE (client_id, idempotency_key)
);
CREATE INDEX field_reports_pending_idx ON field_reports(received_at, report_id) WHERE status = 'accepted';
