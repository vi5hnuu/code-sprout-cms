-- Account erase: a reader can delete their account. The app_user row stays (authored pages
-- and audit entries reference it) but is anonymised and marked here; everything personal in
-- other tables is deleted by the erase itself.

-- +goose Up
ALTER TABLE app_user ADD COLUMN deleted_at timestamptz;

-- +goose Down
ALTER TABLE app_user DROP COLUMN deleted_at;
