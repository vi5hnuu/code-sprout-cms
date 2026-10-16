-- Editorial review, audit trail, the transactional outbox and sitemap bookkeeping.

-- +goose Up
CREATE TABLE review (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    content_id  bigint      NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    rev_no      int         NOT NULL,               -- the revision that was reviewed
    reviewer_id bigint      NOT NULL REFERENCES app_user (id),
    decision    text        NOT NULL CHECK (decision IN ('APPROVED', 'CHANGES_REQUESTED')),
    summary     text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_review_content ON review (content_id, created_at DESC);

-- Inline notes; `anchor` locates the note in the Markdown (line range / heading id) so the
-- studio can pin it next to the text it is about.
CREATE TABLE review_note (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    review_id  bigint      NOT NULL REFERENCES review (id) ON DELETE CASCADE,
    anchor     jsonb       NOT NULL,
    body       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_review_note_review ON review_note (review_id);

CREATE TABLE audit_log (
    id         bigint GENERATED ALWAYS AS IDENTITY,
    actor_id   bigint,
    action     text        NOT NULL,                -- e.g. content.publish, role.grant
    entity     text        NOT NULL,
    entity_id  bigint,
    data       jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (created_at, id)
) PARTITION BY RANGE (created_at);
SELECT create_monthly_partitions('audit_log', current_date, 4);
CREATE INDEX idx_audit_entity ON audit_log (entity, entity_id, created_at DESC);

-- Transactional outbox: a domain change and the event describing it commit atomically, so
-- side effects (search index, sitemap, cache purge, ISR revalidation) can never drift from the
-- database. Workers claim rows with FOR UPDATE SKIP LOCKED; processed months are dropped whole.
CREATE TABLE outbox (
    id           bigint GENERATED ALWAYS AS IDENTITY,
    topic        text        NOT NULL,              -- e.g. content.published
    aggregate_id bigint      NOT NULL,
    payload      jsonb       NOT NULL DEFAULT '{}',
    attempts     int         NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),  -- pushed forward on failure (backoff)
    last_error   text,
    processed_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (created_at, id)
) PARTITION BY RANGE (created_at);
SELECT create_monthly_partitions('outbox', current_date, 4);
CREATE INDEX idx_outbox_pending ON outbox (available_at, id) WHERE processed_at IS NULL;

-- One row per sitemap file. A chunk is a fixed content-id range (chunk = id / 50000), so new
-- content only ever touches the newest chunk and older files stay byte-stable for crawlers.
CREATE TABLE sitemap_chunk (
    type      text        NOT NULL,
    chunk     int         NOT NULL,
    url_count int         NOT NULL DEFAULT 0,
    lastmod   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (type, chunk)
);

-- +goose Down
DROP TABLE sitemap_chunk;
DROP TABLE outbox;
DROP TABLE audit_log;
DROP TABLE review_note;
DROP TABLE review;
