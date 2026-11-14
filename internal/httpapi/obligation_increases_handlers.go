package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// Increases make a debt grow after it started. The kind decides what the
// money side looks like:
//
//   - MONEY: more was borrowed (payable) or lent (receivable) through
//     account_id. A LOAN transaction moves the cash; it is neither income nor
//     expense.
//   - CHARGE: more was bought (payable) or sold (receivable) on credit, or
//     interest or a fee was added. An EXPENSE (or INCOME) transaction with an
//     optional category_id, and no cash, so it counts the day it happens.
//   - ADJUSTMENT: a correction. No transaction; nothing moves or counts.

type increaseRequest struct {
	Kind           store.IncreaseKind `json:"kind"`
	Amount         string             `json:"amount"`
	OccurredOn     string             `json:"occurred_on"`
	OccurredTime   *string            `json:"occurred_time"`
	Description    string             `json:"description"`
	AccountID      *uuid.UUID         `json:"account_id"`
	CategoryID     *uuid.UUID         `json:"category_id"`
	ExchangeRate   string             `json:"exchange_rate"`
	IdempotencyKey string             `json:"idempotency_key"`
}

// increase checks a request against the debt and returns the increase plus
// the transaction it needs (nil for ADJUSTMENT).
func (b *txBuilder) increase(ctx context.Context, o store.Obligation, kind store.IncreaseKind, req increaseRequest) (store.Increase, *store.Transaction, error) {
	in := store.Increase{ObligationID: o.ID, Kind: kind}
	if !kind.Valid() {
		return in, nil, badRequest("kind must be MONEY, CHARGE or ADJUSTMENT")
	}
	if kind != store.IncreaseMoney && req.AccountID != nil {
		return in, nil, badRequest("account_id is only for kind MONEY")
	}
	if kind != store.IncreaseCharge && req.CategoryID != nil {
		return in, nil, badRequest("category_id is only for kind CHARGE")
	}
	day, err := parseDate("occurred_on", req.OccurredOn)
	if err != nil {
		return in, nil, err
	}
	b.day, in.OccurredOn = day, day
	if in.OccurredTime, err = parseTimeOfDay("occurred_time", req.OccurredTime); err != nil {
		return in, nil, err
	}
	if in.Description, err = cleanName(req.Description, "description", false, 200); err != nil {
		return in, nil, err
	}
	cur := b.curs[o.Currency]
	if in.Amount, err = parseAmount("amount", req.Amount, cur, false); err != nil {
		return in, nil, err
	}
	if kind == store.IncreaseAdjustment {
		return in, nil, nil
	}

	desc := in.Description
	if desc == "" {
		desc = o.Description
	}
	// Signs from the workspace's side: a payable growing is negative.
	sign := decimal.NewFromInt(1)
	if o.Direction == store.Payable {
		sign = sign.Neg()
	}
	rate, err := b.rate(ctx, o.Currency, req.ExchangeRate)
	if err != nil {
		return in, nil, err
	}
	basePrec := b.curs[b.m.BaseCurrency].Precision
	entry := func(accountID, obligationID *uuid.UUID, amount decimal.Decimal) store.Entry {
		return store.Entry{AccountID: accountID, ObligationID: obligationID, Amount: amount, Currency: o.Currency,
			ExchangeRate: rate, BaseAmount: amount.Mul(rate).Round(basePrec)}
	}
	t := store.Transaction{OccurredOn: day, OccurredTime: in.OccurredTime, Description: desc, CreatedBy: userIDFrom(ctx)}
	if kind == store.IncreaseMoney {
		acc, err := b.account(ctx, req.AccountID, "account_id")
		if err != nil {
			return in, nil, err
		}
		if acc.Currency != o.Currency {
			return in, nil, txError{http.StatusBadRequest, "currency_mismatch", "the account's currency must be the debt's currency (" + o.Currency + ")"}
		}
		// Borrowing more: cash in (+), payable (-). Lending more: the reverse.
		t.Type = store.TxLoan
		t.Entries = []store.Entry{
			entry(&acc.ID, nil, in.Amount.Mul(sign).Neg()),
			entry(nil, &o.ID, in.Amount.Mul(sign)),
		}
	} else {
		catKind := store.CategoryExpense
		t.Type = store.TxExpense
		if o.Direction == store.Receivable {
			catKind, t.Type = store.CategoryIncome, store.TxIncome
		}
		if t.CategoryID, err = b.category(ctx, req.CategoryID, catKind); err != nil {
			return in, nil, err
		}
		t.Entries = []store.Entry{entry(nil, &o.ID, in.Amount.Mul(sign))}
	}
	return in, &t, nil
}

func belowPaid(o store.Obligation, cur store.Currency) error {
	return txError{http.StatusConflict, "below_paid",
		"the debt can't be less than what's already paid (" + formatAmount(o.PaidAmount, cur.Precision) + ")"}
}

func increaseNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return txError{http.StatusNotFound, "increase_not_found", "increase not found"}
	}
	return err
}

// POST /v1/workspaces/{workspaceID}/obligations/{obligationID}/increases (editor)
// {kind, amount, occurred_on, occurred_time?, description?, account_id?, category_id?,
// exchange_rate?, idempotency_key?}
func (a *api) handleCreateIncrease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	obID, ok := uuidParam(r, "obligationID")
	if !ok {
		a.writeObligationError(w, r, store.ErrNotFound)
		return
	}
	var req increaseRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	key, err := idempotencyKey(req.IdempotencyKey)
	if writeBadRequest(w, err) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	status := http.StatusCreated
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockObligation(ctx, tx, m.ID, obID); err != nil {
			return err
		}
		if key != nil {
			prev, err := store.GetIncreaseByKey(ctx, tx, m.ID, *key)
			if err == nil {
				if prev.ObligationID != obID {
					return txError{http.StatusConflict, "idempotency_key_reused", "idempotency_key was already used for another increase"}
				}
				status = http.StatusOK
				return nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		o, err := store.GetObligation(ctx, tx, m.ID, obID)
		if err != nil {
			return err
		}
		b := &txBuilder{a: a, db: tx, m: m, curs: curs}
		in, t, err := b.increase(ctx, o, req.Kind, req)
		if err != nil {
			return err
		}
		if t != nil {
			txID, err := store.InsertTransaction(ctx, tx, m.ID, *t)
			if err != nil {
				return err
			}
			in.TransactionID = &txID
		}
		if _, err := store.InsertIncrease(ctx, tx, m.ID, in, key, userIDFrom(ctx)); err != nil {
			return err
		}
		return b.saveRates(ctx, tx)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	a.writeObligation(w, r, status, obID)
}

// PUT /v1/workspaces/{workspaceID}/obligations/{obligationID}/increases/{increaseID} (editor)
// Same body as POST; the kind can't change (delete and record it again).
// Its transaction is rewritten in place. The debt can't drop below what's
// already been paid.
func (a *api) handleUpdateIncrease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	obID, ok1 := uuidParam(r, "obligationID")
	incID, ok2 := uuidParam(r, "increaseID")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "increase_not_found", "increase not found")
		return
	}
	var req increaseRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockObligation(ctx, tx, m.ID, obID); err != nil {
			return err
		}
		old, err := store.GetIncrease(ctx, tx, m.ID, obID, incID)
		if err != nil {
			return increaseNotFound(err)
		}
		if req.Kind != "" && req.Kind != old.Kind {
			return badRequest("kind can't change; delete the increase and record it again")
		}
		o, err := store.GetObligation(ctx, tx, m.ID, obID)
		if err != nil {
			return err
		}
		b := &txBuilder{a: a, db: tx, m: m, curs: curs, keepCatID: old.CategoryID}
		in, t, err := b.increase(ctx, o, old.Kind, req)
		if err != nil {
			return err
		}
		if o.Outstanding().Sub(old.Amount).Add(in.Amount).IsNegative() {
			return belowPaid(o, curs[o.Currency])
		}
		in.ID = old.ID
		if t != nil {
			if err := store.ReplaceTransaction(ctx, tx, m.ID, *old.TransactionID, *t); err != nil {
				return err
			}
		}
		if err := store.UpdateIncrease(ctx, tx, m.ID, in); err != nil {
			return err
		}
		return b.saveRates(ctx, tx)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	a.writeObligation(w, r, http.StatusOK, obID)
}

// DELETE /v1/workspaces/{workspaceID}/obligations/{obligationID}/increases/{increaseID} (editor)
// removes an increase and its transaction, unless that leaves the debt below
// what's already been paid.
func (a *api) handleDeleteIncrease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	obID, ok1 := uuidParam(r, "obligationID")
	incID, ok2 := uuidParam(r, "increaseID")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "increase_not_found", "increase not found")
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockObligation(ctx, tx, m.ID, obID); err != nil {
			return err
		}
		in, err := store.GetIncrease(ctx, tx, m.ID, obID, incID)
		if err != nil {
			return increaseNotFound(err)
		}
		o, err := store.GetObligation(ctx, tx, m.ID, obID)
		if err != nil {
			return err
		}
		if o.Outstanding().Sub(in.Amount).IsNegative() {
			return belowPaid(o, curs[o.Currency])
		}
		return store.SoftDeleteIncrease(ctx, tx, m.ID, in)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	a.writeObligation(w, r, http.StatusOK, obID)
}
