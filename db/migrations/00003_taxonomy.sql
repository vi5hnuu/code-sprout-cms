-- Taxonomy, modelled on how GFG / LeetCode organise content:
--   category  — one hierarchical tree (DSA › Algorithms › Searching); drives URL sections,
--               breadcrumbs and editor scopes. Every content item has one primary category.
--   tag       — flat, many-to-many labels of several kinds (topic, company, language, pattern).
-- Collections (ordered series/courses) reference content, so they live in 00004.

-- +goose Up
CREATE TABLE category (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id   bigint      REFERENCES category (id) ON DELETE RESTRICT,
    -- Materialised path of slugs, e.g. 'dsa.algorithms.searching'. Subtree queries
    -- (`path <@ 'dsa'`) are a single GiST index scan at any depth. Kept in sync by the
    -- taxonomy service when a node is created or moved (moves rewrite the subtree's paths).
    path        ltree       NOT NULL UNIQUE,
    slug        text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name        text        NOT NULL,
    description text,
    position    numeric     NOT NULL DEFAULT 0,        -- sibling order; fractional → O(1) reorder
    seo         jsonb       NOT NULL DEFAULT '{}',     -- meta title/description overrides
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (parent_id, slug)
);
CREATE INDEX idx_category_path_gist ON category USING gist (path);
CREATE INDEX idx_category_children ON category (parent_id, position);

ALTER TABLE cms_role_grant
    ADD CONSTRAINT fk_cms_role_grant_scope FOREIGN KEY (scope_category_id) REFERENCES category (id) ON DELETE CASCADE;

CREATE TABLE tag (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text        NOT NULL CHECK (kind IN ('TOPIC', 'COMPANY', 'LANGUAGE', 'PATTERN')),
    slug        text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name        text        NOT NULL,
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, slug)
);
CREATE INDEX idx_tag_name_trgm ON tag USING gin (name gin_trgm_ops);  -- studio tag picker autocomplete

-- +goose Down
DROP TABLE tag;
ALTER TABLE cms_role_grant DROP CONSTRAINT fk_cms_role_grant_scope;
DROP TABLE category;
