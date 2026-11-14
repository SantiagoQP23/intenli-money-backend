package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/defaults"
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
	ID       uuid.UUID
	Kind     CategoryKind
	ParentID *uuid.UUID // set on a subcategory; subcategories have no children
	Name     string
	Icon     *string
	// ArchivedAt is shared by a parent and the subcategories archived with it.
	ArchivedAt *time.Time
}

// ErrParentArchived means a subcategory can't be restored before its parent.
var ErrParentArchived = errors.New("parent category is archived")

// CreateDefaultCategories seeds a new workspace with the defaults for its
// type, named in locale ("es" or "en").
func CreateDefaultCategories(ctx context.Context, db DB, workspaceID uuid.UUID, typ WorkspaceType, locale string) error {
	for _, c := range defaults.For(string(typ), locale) {
		if _, err := db.Exec(ctx,
			`INSERT INTO categories (workspace_id, kind, name, icon) VALUES ($1, $2, $3, $4)`,
			workspaceID, c.Kind, c.Name, c.Icon); err != nil {
			return err
		}
	}
	return nil
}

const categoryCols = `id, kind, parent_id, name, icon, archived_at`

func scanCategory(row pgx.Row) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Kind, &c.ParentID, &c.Name, &c.Icon, &c.ArchivedAt)
	return c, err
}

// ListCategories returns parents and subcategories in one flat list, by name.
func ListCategories(ctx context.Context, db DB, workspaceID uuid.UUID, includeArchived bool) ([]Category, error) {
	rows, err := db.Query(ctx,
		`SELECT `+categoryCols+` FROM categories
		 WHERE workspace_id = $1 AND ($2 OR archived_at IS NULL)
		 ORDER BY kind, lower(name)`, workspaceID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID) (Category, error) {
	c, err := scanCategory(db.QueryRow(ctx,
		`SELECT `+categoryCols+` FROM categories WHERE workspace_id = $1 AND id = $2`, workspaceID, id))
	return c, notFound(err)
}

// HasSubcategories reports whether any category, archived or not, sits
// under id.
func HasSubcategories(ctx context.Context, db DB, workspaceID, id uuid.UUID) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM categories WHERE workspace_id = $1 AND parent_id = $2)`,
		workspaceID, id).Scan(&ok)
	return ok, err
}

// CreateCategory returns ErrConflict when an active sibling (same kind and
// parent) already has the name. The caller checks the parent.
func CreateCategory(ctx context.Context, db DB, workspaceID uuid.UUID, kind CategoryKind, parentID *uuid.UUID, name string, icon *string) (Category, error) {
	c := Category{Kind: kind, ParentID: parentID, Name: name, Icon: icon}
	err := db.QueryRow(ctx,
		`INSERT INTO categories (workspace_id, kind, parent_id, name, icon) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		workspaceID, kind, parentID, name, icon).Scan(&c.ID)
	if isUniqueViolation(err) {
		return Category{}, ErrConflict
	}
	return c, err
}

// CategoryUpdate changes an active category. Icon nil keeps it ("" clears
// it); MoveTo nil keeps the parent, and a pointer to nil makes it top level.
type CategoryUpdate struct {
	Name   string
	Icon   *string
	MoveTo **uuid.UUID
}

// UpdateCategory applies u. The caller checks a new parent.
func UpdateCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID, u CategoryUpdate) error {
	var parent *uuid.UUID
	if u.MoveTo != nil {
		parent = *u.MoveTo
	}
	tag, err := db.Exec(ctx,
		`UPDATE categories SET name = $3,
		   icon = CASE WHEN $4::text IS NULL THEN icon ELSE NULLIF($4, '') END,
		   parent_id = CASE WHEN $5 THEN $6::uuid ELSE parent_id END
		 WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL`,
		workspaceID, id, u.Name, u.Icon, u.MoveTo != nil, parent)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ArchiveCategory hides it, and its active subcategories, from pickers; past
// transactions keep them. They share one archived_at so a restore brings
// them back together.
func ArchiveCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	var n int
	err := db.QueryRow(ctx,
		`WITH p AS (
		   UPDATE categories SET archived_at = now()
		   WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL
		   RETURNING id, archived_at
		 ), children AS (
		   UPDATE categories c SET archived_at = p.archived_at FROM p
		   WHERE c.workspace_id = $1 AND c.parent_id = p.id AND c.archived_at IS NULL
		   RETURNING c.id
		 )
		 SELECT count(*) FROM p`, workspaceID, id).Scan(&n)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// RestoreCategory brings an archived category back, with the subcategories
// that were archived along with it (skipping any whose name is now taken).
// ErrConflict means an active sibling already has the name, and
// ErrParentArchived that its parent must be restored first. Run it in a
// transaction.
func RestoreCategory(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	var archivedAt *time.Time
	var parentArchived bool
	err := db.QueryRow(ctx,
		`SELECT c.archived_at, coalesce(p.archived_at IS NOT NULL, false)
		 FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		 WHERE c.workspace_id = $1 AND c.id = $2
		 FOR UPDATE OF c`, workspaceID, id).Scan(&archivedAt, &parentArchived)
	if err != nil {
		return notFound(err)
	}
	if archivedAt == nil {
		return ErrNotFound
	}
	if parentArchived {
		return ErrParentArchived
	}
	if _, err := db.Exec(ctx,
		`UPDATE categories SET archived_at = NULL WHERE workspace_id = $1 AND id = $2`, workspaceID, id); err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	_, err = db.Exec(ctx,
		`UPDATE categories c SET archived_at = NULL
		 WHERE c.workspace_id = $1 AND c.parent_id = $2 AND c.archived_at = $3
		   AND NOT EXISTS (
		     SELECT 1 FROM categories o
		     WHERE o.workspace_id = $1 AND o.parent_id = $2 AND o.archived_at IS NULL
		       AND lower(o.name) = lower(c.name))`, workspaceID, id, *archivedAt)
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
