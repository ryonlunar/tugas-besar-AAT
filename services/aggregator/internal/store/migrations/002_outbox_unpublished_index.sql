-- Supports the fail-closed pending-outbox capacity check with an index-only count.
CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox (message_id)
    WHERE published_at IS NULL;
