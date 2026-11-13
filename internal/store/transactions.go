package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type TxType string

const (
	TxIncome     TxType = "INCOME"
	TxExpense    TxType = "EXPENSE"
	TxTransfer   TxType = "TRANSFER"
	TxSettlement TxType = "SETTLEMENT" // paying or collecting an obligation
	TxLoan       TxType = "LOAN"       // borrowing or lending money
)

// Valid reports whether t is a type a client may create directly.
// SETTLEMENT and LOAN are created through obligations.
func (t TxType) Valid() bool { return t == TxIncome || t == TxExpense || t == TxTransfer }

// Known reports whether t is any stored type (for list filters).
func (t TxType) Known() bool { return t.Valid() || t == TxSettlement || t == TxLoan }

// Entry is one account movement (AccountID) or one change to an obligation
// (ObligationID); exactly one is set. Amount is signed in Currency: money
// in / receivable up is positive, money out / payable up is negative.
type Entry struct {
	AccountID    *uuid.UUID
	ObligationID *uuid.UUID
	Amount       decimal.Decimal
	Currency     string
	ExchangeRate decimal.Decimal // 1 Currency = ExchangeRate base currency
	BaseAmount   decimal.Decimal
}

type Transaction struct {
	ID          uuid.UUID
	Type        TxType
	OccurredOn  time.Time
	Description string
	CategoryID  *uuid.UUID
	Entries     []Entry
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func insertEntries(ctx context.Context, db DB, workspaceID, txID uuid.UUID, entries []Entry) error {
	for _, e := range entries {
		if _, err := db.Exec(ctx,
			`INSERT INTO transaction_entries (workspace_id, transaction_id, account_id, obligation_id, amount, currency, exchange_rate, base_amount)
			 VALUES ($1, $2, $3, $4, $5::numeric, $6, $7::numeric, $8::numeric)`,
			workspaceID, txID, e.AccountID, e.ObligationID, e.Amount.String(), e.Currency, e.ExchangeRate.String(), e.BaseAmount.String()); err != nil {
			return err
		}
	}
	return nil
}

func InsertTransaction(ctx context.Context, db DB, workspaceID uuid.UUID, t Transaction) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO transactions (workspace_id, type, occurred_on, description, category_id, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		workspaceID, t.Type, t.OccurredOn, t.Description, t.CategoryID, t.CreatedBy).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, insertEntries(ctx, db, workspaceID, id, t.Entries)
}

// LockTransaction locks a live transaction for update.
func LockTransaction(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	var x int
	err := db.QueryRow(ctx,
		`SELECT 1 FROM transactions WHERE workspace_id = $1 AND id = $2 AND deleted_at IS NULL FOR UPDATE`,
		workspaceID, id).Scan(&x)
	return notFound(err)
}

// ReplaceTransaction overwrites a live transaction and its entries.
func ReplaceTransaction(ctx context.Context, db DB, workspaceID, id uuid.UUID, t Transaction) error {
	tag, err := db.Exec(ctx,
		`UPDATE transactions SET type = $3, occurred_on = $4, description = $5, category_id = $6, updated_at = now()
		 WHERE workspace_id = $1 AND id = $2 AND deleted_at IS NULL`,
		workspaceID, id, t.Type, t.OccurredOn, t.Description, t.CategoryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := db.Exec(ctx, `DELETE FROM transaction_entries WHERE transaction_id = $1`, id); err != nil {
		return err
	}
	return insertEntries(ctx, db, workspaceID, id, t.Entries)
}

func SoftDeleteTransaction(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	tag, err := db.Exec(ctx,
		`UPDATE transactions SET deleted_at = now(), updated_at = now()
		 WHERE workspace_id = $1 AND id = $2 AND deleted_at IS NULL`, workspaceID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func GetTransaction(ctx context.Context, db DB, workspaceID, id uuid.UUID) (Transaction, error) {
	page, err := ListTransactions(ctx, db, workspaceID, TxFilter{ID: &id, Limit: 1})
	if err != nil {
		return Transaction{}, err
	}
	if len(page) == 0 {
		return Transaction{}, ErrNotFound
	}
	return page[0], nil
}

// TxCursor is the position after the last row of a page.
type TxCursor struct {
	OccurredOn time.Time
	CreatedAt  time.Time
	ID         uuid.UUID
}

type TxFilter struct {
	ID         *uuid.UUID
	AccountID  *uuid.UUID
	CategoryID *uuid.UUID
	Type       *TxType
	From, To   *time.Time // inclusive dates
	After      *TxCursor
	Limit      int
}

// ListTransactions returns live transactions newest first, with entries.
func ListTransactions(ctx context.Context, db DB, workspaceID uuid.UUID, f TxFilter) ([]Transaction, error) {
	where := []string{"t.workspace_id = $1", "t.deleted_at IS NULL"}
	args := []any{workspaceID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.ID != nil {
		add("t.id = $%d", *f.ID)
	}
	if f.AccountID != nil {
		add("EXISTS (SELECT 1 FROM transaction_entries e WHERE e.transaction_id = t.id AND e.account_id = $%d)", *f.AccountID)
	}
	if f.CategoryID != nil {
		add("t.category_id = $%d", *f.CategoryID)
	}
	if f.Type != nil {
		add("t.type = $%d", *f.Type)
	}
	if f.From != nil {
		add("t.occurred_on >= $%d", *f.From)
	}
	if f.To != nil {
		add("t.occurred_on <= $%d", *f.To)
	}
	if f.After != nil {
		args = append(args, f.After.OccurredOn, f.After.CreatedAt, f.After.ID)
		n := len(args)
		where = append(where, fmt.Sprintf("(t.occurred_on, t.created_at, t.id) < ($%d, $%d, $%d)", n-2, n-1, n))
	}
	args = append(args, f.Limit)

	rows, err := db.Query(ctx, fmt.Sprintf(
		`SELECT t.id, t.type, t.occurred_on, t.description, t.category_id, t.created_by, t.created_at, t.updated_at
		 FROM transactions t WHERE %s
		 ORDER BY t.occurred_on DESC, t.created_at DESC, t.id DESC LIMIT $%d`,
		strings.Join(where, " AND "), len(args)), args...)
	if err != nil {
		return nil, err
	}
	var txs []Transaction
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.Type, &t.OccurredOn, &t.Description, &t.CategoryID, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		index[t.ID] = len(txs)
		txs = append(txs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return []Transaction{}, nil
	}

	ids := make([]uuid.UUID, len(txs))
	for i, t := range txs {
		ids[i] = t.ID
	}
	erows, err := db.Query(ctx,
		`SELECT transaction_id, account_id, obligation_id, amount::text, currency, exchange_rate::text, base_amount::text
		 FROM transaction_entries WHERE transaction_id = ANY($1)
		 ORDER BY transaction_id, account_id IS NULL, amount`, ids) // account entries first, outflow first
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var txID uuid.UUID
		var e Entry
		var amount, rate, base string
		if err := erows.Scan(&txID, &e.AccountID, &e.ObligationID, &amount, &e.Currency, &rate, &base); err != nil {
			return nil, err
		}
		e.Amount = decimal.RequireFromString(amount)
		e.ExchangeRate = decimal.RequireFromString(rate)
		e.BaseAmount = decimal.RequireFromString(base)
		i := index[txID]
		txs[i].Entries = append(txs[i].Entries, e)
	}
	return txs, erows.Err()
}

// TransactionManagedByObligation reports whether the transaction was created
// by an obligation (origin or payment) and must be changed through it.
func TransactionManagedByObligation(ctx context.Context, db DB, workspaceID, id uuid.UUID) (bool, error) {
	var managed bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM transaction_entries WHERE transaction_id = $2 AND workspace_id = $1 AND obligation_id IS NOT NULL)
		     OR EXISTS (SELECT 1 FROM obligation_payments WHERE transaction_id = $2 AND workspace_id = $1)
		     OR EXISTS (SELECT 1 FROM transactions WHERE id = $2 AND workspace_id = $1 AND type IN ('SETTLEMENT', 'LOAN'))`,
		workspaceID, id).Scan(&managed)
	return managed, err
}
