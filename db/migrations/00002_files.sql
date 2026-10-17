-- Generic file registry. Bytes never live in Postgres or pass through the API: clients upload
-- straight to object storage with a presigned URL, then call /complete. Anything that owns a
-- blob (article image, problem test set, avatar, archived revision) points at a row here.
--
-- Lifecycle: PENDING (upload URL issued) → READY (size, type and SHA-256 verified)
--                                        → VERIFYING (large file awaiting a worker hash) → READY
--                                        → QUARANTINED (verification failed; object deleted)

-- +goose Up
CREATE TABLE file (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id     bigint      REFERENCES app_user (id) ON DELETE SET NULL,
    bucket       text        NOT NULL,
    storage_key  text        NOT NULL,
    mime         text        NOT NULL,
    size_bytes   bigint      NOT NULL CHECK (size_bytes >= 0),
    -- The SHA-256 the client declared; storage (S3/R2) or the worker proves the bytes match.
    sha256       bytea       NOT NULL CHECK (octet_length(sha256) = 32),
    status       text        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'VERIFYING', 'READY', 'QUARANTINED')),
    width        int,                                  -- images: pixel size, so pages reserve space (no layout shift)
    height       int,
    lease_until  timestamptz,                          -- VERIFYING: worker lease; expired = reclaimable
    purpose      text        NOT NULL CHECK (purpose IN ('ASSET', 'TEST_SET', 'CHECKER', 'AVATAR', 'ARCHIVE')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (bucket, storage_key)
);
-- Upload GC: PENDING rows older than the presign TTL are abandoned uploads.
CREATE INDEX idx_file_pending ON file (created_at) WHERE status = 'PENDING';
-- Dedup: an author re-uploading identical bytes gets the existing READY file back.
CREATE INDEX idx_file_dedup ON file (owner_id, purpose, sha256) WHERE status = 'READY';
CREATE INDEX idx_file_verifying ON file (id) WHERE status = 'VERIFYING';

ALTER TABLE app_user
    ADD CONSTRAINT fk_app_user_avatar FOREIGN KEY (avatar_file_id) REFERENCES file (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE app_user DROP CONSTRAINT fk_app_user_avatar;
DROP TABLE file;
