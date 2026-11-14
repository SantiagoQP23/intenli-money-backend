package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

type BoxKind string

const (
	BoxSavings BoxKind = "SAVINGS"
	BoxBudget  BoxKind = "BUDGET"
)

func (k BoxKind) Valid() bool { return k == BoxSavings || k == BoxBudget }

type AllocationKind string

const (
	AllocAssign  AllocationKind = "ASSIGN"  // wallet → box
	AllocRelease AllocationKind = "RELEASE" // box → wallet
	AllocMove    AllocationKind = "MOVE"    // box → box, two rows
	AllocFill    AllocationKind = "FILL"    // monthly top-up of a budget
	AllocArchive AllocationKind = "ARCHIVE" // balance returned when archived
)

type Box struct {
	ID            uuid.UUID
	Kind          BoxKind
	Name          string
	Icon          *string
	Currency      string
	MonthlyAmount *decimal.Decimal
	GoalAmount    *decimal.Decimal
	GoalDate      *time.Time
	SortOrder     int
	CategoryIDs   []uuid.UUID
	ArchivedAt    *time.Time
	CreatedAt     time.Time
	// Balance is live allocations plus the account entries of live linked
	// transactions.
	Balance decimal.Decimal
	// MonthNet is the sum of linked account entries in the requested month
	// (negative when money was spent from the box).
	MonthNet decimal.Decimal
}

// boxSelect takes $2 and $3 as the month's first and last day.
const boxSelect = `
	SELECT b.id, b.kind, b.name, b.icon, b.currency, b.monthly_amount::text, b.goal_amount::text, b.goal_date,
	       b.sort_order, b.archived_at, b.created_at,
	       (COALESCE((SELECT sum(x.amount) FROM box_allocations x WHERE x.box_id = b.id AND x.deleted_at IS NULL), 0)
	        + COALESCE((SELECT sum(e.amount) FROM transactions t JOIN transaction_entries e ON e.transaction_id = t.id
	                    WHERE t.box_id = b.id AND t.deleted_at IS NULL AND e.account_id IS NOT NULL), 0))::text,
	       COALESCE((SELECT sum(e.amount) FROM transactions t JOIN transaction_entries e ON e.transaction_id = t.id
	                 WHERE t.box_id = b.id AND t.deleted_at IS NULL AND e.account_id IS NOT NULL
	                   AND t.occurred_on BETWEEN $2 AND $3), 0)::text,
	       ARRAY(SELECT c.category_id FROM box_categories c WHERE c.box_id = b.id ORDER BY c.category_id)
	FROM boxes b`

func scanBox(row pgx.Row) (Box, error) {
	var b Box
	var monthly, goal *string
	var balance, month string
	if err := row.Scan(&b.ID, &b.Kind, &b.Name, &b.Icon, &b.Currency, &monthly, &goal, &b.GoalDate,
		&b.SortOrder, &b.ArchivedAt, &b.CreatedAt, &balance, &month, &b.CategoryIDs); err != nil {
		return Box{}, notFound(err)
	}
	if monthly != nil {
		d := decimal.RequireFromString(*monthly)
		b.MonthlyAmount = &d
	}
	if goal != nil {
		d := decimal.RequireFromString(*goal)
		b.GoalAmount = &d
	}
	b.Balance = decimal.RequireFromString(balance)
	b.MonthNet = decimal.RequireFromString(month)
	if b.CategoryIDs == nil {
		b.CategoryIDs = []uuid.UUID{}
	}
	return b, nil
}

// ListBoxes returns boxes in their display order; month is any day of the
// month whose linked movements fill MonthNet.
func ListBoxes(ctx context.Context, db DB, workspaceID uuid.UUID, includeArchived bool, month time.Time) ([]Box, error) {
	from, to := monthBounds(month)
	rows, err := db.Query(ctx, boxSelect+`
		WHERE b.workspace_id = $1 AND ($4 OR b.archived_at IS NULL)
		ORDER BY b.archived_at NULLS FIRST, b.sort_order, b.created_at`, workspaceID, from, to, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Box{}
	for rows.Next() {
		b, err := scanBox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func GetBox(ctx context.Context, db DB, workspaceID, id uuid.UUID, month time.Time) (Box, error) {
	from, to := monthBounds(month)
	return scanBox(db.QueryRow(ctx, boxSelect+` WHERE b.workspace_id = $1 AND b.id = $4`, workspaceID, from, to, id))
}

func monthBounds(day time.Time) (time.Time, time.Time) {
	from := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 1, -1)
}

// LockBox locks a box row for update.
func LockBox(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	var x int
	err := db.QueryRow(ctx, `SELECT 1 FROM boxes WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, workspaceID, id).Scan(&x)
	return notFound(err)
}

func decPtr(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// CreateBox returns ErrConflict when an active box already has the name.
func CreateBox(ctx context.Context, db DB, workspaceID uuid.UUID, b Box, createdBy uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO boxes (workspace_id, kind, name, icon, currency, monthly_amount, goal_amount, goal_date, sort_order, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8,
		         COALESCE((SELECT max(sort_order) + 1 FROM boxes WHERE workspace_id = $1), 0), $9)
		 RETURNING id`,
		workspaceID, b.Kind, b.Name, b.Icon, b.Currency, decPtr(b.MonthlyAmount), decPtr(b.GoalAmount), b.GoalDate, createdBy).Scan(&id)
	if isUniqueViolation(err) {
		return uuid.Nil, ErrConflict
	}
	return id, err
}

// UpdateBox changes the editable fields; ErrConflict on a taken name.
func UpdateBox(ctx context.Context, db DB, workspaceID uuid.UUID, b Box) error {
	tag, err := db.Exec(ctx,
		`UPDATE boxes SET kind = $3, name = $4, icon = $5, currency = $6, monthly_amount = $7::numeric,
		   goal_amount = $8::numeric, goal_date = $9, updated_at = now()
		 WHERE workspace_id = $1 AND id = $2`,
		workspaceID, b.ID, b.Kind, b.Name, b.Icon, b.Currency, decPtr(b.MonthlyAmount), decPtr(b.GoalAmount), b.GoalDate)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetBoxArchived archives or restores a box; ErrConflict when restoring
// would clash with an active box's name.
func SetBoxArchived(ctx context.Context, db DB, workspaceID, id uuid.UUID, archived bool) error {
	_, err := db.Exec(ctx,
		`UPDATE boxes SET archived_at = CASE WHEN $3 THEN COALESCE(archived_at, now()) ELSE NULL END, updated_at = now()
		 WHERE workspace_id = $1 AND id = $2`, workspaceID, id, archived)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// SetBoxCategories makes categoryIDs suggest this box, taking them from any
// other box, and drops categories no longer listed.
func SetBoxCategories(ctx context.Context, db DB, workspaceID, boxID uuid.UUID, categoryIDs []uuid.UUID) error {
	if _, err := db.Exec(ctx,
		`DELETE FROM box_categories WHERE workspace_id = $1 AND (box_id = $2 OR category_id = ANY($3))`,
		workspaceID, boxID, categoryIDs); err != nil {
		return err
	}
	for _, c := range categoryIDs {
		if _, err := db.Exec(ctx,
			`INSERT INTO box_categories (workspace_id, category_id, box_id) VALUES ($1, $2, $3)`,
			workspaceID, c, boxID); err != nil {
			return err
		}
	}
	return nil
}

// BoxHasHistory reports whether a box has any allocation or linked
// transaction, deleted or not.
func BoxHasHistory(ctx context.Context, db DB, workspaceID, id uuid.UUID) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM box_allocations WHERE workspace_id = $1 AND box_id = $2)
		     OR EXISTS (SELECT 1 FROM transactions WHERE workspace_id = $1 AND box_id = $2)`,
		workspaceID, id).Scan(&ok)
	return ok, err
}

// ---- Allocations ----

type Allocation struct {
	ID         uuid.UUID
	BoxID      uuid.UUID
	Kind       AllocationKind
	Amount     decimal.Decimal
	MoveID     *uuid.UUID
	OtherBoxID *uuid.UUID // the other side of a move
	OccurredOn time.Time
	Note       string
	CreatedAt  time.Time
}

const allocationSelect = `
	SELECT x.id, x.box_id, x.kind, x.amount::text, x.move_id,
	       (SELECT y.box_id FROM box_allocations y WHERE y.move_id = x.move_id AND y.id <> x.id LIMIT 1),
	       x.occurred_on, x.note, x.created_at
	FROM box_allocations x`

func scanAllocation(row pgx.Row) (Allocation, error) {
	var a Allocation
	var amount string
	if err := row.Scan(&a.ID, &a.BoxID, &a.Kind, &amount, &a.MoveID, &a.OtherBoxID, &a.OccurredOn, &a.Note, &a.CreatedAt); err != nil {
		return Allocation{}, notFound(err)
	}
	a.Amount = decimal.RequireFromString(amount)
	return a, nil
}

// ListAllocations returns a box's live allocations, newest first.
func ListAllocations(ctx context.Context, db DB, workspaceID, boxID uuid.UUID, limit int) ([]Allocation, error) {
	rows, err := db.Query(ctx, allocationSelect+`
		WHERE x.workspace_id = $1 AND x.box_id = $2 AND x.deleted_at IS NULL
		ORDER BY x.occurred_on DESC, x.created_at DESC LIMIT $3`, workspaceID, boxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Allocation{}
	for rows.Next() {
		a, err := scanAllocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func GetAllocation(ctx context.Context, db DB, workspaceID, boxID, id uuid.UUID) (Allocation, error) {
	return scanAllocation(db.QueryRow(ctx, allocationSelect+`
		WHERE x.workspace_id = $1 AND x.box_id = $2 AND x.id = $3 AND x.deleted_at IS NULL`, workspaceID, boxID, id))
}

// AllocationKeyUsed reports whether an idempotency key was already used.
func AllocationKeyUsed(ctx context.Context, db DB, workspaceID uuid.UUID, key string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM box_allocations WHERE workspace_id = $1 AND idempotency_key = $2)`,
		workspaceID, key).Scan(&ok)
	return ok, err
}

func InsertAllocation(ctx context.Context, db DB, workspaceID uuid.UUID, a Allocation, key *string, createdBy uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO box_allocations (workspace_id, box_id, kind, amount, move_id, occurred_on, note, idempotency_key, created_by)
		 VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8, $9) RETURNING id`,
		workspaceID, a.BoxID, a.Kind, a.Amount.String(), a.MoveID, a.OccurredOn, a.Note, key, createdBy).Scan(&id)
	if isUniqueViolation(err) {
		return uuid.Nil, ErrConflict
	}
	return id, err
}

// SoftDeleteAllocation deletes an allocation, and the other side of a move.
func SoftDeleteAllocation(ctx context.Context, db DB, workspaceID uuid.UUID, a Allocation) error {
	_, err := db.Exec(ctx,
		`UPDATE box_allocations SET deleted_at = now()
		 WHERE workspace_id = $1 AND deleted_at IS NULL AND (id = $2 OR ($3::uuid IS NOT NULL AND move_id = $3))`,
		workspaceID, a.ID, a.MoveID)
	return err
}
