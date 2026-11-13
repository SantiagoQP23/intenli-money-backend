-- +goose Up

-- Suppliers, customers, lenders and borrowers.
CREATE TABLE contacts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    notes        text NOT NULL DEFAULT '' CHECK (length(notes) <= 500),
    archived_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE INDEX contacts_workspace_idx ON contacts (workspace_id);

-- Money owed by the workspace (PAYABLE) or to it (RECEIVABLE). The
-- outstanding balance is original_amount minus live payments; status and
-- overdue are derived, never stored.
CREATE TABLE obligations (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    direction             text NOT NULL CHECK (direction IN ('PAYABLE', 'RECEIVABLE')),
    origin                text NOT NULL CHECK (origin IN ('PURCHASE', 'SALE', 'LOAN_BORROWED', 'LOAN_LENT', 'OTHER')),
    contact_id            uuid,
    currency              char(3) NOT NULL REFERENCES currencies (code),
    original_amount       numeric(20, 4) NOT NULL CHECK (original_amount > 0),
    description           text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    occurred_on           date NOT NULL,
    due_on                date,
    -- The purchase, sale or loan transaction that created it (none for OTHER).
    origin_transaction_id uuid,
    created_by            uuid NOT NULL REFERENCES users (id),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz,
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, contact_id) REFERENCES contacts (workspace_id, id),
    FOREIGN KEY (workspace_id, origin_transaction_id) REFERENCES transactions (workspace_id, id),
    CHECK (
        (origin = 'PURCHASE' AND direction = 'PAYABLE') OR
        (origin = 'SALE' AND direction = 'RECEIVABLE') OR
        (origin = 'LOAN_BORROWED' AND direction = 'PAYABLE') OR
        (origin = 'LOAN_LENT' AND direction = 'RECEIVABLE') OR
        origin = 'OTHER'
    )
);
CREATE INDEX obligations_workspace_idx ON obligations (workspace_id) WHERE deleted_at IS NULL;

CREATE TABLE obligation_payments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL,
    obligation_id   uuid NOT NULL,
    transaction_id  uuid NOT NULL,
    amount          numeric(20, 4) NOT NULL CHECK (amount > 0),
    paid_on         date NOT NULL,
    idempotency_key text,
    created_by      uuid NOT NULL REFERENCES users (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, obligation_id) REFERENCES obligations (workspace_id, id),
    FOREIGN KEY (workspace_id, transaction_id) REFERENCES transactions (workspace_id, id),
    UNIQUE (workspace_id, idempotency_key)
);
CREATE INDEX obligation_payments_obligation_idx ON obligation_payments (obligation_id);

-- SETTLEMENT pays or collects an obligation; LOAN moves borrowed or lent
-- money. Neither is income or expense.
ALTER TABLE transactions DROP CONSTRAINT transactions_type_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_type_check
    CHECK (type IN ('INCOME', 'EXPENSE', 'TRANSFER', 'SETTLEMENT', 'LOAN'));
ALTER TABLE transactions DROP CONSTRAINT transactions_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_category_only_income_expense
    CHECK (category_id IS NULL OR type IN ('INCOME', 'EXPENSE'));

-- An entry moves money in an account, or changes what an obligation says is
-- owed. Obligation entries are signed by the workspace's position: a payable
-- grows negative, a receivable grows positive. So a purchase of 100 with 50
-- paid is an EXPENSE of -50 cash and -50 payable (100 expense in total), and
-- paying the remaining 50 later is a SETTLEMENT of -50 cash and +50 payable,
-- which nets to zero and is not a second expense.
ALTER TABLE transaction_entries ALTER COLUMN account_id DROP NOT NULL;
ALTER TABLE transaction_entries ADD COLUMN obligation_id uuid;
ALTER TABLE transaction_entries ADD CONSTRAINT transaction_entries_obligation_fkey
    FOREIGN KEY (workspace_id, obligation_id) REFERENCES obligations (workspace_id, id);
ALTER TABLE transaction_entries ADD CONSTRAINT transaction_entries_one_target
    CHECK (num_nonnulls(account_id, obligation_id) = 1);
CREATE INDEX transaction_entries_obligation_idx ON transaction_entries (obligation_id) WHERE obligation_id IS NOT NULL;

-- +goose Down
DROP INDEX transaction_entries_obligation_idx;
ALTER TABLE transaction_entries DROP CONSTRAINT transaction_entries_one_target;
ALTER TABLE transaction_entries DROP CONSTRAINT transaction_entries_obligation_fkey;
ALTER TABLE transaction_entries DROP COLUMN obligation_id;
ALTER TABLE transaction_entries ALTER COLUMN account_id SET NOT NULL;
ALTER TABLE transactions DROP CONSTRAINT transactions_category_only_income_expense;
ALTER TABLE transactions ADD CONSTRAINT transactions_check CHECK (type <> 'TRANSFER' OR category_id IS NULL);
ALTER TABLE transactions DROP CONSTRAINT transactions_type_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_type_check CHECK (type IN ('INCOME', 'EXPENSE', 'TRANSFER'));
DROP TABLE obligation_payments;
DROP TABLE obligations;
DROP TABLE contacts;
