-- +goose Up

-- Boxes say what money is for (a budget like "Food" or savings like "Car");
-- accounts say where it is. A box's balance is its live allocations plus the
-- account entries of live transactions linked to it, never stored. The money
-- not in any box (the wallet) is, per currency, the active accounts' balance
-- minus the active boxes' balance.
CREATE TABLE boxes (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('SAVINGS', 'BUDGET')),
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    icon           text,
    currency       char(3) NOT NULL REFERENCES currencies (code),
    monthly_amount numeric(20, 4) CHECK (monthly_amount > 0),
    goal_amount    numeric(20, 4) CHECK (goal_amount > 0),
    goal_date      date,
    sort_order     integer NOT NULL DEFAULT 0,
    archived_at    timestamptz,
    created_by     uuid NOT NULL REFERENCES users (id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    CHECK (kind = 'BUDGET' OR monthly_amount IS NULL),
    CHECK (kind = 'SAVINGS' OR (goal_amount IS NULL AND goal_date IS NULL))
);
CREATE UNIQUE INDEX boxes_unique_active_name
    ON boxes (workspace_id, lower(name)) WHERE archived_at IS NULL;

-- Money put into (+) or taken out of (-) a box by hand. A move between two
-- boxes is two rows sharing move_id.
CREATE TABLE box_allocations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL,
    box_id          uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('ASSIGN', 'RELEASE', 'MOVE', 'FILL', 'ARCHIVE')),
    amount          numeric(20, 4) NOT NULL CHECK (amount <> 0),
    move_id         uuid,
    occurred_on     date NOT NULL,
    note            text NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    idempotency_key text,
    created_by      uuid NOT NULL REFERENCES users (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,
    FOREIGN KEY (workspace_id, box_id) REFERENCES boxes (workspace_id, id) ON DELETE CASCADE,
    UNIQUE (workspace_id, idempotency_key),
    CHECK ((kind = 'MOVE') = (move_id IS NOT NULL))
);
CREATE INDEX box_allocations_box_idx ON box_allocations (box_id) WHERE deleted_at IS NULL;

-- Expenses, income and debt payments can be paid from (or into) a box.
ALTER TABLE transactions ADD COLUMN box_id uuid;
ALTER TABLE transactions ADD CONSTRAINT transactions_box_fkey
    FOREIGN KEY (workspace_id, box_id) REFERENCES boxes (workspace_id, id);
ALTER TABLE transactions ADD CONSTRAINT transactions_box_type
    CHECK (box_id IS NULL OR type IN ('INCOME', 'EXPENSE', 'SETTLEMENT'));
CREATE INDEX transactions_box_idx ON transactions (box_id) WHERE box_id IS NOT NULL AND deleted_at IS NULL;

-- A category can suggest one box in the transaction form.
CREATE TABLE box_categories (
    workspace_id uuid NOT NULL,
    category_id  uuid NOT NULL,
    box_id       uuid NOT NULL,
    PRIMARY KEY (workspace_id, category_id),
    FOREIGN KEY (workspace_id, category_id) REFERENCES categories (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, box_id) REFERENCES boxes (workspace_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE box_categories;
DROP INDEX transactions_box_idx;
ALTER TABLE transactions DROP CONSTRAINT transactions_box_type;
ALTER TABLE transactions DROP CONSTRAINT transactions_box_fkey;
ALTER TABLE transactions DROP COLUMN box_id;
DROP TABLE box_allocations;
DROP TABLE boxes;
