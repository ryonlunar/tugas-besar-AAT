ALTER TABLE outbox ADD COLUMN traceparent text NOT NULL DEFAULT '';
ALTER TABLE outbox ADD COLUMN tracestate text NOT NULL DEFAULT '';
