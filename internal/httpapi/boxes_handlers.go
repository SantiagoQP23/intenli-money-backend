package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// Boxes say what money is for; accounts say where it is. Per currency,
// wallet (unassigned) + every active box = every active account, always.

var (
	errBoxNotFound = txError{http.StatusNotFound, "box_not_found", "box not found"}
	errBoxArchived = txError{http.StatusConflict, "box_archived", "box is archived"}
	errBoxNameTake = txError{http.StatusConflict, "box_name_taken", "another box already has this name"}
)

// ---- DTOs ----

type boxMonthDTO struct {
	// Spent is what linked movements took from the box this month (negative
	// when more came in than went out).
	Spent string `json:"spent"`
	// Progress is spent / monthly_amount, for the bar; null without a
	// monthly amount.
	Progress *float64 `json:"progress"`
	// NeedsFill is true when a budget holds less than its monthly amount.
	NeedsFill bool `json:"needs_fill"`
}

type boxGoalDTO struct {
	Amount   string  `json:"amount"`
	Date     *string `json:"date"`
	Progress float64 `json:"progress"` // balance / amount
	Missing  string  `json:"missing"`  // what's left to save, never negative
	PerMonth *string `json:"per_month"`
	Months   *int    `json:"months"`
}

type boxDTO struct {
	ID            uuid.UUID     `json:"id"`
	Kind          store.BoxKind `json:"kind"`
	Name          string        `json:"name"`
	Icon          *string       `json:"icon"`
	Currency      string        `json:"currency"`
	MonthlyAmount *string       `json:"monthly_amount"`
	GoalAmount    *string       `json:"goal_amount"`
	GoalDate      *string       `json:"goal_date"`
	CategoryIDs   []uuid.UUID   `json:"category_ids"`
	Archived      bool          `json:"archived"`
	Balance       string        `json:"balance"`
	Month         boxMonthDTO   `json:"month"`
	Goal          *boxGoalDTO   `json:"goal"`
	CreatedAt     time.Time     `json:"created_at"`
}

type walletDTO struct {
	Currency      string `json:"currency"`
	AccountsTotal string `json:"accounts_total"`
	InBoxes       string `json:"in_boxes"`
	Unassigned    string `json:"unassigned"`
}

func ratio(a, b decimal.Decimal) float64 {
	f, _ := a.DivRound(b, 4).Float64()
	return f
}

func toBoxDTO(b store.Box, cur store.Currency, today time.Time) boxDTO {
	p := cur.Precision
	dto := boxDTO{
		ID: b.ID, Kind: b.Kind, Name: b.Name, Icon: b.Icon, Currency: b.Currency, CategoryIDs: b.CategoryIDs,
		Archived: b.ArchivedAt != nil, Balance: formatAmount(b.Balance, p), CreatedAt: b.CreatedAt,
	}
	spent := b.MonthNet.Neg()
	dto.Month.Spent = formatAmount(spent, p)
	if b.MonthlyAmount != nil {
		s := formatAmount(*b.MonthlyAmount, p)
		dto.MonthlyAmount = &s
		pr := ratio(spent, *b.MonthlyAmount)
		dto.Month.Progress = &pr
		dto.Month.NeedsFill = b.ArchivedAt == nil && b.Balance.LessThan(*b.MonthlyAmount)
	}
	if b.GoalDate != nil {
		s := b.GoalDate.Format(time.DateOnly)
		dto.GoalDate = &s
	}
	if b.GoalAmount != nil {
		s := formatAmount(*b.GoalAmount, p)
		dto.GoalAmount = &s
		missing := decimal.Max(b.GoalAmount.Sub(b.Balance), decimal.Zero)
		g := &boxGoalDTO{Amount: s, Date: dto.GoalDate, Progress: ratio(b.Balance, *b.GoalAmount), Missing: formatAmount(missing, p)}
		if b.GoalDate != nil {
			// Months to save in: from this month up to the goal's month.
			months := (b.GoalDate.Year()-today.Year())*12 + int(b.GoalDate.Month()-today.Month())
			if months < 1 {
				months = 1
			}
			per := formatAmount(missing.Div(decimal.NewFromInt(int64(months))).RoundCeil(p), p)
			g.PerMonth, g.Months = &per, &months
		}
		dto.Goal = g
	}
	return dto
}

// wallets totals active accounts and active boxes per currency, base first.
func wallets(accounts []store.Account, boxes []store.Box, curs map[string]store.Currency, base string) []walletDTO {
	type sums struct{ acc, box decimal.Decimal }
	by := map[string]*sums{}
	get := func(c string) *sums {
		if by[c] == nil {
			by[c] = &sums{}
		}
		return by[c]
	}
	for _, a := range accounts {
		if a.ArchivedAt == nil {
			s := get(a.Currency)
			s.acc = s.acc.Add(a.Balance)
		}
	}
	for _, b := range boxes {
		if b.ArchivedAt == nil {
			s := get(b.Currency)
			s.box = s.box.Add(b.Balance)
		}
	}
	codes := make([]string, 0, len(by))
	for c := range by {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		if (codes[i] == base) != (codes[j] == base) {
			return codes[i] == base
		}
		return codes[i] < codes[j]
	})
	out := make([]walletDTO, len(codes))
	for i, c := range codes {
		s, p := by[c], curs[c].Precision
		out[i] = walletDTO{Currency: c, AccountsTotal: formatAmount(s.acc, p), InBoxes: formatAmount(s.box, p), Unassigned: formatAmount(s.acc.Sub(s.box), p)}
	}
	return out
}

type boxesOverview struct {
	Wallets []walletDTO `json:"wallets"`
	Data    []boxDTO    `json:"data"`
}

func (a *api) boxesOverview(ctx context.Context, db store.DB, m store.Membership, includeArchived bool, today time.Time) (boxesOverview, error) {
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		return boxesOverview{}, err
	}
	accounts, err := store.ListAccounts(ctx, db, m.ID, false)
	if err != nil {
		return boxesOverview{}, err
	}
	boxes, err := store.ListBoxes(ctx, db, m.ID, includeArchived, today)
	if err != nil {
		return boxesOverview{}, err
	}
	out := boxesOverview{Wallets: wallets(accounts, boxes, curs, m.BaseCurrency), Data: make([]boxDTO, len(boxes))}
	for i, b := range boxes {
		out.Data[i] = toBoxDTO(b, curs[b.Currency], today)
	}
	return out, nil
}

func (a *api) writeBoxError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		err = errBoxNotFound
	}
	a.writeTxError(w, r, err)
}

// ---- Linking (used by transactions and debt payments) ----

// box checks an optional box for a transaction moving money in currency.
// An archived box is refused unless the transaction already had it.
func (b *txBuilder) box(ctx context.Context, id *uuid.UUID, currency string) (*uuid.UUID, error) {
	if id == nil {
		return nil, nil
	}
	bx, err := store.GetBox(ctx, b.db, b.m.ID, *id, b.day)
	if errors.Is(err, store.ErrNotFound) {
		return nil, txError{http.StatusBadRequest, "box_not_found", "box_id is not a box in this workspace"}
	}
	if err != nil {
		return nil, err
	}
	if bx.ArchivedAt != nil && (b.keepBoxID == nil || *b.keepBoxID != bx.ID) {
		return nil, errBoxArchived
	}
	if bx.Currency != currency {
		return nil, txError{http.StatusBadRequest, "box_currency_mismatch", "the box's currency (" + bx.Currency + ") must be the account's (" + currency + ")"}
	}
	return &bx.ID, nil
}

// ---- Box handlers ----

// GET /v1/workspaces/{workspaceID}/boxes[?include_archived=true&today=YYYY-MM-DD]
// returns the wallets (unassigned money per currency) and the boxes, with
// this month's spending from today's month.
func (a *api) handleListBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out, err := a.boxesOverview(ctx, a.db, membershipFrom(ctx), r.URL.Query().Get("include_archived") == "true", a.today(r))
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type boxHistoryDTO struct {
	// Type is ALLOCATION (money assigned, released, moved or filled by hand)
	// or TRANSACTION (a linked expense, income or debt payment).
	Type         string    `json:"type"`
	ID           uuid.UUID `json:"id"`
	OccurredOn   string    `json:"occurred_on"`
	OccurredTime *string   `json:"occurred_time"`
	// Amount is signed from the box's side: + into it, - out of it.
	Amount      string                `json:"amount"`
	Kind        *store.AllocationKind `json:"kind,omitempty"`
	Note        string                `json:"note,omitempty"`
	OtherBoxID  *uuid.UUID            `json:"other_box_id,omitempty"`
	Transaction *transactionDTO       `json:"transaction,omitempty"`
	createdAt   time.Time
}

// GET /v1/workspaces/{workspaceID}/boxes/{boxID}[?today=] returns the box
// with its history (the latest 100 allocations and 100 linked transactions,
// merged, newest first).
func (a *api) handleGetBox(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "boxID")
	if !ok {
		a.writeBoxError(w, r, store.ErrNotFound)
		return
	}
	a.writeBox(w, r, http.StatusOK, id)
}

func (a *api) writeBox(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	today := a.today(r)
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	b, err := store.GetBox(ctx, a.db, m.ID, id, today)
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	allocs, err := store.ListAllocations(ctx, a.db, m.ID, id, 100)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	txs, err := store.ListTransactions(ctx, a.db, m.ID, store.TxFilter{BoxID: &id, Limit: 100})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	p := curs[b.Currency].Precision
	history := make([]boxHistoryDTO, 0, len(allocs)+len(txs))
	for _, x := range allocs {
		kind := x.Kind
		history = append(history, boxHistoryDTO{
			Type: "ALLOCATION", ID: x.ID, OccurredOn: x.OccurredOn.Format(time.DateOnly), Amount: formatAmount(x.Amount, p),
			Kind: &kind, Note: x.Note, OtherBoxID: x.OtherBoxID, createdAt: x.CreatedAt,
		})
	}
	for _, t := range txs {
		effect := decimal.Zero
		for _, e := range t.Entries {
			if e.AccountID != nil {
				effect = effect.Add(e.Amount)
			}
		}
		dto := toTransactionDTO(t, curs, m.BaseCurrency)
		history = append(history, boxHistoryDTO{
			Type: "TRANSACTION", ID: t.ID, OccurredOn: t.OccurredOn.Format(time.DateOnly), OccurredTime: t.OccurredTime,
			Amount: formatAmount(effect, p), Transaction: &dto, createdAt: t.CreatedAt,
		})
	}
	sortKey := func(h boxHistoryDTO) string {
		tod := "00:00"
		if h.OccurredTime != nil {
			tod = *h.OccurredTime
		}
		return h.OccurredOn + " " + tod
	}
	sort.SliceStable(history, func(i, j int) bool {
		ki, kj := sortKey(history[i]), sortKey(history[j])
		if ki != kj {
			return ki > kj
		}
		return history[i].createdAt.After(history[j].createdAt)
	})
	writeJSON(w, status, struct {
		boxDTO
		History []boxHistoryDTO `json:"history"`
	}{toBoxDTO(b, curs[b.Currency], today), history})
}

// boxRequest is the body for creating or replacing a box.
type boxRequest struct {
	Kind          store.BoxKind `json:"kind"`
	Name          string        `json:"name"`
	Icon          *string       `json:"icon"`
	Currency      string        `json:"currency"` // defaults to the base currency
	MonthlyAmount *string       `json:"monthly_amount"`
	GoalAmount    *string       `json:"goal_amount"`
	GoalDate      *string       `json:"goal_date"`
	CategoryIDs   []uuid.UUID   `json:"category_ids"`
}

func (a *api) parseBox(ctx context.Context, db store.DB, m store.Membership, req boxRequest) (store.Box, error) {
	var b store.Box
	if !req.Kind.Valid() {
		return b, badRequest("kind must be SAVINGS or BUDGET")
	}
	name, err := cleanName(req.Name, "name", true, 60)
	if err != nil {
		return b, err
	}
	if err := checkIcon(req.Icon, true); err != nil {
		return b, err
	}
	icon := req.Icon
	if icon != nil && *icon == "" {
		icon = nil
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		return b, err
	}
	code := req.Currency
	if code == "" {
		code = m.BaseCurrency
	}
	cur, ok := curs[code]
	if !ok {
		return b, badRequest("currency is not supported")
	}
	b = store.Box{Kind: req.Kind, Name: name, Icon: icon, Currency: code}
	optAmount := func(field string, raw *string) (*decimal.Decimal, error) {
		if raw == nil || *raw == "" {
			return nil, nil
		}
		d, err := parseAmount(field, *raw, cur, false)
		return &d, err
	}
	if req.Kind == store.BoxBudget {
		if req.GoalAmount != nil || req.GoalDate != nil {
			return b, badRequest("a budget has monthly_amount, not a goal")
		}
		if b.MonthlyAmount, err = optAmount("monthly_amount", req.MonthlyAmount); err != nil {
			return b, err
		}
	} else {
		if req.MonthlyAmount != nil {
			return b, badRequest("savings have a goal, not monthly_amount")
		}
		if b.GoalAmount, err = optAmount("goal_amount", req.GoalAmount); err != nil {
			return b, err
		}
		if b.GoalDate, err = parseOptionalDate("goal_date", req.GoalDate); err != nil {
			return b, err
		}
	}
	if len(req.CategoryIDs) > 100 {
		return b, badRequest("category_ids has too many items")
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range req.CategoryIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		c, err := store.GetCategory(ctx, db, m.ID, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && c.ArchivedAt != nil) {
			return b, txError{http.StatusBadRequest, "category_not_found", "category_ids has a category that isn't active in this workspace"}
		}
		if err != nil {
			return b, err
		}
		b.CategoryIDs = append(b.CategoryIDs, id)
	}
	return b, nil
}

// POST /v1/workspaces/{workspaceID}/boxes (editor)
// {kind, name, icon?, currency?, monthly_amount?, goal_amount?, goal_date?, category_ids?}
func (a *api) handleCreateBox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req boxRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	var id uuid.UUID
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		b, err := a.parseBox(ctx, tx, m, req)
		if err != nil {
			return err
		}
		id, err = store.CreateBox(ctx, tx, m.ID, b, userIDFrom(ctx))
		if errors.Is(err, store.ErrConflict) {
			return errBoxNameTake
		}
		if err != nil {
			return err
		}
		return store.SetBoxCategories(ctx, tx, m.ID, id, b.CategoryIDs)
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	a.writeBox(w, r, http.StatusCreated, id)
}

// PUT /v1/workspaces/{workspaceID}/boxes/{boxID} (editor), same body as
// POST. The currency can only change while the box has no history.
func (a *api) handleUpdateBox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "boxID")
	if !ok {
		a.writeBoxError(w, r, store.ErrNotFound)
		return
	}
	var req boxRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockBox(ctx, tx, m.ID, id); err != nil {
			return err
		}
		cur, err := store.GetBox(ctx, tx, m.ID, id, a.today(r))
		if err != nil {
			return err
		}
		if cur.ArchivedAt != nil {
			return errBoxArchived
		}
		b, err := a.parseBox(ctx, tx, m, req)
		if err != nil {
			return err
		}
		if b.Currency != cur.Currency {
			has, err := store.BoxHasHistory(ctx, tx, m.ID, id)
			if err != nil {
				return err
			}
			if has {
				return txError{http.StatusConflict, "box_has_history", "the currency can't change once the box has movements"}
			}
		}
		b.ID = id
		if err := store.UpdateBox(ctx, tx, m.ID, b); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return errBoxNameTake
			}
			return err
		}
		return store.SetBoxCategories(ctx, tx, m.ID, id, b.CategoryIDs)
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	a.writeBox(w, r, http.StatusOK, id)
}

// POST /v1/workspaces/{workspaceID}/boxes/{boxID}/archive (editor) returns
// the box's balance to the wallet and hides it. Its history stays.
func (a *api) handleArchiveBox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "boxID")
	if !ok {
		a.writeBoxError(w, r, store.ErrNotFound)
		return
	}
	today := a.today(r)
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockBox(ctx, tx, m.ID, id); err != nil {
			return err
		}
		b, err := store.GetBox(ctx, tx, m.ID, id, today)
		if err != nil {
			return err
		}
		if b.ArchivedAt != nil {
			return nil
		}
		if !b.Balance.IsZero() {
			if _, err := store.InsertAllocation(ctx, tx, m.ID, store.Allocation{
				BoxID: id, Kind: store.AllocArchive, Amount: b.Balance.Neg(), OccurredOn: today,
			}, nil, userIDFrom(ctx)); err != nil {
				return err
			}
		}
		if err := store.SetBoxCategories(ctx, tx, m.ID, id, nil); err != nil {
			return err
		}
		return store.SetBoxArchived(ctx, tx, m.ID, id, true)
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	a.writeBox(w, r, http.StatusOK, id)
}

// POST /v1/workspaces/{workspaceID}/boxes/{boxID}/restore (editor) brings an
// archived box back, empty.
func (a *api) handleRestoreBox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "boxID")
	if !ok {
		a.writeBoxError(w, r, store.ErrNotFound)
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := store.LockBox(ctx, tx, m.ID, id); err != nil {
			return err
		}
		err := store.SetBoxArchived(ctx, tx, m.ID, id, false)
		if errors.Is(err, store.ErrConflict) {
			return errBoxNameTake
		}
		return err
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	a.writeBox(w, r, http.StatusOK, id)
}

type allocationRequest struct {
	Amount         string  `json:"amount"`
	Note           string  `json:"note"`
	OccurredOn     *string `json:"occurred_on"`
	IdempotencyKey string  `json:"idempotency_key"`
}

func (a *api) parseAllocationCommon(r *http.Request, note string, occurredOn *string) (string, time.Time, error) {
	n, err := cleanName(note, "note", false, 200)
	if err != nil {
		return "", time.Time{}, err
	}
	day := a.today(r)
	if occurredOn != nil && *occurredOn != "" {
		if day, err = parseDate("occurred_on", *occurredOn); err != nil {
			return "", time.Time{}, err
		}
	}
	return n, day, nil
}

// activeBox loads and locks a box that can take allocations.
func activeBox(ctx context.Context, tx pgx.Tx, wsID, id uuid.UUID, day time.Time) (store.Box, error) {
	if err := store.LockBox(ctx, tx, wsID, id); err != nil {
		return store.Box{}, err
	}
	b, err := store.GetBox(ctx, tx, wsID, id, day)
	if err != nil {
		return b, err
	}
	if b.ArchivedAt != nil {
		return b, errBoxArchived
	}
	return b, nil
}

// POST /v1/workspaces/{workspaceID}/boxes/{boxID}/allocations (editor)
// {amount, note?, occurred_on?, idempotency_key?}: a positive amount assigns
// money from the wallet to the box, a negative one releases it back. The box
// or the wallet may go negative; the app shows it.
func (a *api) handleCreateAllocation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "boxID")
	if !ok {
		a.writeBoxError(w, r, store.ErrNotFound)
		return
	}
	var req allocationRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	note, day, err := a.parseAllocationCommon(r, req.Note, req.OccurredOn)
	if writeBadRequest(w, err) {
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
		b, err := activeBox(ctx, tx, m.ID, id, day)
		if err != nil {
			return err
		}
		amount, err := parseAmount("amount", req.Amount, curs[b.Currency], true)
		if err != nil {
			return err
		}
		if amount.IsZero() {
			return badRequest("amount must not be zero")
		}
		if key != nil {
			used, err := store.AllocationKeyUsed(ctx, tx, m.ID, *key)
			if err != nil {
				return err
			}
			if used {
				status = http.StatusOK
				return nil
			}
		}
		kind := store.AllocAssign
		if amount.IsNegative() {
			kind = store.AllocRelease
		}
		_, err = store.InsertAllocation(ctx, tx, m.ID, store.Allocation{
			BoxID: id, Kind: kind, Amount: amount, OccurredOn: day, Note: note,
		}, key, userIDFrom(ctx))
		return err
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	a.writeBox(w, r, status, id)
}

// DELETE /v1/workspaces/{workspaceID}/boxes/{boxID}/allocations/{allocationID}
// (editor) undoes an allocation; undoing one side of a move undoes both.
func (a *api) handleDeleteAllocation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok1 := uuidParam(r, "boxID")
	allocID, ok2 := uuidParam(r, "allocationID")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "allocation_not_found", "allocation not found")
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if _, err := activeBox(ctx, tx, m.ID, id, a.today(r)); err != nil {
			return err
		}
		x, err := store.GetAllocation(ctx, tx, m.ID, id, allocID)
		if errors.Is(err, store.ErrNotFound) {
			return txError{http.StatusNotFound, "allocation_not_found", "allocation not found"}
		}
		if err != nil {
			return err
		}
		if x.Kind == store.AllocArchive {
			return txError{http.StatusConflict, "allocation_locked", "this movement is undone by restoring the box"}
		}
		if x.OtherBoxID != nil {
			// The other box must still be active, and is locked too.
			if _, err := activeBox(ctx, tx, m.ID, *x.OtherBoxID, a.today(r)); err != nil {
				return err
			}
		}
		return store.SoftDeleteAllocation(ctx, tx, m.ID, x)
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	a.writeBox(w, r, http.StatusOK, id)
}

type moveRequest struct {
	FromBoxID      uuid.UUID `json:"from_box_id"`
	ToBoxID        uuid.UUID `json:"to_box_id"`
	Amount         string    `json:"amount"`
	Note           string    `json:"note"`
	OccurredOn     *string   `json:"occurred_on"`
	IdempotencyKey string    `json:"idempotency_key"`
}

// POST /v1/workspaces/{workspaceID}/boxes/moves (editor)
// {from_box_id, to_box_id, amount, note?, occurred_on?, idempotency_key?}
// moves money between two boxes of the same currency. Answers with the
// overview, like GET /boxes.
func (a *api) handleMoveBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req moveRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	note, day, err := a.parseAllocationCommon(r, req.Note, req.OccurredOn)
	if writeBadRequest(w, err) {
		return
	}
	key, err := idempotencyKey(req.IdempotencyKey)
	if writeBadRequest(w, err) {
		return
	}
	if req.FromBoxID == req.ToBoxID {
		writeError(w, http.StatusBadRequest, "invalid_request", "from_box_id and to_box_id must be different")
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	status := http.StatusCreated
	var out boxesOverview
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		// Lock in a fixed order so two opposite moves can't deadlock.
		first, second := req.FromBoxID, req.ToBoxID
		if first.String() > second.String() {
			first, second = second, first
		}
		boxes := map[uuid.UUID]store.Box{}
		for _, bid := range []uuid.UUID{first, second} {
			b, err := activeBox(ctx, tx, m.ID, bid, day)
			if errors.Is(err, store.ErrNotFound) {
				return txError{http.StatusBadRequest, "box_not_found", "the box is not in this workspace"}
			}
			if err != nil {
				return err
			}
			boxes[bid] = b
		}
		from, to := boxes[req.FromBoxID], boxes[req.ToBoxID]
		if from.Currency != to.Currency {
			return txError{http.StatusBadRequest, "box_currency_mismatch", "both boxes must have the same currency"}
		}
		amount, err := parseAmount("amount", req.Amount, curs[from.Currency], false)
		if err != nil {
			return err
		}
		if key != nil {
			used, err := store.AllocationKeyUsed(ctx, tx, m.ID, *key)
			if err != nil {
				return err
			}
			if used {
				status = http.StatusOK
				out, err = a.boxesOverview(ctx, tx, m, false, a.today(r))
				return err
			}
		}
		moveID := uuid.New()
		user := userIDFrom(ctx)
		if _, err := store.InsertAllocation(ctx, tx, m.ID, store.Allocation{
			BoxID: from.ID, Kind: store.AllocMove, Amount: amount.Neg(), MoveID: &moveID, OccurredOn: day, Note: note,
		}, key, user); err != nil {
			return err
		}
		if _, err := store.InsertAllocation(ctx, tx, m.ID, store.Allocation{
			BoxID: to.ID, Kind: store.AllocMove, Amount: amount, MoveID: &moveID, OccurredOn: day, Note: note,
		}, nil, user); err != nil {
			return err
		}
		out, err = a.boxesOverview(ctx, tx, m, false, a.today(r))
		return err
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	writeJSON(w, status, out)
}

type fillRequest struct {
	DryRun     bool    `json:"dry_run"`
	OccurredOn *string `json:"occurred_on"`
}

type fillItemDTO struct {
	BoxID    uuid.UUID `json:"box_id"`
	Name     string    `json:"name"`
	Currency string    `json:"currency"`
	Balance  string    `json:"balance"`
	Amount   string    `json:"amount"`
}

type fillTotalDTO struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	// WalletAfter is the unassigned money once the fill is done.
	WalletAfter string `json:"wallet_after"`
}

// POST /v1/workspaces/{workspaceID}/boxes/fill (editor) {dry_run?, occurred_on?}
// tops every active budget up to its monthly amount: a budget holding less
// gets the difference from the wallet, one holding more keeps it. With
// dry_run nothing is saved and the answer is the preview.
func (a *api) handleFillBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req fillRequest
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	_, day, err := a.parseAllocationCommon(r, "", req.OccurredOn)
	if writeBadRequest(w, err) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	items := []fillItemDTO{}
	totals := []fillTotalDTO{}
	err = store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT 1 FROM boxes WHERE workspace_id = $1 AND kind = 'BUDGET' AND archived_at IS NULL ORDER BY id FOR UPDATE`, m.ID); err != nil {
			return err
		}
		before, err := a.boxesOverview(ctx, tx, m, false, a.today(r))
		if err != nil {
			return err
		}
		boxes, err := store.ListBoxes(ctx, tx, m.ID, false, day)
		if err != nil {
			return err
		}
		sum := map[string]decimal.Decimal{}
		user := userIDFrom(ctx)
		for _, b := range boxes {
			if b.Kind != store.BoxBudget || b.MonthlyAmount == nil || !b.Balance.LessThan(*b.MonthlyAmount) {
				continue
			}
			amount := b.MonthlyAmount.Sub(b.Balance)
			p := curs[b.Currency].Precision
			items = append(items, fillItemDTO{BoxID: b.ID, Name: b.Name, Currency: b.Currency, Balance: formatAmount(b.Balance, p), Amount: formatAmount(amount, p)})
			sum[b.Currency] = sum[b.Currency].Add(amount)
			if !req.DryRun {
				if _, err := store.InsertAllocation(ctx, tx, m.ID, store.Allocation{
					BoxID: b.ID, Kind: store.AllocFill, Amount: amount, OccurredOn: day,
				}, nil, user); err != nil {
					return err
				}
			}
		}
		for _, wl := range before.Wallets {
			if s, ok := sum[wl.Currency]; ok {
				p := curs[wl.Currency].Precision
				after := decimal.RequireFromString(wl.Unassigned).Sub(s)
				totals = append(totals, fillTotalDTO{Currency: wl.Currency, Amount: formatAmount(s, p), WalletAfter: formatAmount(after, p)})
			}
		}
		return nil
	})
	if err != nil {
		a.writeBoxError(w, r, err)
		return
	}
	status := http.StatusOK
	if !req.DryRun && len(items) > 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, struct {
		Applied bool           `json:"applied"`
		Items   []fillItemDTO  `json:"items"`
		Totals  []fillTotalDTO `json:"totals"`
	}{!req.DryRun, items, totals})
}
