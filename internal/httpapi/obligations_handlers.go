package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// Obligations are what the workspace owes (PAYABLE) or is owed (RECEIVABLE).
//
// The accounting rule: a debt is recorded once, when it starts. A purchase on
// credit is an EXPENSE whose entries are the cash paid now (if any) plus the
// payable for the rest, so the full purchase counts as an expense that day.
// Paying the payable later is a SETTLEMENT (cash out, payable down) that nets
// to zero, so it is never a second expense. Sales mirror this with INCOME,
// and loans use LOAN transactions, which are neither income nor expense.

// ---- Contacts ----

type contactDTO struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Notes    string    `json:"notes"`
	Archived bool      `json:"archived"`
}

func toContactDTO(c store.Contact) contactDTO {
	return contactDTO{ID: c.ID, Name: c.Name, Notes: c.Notes, Archived: c.ArchivedAt != nil}
}

// GET /v1/workspaces/{workspaceID}/contacts
func (a *api) handleListContacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := store.ListContacts(ctx, a.db, membershipFrom(ctx).ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]contactDTO, len(list))
	for i, c := range list {
		out[i] = toContactDTO(c)
	}
	writeJSON(w, http.StatusOK, listDTO[contactDTO]{Data: out})
}

type contactRequest struct {
	Name     string `json:"name"`
	Notes    string `json:"notes"`
	Archived bool   `json:"archived"`
}

func (req contactRequest) clean() (string, string, error) {
	name, err := cleanName(req.Name, "name", true, 100)
	if err != nil {
		return "", "", err
	}
	notes, err := cleanName(req.Notes, "notes", false, 500)
	return name, notes, err
}

// POST /v1/workspaces/{workspaceID}/contacts (editor) {name, notes?}
func (a *api) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req contactRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	name, notes, err := req.clean()
	if writeBadRequest(w, err) {
		return
	}
	c, err := store.CreateContact(ctx, a.db, membershipFrom(ctx).ID, name, notes)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toContactDTO(c))
}

// PUT /v1/workspaces/{workspaceID}/contacts/{contactID} (editor)
// {name, notes, archived}
func (a *api) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "contactID")
	var req contactRequest
	if ok && writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	name, notes, err := req.clean()
	if ok && writeBadRequest(w, err) {
		return
	}
	if ok {
		err = store.UpdateContact(ctx, a.db, m.ID, id, name, notes, req.Archived)
	}
	if !ok || errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "contact_not_found", "contact not found")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	c, err := store.GetContact(ctx, a.db, m.ID, id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toContactDTO(c))
}

// ---- Obligation DTOs ----

type ObligationStatus string

const (
	StatusUnpaid        ObligationStatus = "UNPAID"
	StatusPartiallyPaid ObligationStatus = "PARTIALLY_PAID"
	StatusPaid          ObligationStatus = "PAID"
)

type obligationDTO struct {
	ID          uuid.UUID       `json:"id"`
	Direction   store.Direction `json:"direction"`
	Origin      store.Origin    `json:"origin"`
	Contact     *contactRefDTO  `json:"contact"`
	Currency    string          `json:"currency"`
	Description string          `json:"description"`
	OccurredOn  string          `json:"occurred_on"`
	DueOn       *string         `json:"due_on"`
	// OriginalAmount is what was owed when the debt started, Increased the
	// sum of later increases and Total both together. Paid is the sum of
	// payments and Outstanding what's left (Total - Paid).
	OriginalAmount      string           `json:"original_amount"`
	Increased           string           `json:"increased"`
	Total               string           `json:"total"`
	Paid                string           `json:"paid"`
	Outstanding         string           `json:"outstanding"`
	Status              ObligationStatus `json:"status"`
	Overdue             bool             `json:"overdue"`
	OriginTransactionID *uuid.UUID       `json:"origin_transaction_id"`
	CreatedAt           time.Time        `json:"created_at"`
	Payments            []paymentDTO     `json:"payments,omitempty"`
	Increases           []increaseDTO    `json:"increases,omitempty"`
}

type contactRefDTO struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type paymentDTO struct {
	ID            uuid.UUID  `json:"id"`
	TransactionID uuid.UUID  `json:"transaction_id"`
	AccountID     *uuid.UUID `json:"account_id"`
	Amount        string     `json:"amount"`
	PaidOn        string     `json:"paid_on"`
	PaidTime      *string    `json:"paid_time"`
	Description   string     `json:"description"`
	BoxID         *uuid.UUID `json:"box_id"`
	CreatedAt     time.Time  `json:"created_at"`
}

type increaseDTO struct {
	ID            uuid.UUID          `json:"id"`
	Kind          store.IncreaseKind `json:"kind"`
	TransactionID *uuid.UUID         `json:"transaction_id"`
	AccountID     *uuid.UUID         `json:"account_id"`
	CategoryID    *uuid.UUID         `json:"category_id"`
	Amount        string             `json:"amount"`
	OccurredOn    string             `json:"occurred_on"`
	OccurredTime  *string            `json:"occurred_time"`
	Description   string             `json:"description"`
	CreatedAt     time.Time          `json:"created_at"`
}

func toIncreaseDTO(in store.Increase, cur store.Currency) increaseDTO {
	return increaseDTO{
		ID: in.ID, Kind: in.Kind, TransactionID: in.TransactionID, AccountID: in.AccountID, CategoryID: in.CategoryID,
		Amount: formatAmount(in.Amount, cur.Precision), OccurredOn: in.OccurredOn.Format(time.DateOnly),
		OccurredTime: in.OccurredTime, Description: in.Description, CreatedAt: in.CreatedAt,
	}
}

func obligationStatus(o store.Obligation) ObligationStatus {
	switch {
	case !o.Outstanding().IsPositive():
		return StatusPaid
	case o.PaidAmount.IsPositive():
		return StatusPartiallyPaid
	default:
		return StatusUnpaid
	}
}

func toObligationDTO(o store.Obligation, cur store.Currency, today time.Time) obligationDTO {
	p := cur.Precision
	dto := obligationDTO{
		ID: o.ID, Direction: o.Direction, Origin: o.Origin, Currency: o.Currency, Description: o.Description,
		OccurredOn:          o.OccurredOn.Format(time.DateOnly),
		OriginalAmount:      formatAmount(o.OriginalAmount, p),
		Increased:           formatAmount(o.IncreasedAmount, p),
		Total:               formatAmount(o.Total(), p),
		Paid:                formatAmount(o.PaidAmount, p),
		Outstanding:         formatAmount(o.Outstanding(), p),
		Status:              obligationStatus(o),
		OriginTransactionID: o.OriginTransactionID,
		CreatedAt:           o.CreatedAt,
	}
	if o.ContactID != nil && o.ContactName != nil {
		dto.Contact = &contactRefDTO{ID: *o.ContactID, Name: *o.ContactName}
	}
	if o.DueOn != nil {
		s := o.DueOn.Format(time.DateOnly)
		dto.DueOn = &s
		dto.Overdue = dto.Status != StatusPaid && o.DueOn.Before(today)
	}
	return dto
}

func toPaymentDTO(pm store.Payment, cur store.Currency) paymentDTO {
	return paymentDTO{
		ID: pm.ID, TransactionID: pm.TransactionID, AccountID: pm.AccountID,
		Amount: formatAmount(pm.Amount, cur.Precision), PaidOn: pm.PaidOn.Format(time.DateOnly),
		PaidTime: pm.PaidTime, Description: pm.Description, BoxID: pm.BoxID, CreatedAt: pm.CreatedAt,
	}
}

// today is the date used to decide what's overdue. Clients may send their
// local date as ?today=YYYY-MM-DD, since the server runs in UTC.
func (a *api) today(r *http.Request) time.Time {
	if v := r.URL.Query().Get("today"); v != "" {
		if d, err := time.Parse(time.DateOnly, v); err == nil {
			return d
		}
	}
	y, mo, d := a.now().UTC().Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
}

func (a *api) writeObligationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "obligation_not_found", "debt not found")
		return
	}
	a.writeTxError(w, r, err)
}

// ---- Obligation handlers ----

// GET /v1/workspaces/{workspaceID}/obligations
// Filters: direction (PAYABLE|RECEIVABLE), contact_id, status (open|paid).
func (a *api) handleListObligations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	q := r.URL.Query()
	var f store.ObligationFilter
	if v := q.Get("direction"); v != "" {
		d := store.Direction(v)
		if d != store.Payable && d != store.Receivable {
			writeBadRequest(w, badRequest("direction must be PAYABLE or RECEIVABLE"))
			return
		}
		f.Direction = &d
	}
	if v := q.Get("contact_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeBadRequest(w, badRequest("contact_id must be a UUID"))
			return
		}
		f.ContactID = &id
	}
	switch q.Get("status") {
	case "":
	case "open":
		f.Open = true
	case "paid":
		f.Closed = true
	default:
		writeBadRequest(w, badRequest("status must be open or paid"))
		return
	}
	list, err := store.ListObligations(ctx, a.db, m.ID, f)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	today := a.today(r)
	out := make([]obligationDTO, len(list))
	for i, o := range list {
		out[i] = toObligationDTO(o, curs[o.Currency], today)
	}
	writeJSON(w, http.StatusOK, listDTO[obligationDTO]{Data: out})
}

func (a *api) writeObligation(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	o, err := store.GetObligation(ctx, a.db, m.ID, id)
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	payments, err := store.ListPayments(ctx, a.db, m.ID, id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	increases, err := store.ListIncreases(ctx, a.db, m.ID, id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	cur := curs[o.Currency]
	dto := toObligationDTO(o, cur, a.today(r))
	dto.Payments = make([]paymentDTO, len(payments))
	for i, pm := range payments {
		dto.Payments[i] = toPaymentDTO(pm, cur)
	}
	dto.Increases = make([]increaseDTO, len(increases))
	for i, in := range increases {
		dto.Increases[i] = toIncreaseDTO(in, cur)
	}
	writeJSON(w, status, dto)
}

// GET /v1/workspaces/{workspaceID}/obligations/{obligationID} (with payments)
func (a *api) handleGetObligation(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "obligationID")
	if !ok {
		a.writeObligationError(w, r, store.ErrNotFound)
		return
	}
	a.writeObligation(w, r, http.StatusOK, id)
}

// obligationRequest creates a debt.
//
// origin PURCHASE (payable) / SALE (receivable): amount is the full price;
// paid_now (optional, less than amount) moved through account_id today. The
// purchase is one EXPENSE (or the sale one INCOME) for the full amount with
// an optional category_id; the obligation is the unpaid rest.
//
// origin LOAN_BORROWED (payable) / LOAN_LENT (receivable): amount moves in or
// out of account_id as a LOAN transaction.
//
// origin OTHER: a debt that already exists (direction required); no money
// moves and nothing counts as income or expense.
//
// The debt's currency is the account's; without an account, pass currency
// (defaults to the base currency). A contact is contact_id, or contact_name
// to reuse or create one by name.
type obligationRequest struct {
	Origin       store.Origin    `json:"origin"`
	Direction    store.Direction `json:"direction"`
	ContactID    *uuid.UUID      `json:"contact_id"`
	ContactName  string          `json:"contact_name"`
	Currency     string          `json:"currency"`
	Amount       string          `json:"amount"`
	PaidNow      string          `json:"paid_now"`
	AccountID    *uuid.UUID      `json:"account_id"`
	CategoryID   *uuid.UUID      `json:"category_id"`
	Description  string          `json:"description"`
	OccurredOn   string          `json:"occurred_on"`
	DueOn        *string         `json:"due_on"`
	ExchangeRate string          `json:"exchange_rate"`
}

func (b *txBuilder) contact(ctx context.Context, id *uuid.UUID, name string) (*uuid.UUID, error) {
	if id != nil {
		c, err := store.GetContact(ctx, b.db, b.m.ID, *id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, txError{http.StatusBadRequest, "contact_not_found", "contact_id is not a contact in this workspace"}
		}
		return &c.ID, err
	}
	name, err := cleanName(name, "contact_name", false, 100)
	if err != nil || name == "" {
		return nil, err
	}
	c, err := store.FindContactByName(ctx, b.db, b.m.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		c, err = store.CreateContact(ctx, b.db, b.m.ID, name, "")
	}
	return &c.ID, err
}

func parseOptionalDate(field string, raw *string) (*time.Time, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	d, err := parseDate(field, *raw)
	return &d, err
}

// POST /v1/workspaces/{workspaceID}/obligations (editor)
func (a *api) handleCreateObligation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req obligationRequest
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
		b := &txBuilder{a: a, db: tx, m: m, curs: curs}
		var err error
		id, err = b.createObligation(ctx, req)
		if err != nil {
			return err
		}
		return b.saveRates(ctx, tx)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	a.writeObligation(w, r, http.StatusCreated, id)
}

func (b *txBuilder) createObligation(ctx context.Context, req obligationRequest) (uuid.UUID, error) {
	o := store.Obligation{Origin: req.Origin}
	var txType store.TxType
	switch req.Origin {
	case store.OriginPurchase:
		o.Direction, txType = store.Payable, store.TxExpense
	case store.OriginSale:
		o.Direction, txType = store.Receivable, store.TxIncome
	case store.OriginLoanBorrowed:
		o.Direction, txType = store.Payable, store.TxLoan
	case store.OriginLoanLent:
		o.Direction, txType = store.Receivable, store.TxLoan
	case store.OriginOther:
		if req.Direction != store.Payable && req.Direction != store.Receivable {
			return uuid.Nil, badRequest("direction must be PAYABLE or RECEIVABLE")
		}
		o.Direction = req.Direction
	default:
		return uuid.Nil, badRequest("origin must be PURCHASE, SALE, LOAN_BORROWED, LOAN_LENT or OTHER")
	}
	if req.Origin != store.OriginOther && req.Direction != "" && req.Direction != o.Direction {
		return uuid.Nil, badRequest("direction does not match origin %s", req.Origin)
	}
	isTrade := txType == store.TxExpense || txType == store.TxIncome
	if !isTrade && (req.CategoryID != nil || req.PaidNow != "") {
		return uuid.Nil, badRequest("category_id and paid_now are only for purchases and sales")
	}
	if req.Origin == store.OriginOther && req.AccountID != nil {
		return uuid.Nil, badRequest("a debt with origin OTHER moves no money and takes no account_id")
	}

	day, err := parseDate("occurred_on", req.OccurredOn)
	if err != nil {
		return uuid.Nil, err
	}
	b.day, o.OccurredOn = day, day
	if o.DueOn, err = parseOptionalDate("due_on", req.DueOn); err != nil {
		return uuid.Nil, err
	}
	if o.DueOn != nil && o.DueOn.Before(day) {
		return uuid.Nil, badRequest("due_on cannot be before occurred_on")
	}
	if o.Description, err = cleanName(req.Description, "description", false, 200); err != nil {
		return uuid.Nil, err
	}
	if o.ContactID, err = b.contact(ctx, req.ContactID, req.ContactName); err != nil {
		return uuid.Nil, err
	}

	// Currency: the account's, else the one given, else the base.
	var acc *store.Account
	needAccount := txType == store.TxLoan || (isTrade && req.PaidNow != "")
	if needAccount || req.AccountID != nil {
		a, err := b.account(ctx, req.AccountID, "account_id")
		if err != nil {
			return uuid.Nil, err
		}
		acc = &a
		if req.Currency != "" && req.Currency != a.Currency {
			return uuid.Nil, txError{http.StatusBadRequest, "currency_mismatch", "currency must match the account's currency"}
		}
		o.Currency = a.Currency
	} else {
		o.Currency = req.Currency
		if o.Currency == "" {
			o.Currency = b.m.BaseCurrency
		}
	}
	cur, ok := b.curs[o.Currency]
	if !ok {
		return uuid.Nil, txError{http.StatusBadRequest, "unsupported_currency", "currency is not supported"}
	}
	total, err := parseAmount("amount", req.Amount, cur, false)
	if err != nil {
		return uuid.Nil, err
	}
	paidNow := decimal.Zero
	if isTrade && req.PaidNow != "" {
		if paidNow, err = parseAmount("paid_now", req.PaidNow, cur, true); err != nil {
			return uuid.Nil, err
		}
		if paidNow.IsNegative() || paidNow.GreaterThanOrEqual(total) {
			return uuid.Nil, badRequest("paid_now must be at least zero and less than amount")
		}
	}
	o.OriginalAmount = total.Sub(paidNow)

	var catID *uuid.UUID
	if isTrade {
		kind := store.CategoryExpense
		if txType == store.TxIncome {
			kind = store.CategoryIncome
		}
		if catID, err = b.category(ctx, req.CategoryID, kind); err != nil {
			return uuid.Nil, err
		}
	}
	rate := decimal.NewFromInt(1)
	if o.Currency != b.m.BaseCurrency && req.Origin != store.OriginOther {
		if rate, err = b.rate(ctx, o.Currency, req.ExchangeRate); err != nil {
			return uuid.Nil, err
		}
	}

	userID := userIDFrom(ctx)
	id, err := store.InsertObligation(ctx, b.db, b.m.ID, o, userID)
	if err != nil {
		return uuid.Nil, err
	}
	if req.Origin == store.OriginOther {
		return id, nil
	}

	// Signs from the workspace's side: a payable (or cash out) is negative.
	sign := decimal.NewFromInt(1)
	if o.Direction == store.Payable {
		sign = sign.Neg()
	}
	basePrec := b.curs[b.m.BaseCurrency].Precision
	entry := func(accountID, obligationID *uuid.UUID, amount decimal.Decimal) store.Entry {
		return store.Entry{AccountID: accountID, ObligationID: obligationID, Amount: amount, Currency: o.Currency,
			ExchangeRate: rate, BaseAmount: amount.Mul(rate).Round(basePrec)}
	}
	t := store.Transaction{Type: txType, OccurredOn: day, Description: o.Description, CategoryID: catID, CreatedBy: userID}
	if txType == store.TxLoan {
		// Borrowing: cash in (+), payable (-). Lending: cash out (-), receivable (+).
		t.Entries = []store.Entry{
			entry(&acc.ID, nil, total.Mul(sign).Neg()),
			entry(nil, &id, total.Mul(sign)),
		}
	} else {
		// Purchase: cash out and payable, both negative (total = expense).
		// Sale: cash in and receivable, both positive (total = income).
		if paidNow.IsPositive() {
			t.Entries = append(t.Entries, entry(&acc.ID, nil, paidNow.Mul(sign)))
		}
		t.Entries = append(t.Entries, entry(nil, &id, o.OriginalAmount.Mul(sign)))
	}
	txID, err := store.InsertTransaction(ctx, b.db, b.m.ID, t)
	if err != nil {
		return uuid.Nil, err
	}
	return id, store.SetObligationOriginTransaction(ctx, b.db, b.m.ID, id, txID)
}

// PUT /v1/workspaces/{workspaceID}/obligations/{obligationID} (editor)
// {description, due_on, contact_id | contact_name}: replaces the details. The
// amount can't change; delete the debt and record it again instead.
func (a *api) handleUpdateObligation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "obligationID")
	if !ok {
		a.writeObligationError(w, r, store.ErrNotFound)
		return
	}
	var req struct {
		Description string     `json:"description"`
		DueOn       *string    `json:"due_on"`
		ContactID   *uuid.UUID `json:"contact_id"`
		ContactName string     `json:"contact_name"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockObligation(ctx, tx, m.ID, id); err != nil {
			return err
		}
		o, err := store.GetObligation(ctx, tx, m.ID, id)
		if err != nil {
			return err
		}
		desc, err := cleanName(req.Description, "description", false, 200)
		if err != nil {
			return err
		}
		due, err := parseOptionalDate("due_on", req.DueOn)
		if err != nil {
			return err
		}
		if due != nil && due.Before(o.OccurredOn) {
			return badRequest("due_on cannot be before occurred_on")
		}
		b := &txBuilder{a: a, db: tx, m: m}
		contactID, err := b.contact(ctx, req.ContactID, req.ContactName)
		if err != nil {
			return err
		}
		return store.UpdateObligationDetails(ctx, tx, m.ID, id, desc, due, contactID)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	a.writeObligation(w, r, http.StatusOK, id)
}

// DELETE /v1/workspaces/{workspaceID}/obligations/{obligationID} (editor)
// removes a debt recorded by mistake, with the transaction that created it
// and its increases. A debt with payments can't be deleted; delete the
// payments first, since they moved real money.
func (a *api) handleDeleteObligation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "obligationID")
	if !ok {
		a.writeObligationError(w, r, store.ErrNotFound)
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockObligation(ctx, tx, m.ID, id); err != nil {
			return err
		}
		n, err := store.CountLivePayments(ctx, tx, m.ID, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return txError{http.StatusConflict, "obligation_has_payments", "delete this debt's payments first"}
		}
		o, err := store.GetObligation(ctx, tx, m.ID, id)
		if err != nil {
			return err
		}
		increases, err := store.ListIncreases(ctx, tx, m.ID, id)
		if err != nil {
			return err
		}
		for _, in := range increases {
			if err := store.SoftDeleteIncrease(ctx, tx, m.ID, in); err != nil {
				return err
			}
		}
		if o.OriginTransactionID != nil {
			if err := store.SoftDeleteTransaction(ctx, tx, m.ID, *o.OriginTransactionID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		return store.SoftDeleteObligation(ctx, tx, m.ID, id)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Payments ----

type paymentRequest struct {
	AccountID      *uuid.UUID `json:"account_id"`
	Amount         string     `json:"amount"`
	PaidOn         string     `json:"paid_on"`
	PaidTime       *string    `json:"paid_time"`
	Description    string     `json:"description"`
	ExchangeRate   string     `json:"exchange_rate"`
	BoxID          *uuid.UUID `json:"box_id"` // pay from (or collect into) a box
	IdempotencyKey string     `json:"idempotency_key"`
}

// paymentTx checks a payment request against the debt and builds its
// SETTLEMENT transaction. max is the most the payment may be: what's
// outstanding, plus the payment's old amount when editing.
func (b *txBuilder) paymentTx(ctx context.Context, o store.Obligation, req paymentRequest, max decimal.Decimal) (store.Transaction, decimal.Decimal, error) {
	var t store.Transaction
	day, err := parseDate("paid_on", req.PaidOn)
	if err != nil {
		return t, decimal.Zero, err
	}
	b.day = day
	tod, err := parseTimeOfDay("paid_time", req.PaidTime)
	if err != nil {
		return t, decimal.Zero, err
	}
	acc, err := b.account(ctx, req.AccountID, "account_id")
	if err != nil {
		return t, decimal.Zero, err
	}
	if acc.Currency != o.Currency {
		return t, decimal.Zero, txError{http.StatusBadRequest, "currency_mismatch", "the account's currency must be the debt's currency (" + o.Currency + ")"}
	}
	cur := b.curs[o.Currency]
	amount, err := parseAmount("amount", req.Amount, cur, false)
	if err != nil {
		return t, decimal.Zero, err
	}
	if amount.GreaterThan(max) {
		return t, decimal.Zero, txError{http.StatusConflict, "overpayment",
			"amount is more than the outstanding " + formatAmount(max, cur.Precision)}
	}
	desc, err := cleanName(req.Description, "description", false, 200)
	if err != nil {
		return t, decimal.Zero, err
	}
	if desc == "" {
		desc = o.Description
	}
	boxID, err := b.box(ctx, req.BoxID, o.Currency)
	if err != nil {
		return t, decimal.Zero, err
	}
	rate, err := b.rate(ctx, o.Currency, req.ExchangeRate)
	if err != nil {
		return t, decimal.Zero, err
	}
	// Paying a payable: cash out (-), payable down (+). Collecting a
	// receivable: cash in (+), receivable down (-). Nets to zero.
	cash := amount
	if o.Direction == store.Payable {
		cash = amount.Neg()
	}
	base := cash.Mul(rate).Round(b.curs[b.m.BaseCurrency].Precision)
	t = store.Transaction{
		Type: store.TxSettlement, OccurredOn: day, OccurredTime: tod, Description: desc, BoxID: boxID, CreatedBy: userIDFrom(ctx),
		Entries: []store.Entry{
			{AccountID: &acc.ID, Amount: cash, Currency: o.Currency, ExchangeRate: rate, BaseAmount: base},
			{ObligationID: &o.ID, Amount: cash.Neg(), Currency: o.Currency, ExchangeRate: rate, BaseAmount: base.Neg()},
		},
	}
	return t, amount, nil
}

func idempotencyKey(raw string) (*string, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 100 {
		return nil, badRequest("idempotency_key must be at most 100 characters")
	}
	return &raw, nil
}

// POST /v1/workspaces/{workspaceID}/obligations/{obligationID}/payments (editor)
// {account_id, amount, paid_on, paid_time?, description?, exchange_rate?, idempotency_key?}
//
// Pays a payable from the account, or collects a receivable into it, as a
// SETTLEMENT transaction. The account's currency must be the debt's, and the
// amount can't exceed what's outstanding. Retrying with the same
// idempotency_key returns the first payment instead of paying twice.
func (a *api) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	obID, ok := uuidParam(r, "obligationID")
	if !ok {
		a.writeObligationError(w, r, store.ErrNotFound)
		return
	}
	var req paymentRequest
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
			prev, err := store.GetPaymentByKey(ctx, tx, m.ID, *key)
			if err == nil {
				if prev.ObligationID != obID {
					return txError{http.StatusConflict, "idempotency_key_reused", "idempotency_key was already used for another payment"}
				}
				status = http.StatusOK // a retry: answer with the debt as it is
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
		t, amount, err := b.paymentTx(ctx, o, req, o.Outstanding())
		if err != nil {
			return err
		}
		txID, err := store.InsertTransaction(ctx, tx, m.ID, t)
		if err != nil {
			return err
		}
		if _, err := store.InsertPayment(ctx, tx, m.ID, o.ID, txID, amount, t.OccurredOn, key, t.CreatedBy); err != nil {
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

// PUT /v1/workspaces/{workspaceID}/obligations/{obligationID}/payments/{paymentID} (editor)
// {account_id, amount, paid_on, paid_time?, description?, exchange_rate?}
//
// Changes a payment. Its SETTLEMENT transaction is rewritten in place (same
// id), so balances follow. The new amount can't exceed what's outstanding
// plus the payment's old amount.
func (a *api) handleUpdatePayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	obID, ok1 := uuidParam(r, "obligationID")
	pmID, ok2 := uuidParam(r, "paymentID")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "payment_not_found", "payment not found")
		return
	}
	var req paymentRequest
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
		pm, err := store.GetPayment(ctx, tx, m.ID, obID, pmID)
		if errors.Is(err, store.ErrNotFound) {
			return txError{http.StatusNotFound, "payment_not_found", "payment not found"}
		}
		if err != nil {
			return err
		}
		o, err := store.GetObligation(ctx, tx, m.ID, obID)
		if err != nil {
			return err
		}
		b := &txBuilder{a: a, db: tx, m: m, curs: curs, keepBoxID: pm.BoxID}
		t, amount, err := b.paymentTx(ctx, o, req, o.Outstanding().Add(pm.Amount))
		if err != nil {
			return err
		}
		if err := store.ReplaceTransaction(ctx, tx, m.ID, pm.TransactionID, t); err != nil {
			return err
		}
		if err := store.UpdatePayment(ctx, tx, m.ID, pm.ID, amount, t.OccurredOn); err != nil {
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

// DELETE /v1/workspaces/{workspaceID}/obligations/{obligationID}/payments/{paymentID} (editor)
// undoes a payment: its transaction is deleted and the amount is owed again.
func (a *api) handleDeletePayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	obID, ok1 := uuidParam(r, "obligationID")
	pmID, ok2 := uuidParam(r, "paymentID")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "payment_not_found", "payment not found")
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockObligation(ctx, tx, m.ID, obID); err != nil {
			return err
		}
		pm, err := store.GetPayment(ctx, tx, m.ID, obID, pmID)
		if errors.Is(err, store.ErrNotFound) {
			return txError{http.StatusNotFound, "payment_not_found", "payment not found"}
		}
		if err != nil {
			return err
		}
		return store.SoftDeleteTransaction(ctx, tx, m.ID, pm.TransactionID)
	})
	if err != nil {
		a.writeObligationError(w, r, err)
		return
	}
	a.writeObligation(w, r, http.StatusOK, obID)
}
