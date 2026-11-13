package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Currency struct {
	Code      string
	Name      string
	Symbol    string
	Precision int32
}

func ListCurrencies(ctx context.Context, db DB) ([]Currency, error) {
	rows, err := db.Query(ctx, `SELECT code, name, symbol, precision FROM currencies ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Currency{}
	for rows.Next() {
		var c Currency
		if err := rows.Scan(&c.Code, &c.Name, &c.Symbol, &c.Precision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetCurrency(ctx context.Context, db DB, code string) (Currency, error) {
	var c Currency
	err := db.QueryRow(ctx, `SELECT code, name, symbol, precision FROM currencies WHERE code = $1`, code).
		Scan(&c.Code, &c.Name, &c.Symbol, &c.Precision)
	return c, notFound(err)
}

// ---- Categories ----

type CategoryKind string

const (
	CategoryIncome  CategoryKind = "INCOME"
	CategoryExpense CategoryKind = "EXPENSE"
)

type Category struct {
	ID         uuid.UUID
	Kind       CategoryKind
	Name       string
	ArchivedAt *time.Time
}

var defaultCategories = []struct {
	kind CategoryKind
	name string
}{
	{CategoryIncome, "Salario"},
	{CategoryIncome, "Ventas"},
	{CategoryIncome, "Otros ingresos"},
	{CategoryExpense, "Comida"},
	{CategoryExpense, "Transporte"},
	{CategoryExpense, "Vivienda"},
	{CategoryExpense, "Servicios"},
	{CategoryExpense, "Salud"},
	{CategoryExpense, "Compras"},
	{CategoryExpense, "Entretenimiento"},
	{CategoryExpense, "Proveedores"},
	{CategoryExpense, "Otros gastos"},
}

// CreateDefaultCategories seeds a new workspace with common categories.
func CreateDefaultCategories(ctx context.Context, db DB, workspaceID uuid.UUID) error {
	for _, c := range defaultCategories {
		if _, err := db.Exec(ctx,
			`INSERT INTO categories (workspace_id, kind, name) VALUES ($1, $2, $3)`,
			workspaceID, c.kind, c.name); err != nil {
			return err
		}
	}
	return nil
}

func ListCategories(ctx context.Context, db DB, workspaceID uuid.UUID, includeArchived bool) ([]Category, error) {
	rows, err := db.Query(ctx,
		`SELECT id, kind, name, archived_at FROM categories
		 WHERE workspace_id = $1 AND ($2 OR archived_at IS NULL)
		 ORDER BY kind, lower(name)`, workspaceID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID) (Category, error) {
	var c Category
	err := db.QueryRow(ctx,
		`SELECT id, kind, name, archived_at FROM categories WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id).Scan(&c.ID, &c.Kind, &c.Name, &c.ArchivedAt)
	return c, notFound(err)
}

// CreateCategory returns ErrConflict when an active category of that kind
// already has the name.
func CreateCategory(ctx context.Context, db DB, workspaceID uuid.UUID, kind CategoryKind, name string) (Category, error) {
	c := Category{Kind: kind, Name: name}
	err := db.QueryRow(ctx,
		`INSERT INTO categories (workspace_id, kind, name) VALUES ($1, $2, $3) RETURNING id`,
		workspaceID, kind, name).Scan(&c.ID)
	if isUniqueViolation(err) {
		return Category{}, ErrConflict
	}
	return c, err
}

func RenameCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID, name string) error {
	tag, err := db.Exec(ctx,
		`UPDATE categories SET name = $3 WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL`,
		workspaceID, id, name)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ArchiveCategory hides it from pickers; past transactions keep it.
func ArchiveCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	tag, err := db.Exec(ctx,
		`UPDATE categories SET archived_at = now() WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL`,
		workspaceID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- Exchange rates ----

// LatestRate returns the newest rate for from→base on or before day.
func LatestRate(ctx context.Context, db DB, workspaceID uuid.UUID, from string, day time.Time) (decimal.Decimal, error) {
	var s string
	err := db.QueryRow(ctx,
		`SELECT rate::text FROM exchange_rates
		 WHERE workspace_id = $1 AND from_currency = $2 AND effective_on <= $3
		 ORDER BY effective_on DESC LIMIT 1`, workspaceID, from, day).Scan(&s)
	if err != nil {
		return decimal.Decimal{}, notFound(err)
	}
	return decimal.NewFromString(s)
}

// SaveRate records the rate used on day (last write wins for that day).
func SaveRate(ctx context.Context, db DB, workspaceID uuid.UUID, from string, day time.Time, rate decimal.Decimal) error {
	_, err := db.Exec(ctx,
		`INSERT INTO exchange_rates (workspace_id, from_currency, effective_on, rate) VALUES ($1, $2, $3, $4::numeric)
		 ON CONFLICT (workspace_id, from_currency, effective_on)
		 DO UPDATE SET rate = EXCLUDED.rate, updated_at = now()`,
		workspaceID, from, day, rate.String())
	return err
}

type Rate struct {
	FromCurrency string
	EffectiveOn  time.Time
	Rate         decimal.Decimal
}

// ListLatestRates returns the newest rate per currency.
func ListLatestRates(ctx context.Context, db DB, workspaceID uuid.UUID) ([]Rate, error) {
	rows, err := db.Query(ctx,
		`SELECT DISTINCT ON (from_currency) from_currency, effective_on, rate::text
		 FROM exchange_rates WHERE workspace_id = $1
		 ORDER BY from_currency, effective_on DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rate{}
	for rows.Next() {
		var r Rate
		var s string
		if err := rows.Scan(&r.FromCurrency, &r.EffectiveOn, &s); err != nil {
			return nil, err
		}
		if r.Rate, err = decimal.NewFromString(s); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
