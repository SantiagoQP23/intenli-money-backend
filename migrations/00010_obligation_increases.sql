-- +goose Up

-- A debt can grow after it starts: borrowing more (MONEY, a LOAN
-- transaction through an account), buying or selling more on credit or
-- interest (CHARGE, an EXPENSE or INCOME transaction with no cash), or a
-- correction that moves no money (ADJUSTMENT, no transaction). What is owed
-- is original_amount plus live increases minus live payments.
CREATE TABLE obligation_increases (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL,
    obligation_id   uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('MONEY', 'CHARGE', 'ADJUSTMENT')),
    transaction_id  uuid,
    amount          numeric(20, 4) NOT NULL CHECK (amount > 0),
    occurred_on     date NOT NULL,
    occurred_time   time(0),
    description     text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    idempotency_key text,
    created_by      uuid NOT NULL REFERENCES users (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,
    FOREIGN KEY (workspace_id, obligation_id) REFERENCES obligations (workspace_id, id),
    FOREIGN KEY (workspace_id, transaction_id) REFERENCES transactions (workspace_id, id),
    UNIQUE (workspace_id, idempotency_key),
    CHECK ((kind = 'ADJUSTMENT') = (transaction_id IS NULL))
);
CREATE INDEX obligation_increases_obligation_idx ON obligation_increases (obligation_id) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE obligation_increases;
