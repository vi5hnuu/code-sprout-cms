-- Files the system has let go of (an erased account's avatars) move to DELETING: no longer
-- served as READY, and upload garbage collection removes the object and the row on its next
-- pass, oldest first.

-- +goose Up
ALTER TABLE file DROP CONSTRAINT file_status_check;
ALTER TABLE file ADD CONSTRAINT file_status_check
    CHECK (status IN ('PENDING', 'VERIFYING', 'READY', 'QUARANTINED', 'DELETING'));
CREATE INDEX idx_file_deleting ON file (id) WHERE status = 'DELETING';
-- Account erase finds a user's files by owner.
CREATE INDEX idx_file_owner ON file (owner_id, purpose) WHERE owner_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_file_owner;
DROP INDEX idx_file_deleting;
DELETE FROM file WHERE status = 'DELETING';
ALTER TABLE file DROP CONSTRAINT file_status_check;
ALTER TABLE file ADD CONSTRAINT file_status_check
    CHECK (status IN ('PENDING', 'VERIFYING', 'READY', 'QUARANTINED'));
