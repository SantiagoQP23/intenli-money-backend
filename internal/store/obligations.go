package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ---- Contacts ----

type Contact struct {
	ID         uuid.UUID
	Name       string
	Notes      string
	ArchivedAt *time.Time
}

func ListContacts(ctx context.Context, db DB, workspaceID uuid.UUID) ([]Contact, error) {
	rows, err := db.Query(ctx,
		`SELECT id, name, notes, archived_at FROM contacts
		 WHERE workspace_id = $1 ORDER BY archived_at NULLS FIRST, lower(name)`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Notes, &c.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetContact(ctx context.Context, db DB, workspaceID, id uuid.UUID) (Contact, error) {
	var c Contact
	err := db.QueryRow(ctx,
		`SELECT id, name, notes, archived_at FROM contacts WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id).Scan(&c.ID, &c.Name, &c.Notes, &c.ArchivedAt)
	return c, notFound(err)
}

func CreateContact(ctx context.Context, db DB, workspaceID uuid.UUID, name, notes string) (Contact, error) {
	c := Contact{Name: name, Notes: notes}
	err := db.QueryRow(ctx,
		`INSERT INTO contacts (workspace_id, name, notes) VALUES ($1, $2, $3) RETURNING id`,
		workspaceID, name, notes).Scan(&c.ID)
	return c, err
}

func UpdateContact(ctx context.Context, db DB, workspaceID, id uuid.UUID, name, notes string, archived bool) error {
	tag, err := db.Exec(ctx,
		`UPDATE contacts SET name = $3, notes = $4,
		   archived_at = CASE WHEN $5 THEN COALESCE(archived_at, now()) ELSE NULL END
		 WHERE workspace_id = $1 AND id = $2`, workspaceID, id, name, notes, archived)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- Obligations ----

type Direction string

const (
	Payable    Direction = "PAYABLE"
	Receivable Direction = "RECEIVABLE"
)

type Origin string

const (
	OriginPurchase     Origin = "PURCHASE"
	OriginSale         Origin = "SALE"
	OriginLoanBorrowed Origin = "LOAN_BORROWED"
	OriginLoanLent     Origin = "LOAN_LENT"
	OriginOther        Origin = "OTHER"
)

type Obligation struct {
	ID                  uuid.UUID
	Direction           Direction
	Origin              Origin
	ContactID           *uuid.UUID
	ContactName         *string
	Currency            string
	OriginalAmount      decimal.Decimal
	PaidAmount          decimal.Decimal // sum of live payments
	Description         string
	OccurredOn          time.Time
	DueOn               *time.Time
	OriginTransactionID *uuid.UUID
	CreatedAt           time.Time
}

func (o Obligation) Outstanding() decimal.Decimal { return o.OriginalAmount.Sub(o.PaidAmount) }

const obligationSelect = `
	SELECT o.id, o.direction, o.origin, o.contact_id, c.name, o.currency, o.original_amount::text,
	       COALESCE((SELECT sum(p.amount) FROM obligation_payments p
	                 JOIN transactions t ON t.id = p.transaction_id
	                 WHERE p.obligation_id = o.id AND t.deleted_at IS NULL), 0)::text,
	       o.description, o.occurred_on, o.due_on, o.origin_transaction_id, o.created_at
	FROM obligations o LEFT JOIN contacts c ON c.id = o.contact_id`

func scanObligation(row pgx.Row) (Obligation, error) {
	var o Obligation
	var orig, paid string
	if err := row.Scan(&o.ID, &o.Direction, &o.Origin, &o.ContactID, &o.ContactName, &o.Currency, &orig, &paid,
		&o.Description, &o.OccurredOn, &o.DueOn, &o.OriginTransactionID, &o.CreatedAt); err != nil {
		return Obligation{}, notFound(err)
	}
	o.OriginalAmount = decimal.RequireFromString(orig)
	o.PaidAmount = decimal.RequireFromString(paid)
	return o, nil
}

type ObligationFilter struct {
	Direction *Direction
	ContactID *uuid.UUID
	// Open: only with an outstanding balance; Closed: only fully paid.
	Open, Closed bool
}

func ListObligations(ctx context.Context, db DB, workspaceID uuid.UUID, f ObligationFilter) ([]Obligation, error) {
	where := []string{"o.workspace_id = $1", "o.deleted_at IS NULL"}
	args := []any{workspaceID}
	if f.Direction != nil {
		args = append(args, *f.Direction)
		where = append(where, fmt.Sprintf("o.direction = $%d", len(args)))
	}
	if f.ContactID != nil {
		args = append(args, *f.ContactID)
		where = append(where, fmt.Sprintf("o.contact_id = $%d", len(args)))
	}
	rows, err := db.Query(ctx, obligationSelect+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY o.due_on NULLS LAST, o.occurred_on DESC, o.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Obligation{}
	for rows.Next() {
		o, err := scanObligation(rows)
		if err != nil {
			return nil, err
		}
		open := o.Outstanding().IsPositive()
		if (f.Open && !open) || (f.Closed && open) {
			continue
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func GetObligation(ctx context.Context, db DB, workspaceID, id uuid.UUID) (Obligation, error) {
	return scanObligation(db.QueryRow(ctx, obligationSelect+
		` WHERE o.workspace_id = $1 AND o.id = $2 AND o.deleted_at IS NULL`, workspaceID, id))
}

// LockObligation serializes payments on one obligation.
func LockObligation(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	var x int
	err := db.QueryRow(ctx,
		`SELECT 1 FROM obligations WHERE workspace_id = $1 AND id = $2 AND deleted_at IS NULL FOR UPDATE`,
		workspaceID, id).Scan(&x)
	return notFound(err)
}

func InsertObligation(ctx context.Context, db DB, workspaceID uuid.UUID, o Obligation, createdBy uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO obligations (workspace_id, direction, origin, contact_id, currency, original_amount,
		   description, occurred_on, due_on, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10) RETURNING id`,
		workspaceID, o.Direction, o.Origin, o.ContactID, o.Currency, o.OriginalAmount.String(),
		o.Description, o.OccurredOn, o.DueOn, createdBy).Scan(&id)
	return id, err
}

func SetObligationOriginTransaction(ctx context.Context, db DB, workspaceID, id, txID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE obligations SET origin_transaction_id = $3 WHERE workspace_id = $1 AND id = $2`, workspaceID, id, txID)
	return err
}

func UpdateObligationDetails(ctx context.Context, db DB, workspaceID, id uuid.UUID, description string, dueOn *time.Time, contactID *uuid.UUID) error {
	tag, err := db.Exec(ctx,
		`UPDATE obligations SET description = $3, due_on = $4, contact_id = $5, updated_at = now()
		 WHERE workspace_id = $1 AND id = $2 AND deleted_at IS NULL`,
		workspaceID, id, description, dueOn, contactID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func SoftDeleteObligation(ctx context.Context, db DB, workspaceID, id uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE obligations SET deleted_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`, workspaceID, id)
	return err
}

// ---- Payments ----

type Payment struct {
	ID            uuid.UUID
	ObligationID  uuid.UUID
	TransactionID uuid.UUID
	AccountID     *uuid.UUID
	Amount        decimal.Decimal
	PaidOn        time.Time
	CreatedAt     time.Time
}

const paymentSelect = `
	SELECT p.id, p.obligation_id, p.transaction_id,
	       (SELECT e.account_id FROM transaction_entries e WHERE e.transaction_id = p.transaction_id AND e.account_id IS NOT NULL LIMIT 1),
	       p.amount::text, p.paid_on, p.created_at
	FROM obligation_payments p JOIN transactions t ON t.id = p.transaction_id`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	var amount string
	if err := row.Scan(&p.ID, &p.ObligationID, &p.TransactionID, &p.AccountID, &amount, &p.PaidOn, &p.CreatedAt); err != nil {
		return Payment{}, notFound(err)
	}
	p.Amount = decimal.RequireFromString(amount)
	return p, nil
}

func ListPayments(ctx context.Context, db DB, workspaceID, obligationID uuid.UUID) ([]Payment, error) {
	rows, err := db.Query(ctx, paymentSelect+
		` WHERE p.workspace_id = $1 AND p.obligation_id = $2 AND t.deleted_at IS NULL
		  ORDER BY p.paid_on DESC, p.created_at DESC`, workspaceID, obligationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetPayment(ctx context.Context, db DB, workspaceID, obligationID, id uuid.UUID) (Payment, error) {
	return scanPayment(db.QueryRow(ctx, paymentSelect+
		` WHERE p.workspace_id = $1 AND p.obligation_id = $2 AND p.id = $3 AND t.deleted_at IS NULL`,
		workspaceID, obligationID, id))
}

// GetPaymentByKey finds a payment by its idempotency key (deleted or not).
func GetPaymentByKey(ctx context.Context, db DB, workspaceID uuid.UUID, key string) (Payment, error) {
	return scanPayment(db.QueryRow(ctx, paymentSelect+
		` WHERE p.workspace_id = $1 AND p.idempotency_key = $2`, workspaceID, key))
}

func InsertPayment(ctx context.Context, db DB, workspaceID, obligationID, txID uuid.UUID, amount decimal.Decimal, paidOn time.Time, key *string, createdBy uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO obligation_payments (workspace_id, obligation_id, transaction_id, amount, paid_on, idempotency_key, created_by)
		 VALUES ($1, $2, $3, $4::numeric, $5, $6, $7) RETURNING id`,
		workspaceID, obligationID, txID, amount.String(), paidOn, key, createdBy).Scan(&id)
	if isUniqueViolation(err) {
		return uuid.Nil, ErrConflict
	}
	return id, err
}

// CountLivePayments counts payments whose transaction isn't deleted.
func CountLivePayments(ctx context.Context, db DB, workspaceID, obligationID uuid.UUID) (int, error) {
	var n int
	err := db.QueryRow(ctx,
		`SELECT count(*) FROM obligation_payments p JOIN transactions t ON t.id = p.transaction_id
		 WHERE p.workspace_id = $1 AND p.obligation_id = $2 AND t.deleted_at IS NULL`,
		workspaceID, obligationID).Scan(&n)
	return n, err
}

// FindContactByName returns an active contact with this name (any case).
func FindContactByName(ctx context.Context, db DB, workspaceID uuid.UUID, name string) (Contact, error) {
	var c Contact
	err := db.QueryRow(ctx,
		`SELECT id, name, notes, archived_at FROM contacts
		 WHERE workspace_id = $1 AND lower(name) = lower($2) AND archived_at IS NULL
		 ORDER BY created_at LIMIT 1`, workspaceID, name).Scan(&c.ID, &c.Name, &c.Notes, &c.ArchivedAt)
	return c, notFound(err)
}
