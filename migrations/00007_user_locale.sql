-- +goose Up
-- The user's language: the app's UI, the default category names of the
-- workspaces they create, and (later) emails.
ALTER TABLE users ADD COLUMN locale text NOT NULL DEFAULT 'es' CHECK (locale IN ('es', 'en'));

-- +goose Down
ALTER TABLE users DROP COLUMN locale;
