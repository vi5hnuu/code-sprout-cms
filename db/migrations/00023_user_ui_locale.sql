-- +goose Up
-- The reader's interface language (buttons, menus), saved on the account so it follows them to
-- every device. NULL means "no choice made": the web falls back to the browser's language.
-- Format only here; which languages the web ships is the web's concern.
ALTER TABLE app_user
    ADD COLUMN ui_locale text NULL CHECK (ui_locale ~ '^[a-z]{2}(-[A-Z]{2})?$');

-- +goose Down
ALTER TABLE app_user DROP COLUMN ui_locale;
