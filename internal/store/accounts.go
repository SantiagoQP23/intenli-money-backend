package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

type AccountType string

const (
	AccountCash       AccountType = "CASH"
	AccountBank       AccountType = "BANK"
	AccountSavings    AccountType = "SAVINGS"
	AccountWallet     AccountType = "WALLET"
	AccountCreditCard AccountType = "CREDIT_CARD"
)

func (t AccountType) Valid() bool {
	switch t {
	case AccountCash, AccountBank, AccountSavings, AccountWallet, AccountCreditCard:
		return true
	}
	return false
}

type Account struct {
	ID             uuid.UUID
	Name           string
	Type           AccountType
	Currency       string
	OpeningBalance decimal.Decimal
	// Balance is opening balance plus all entries of non-deleted transactions.
	Balance    decimal.Decimal
	ArchivedAt *time.Time
	CreatedAt  time.Time
}

const accountSelect = `
	SELECT a.id, a.name, a.type, a.currency, a.opening_balance::text,
	       (a.opening_balance + COALESCE((
	           SELECT sum(e.amount) FROM transaction_entries e
	           JOIN transactions t ON t.id = e.transaction_id
	           WHERE e.account_id = a.id AND t.deleted_at IS NULL), 0))::text,
	       a.archived_at, a.created_at
	FROM accounts a`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	var opening, balance string
	if err := row.Scan(&a.ID, &a.Name, &a.Type, &a.Currency, &opening, &balance, &a.ArchivedAt, &a.CreatedAt); err != nil {
		return Account{}, notFound(err)
	}
	a.OpeningBalance = decimal.RequireFromString(opening)
	a.Balance = decimal.RequireFromString(balance)
	return a, nil
}

func ListAccounts(ctx context.Context, db DB, workspaceID uuid.UUID, includeArchived bool) ([]Account, error) {
	rows, err := db.Query(ctx, accountSelect+`
		WHERE a.workspace_id = $1 AND ($2 OR a.archived_at IS NULL)
		ORDER BY a.archived_at NULLS FIRST, a.created_at`, workspaceID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func GetAccount(ctx context.Context, db DB, workspaceID, id uuid.UUID) (Account, error) {
	return scanAccount(db.QueryRow(ctx, accountSelect+` WHERE a.workspace_id = $1 AND a.id = $2`, workspaceID, id))
}

func CreateAccount(ctx context.Context, db DB, workspaceID uuid.UUID, name string, typ AccountType, currency string, opening decimal.Decimal, createdBy uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO accounts (workspace_id, name, type, currency, opening_balance, created_by)
		 VALUES ($1, $2, $3, $4, $5::numeric, $6) RETURNING id`,
		workspaceID, name, typ, currency, opening.String(), createdBy).Scan(&id)
	return id, err
}

// UpdateAccount changes the editable fields. Currency is fixed once created.
func UpdateAccount(ctx context.Context, db DB, workspaceID, id uuid.UUID, name string, typ AccountType, opening decimal.Decimal) error {
	tag, err := db.Exec(ctx,
		`UPDATE accounts SET name = $3, type = $4, opening_balance = $5::numeric, updated_at = now()
		 WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id, name, typ, opening.String())
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetAccountArchived archives or restores an account. Archived accounts keep
// their history but can't receive new transactions.
func SetAccountArchived(ctx context.Context, db DB, workspaceID, id uuid.UUID, archived bool) error {
	tag, err := db.Exec(ctx,
		`UPDATE accounts SET archived_at = CASE WHEN $3 THEN COALESCE(archived_at, now()) ELSE NULL END, updated_at = now()
		 WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id, archived)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
