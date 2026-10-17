-- Content core (class-table inheritance): one `content` row per item of any type holds what
-- every type shares (identity, slug, workflow state, ownership, taxonomy); per-type columns
-- live in 1:1 extension tables (article_ext, problem_ext, ...). Listing, search, sitemaps,
-- engagement and workflow therefore work on one table regardless of type.
--
-- The body is Markdown + whitelisted directives (see docs/sprout-markdown.md), stored per
-- revision. Nothing executable is ever stored.
--
-- Two independent state axes, so a live page stays live while an edit to it is reviewed:
--   status       the item's life:      DRAFT (never live) → PUBLISHED (live) ⇄ ARCHIVED (taken down)
--   review_state the pending draft:    NONE → IN_REVIEW → CHANGES_REQUESTED | APPROVED → SCHEDULED
-- The content row's title/slug/summary/category are the *public face*: copied from the draft
-- revision's meta at publish. Until first publish they mirror the draft, so the studio can list
-- drafts and the slug is reserved early.

-- +goose Up
CREATE TABLE content (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type                 text        NOT NULL CHECK (type IN ('ARTICLE', 'BLOG', 'PROBLEM', 'TOPIC_PAGE', 'QUIZ')),
    locale               text        NOT NULL DEFAULT 'en' CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$'),
    slug                 text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title                text        NOT NULL,
    summary              text,
    status               text        NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'PUBLISHED', 'ARCHIVED')),
    review_state         text        NOT NULL DEFAULT 'NONE'
        CHECK (review_state IN ('NONE', 'IN_REVIEW', 'CHANGES_REQUESTED', 'APPROVED', 'SCHEDULED')),
    visibility           text        NOT NULL DEFAULT 'PUBLIC' CHECK (visibility IN ('PUBLIC', 'UNLISTED', 'PRIVATE')),
    access               text        NOT NULL DEFAULT 'FREE' CHECK (access IN ('FREE', 'PREMIUM')),
    author_id            bigint      NOT NULL REFERENCES app_user (id),
    primary_category_id  bigint      REFERENCES category (id) ON DELETE SET NULL,
    -- Revisions are numbered per content item. last_rev_no is the allocator; draft_rev_no is
    -- what the studio edits (and the If-Match value for optimistic concurrency);
    -- published_rev_no is what readers see; draft_rev_no > published_rev_no means unpublished
    -- changes. Numbers instead of FKs because the revision table is partitioned and keyed by
    -- (content_id, rev_no).
    last_rev_no          int         NOT NULL DEFAULT 0,
    draft_rev_no         int         NOT NULL DEFAULT 0,
    published_rev_no     int,
    scheduled_at         timestamptz,              -- SCHEDULED: when the worker publishes it
    published_at         timestamptz,              -- first publish; drives "latest" ordering
    last_published_at    timestamptz,              -- most recent publish; sitemap lastmod, "updated" label
    translation_group_id bigint,                   -- items that are translations of each other share this
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz,
    CONSTRAINT ck_content_published_has_revision
        CHECK (status <> 'PUBLISHED' OR (published_rev_no IS NOT NULL AND published_at IS NOT NULL)),
    CONSTRAINT ck_content_scheduled_has_time
        CHECK ((review_state = 'SCHEDULED') = (scheduled_at IS NOT NULL))
);

-- Soft-deleted rows release their slug so it can be reused.
CREATE UNIQUE INDEX uq_content_slug ON content (type, locale, slug) WHERE deleted_at IS NULL;

-- Public listings: newest first by type / category / author, keyset-paginated on
-- (published_at, id). Only PUBLIC items are listed; UNLISTED ones are reachable by URL alone.
CREATE INDEX idx_content_live_by_type ON content (type, locale, published_at DESC, id DESC)
    WHERE status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL;
CREATE INDEX idx_content_live_by_category ON content (primary_category_id, published_at DESC, id DESC)
    WHERE status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL;
CREATE INDEX idx_content_live_by_author ON content (author_id, published_at DESC, id DESC)
    WHERE status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL;

-- Sitemap chunks are id ranges per type; INCLUDE makes chunk generation an index-only scan.
CREATE INDEX idx_content_sitemap ON content (type, id) INCLUDE (locale, slug, last_published_at)
    WHERE status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL;

-- Studio: "my content", review queue, scheduler, admin title search.
CREATE INDEX idx_content_author ON content (author_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_content_review_queue ON content (review_state, updated_at, id)
    WHERE review_state IN ('IN_REVIEW', 'APPROVED') AND deleted_at IS NULL;
CREATE INDEX idx_content_scheduled ON content (scheduled_at) WHERE review_state = 'SCHEDULED' AND deleted_at IS NULL;
CREATE INDEX idx_content_title_trgm ON content USING gin (title gin_trgm_ops);
CREATE INDEX idx_content_translation ON content (translation_group_id) WHERE translation_group_id IS NOT NULL;

-- Append-only revision history. Hash-partitioned by content_id so the largest table in the
-- schema stays fast to vacuum and index. The archiver moves old bodies to object storage
-- (body_md → NULL, archive_file_id set), keeping hot partitions bounded.
CREATE TABLE content_revision (
    content_id      bigint      NOT NULL,
    rev_no          int         NOT NULL,
    body_md         text,
    body_sha256     bytea       NOT NULL,          -- skip no-op saves; dedupe on archive
    meta            jsonb       NOT NULL DEFAULT '{}',  -- draft title/slug/summary/category/tags/type fields
    editor_id       bigint      NOT NULL,
    message         text,
    archive_file_id bigint,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, rev_no),
    CHECK (body_md IS NOT NULL OR archive_file_id IS NOT NULL)
) PARTITION BY HASH (content_id);
SELECT create_hash_partitions('content_revision', 16);

-- Derived from the published revision at publish time (never edited directly). Gives search,
-- previews and the TOC their data without re-parsing Markdown on the read path.
CREATE TABLE content_render (
    content_id      bigint      PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    rev_no          int         NOT NULL,
    toc             jsonb       NOT NULL DEFAULT '[]',
    plain_text      text        NOT NULL DEFAULT '',
    word_count      int         NOT NULL DEFAULT 0,
    reading_minutes int         NOT NULL DEFAULT 0,
    snippet_count   int         NOT NULL DEFAULT 0,    -- runnable code blocks
    rendered_at     timestamptz NOT NULL DEFAULT now()
);

-- Files referenced by the published body; rebuilt on publish so orphan GC is a set difference.
CREATE TABLE content_asset (
    content_id bigint NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    file_id    bigint NOT NULL REFERENCES file (id) ON DELETE RESTRICT,
    PRIMARY KEY (content_id, file_id)
);
CREATE INDEX idx_content_asset_file ON content_asset (file_id);

-- Renamed slugs answer with 301 → current URL, so links and rankings survive renames.
CREATE TABLE slug_redirect (
    type       text        NOT NULL,
    locale     text        NOT NULL,
    old_slug   text        NOT NULL,
    content_id bigint      NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (type, locale, old_slug)
);

-- published_at is denormalised from content so "newest in tag X" is one index range scan even
-- for tags with millions of items. It is set only while the item is live and PUBLIC; the content
-- service keeps it in step on publish, archive and visibility changes.
CREATE TABLE content_tag (
    tag_id       bigint NOT NULL REFERENCES tag (id) ON DELETE CASCADE,
    content_id   bigint NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    published_at timestamptz,                       -- NULL while the content is not live
    PRIMARY KEY (tag_id, content_id)
);
CREATE INDEX idx_content_tag_content ON content_tag (content_id);
CREATE INDEX idx_content_tag_live ON content_tag (tag_id, published_at DESC, content_id DESC)
    WHERE published_at IS NOT NULL;

-- Typed edges between items: an article's "Try it yourself" problem, a problem's editorial,
-- related reading, prerequisites.
CREATE TABLE content_link (
    from_id  bigint NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    kind     text   NOT NULL CHECK (kind IN ('PRACTICE', 'RELATED', 'PREREQUISITE', 'EDITORIAL')),
    to_id    bigint NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    position int    NOT NULL DEFAULT 0,
    PRIMARY KEY (from_id, kind, to_id),
    CHECK (from_id <> to_id)
);
CREATE INDEX idx_content_link_to ON content_link (to_id, kind);

-- Ordered groupings: series, courses, study plans ("Top 150"), and user-owned lists.
CREATE TABLE collection (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text        NOT NULL CHECK (kind IN ('SERIES', 'COURSE', 'STUDY_PLAN', 'LIST')),
    slug        text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title       text        NOT NULL,
    description text,
    owner_id    bigint      NOT NULL REFERENCES app_user (id),
    visibility  text        NOT NULL DEFAULT 'PRIVATE' CHECK (visibility IN ('PUBLIC', 'UNLISTED', 'PRIVATE')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
-- Editorial collections own the public slug space; user LISTs are addressed by id.
CREATE UNIQUE INDEX uq_collection_slug ON collection (kind, slug) WHERE kind <> 'LIST';
CREATE INDEX idx_collection_owner ON collection (owner_id, updated_at DESC);

CREATE TABLE collection_item (
    collection_id bigint      NOT NULL REFERENCES collection (id) ON DELETE CASCADE,
    content_id    bigint      NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    -- Fractional: insert between a and b at (a+b)/2. When a gap gets too small for a double
    -- to split, the service renumbers that collection's items 1..n.
    position      double precision NOT NULL,
    added_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection_id, content_id)
);
CREATE INDEX idx_collection_item_order ON collection_item (collection_id, position);
CREATE INDEX idx_collection_item_content ON collection_item (content_id);

-- Per-type extensions.
CREATE TABLE article_ext (
    content_id bigint PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    level      text   CHECK (level IN ('BEGINNER', 'INTERMEDIATE', 'ADVANCED'))
);

CREATE TABLE blog_ext (
    content_id    bigint PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    cover_file_id bigint REFERENCES file (id) ON DELETE SET NULL
);

-- +goose Down
DROP TABLE blog_ext;
DROP TABLE article_ext;
DROP TABLE collection_item;
DROP TABLE collection;
DROP TABLE content_link;
DROP TABLE content_tag;
DROP TABLE slug_redirect;
DROP TABLE content_asset;
DROP TABLE content_render;
DROP TABLE content_revision;
DROP TABLE content;
