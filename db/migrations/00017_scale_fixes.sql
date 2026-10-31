-- +goose Up
--
-- Scale-audit follow-ups (see docs or audit report):
--
--   1. audit_log retention — mirror the outbox pattern so an admin-action trail doesn't grow
--      forever. Default retention: 2 years. Called from the worker's maintenance loop.
--   2. Drop idx_bookmark_user_list — redundant with idx_bookmark_user_recent; any query that
--      filters by list_id can use the broader index with a filter. Keeping both just doubles
--      write amplification on every bookmark insert/update.
--   3. Make idx_content_title_trgm (GIN) partial on `deleted_at IS NULL` so soft-deleted rows
--      stop bloating the index (search never shows them anyway).
--

-- 1. audit_log retention. Instant partition DROP, no vacuum debt, mirrors the outbox pattern.
-- +goose StatementBegin
CREATE FUNCTION drop_old_audit_partitions(retention_days int) RETURNS int
LANGUAGE plpgsql AS $$
DECLARE
    part record;
    upper_bound date;
    dropped int := 0;
BEGIN
    FOR part IN
        SELECT c.oid, c.relname, c.relpartbound FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'audit_log'::regclass
    LOOP
        upper_bound := (regexp_match(pg_get_expr(part.relpartbound, part.oid), 'TO \(''([^'']+)''\)'))[1]::date;
        CONTINUE WHEN upper_bound IS NULL OR upper_bound > current_date - retention_days;
        EXECUTE format('DROP TABLE %I', part.relname);
        dropped := dropped + 1;
    END LOOP;
    RETURN dropped;
END;
$$;
-- +goose StatementEnd

-- 2. Drop the redundant bookmark index.
DROP INDEX IF EXISTS idx_bookmark_user_list;

-- 3. Make the title trigram index partial. Rebuild the index (not CONCURRENTLY — this
-- migration is fine on the current size; once the table is multi-GB, future migrations should
-- opt out of the goose transaction (NO TRANSACTION directive) and use CONCURRENTLY to avoid
-- blocking writes.
DROP INDEX IF EXISTS idx_content_title_trgm;
CREATE INDEX idx_content_title_trgm ON content USING gin (title gin_trgm_ops) WHERE deleted_at IS NULL;

-- 4. sitemap_chunk.chunk widened to bigint. The chunk number is `content_id / 50_000`, and
-- content_id uses the shard-prefixed scheme introduced in migration 00011 (id = shard<<47 | seq).
-- On any shard > 0 the chunk number exceeds int4's 2.1-billion ceiling. Widening the column
-- (and the ordering columns in the dependent queries) lets the chunking math stay global and
-- URL-stable regardless of which shard a row lives on. The table has at most a few rows per
-- 50 000 URLs so the ALTER is instant.
ALTER TABLE sitemap_chunk ALTER COLUMN chunk TYPE bigint;


-- +goose Down
ALTER TABLE sitemap_chunk ALTER COLUMN chunk TYPE int;
DROP FUNCTION IF EXISTS drop_old_audit_partitions(int);

-- Restore the redundant bookmark index.
CREATE INDEX IF NOT EXISTS idx_bookmark_user_list ON bookmark (user_id, list_id, created_at DESC) WHERE list_id IS NOT NULL;

-- Restore the unconditional trigram index.
DROP INDEX IF EXISTS idx_content_title_trgm;
CREATE INDEX idx_content_title_trgm ON content USING gin (title gin_trgm_ops);
