-- +goose Up
-- citext gives case-insensitive emails for the users table (step 2).
CREATE EXTENSION IF NOT EXISTS citext;

-- +goose Down
DROP EXTENSION IF EXISTS citext;
