package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Income and expenses come from INCOME and EXPENSE transactions only, in the
// base currency, counting every entry (so a purchase on credit counts in
// full the day it happens). SETTLEMENT, LOAN and TRANSFER never count.

type IncomeExpense struct {
	Income, Expenses decimal.Decimal
}

func PeriodIncomeExpense(ctx context.Context, db DB, workspaceID uuid.UUID, from, to time.Time) (IncomeExpense, error) {
	var inc, exp string
	err := db.QueryRow(ctx,
		`SELECT COALESCE(sum(e.base_amount) FILTER (WHERE t.type = 'INCOME'), 0)::text,
		        COALESCE(-sum(e.base_amount) FILTER (WHERE t.type = 'EXPENSE'), 0)::text
		 FROM transactions t JOIN transaction_entries e ON e.transaction_id = t.id
		 WHERE t.workspace_id = $1 AND t.deleted_at IS NULL AND t.occurred_on BETWEEN $2 AND $3`,
		workspaceID, from, to).Scan(&inc, &exp)
	if err != nil {
		return IncomeExpense{}, err
	}
	return IncomeExpense{decimal.RequireFromString(inc), decimal.RequireFromString(exp)}, nil
}

type CategoryTotal struct {
	CategoryID *uuid.UUID
	Name       *string
	Total      decimal.Decimal
	Count      int
}

// CategoryTotals sums INCOME or EXPENSE transactions by category (positive
// totals, largest first). Uncategorized ones have a nil CategoryID.
func CategoryTotals(ctx context.Context, db DB, workspaceID uuid.UUID, typ TxType, from, to time.Time) ([]CategoryTotal, error) {
	rows, err := db.Query(ctx,
		`SELECT t.category_id, c.name, abs(sum(e.base_amount))::text, count(DISTINCT t.id)
		 FROM transactions t
		 JOIN transaction_entries e ON e.transaction_id = t.id
		 LEFT JOIN categories c ON c.id = t.category_id
		 WHERE t.workspace_id = $1 AND t.deleted_at IS NULL AND t.type = $2 AND t.occurred_on BETWEEN $3 AND $4
		 GROUP BY t.category_id, c.name
		 ORDER BY abs(sum(e.base_amount)) DESC`, workspaceID, typ, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CategoryTotal{}
	for rows.Next() {
		var c CategoryTotal
		var total string
		if err := rows.Scan(&c.CategoryID, &c.Name, &total, &c.Count); err != nil {
			return nil, err
		}
		c.Total = decimal.RequireFromString(total)
		out = append(out, c)
	}
	return out, rows.Err()
}

type MonthTotal struct {
	Month time.Time
	IncomeExpense
}

// MonthlyIncomeExpense returns one row per month that has data in [from, to].
func MonthlyIncomeExpense(ctx context.Context, db DB, workspaceID uuid.UUID, from, to time.Time) ([]MonthTotal, error) {
	rows, err := db.Query(ctx,
		`SELECT date_trunc('month', t.occurred_on)::date,
		        COALESCE(sum(e.base_amount) FILTER (WHERE t.type = 'INCOME'), 0)::text,
		        COALESCE(-sum(e.base_amount) FILTER (WHERE t.type = 'EXPENSE'), 0)::text
		 FROM transactions t JOIN transaction_entries e ON e.transaction_id = t.id
		 WHERE t.workspace_id = $1 AND t.deleted_at IS NULL AND t.type IN ('INCOME', 'EXPENSE')
		   AND t.occurred_on BETWEEN $2 AND $3
		 GROUP BY 1 ORDER BY 1`, workspaceID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonthTotal{}
	for rows.Next() {
		var m MonthTotal
		var inc, exp string
		if err := rows.Scan(&m.Month, &inc, &exp); err != nil {
			return nil, err
		}
		m.Income, m.Expenses = decimal.RequireFromString(inc), decimal.RequireFromString(exp)
		out = append(out, m)
	}
	return out, rows.Err()
}

// PeriodTotals backs the totals strip above a transaction list.
//
// For the workspace (accountID nil) it is income and expenses in the base
// currency, like PeriodIncomeExpense, optionally narrowed to one of those
// two types. For one account it is the money that came in and went out of
// that account in its own currency, counting every type (transfers,
// settlements and loans included), so in - out is the balance change over
// the period. typ, when set, narrows either to that transaction type.
func PeriodTotals(ctx context.Context, db DB, workspaceID uuid.UUID, accountID *uuid.UUID, typ *TxType, from, to time.Time) (IncomeExpense, error) {
	var query string
	if accountID == nil {
		query = `SELECT COALESCE(sum(e.base_amount) FILTER (WHERE t.type = 'INCOME'), 0)::text,
		                COALESCE(-sum(e.base_amount) FILTER (WHERE t.type = 'EXPENSE'), 0)::text
		         FROM transactions t JOIN transaction_entries e ON e.transaction_id = t.id
		         WHERE t.workspace_id = $1 AND t.deleted_at IS NULL AND t.occurred_on BETWEEN $2 AND $3
		           AND t.type IN ('INCOME', 'EXPENSE') AND ($4::text IS NULL OR t.type = $4::text)
		           AND $5::uuid IS NULL`
	} else {
		query = `SELECT COALESCE(sum(e.amount) FILTER (WHERE e.amount > 0), 0)::text,
		                COALESCE(-sum(e.amount) FILTER (WHERE e.amount < 0), 0)::text
		         FROM transactions t JOIN transaction_entries e ON e.transaction_id = t.id
		         WHERE t.workspace_id = $1 AND t.deleted_at IS NULL AND t.occurred_on BETWEEN $2 AND $3
		           AND ($4::text IS NULL OR t.type = $4::text) AND e.account_id = $5`
	}
	var typArg *string
	if typ != nil {
		s := string(*typ)
		typArg = &s
	}
	var in, out string
	if err := db.QueryRow(ctx, query, workspaceID, from, to, typArg, accountID).Scan(&in, &out); err != nil {
		return IncomeExpense{}, err
	}
	return IncomeExpense{decimal.RequireFromString(in), decimal.RequireFromString(out)}, nil
}
