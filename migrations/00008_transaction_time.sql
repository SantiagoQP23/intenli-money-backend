-- +goose Up
-- Optional local time of day of a transaction ("14:30"). NULL for older
-- transactions and ones created by debts; they sort as 00:00 within the day.
ALTER TABLE transactions ADD COLUMN occurred_time time(0);

DROP INDEX transactions_list_idx;
CREATE INDEX transactions_list_idx
    ON transactions (workspace_id, occurred_on DESC, (coalesce(occurred_time, '00:00')) DESC, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX transactions_list_idx;
CREATE INDEX transactions_list_idx
    ON transactions (workspace_id, occurred_on DESC, created_at DESC, id DESC) WHERE deleted_at IS NULL;
ALTER TABLE transactions DROP COLUMN occurred_time;
