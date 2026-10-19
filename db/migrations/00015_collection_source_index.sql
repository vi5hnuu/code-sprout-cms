-- The video hub lists the series mirrored from YouTube playlists. Mirrored collections are a
-- small share of all collections, so they get their own partial index instead of a filter over
-- every public series.

-- +goose Up
CREATE INDEX idx_collection_source ON collection (source, kind, id DESC) WHERE source IS NOT NULL AND visibility = 'PUBLIC';

-- +goose Down
DROP INDEX idx_collection_source;
