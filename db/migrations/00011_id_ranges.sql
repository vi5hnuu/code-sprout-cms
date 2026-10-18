-- Ids that stay unique across shards and regions, and safe in every client.
--
-- Every table keys on a bigint identity. One database makes that trivially unique; several
-- (app-level shards, a second region, merging an import) would hand out the same numbers. So
-- each database owns a disjoint id range: id = shard << 47 | sequence, for shards 0..63. That
-- is the scheme Citus itself applies to identity columns of distributed tables, so the data
-- moves to Citus or to separate databases without renumbering anything.
--
-- 47 bits per shard is 1.4 × 10^14 ids per table per shard, and the largest possible id
-- (63 << 47 | 2^47 - 1) is 2^53 - 1: every id survives a JavaScript number (a double) exactly,
-- so web clients never round an id. Sequences stop with an error at their range's end rather
-- than spill into the next shard's.
--
-- This database is shard 0: existing ids are already in range and nothing is renumbered. A new
-- shard runs `SELECT assign_id_range(<n>)` once after migrating. Later migrations that add an
-- identity column call `SELECT assign_id_range(current_id_shard())` so the new sequence joins
-- the range (TestIdentitySequencesStayInShardRange fails otherwise).

-- +goose Up
CREATE TABLE id_shard (
    singleton bool PRIMARY KEY DEFAULT true CHECK (singleton),
    shard     int  NOT NULL CHECK (shard BETWEEN 0 AND 63)
);

-- +goose StatementBegin
CREATE FUNCTION current_id_shard() RETURNS int LANGUAGE sql STABLE AS $$
    SELECT shard FROM id_shard
$$;

CREATE FUNCTION assign_id_range(shard int) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    lo  bigint;
    hi  bigint;
    seq record;
    cur bigint;
BEGIN
    IF shard < 0 OR shard > 63 THEN
        RAISE EXCEPTION 'shard must be between 0 and 63, got %', shard;
    END IF;
    lo := (shard::bigint << 47) + 1;           -- ids start at 1 within the range
    hi := ((shard::bigint + 1) << 47) - 1;
    INSERT INTO id_shard (shard) VALUES (shard)
    ON CONFLICT (singleton) DO UPDATE SET shard = EXCLUDED.shard;
    -- Every sequence that backs an identity column ('i' dependency), parents of partitioned
    -- tables included.
    FOR seq IN
        SELECT d.objid::regclass AS name
        FROM pg_depend d
        JOIN pg_class c ON c.oid = d.objid AND c.relkind = 'S'
        WHERE d.deptype = 'i' AND d.classid = 'pg_class'::regclass
    LOOP
        SELECT last_value INTO cur FROM pg_sequences
        WHERE format('%I.%I', schemaname, sequencename)::regclass = seq.name;
        EXECUTE format('ALTER SEQUENCE %s MINVALUE %s MAXVALUE %s START WITH %s NO CYCLE', seq.name, lo, hi, lo);
        -- A sequence below the range (a fresh shard) starts at its beginning; one inside it
        -- keeps counting from where it is.
        IF cur IS NULL OR cur < lo THEN
            EXECUTE format('ALTER SEQUENCE %s RESTART WITH %s', seq.name, lo);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

SELECT assign_id_range(0);

-- +goose Down
DROP FUNCTION assign_id_range(int);
DROP FUNCTION current_id_shard();
DROP TABLE id_shard;
