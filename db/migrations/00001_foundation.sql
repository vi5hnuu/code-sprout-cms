-- Foundation: extensions, partition helpers, identity mirror, CMS roles.
--
-- Conventions used across every migration:
--   * Internal keys are bigint identities; public URLs use slugs (never ids).
--   * Enum-like columns are text + CHECK, so adding a value is a one-line constraint swap
--     instead of an ALTER TYPE that cannot run inside a transaction.
--   * Tables that grow per user action are partitioned from day one, keyed by content_id or
--     user_id. Those are also the distribution columns if we ever move to Citus.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Creates `<parent>_p<i>` for i in [0, modulus). Used once per hash-partitioned table.
-- +goose StatementBegin
CREATE FUNCTION create_hash_partitions(parent regclass, modulus int) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    FOR i IN 0..modulus - 1 LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %s_p%s PARTITION OF %s FOR VALUES WITH (MODULUS %s, REMAINDER %s)',
            parent, i, parent, modulus, i);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- Creates monthly partitions `<parent>_yYYYYmMM` covering `months` months from the month of
-- `from_day`. Idempotent. The worker calls it daily to stay ahead; there is deliberately no
-- DEFAULT partition, so a missing month fails loudly instead of silently piling rows into
-- a catch-all that later blocks attaching the real partition.
-- +goose StatementBegin
CREATE FUNCTION create_monthly_partitions(parent regclass, from_day date, months int) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    start_month date := date_trunc('month', from_day)::date;
    lo date;
    hi date;
BEGIN
    FOR i IN 0..months - 1 LOOP
        lo := (start_month + make_interval(months => i))::date;
        hi := (lo + interval '1 month')::date;
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %s_y%sm%s PARTITION OF %s FOR VALUES FROM (%L) TO (%L)',
            parent, to_char(lo, 'YYYY'), to_char(lo, 'MM'), parent, lo, hi);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- Local mirror of an auth-service account. Auth stays the only place that authenticates;
-- this row is created on a user's first authenticated request and maps auth's string `sub`
-- to a compact bigint that every CMS table references (keeps billion-row indexes small).
CREATE TABLE app_user (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    auth_sub       text        NOT NULL UNIQUE,
    handle         citext      UNIQUE,                 -- public profile URL /u/{handle}
    display_name   text        NOT NULL,
    bio            text,
    avatar_file_id bigint,                             -- FK added in 00002 (file table)
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Editorial roles live here, not in auth, so auth stays product-agnostic. Auth's ROLE_ADMIN
-- is treated as CMS super-admin in code; everything finer-grained is a grant row.
-- scope_category_id NULL = role applies everywhere; otherwise to that category subtree.
CREATE TABLE cms_role_grant (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           bigint      NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    role              text        NOT NULL CHECK (role IN ('CONTRIBUTOR', 'REVIEWER', 'EDITOR', 'ADMIN')),
    scope_category_id bigint,                          -- FK added in 00003 (category table)
    granted_by        bigint      REFERENCES app_user (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_cms_role_grant ON cms_role_grant (user_id, role, coalesce(scope_category_id, 0));

-- +goose Down
DROP TABLE cms_role_grant;
DROP TABLE app_user;
DROP FUNCTION create_monthly_partitions(regclass, date, int);
DROP FUNCTION create_hash_partitions(regclass, int);
