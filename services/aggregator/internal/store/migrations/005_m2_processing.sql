ALTER TABLE hazard_events DROP CONSTRAINT hazard_events_source_check;
ALTER TABLE hazard_events DROP CONSTRAINT hazard_events_hazard_type_check;
ALTER TABLE hazard_events DROP CONSTRAINT hazard_events_check;
ALTER TABLE hazard_events ADD CHECK (source IN ('BMKG','PVMBG','SATELIT','SENSOR_PANTAI'));
ALTER TABLE hazard_events ADD CHECK ((source='BMKG' AND hazard_type='SEISMIC') OR (source='PVMBG' AND hazard_type='VOLCANIC') OR (source='SATELIT' AND hazard_type='WILDFIRE') OR (source='SENSOR_PANTAI' AND hazard_type='COASTAL'));
ALTER TABLE source_state DROP CONSTRAINT source_state_source_check;
ALTER TABLE source_state ADD CHECK (source IN ('BMKG','PVMBG','SATELIT','SENSOR_PANTAI'));
ALTER TABLE outbox DROP CONSTRAINT outbox_subject_check;
ALTER TABLE outbox ADD CHECK (subject IN ('hazard.events.v1.BMKG','hazard.events.v1.PVMBG','hazard.events.v1.SATELIT','hazard.events.v1.SENSOR_PANTAI'));
ALTER TABLE hazard_events ADD COLUMN retracted boolean NOT NULL DEFAULT false;
CREATE INDEX hazard_events_ingested_idx ON hazard_events(ingested_at,hazard_id) WHERE NOT retracted;
CREATE TABLE processed_inputs (
    source text NOT NULL,
    input_id text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(source,input_id)
);
CREATE TABLE canonical_progress (
    source text NOT NULL,
    source_ref_id text NOT NULL,
    plan_sequence bigint NOT NULL,
    PRIMARY KEY(source,source_ref_id)
);
