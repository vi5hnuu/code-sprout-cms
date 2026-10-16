-- Generic file registry. Bytes never live in Postgres or pass through the API: clients upload
-- straight to object storage with a presigned URL, then call /complete, which verifies size
-- and checksum and flips the row to READY. Anything that owns a blob (article image, problem
-- test set, avatar, archived revision) points at a row here.

-- +goose Up
CREATE TABLE file (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id     bigint      REFERENCES app_user (id) ON DELETE SET NULL,
    bucket       text        NOT NULL,
    storage_key  text        NOT NULL,
    mime         text        NOT NULL,
    size_bytes   bigint      NOT NULL CHECK (size_bytes >= 0),
    sha256       bytea       CHECK (sha256 IS NULL OR octet_length(sha256) = 32),  -- known once READY
    status       text        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'READY', 'QUARANTINED')),
    purpose      text        NOT NULL CHECK (purpose IN ('ASSET', 'TEST_SET', 'CHECKER', 'AVATAR', 'ARCHIVE')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (bucket, storage_key)
);
-- Upload GC: PENDING rows older than the presign TTL are abandoned uploads.
CREATE INDEX idx_file_pending ON file (created_at) WHERE status = 'PENDING';
-- Dedup: re-uploading identical bytes reuses the existing READY object.
CREATE INDEX idx_file_sha256 ON file (sha256) WHERE status = 'READY';

ALTER TABLE app_user
    ADD CONSTRAINT fk_app_user_avatar FOREIGN KEY (avatar_file_id) REFERENCES file (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE app_user DROP CONSTRAINT fk_app_user_avatar;
DROP TABLE file;
