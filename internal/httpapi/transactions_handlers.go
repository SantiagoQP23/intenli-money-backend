package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// txRequest is the body for creating or replacing a transaction.
//
// INCOME / EXPENSE: account_id, amount (positive), category_id (optional).
// TRANSFER: from_account_id, to_account_id, amount (leaves from_account),
// to_amount (arrives; required when the currencies differ).
//
// exchange_rate is "1 <account currency> = rate <base currency>". It is only
// used when that currency isn't the base; when omitted, the latest saved rate
// on or before occurred_on is used. For a transfer it applies to the
// from_account currency.
type txRequest struct {
	Type          store.TxType `json:"type"`
	OccurredOn    string       `json:"occurred_on"`
	OccurredTime  *string      `json:"occurred_time"` // "15:04", optional
	Description   string       `json:"description"`
	CategoryID    *uuid.UUID   `json:"category_id"`
	BoxID         *uuid.UUID   `json:"box_id"` // income and expenses only, same currency as the account
	AccountID     *uuid.UUID   `json:"account_id"`
	FromAccountID *uuid.UUID   `json:"from_account_id"`
	ToAccountID   *uuid.UUID   `json:"to_account_id"`
	Amount        string       `json:"amount"`
	ToAmount      string       `json:"to_amount"`
	ExchangeRate  string       `json:"exchange_rate"`
}

// txError is a client error with its own code and status.
type txError struct {
	status  int
	code    string
	message string
}

func (e txError) Error() string { return e.message }

type rateToSave struct {
	currency string
	rate     decimal.Decimal
}

// txBuilder turns a txRequest into a store.Transaction, checking every
// reference against the workspace.
type txBuilder struct {
	a          *api
	db         store.DB
	m          store.Membership
	curs       map[string]store.Currency
	day        time.Time
	keepCatID  *uuid.UUID // an archived category the transaction already had may stay
	keepBoxID  *uuid.UUID // likewise for an archived box
	ratesToSet []rateToSave
}

func (b *txBuilder) account(ctx context.Context, id *uuid.UUID, field string) (store.Account, error) {
	if id == nil {
		return store.Account{}, badRequest("%s is required", field)
	}
	acc, err := store.GetAccount(ctx, b.db, b.m.ID, *id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Account{}, txError{http.StatusBadRequest, "account_not_found", field + " is not an account in this workspace"}
	}
	if err != nil {
		return store.Account{}, err
	}
	if acc.ArchivedAt != nil {
		return store.Account{}, txError{http.StatusConflict, "account_archived", field + " is archived"}
	}
	return acc, nil
}

// rate returns the rate from currency to base for the transaction date.
func (b *txBuilder) rate(ctx context.Context, currency, provided string) (decimal.Decimal, error) {
	if currency == b.m.BaseCurrency {
		return decimal.NewFromInt(1), nil
	}
	if provided != "" {
		r, err := parseRate("exchange_rate", provided)
		if err != nil {
			return decimal.Decimal{}, err
		}
		b.ratesToSet = append(b.ratesToSet, rateToSave{currency, r})
		return r, nil
	}
	r, err := store.LatestRate(ctx, b.db, b.m.ID, currency, b.day)
	if errors.Is(err, store.ErrNotFound) {
		return decimal.Decimal{}, txError{http.StatusBadRequest, "exchange_rate_required",
			"exchange_rate is required: there is no saved rate for " + currency + " to " + b.m.BaseCurrency}
	}
	return r, err
}

func (b *txBuilder) build(ctx context.Context, req txRequest) (store.Transaction, error) {
	if !req.Type.Valid() {
		return store.Transaction{}, badRequest("type must be INCOME, EXPENSE or TRANSFER")
	}
	day, err := parseDate("occurred_on", req.OccurredOn)
	if err != nil {
		return store.Transaction{}, err
	}
	b.day = day
	tod, err := parseTimeOfDay("occurred_time", req.OccurredTime)
	if err != nil {
		return store.Transaction{}, err
	}
	desc, err := cleanName(req.Description, "description", false, 200)
	if err != nil {
		return store.Transaction{}, err
	}
	base := b.curs[b.m.BaseCurrency]
	t := store.Transaction{Type: req.Type, OccurredOn: day, OccurredTime: tod, Description: desc}

	if req.Type == store.TxTransfer {
		if req.AccountID != nil || req.CategoryID != nil || req.BoxID != nil {
			return t, badRequest("a transfer uses from_account_id and to_account_id and has no category or box")
		}
		from, err := b.account(ctx, req.FromAccountID, "from_account_id")
		if err != nil {
			return t, err
		}
		to, err := b.account(ctx, req.ToAccountID, "to_account_id")
		if err != nil {
			return t, err
		}
		if from.ID == to.ID {
			return t, badRequest("from_account_id and to_account_id must be different")
		}
		fromCur, toCur := b.curs[from.Currency], b.curs[to.Currency]
		amount, err := parseAmount("amount", req.Amount, fromCur, false)
		if err != nil {
			return t, err
		}
		toAmount := amount
		switch {
		case req.ToAmount != "":
			if toAmount, err = parseAmount("to_amount", req.ToAmount, toCur, false); err != nil {
				return t, err
			}
		case from.Currency != to.Currency:
			return t, badRequest("to_amount is required when the accounts have different currencies")
		}

		// Both sides carry the same base value so a transfer nets to zero
		// in the base currency.
		var baseValue, fromRate decimal.Decimal
		switch {
		case from.Currency == b.m.BaseCurrency:
			baseValue, fromRate = amount, decimal.NewFromInt(1)
		case to.Currency == b.m.BaseCurrency:
			baseValue = toAmount
			fromRate = toAmount.DivRound(amount, 10)
		default:
			if fromRate, err = b.rate(ctx, from.Currency, req.ExchangeRate); err != nil {
				return t, err
			}
			baseValue = amount.Mul(fromRate).Round(base.Precision)
		}
		toRate := baseValue.DivRound(toAmount, 10)
		if to.Currency == b.m.BaseCurrency {
			toRate = decimal.NewFromInt(1)
		}
		t.Entries = []store.Entry{
			{AccountID: &from.ID, Amount: amount.Neg(), Currency: from.Currency, ExchangeRate: fromRate, BaseAmount: baseValue.Neg()},
			{AccountID: &to.ID, Amount: toAmount, Currency: to.Currency, ExchangeRate: toRate, BaseAmount: baseValue},
		}
		return t, nil
	}

	if req.FromAccountID != nil || req.ToAccountID != nil || req.ToAmount != "" {
		return t, badRequest("income and expenses use account_id and amount")
	}
	acc, err := b.account(ctx, req.AccountID, "account_id")
	if err != nil {
		return t, err
	}
	amount, err := parseAmount("amount", req.Amount, b.curs[acc.Currency], false)
	if err != nil {
		return t, err
	}
	wantKind := store.CategoryExpense
	if req.Type == store.TxIncome {
		wantKind = store.CategoryIncome
	}
	if t.CategoryID, err = b.category(ctx, req.CategoryID, wantKind); err != nil {
		return t, err
	}
	if t.BoxID, err = b.box(ctx, req.BoxID, acc.Currency); err != nil {
		return t, err
	}
	rate, err := b.rate(ctx, acc.Currency, req.ExchangeRate)
	if err != nil {
		return t, err
	}
	signed := amount
	if req.Type == store.TxExpense {
		signed = amount.Neg()
	}
	t.Entries = []store.Entry{{
		AccountID:    &acc.ID,
		Amount:       signed,
		Currency:     acc.Currency,
		ExchangeRate: rate,
		BaseAmount:   signed.Mul(rate).Round(base.Precision),
	}}
	return t, nil
}

// category checks an optional category id against the workspace and kind.
func (b *txBuilder) category(ctx context.Context, id *uuid.UUID, kind store.CategoryKind) (*uuid.UUID, error) {
	if id == nil {
		return nil, nil
	}
	cat, err := store.GetCategory(ctx, b.db, b.m.ID, *id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, txError{http.StatusBadRequest, "category_not_found", "category_id is not a category in this workspace"}
	}
	if err != nil {
		return nil, err
	}
	if cat.Kind != kind {
		return nil, txError{http.StatusBadRequest, "category_kind_mismatch", "category kind does not match the transaction type"}
	}
	if cat.ArchivedAt != nil && (b.keepCatID == nil || *b.keepCatID != cat.ID) {
		return nil, txError{http.StatusConflict, "category_archived", "category is archived"}
	}
	return &cat.ID, nil
}

func (b *txBuilder) saveRates(ctx context.Context, db store.DB) error {
	for _, r := range b.ratesToSet {
		if err := store.SaveRate(ctx, db, b.m.ID, r.currency, b.day, r.rate); err != nil {
			return err
		}
	}
	return nil
}

func (a *api) writeTxError(w http.ResponseWriter, r *http.Request, err error) {
	var te txError
	switch {
	case writeBadRequest(w, err):
	case errors.As(err, &te):
		writeError(w, te.status, te.code, te.message)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "transaction_not_found", "transaction not found")
	default:
		a.internalError(w, r, err)
	}
}

// ---- DTOs ----

type entryDTO struct {
	AccountID    *uuid.UUID `json:"account_id"`
	ObligationID *uuid.UUID `json:"obligation_id"`
	Amount       string     `json:"amount"`
	Currency     string     `json:"currency"`
	ExchangeRate string     `json:"exchange_rate"`
	BaseAmount   string     `json:"base_amount"`
}

type transactionDTO struct {
	ID         uuid.UUID    `json:"id"`
	Type       store.TxType `json:"type"`
	OccurredOn string       `json:"occurred_on"`
	// OccurredTime is the local time of day ("15:04"), or null when unknown.
	OccurredTime *string    `json:"occurred_time"`
	Description  string     `json:"description"`
	CategoryID   *uuid.UUID `json:"category_id"`
	BoxID        *uuid.UUID `json:"box_id"`
	// Amount is the positive amount in the (from) account's currency, and
	// BaseAmount the same in the workspace base currency.
	Amount     string     `json:"amount"`
	Currency   string     `json:"currency"`
	BaseAmount string     `json:"base_amount"`
	Entries    []entryDTO `json:"entries"`
	// ObligationID is set when an obligation manages this transaction.
	ObligationID *uuid.UUID `json:"obligation_id"`
	CreatedBy    uuid.UUID  `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func toTransactionDTO(t store.Transaction, curs map[string]store.Currency, base string) transactionDTO {
	dto := transactionDTO{
		ID: t.ID, Type: t.Type, OccurredOn: t.OccurredOn.Format(time.DateOnly), OccurredTime: t.OccurredTime,
		Description: t.Description,
		CategoryID:  t.CategoryID, BoxID: t.BoxID, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Entries: make([]entryDTO, len(t.Entries)),
	}
	basePrec := curs[base].Precision
	for i, e := range t.Entries {
		p := curs[e.Currency].Precision
		dto.Entries[i] = entryDTO{
			AccountID:    e.AccountID,
			ObligationID: e.ObligationID,
			Amount:       formatAmount(e.Amount, p),
			Currency:     e.Currency,
			ExchangeRate: e.ExchangeRate.String(),
			BaseAmount:   formatAmount(e.BaseAmount, basePrec),
		}
	}
	if len(t.Entries) > 0 {
		// Income and expense count every entry (a purchase on credit is part
		// cash, part payable). Other types show the first account movement.
		main := t.Entries[0]
		amount, baseAmount := main.Amount, main.BaseAmount
		if t.Type == store.TxIncome || t.Type == store.TxExpense {
			amount, baseAmount = decimal.Zero, decimal.Zero
			for _, e := range t.Entries {
				amount = amount.Add(e.Amount)
				baseAmount = baseAmount.Add(e.BaseAmount)
			}
		}
		dto.Amount = formatAmount(amount.Abs(), curs[main.Currency].Precision)
		dto.Currency = main.Currency
		dto.BaseAmount = formatAmount(baseAmount.Abs(), basePrec)
		for _, e := range t.Entries {
			if e.ObligationID != nil {
				dto.ObligationID = e.ObligationID
			}
		}
	}
	return dto
}

// ---- Handlers ----

// GET /v1/workspaces/{workspaceID}/transactions
// Filters: account_id, category_id (a parent includes its subcategories),
// box_id, type, from, to (dates). Paging: limit
// (default 50, max 200) and cursor (next_cursor from the previous page).
func (a *api) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	q := r.URL.Query()
	f := store.TxFilter{Limit: 50}
	var err error
	parseID := func(key string) *uuid.UUID {
		if v := q.Get(key); v != "" && err == nil {
			id, perr := uuid.Parse(v)
			if perr != nil {
				err = badRequest("%s must be a UUID", key)
				return nil
			}
			return &id
		}
		return nil
	}
	parseDay := func(key string) *time.Time {
		if v := q.Get(key); v != "" && err == nil {
			d, perr := parseDate(key, v)
			if perr != nil {
				err = perr
				return nil
			}
			return &d
		}
		return nil
	}
	f.AccountID = parseID("account_id")
	f.CategoryID = parseID("category_id")
	f.BoxID = parseID("box_id")
	f.From = parseDay("from")
	f.To = parseDay("to")
	if v := q.Get("type"); v != "" && err == nil {
		t := store.TxType(v)
		if !t.Known() {
			err = badRequest("type must be INCOME, EXPENSE, TRANSFER, SETTLEMENT or LOAN")
		}
		f.Type = &t
	}
	if v := q.Get("limit"); v != "" && err == nil {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 200 {
			err = badRequest("limit must be between 1 and 200")
		}
		f.Limit = n
	}
	if v := q.Get("cursor"); v != "" && err == nil {
		f.After, err = decodeCursor(v)
	}
	if writeBadRequest(w, err) {
		return
	}

	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	limit := f.Limit
	f.Limit++ // fetch one extra to know if there's another page
	txs, err := store.ListTransactions(ctx, a.db, m.ID, f)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	var next *string
	if len(txs) > limit {
		txs = txs[:limit]
		last := txs[limit-1]
		sortTime := "00:00"
		if last.OccurredTime != nil {
			sortTime = *last.OccurredTime
		}
		c := encodeCursor(store.TxCursor{OccurredOn: last.OccurredOn, SortTime: sortTime, CreatedAt: last.CreatedAt, ID: last.ID})
		next = &c
	}
	out := make([]transactionDTO, len(txs))
	for i, t := range txs {
		out[i] = toTransactionDTO(t, curs, m.BaseCurrency)
	}
	writeJSON(w, http.StatusOK, struct {
		Data       []transactionDTO `json:"data"`
		NextCursor *string          `json:"next_cursor"`
	}{out, next})
}

type cursorJSON struct {
	D string    `json:"d"`
	T string    `json:"t,omitempty"`
	C time.Time `json:"c"`
	I uuid.UUID `json:"i"`
}

func encodeCursor(c store.TxCursor) string {
	b, _ := json.Marshal(cursorJSON{D: c.OccurredOn.Format(time.DateOnly), T: c.SortTime, C: c.CreatedAt, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (*store.TxCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	var cj cursorJSON
	if err == nil {
		err = json.Unmarshal(b, &cj)
	}
	day, derr := time.Parse(time.DateOnly, cj.D)
	if cj.T == "" {
		cj.T = "00:00" // cursors from before transactions had a time
	}
	_, terr := time.Parse("15:04", cj.T)
	if err != nil || derr != nil || terr != nil {
		return nil, badRequest("cursor is invalid")
	}
	return &store.TxCursor{OccurredOn: day, SortTime: cj.T, CreatedAt: cj.C, ID: cj.I}, nil
}

func (a *api) writeTransaction(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	t, err := store.GetTransaction(ctx, a.db, m.ID, id)
	if err != nil {
		a.writeTxError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, status, toTransactionDTO(t, curs, m.BaseCurrency))
}

// GET /v1/workspaces/{workspaceID}/transactions/{transactionID}
func (a *api) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "transactionID")
	if !ok {
		writeError(w, http.StatusNotFound, "transaction_not_found", "transaction not found")
		return
	}
	a.writeTransaction(w, r, http.StatusOK, id)
}

// POST /v1/workspaces/{workspaceID}/transactions (editor)
func (a *api) handleCreateTransaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req txRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	var id uuid.UUID
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		b := &txBuilder{a: a, db: tx, m: membershipFrom(ctx), curs: curs}
		t, err := b.build(ctx, req)
		if err != nil {
			return err
		}
		t.CreatedBy = userIDFrom(ctx)
		if id, err = store.InsertTransaction(ctx, tx, b.m.ID, t); err != nil {
			return err
		}
		return b.saveRates(ctx, tx)
	})
	if err != nil {
		a.writeTxError(w, r, err)
		return
	}
	a.writeTransaction(w, r, http.StatusCreated, id)
}

// PUT /v1/workspaces/{workspaceID}/transactions/{transactionID} (editor)
// replaces the transaction with the same body as POST.
func (a *api) handleReplaceTransaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "transactionID")
	if !ok {
		writeError(w, http.StatusNotFound, "transaction_not_found", "transaction not found")
		return
	}
	var req txRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockTransaction(ctx, tx, m.ID, id); err != nil {
			return err
		}
		if err := guardManaged(ctx, tx, m.ID, id); err != nil {
			return err
		}
		current, err := store.GetTransaction(ctx, tx, m.ID, id)
		if err != nil {
			return err
		}
		b := &txBuilder{a: a, db: tx, m: m, curs: curs, keepCatID: current.CategoryID, keepBoxID: current.BoxID}
		t, err := b.build(ctx, req)
		if err != nil {
			return err
		}
		if err := store.ReplaceTransaction(ctx, tx, m.ID, id, t); err != nil {
			return err
		}
		return b.saveRates(ctx, tx)
	})
	if err != nil {
		a.writeTxError(w, r, err)
		return
	}
	a.writeTransaction(w, r, http.StatusOK, id)
}

// DELETE /v1/workspaces/{workspaceID}/transactions/{transactionID} (editor)
func (a *api) handleDeleteTransaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := uuidParam(r, "transactionID")
	if !ok {
		writeError(w, http.StatusNotFound, "transaction_not_found", "transaction not found")
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		wsID := membershipFrom(ctx).ID
		if err := store.LockTransaction(ctx, tx, wsID, id); err != nil {
			return err
		}
		if err := guardManaged(ctx, tx, wsID, id); err != nil {
			return err
		}
		return store.SoftDeleteTransaction(ctx, tx, wsID, id)
	})
	if err != nil {
		a.writeTxError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errManagedByObligation = txError{http.StatusConflict, "managed_by_obligation",
	"this transaction belongs to a debt; change it from the debt or its payments"}

func guardManaged(ctx context.Context, db store.DB, workspaceID, id uuid.UUID) error {
	managed, err := store.TransactionManagedByObligation(ctx, db, workspaceID, id)
	if err != nil {
		return err
	}
	if managed {
		return errManagedByObligation
	}
	return nil
}
