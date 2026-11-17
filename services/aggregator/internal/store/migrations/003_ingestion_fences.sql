CREATE TABLE ingestion_fences (
    source text PRIMARY KEY,
    generation bigint NOT NULL CHECK (generation > 0)
);
