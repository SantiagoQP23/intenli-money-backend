-- +goose Up
-- A Lucide icon name; the API only accepts names from internal/defaults.
ALTER TABLE categories ADD COLUMN icon text CHECK (length(icon) <= 40);

-- +goose Down
ALTER TABLE categories DROP COLUMN icon;
