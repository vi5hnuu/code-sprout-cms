-- Upload garbage collection reads quarantined files oldest first; without this index that is a
-- scan of every file. Pending uploads already have idx_file_pending.

-- +goose Up
CREATE INDEX idx_file_quarantined ON file (created_at) WHERE status = 'QUARANTINED';

-- +goose Down
DROP INDEX idx_file_quarantined;
