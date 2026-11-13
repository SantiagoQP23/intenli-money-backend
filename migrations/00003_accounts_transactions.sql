-- +goose Up

CREATE TABLE currencies (
    code      char(3) PRIMARY KEY CHECK (code ~ '^[A-Z]{3}$'),
    name      text NOT NULL,
    symbol    text NOT NULL,
    -- Decimal places used for amounts in this currency (ISO 4217 minor unit).
    precision smallint NOT NULL CHECK (precision BETWEEN 0 AND 4)
);

INSERT INTO currencies (code, name, symbol, precision) VALUES
    ('USD', 'Dólar estadounidense', '$', 2),
    ('EUR', 'Euro', '€', 2),
    ('GBP', 'Libra esterlina', '£', 2),
    ('CAD', 'Dólar canadiense', 'CA$', 2),
    ('MXN', 'Peso mexicano', 'MX$', 2),
    ('COP', 'Peso colombiano', 'COL$', 2),
    ('PEN', 'Sol peruano', 'S/', 2),
    ('CLP', 'Peso chileno', 'CLP$', 0),
    ('ARS', 'Peso argentino', 'AR$', 2),
    ('BRL', 'Real brasileño', 'R$', 2),
    ('BOB', 'Boliviano', 'Bs', 2),
    ('UYU', 'Peso uruguayo', '$U', 2),
    ('PYG', 'Guaraní paraguayo', '₲', 0),
    ('GTQ', 'Quetzal guatemalteco', 'Q', 2),
    ('CRC', 'Colón costarricense', '₡', 2),
    ('DOP', 'Peso dominicano', 'RD$', 2),
    ('PAB', 'Balboa panameño', 'B/.', 2),
    ('JPY', 'Yen japonés', '¥', 0),
    ('CNY', 'Yuan chino', 'CN¥', 2),
    ('CHF', 'Franco suizo', 'CHF', 2);

ALTER TABLE workspaces
    ADD CONSTRAINT workspaces_base_currency_fkey FOREIGN KEY (base_currency) REFERENCES currencies (code);

-- Where money is held. The balance is opening_balance plus the account's
-- transaction entries; it is never stored.
CREATE TABLE accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    type            text NOT NULL CHECK (type IN ('CASH', 'BANK', 'SAVINGS', 'WALLET', 'CREDIT_CARD')),
    currency        char(3) NOT NULL REFERENCES currencies (code),
    opening_balance numeric(20, 4) NOT NULL DEFAULT 0,
    archived_at     timestamptz,
    created_by      uuid NOT NULL REFERENCES users (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE INDEX accounts_workspace_idx ON accounts (workspace_id);

CREATE TABLE categories (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('INCOME', 'EXPENSE')),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    archived_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE UNIQUE INDEX categories_unique_active_name
    ON categories (workspace_id, kind, lower(name)) WHERE archived_at IS NULL;

-- Rates the workspace used, as "1 from_currency = rate base currency".
-- The latest rate on or before a date is suggested for new transactions.
CREATE TABLE exchange_rates (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    from_currency char(3) NOT NULL REFERENCES currencies (code),
    effective_on  date NOT NULL,
    rate          numeric(24, 10) NOT NULL CHECK (rate > 0),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, from_currency, effective_on)
);

CREATE TABLE transactions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    type         text NOT NULL CHECK (type IN ('INCOME', 'EXPENSE', 'TRANSFER')),
    occurred_on  date NOT NULL,
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    category_id  uuid,
    created_by   uuid NOT NULL REFERENCES users (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- Soft delete: keeps history and lets future obligation payments refer to it.
    deleted_at   timestamptz,
    UNIQUE (workspace_id, id),
    -- A category must belong to the same workspace.
    FOREIGN KEY (workspace_id, category_id) REFERENCES categories (workspace_id, id),
    CHECK (type <> 'TRANSFER' OR category_id IS NULL)
);
CREATE INDEX transactions_list_idx
    ON transactions (workspace_id, occurred_on DESC, created_at DESC, id DESC) WHERE deleted_at IS NULL;

-- Account movements (not general-ledger postings). Income has one positive
-- entry, an expense one negative entry, and a transfer one negative and one
-- positive entry. base_amount is the amount in the workspace base currency
-- at the rate used, so totals never mix currencies.
CREATE TABLE transaction_entries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL,
    transaction_id uuid NOT NULL,
    account_id     uuid NOT NULL,
    amount         numeric(20, 4) NOT NULL CHECK (amount <> 0),
    currency       char(3) NOT NULL REFERENCES currencies (code),
    exchange_rate  numeric(24, 10) NOT NULL CHECK (exchange_rate > 0),
    base_amount    numeric(20, 4) NOT NULL,
    FOREIGN KEY (workspace_id, transaction_id) REFERENCES transactions (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, account_id) REFERENCES accounts (workspace_id, id)
);
CREATE INDEX transaction_entries_tx_idx ON transaction_entries (transaction_id);
CREATE INDEX transaction_entries_account_idx ON transaction_entries (account_id);

-- +goose Down
DROP TABLE transaction_entries;
DROP TABLE transactions;
DROP TABLE exchange_rates;
DROP TABLE categories;
DROP TABLE accounts;
ALTER TABLE workspaces DROP CONSTRAINT workspaces_base_currency_fkey;
DROP TABLE currencies;
