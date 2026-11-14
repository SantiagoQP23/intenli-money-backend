package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/defaults"
	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

type currencyDTO struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Symbol    string `json:"symbol"`
	Precision int32  `json:"precision"`
}

// GET /v1/currencies
func (a *api) handleListCurrencies(w http.ResponseWriter, r *http.Request) {
	list, err := store.ListCurrencies(r.Context(), a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]currencyDTO, len(list))
	for i, c := range list {
		out[i] = currencyDTO(c)
	}
	writeJSON(w, http.StatusOK, listDTO[currencyDTO]{Data: out})
}

type accountDTO struct {
	ID             uuid.UUID         `json:"id"`
	Name           string            `json:"name"`
	Type           store.AccountType `json:"type"`
	Currency       string            `json:"currency"`
	OpeningBalance string            `json:"opening_balance"`
	Balance        string            `json:"balance"`
	Archived       bool              `json:"archived"`
	CreatedAt      time.Time         `json:"created_at"`
}

func toAccountDTO(acc store.Account, cur store.Currency) accountDTO {
	return accountDTO{
		ID:             acc.ID,
		Name:           acc.Name,
		Type:           acc.Type,
		Currency:       acc.Currency,
		OpeningBalance: formatAmount(acc.OpeningBalance, cur.Precision),
		Balance:        formatAmount(acc.Balance, cur.Precision),
		Archived:       acc.ArchivedAt != nil,
		CreatedAt:      acc.CreatedAt,
	}
}

// GET /v1/workspaces/{workspaceID}/accounts[?include_archived=true]
func (a *api) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	list, err := store.ListAccounts(ctx, a.db, membershipFrom(ctx).ID, r.URL.Query().Get("include_archived") == "true")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]accountDTO, len(list))
	for i, acc := range list {
		out[i] = toAccountDTO(acc, curs[acc.Currency])
	}
	writeJSON(w, http.StatusOK, listDTO[accountDTO]{Data: out})
}

func (a *api) writeAccount(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	ctx := r.Context()
	acc, err := store.GetAccount(ctx, a.db, membershipFrom(ctx).ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, status, toAccountDTO(acc, curs[acc.Currency]))
}

// GET /v1/workspaces/{workspaceID}/accounts/{accountID}
func (a *api) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "accountID")
	if !ok {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	a.writeAccount(w, r, http.StatusOK, id)
}

// POST /v1/workspaces/{workspaceID}/accounts (editor)
func (a *api) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req struct {
		Name           string            `json:"name"`
		Type           store.AccountType `json:"type"`
		Currency       string            `json:"currency"`
		OpeningBalance string            `json:"opening_balance"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil {
		req.Name, err = cleanName(req.Name, "name", true, 100)
	}
	if err == nil && !req.Type.Valid() {
		err = badRequest("type must be CASH, BANK, SAVINGS, WALLET or CREDIT_CARD")
	}
	if writeBadRequest(w, err) {
		return
	}
	if req.Currency == "" {
		req.Currency = m.BaseCurrency
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	cur, ok := curs[req.Currency]
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported_currency", "currency is not supported")
		return
	}
	opening := decimal.Zero
	if req.OpeningBalance != "" {
		// Negative is allowed: a credit card or overdraft starts in debt.
		opening, err = parseAmount("opening_balance", req.OpeningBalance, cur, true)
		if writeBadRequest(w, err) {
			return
		}
	}
	id, err := store.CreateAccount(ctx, a.pool, m.ID, req.Name, req.Type, cur.Code, opening, userIDFrom(ctx))
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.writeAccount(w, r, http.StatusCreated, id)
}

// PATCH /v1/workspaces/{workspaceID}/accounts/{accountID} (editor)
// Any field may be omitted. Currency can't change once created.
func (a *api) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "accountID")
	if !ok {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	var req struct {
		Name           *string            `json:"name"`
		Type           *store.AccountType `json:"type"`
		OpeningBalance *string            `json:"opening_balance"`
		Archived       *bool              `json:"archived"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	acc, err := store.GetAccount(ctx, a.db, m.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	name, typ, opening := acc.Name, acc.Type, acc.OpeningBalance
	if req.Name != nil {
		name, err = cleanName(*req.Name, "name", true, 100)
	}
	if err == nil && req.Type != nil {
		if !req.Type.Valid() {
			err = badRequest("type must be CASH, BANK, SAVINGS, WALLET or CREDIT_CARD")
		}
		typ = *req.Type
	}
	if err == nil && req.OpeningBalance != nil {
		opening, err = parseAmount("opening_balance", *req.OpeningBalance, curs[acc.Currency], true)
	}
	if writeBadRequest(w, err) {
		return
	}
	if err := store.UpdateAccount(ctx, a.pool, m.ID, id, name, typ, opening); err != nil {
		a.internalError(w, r, err)
		return
	}
	if req.Archived != nil {
		if err := store.SetAccountArchived(ctx, a.pool, m.ID, id, *req.Archived); err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	a.writeAccount(w, r, http.StatusOK, id)
}

// ---- Categories ----

type categoryDTO struct {
	ID       uuid.UUID          `json:"id"`
	Kind     store.CategoryKind `json:"kind"`
	ParentID *uuid.UUID         `json:"parent_id"` // set on a subcategory
	Name     string             `json:"name"`
	Icon     *string            `json:"icon"`
	Archived bool               `json:"archived"`
}

func toCategoryDTO(c store.Category) categoryDTO {
	return categoryDTO{ID: c.ID, Kind: c.Kind, ParentID: c.ParentID, Name: c.Name, Icon: c.Icon, Archived: c.ArchivedAt != nil}
}

// checkIcon accepts a nil or known icon; "" is allowed when clearing is.
func checkIcon(icon *string, allowEmpty bool) error {
	if icon == nil || (allowEmpty && *icon == "") || defaults.ValidIcon(*icon) {
		return nil
	}
	return badRequest("icon is not one of the supported icons")
}

// GET /v1/category-icons: the icons a category may use, in picker order.
func (a *api) handleListCategoryIcons(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, listDTO[string]{Data: defaults.Icons})
}

// GET /v1/workspaces/{workspaceID}/categories[?include_archived=true]
func (a *api) handleListCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := store.ListCategories(ctx, a.db, membershipFrom(ctx).ID, r.URL.Query().Get("include_archived") == "true")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]categoryDTO, len(list))
	for i, c := range list {
		out[i] = toCategoryDTO(c)
	}
	writeJSON(w, http.StatusOK, listDTO[categoryDTO]{Data: out})
}

// checkParent loads a category that another one should sit under. It must be
// an active top-level category of kind (any kind when kind is "").
func (a *api) checkParent(ctx context.Context, workspaceID, id uuid.UUID, kind store.CategoryKind) (store.Category, error) {
	p, err := store.GetCategory(ctx, a.pool, workspaceID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return p, txError{http.StatusBadRequest, "parent_not_found", "parent_id is not a category in this workspace"}
	case err != nil:
		return p, err
	case p.ParentID != nil:
		return p, txError{http.StatusBadRequest, "parent_is_subcategory", "a subcategory can't have subcategories"}
	case kind != "" && p.Kind != kind:
		return p, txError{http.StatusBadRequest, "parent_kind_mismatch", "the parent category is of another kind"}
	case p.ArchivedAt != nil:
		return p, txError{http.StatusConflict, "parent_archived", "the parent category is archived"}
	}
	return p, nil
}

// writeCategoryError writes a txError or an internal error.
func (a *api) writeCategoryError(w http.ResponseWriter, r *http.Request, err error) {
	var te txError
	if errors.As(err, &te) {
		writeError(w, te.status, te.code, te.message)
		return
	}
	a.internalError(w, r, err)
}

// POST /v1/workspaces/{workspaceID}/categories (editor)
// {kind, name, icon?, parent_id?}. A subcategory takes its parent's kind
// when kind is omitted.
func (a *api) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req struct {
		Kind     store.CategoryKind `json:"kind"`
		Name     string             `json:"name"`
		Icon     *string            `json:"icon"`
		ParentID *uuid.UUID         `json:"parent_id"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil && req.Kind != store.CategoryIncome && req.Kind != store.CategoryExpense && !(req.Kind == "" && req.ParentID != nil) {
		err = badRequest("kind must be INCOME or EXPENSE")
	}
	if err == nil {
		err = checkIcon(req.Icon, false)
	}
	if err == nil {
		req.Name, err = cleanName(req.Name, "name", true, 60)
	}
	if writeBadRequest(w, err) {
		return
	}
	if req.ParentID != nil {
		p, err := a.checkParent(ctx, m.ID, *req.ParentID, req.Kind)
		if err != nil {
			a.writeCategoryError(w, r, err)
			return
		}
		req.Kind = p.Kind
	}
	c, err := store.CreateCategory(ctx, a.pool, m.ID, req.Kind, req.ParentID, req.Name, req.Icon)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "category_exists", "a category with this name already exists")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCategoryDTO(c))
}

// PATCH /v1/workspaces/{workspaceID}/categories/{categoryID} (editor)
// {name, icon?, parent_id?}. parent_id moves it: omit to keep, null to make
// it a top-level category, or a top-level category of the same kind. A
// category with subcategories can't move under another.
func (a *api) handleRenameCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	id, ok := uuidParam(r, "categoryID")
	if !ok {
		writeError(w, http.StatusNotFound, "category_not_found", "category not found")
		return
	}
	var req struct {
		Name     string          `json:"name"`
		Icon     *string         `json:"icon"` // omit to keep, "" to clear
		ParentID json.RawMessage `json:"parent_id"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil {
		req.Name, err = cleanName(req.Name, "name", true, 60)
	}
	if err == nil {
		err = checkIcon(req.Icon, true)
	}
	u := store.CategoryUpdate{Name: req.Name, Icon: req.Icon}
	if err == nil && req.ParentID != nil {
		var parent *uuid.UUID
		if json.Unmarshal(req.ParentID, &parent) != nil {
			err = badRequest("parent_id must be a category id or null")
		}
		u.MoveTo = &parent
	}
	if writeBadRequest(w, err) {
		return
	}
	if u.MoveTo != nil {
		if err := a.checkMove(ctx, m.ID, id, *u.MoveTo); err != nil {
			a.writeCategoryError(w, r, err)
			return
		}
	}
	switch err := store.UpdateCategory(ctx, a.pool, m.ID, id, u); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "category_not_found", "category not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "category_exists", "a category with this name already exists")
	case err != nil:
		a.internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// checkMove checks that category id may sit under parent (nil: top level).
func (a *api) checkMove(ctx context.Context, workspaceID, id uuid.UUID, parent *uuid.UUID) error {
	c, err := store.GetCategory(ctx, a.pool, workspaceID, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && c.ArchivedAt != nil) {
		return txError{http.StatusNotFound, "category_not_found", "category not found"}
	}
	if err != nil || parent == nil {
		return err
	}
	if *parent == id {
		return txError{http.StatusBadRequest, "parent_is_self", "a category can't be its own parent"}
	}
	if _, err := a.checkParent(ctx, workspaceID, *parent, c.Kind); err != nil {
		return err
	}
	has, err := store.HasSubcategories(ctx, a.pool, workspaceID, id)
	if err == nil && has {
		return txError{http.StatusConflict, "category_has_subcategories", "a category with subcategories can't become a subcategory"}
	}
	return err
}

// DELETE /v1/workspaces/{workspaceID}/categories/{categoryID} (editor)
// archives it, along with its subcategories.
func (a *api) handleArchiveCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := uuidParam(r, "categoryID")
	if !ok {
		writeError(w, http.StatusNotFound, "category_not_found", "category not found")
		return
	}
	switch err := store.ArchiveCategory(ctx, a.pool, membershipFrom(ctx).ID, id); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "category_not_found", "category not found")
	case err != nil:
		a.internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// POST /v1/workspaces/{workspaceID}/categories/{categoryID}/restore (editor)
// un-archives it, and the subcategories archived along with it.
func (a *api) handleRestoreCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := uuidParam(r, "categoryID")
	if !ok {
		writeError(w, http.StatusNotFound, "category_not_found", "category not found")
		return
	}
	err := store.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		return store.RestoreCategory(ctx, tx, membershipFrom(ctx).ID, id)
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "category_not_found", "archived category not found")
	case errors.Is(err, store.ErrParentArchived):
		writeError(w, http.StatusConflict, "parent_archived", "restore the parent category first")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "category_exists", "an active category with this name already exists")
	case err != nil:
		a.internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- Exchange rates ----

type rateDTO struct {
	Currency    string `json:"currency"`
	Rate        string `json:"rate"`
	EffectiveOn string `json:"effective_on"`
}

// GET /v1/workspaces/{workspaceID}/exchange-rates: the latest rate per currency.
func (a *api) handleListRates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := store.ListLatestRates(ctx, a.db, membershipFrom(ctx).ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]rateDTO, len(list))
	for i, rt := range list {
		out[i] = rateDTO{Currency: rt.FromCurrency, Rate: rt.Rate.String(), EffectiveOn: rt.EffectiveOn.Format(time.DateOnly)}
	}
	writeJSON(w, http.StatusOK, listDTO[rateDTO]{Data: out})
}

// PUT /v1/workspaces/{workspaceID}/exchange-rates (editor)
// {currency, rate, effective_on?}: 1 currency = rate base currency.
func (a *api) handleSetRate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	var req struct {
		Currency    string `json:"currency"`
		Rate        string `json:"rate"`
		EffectiveOn string `json:"effective_on"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if _, ok := curs[req.Currency]; !ok || req.Currency == m.BaseCurrency {
		writeError(w, http.StatusBadRequest, "unsupported_currency", "currency must be a supported currency other than the base currency")
		return
	}
	rate, err := parseRate("rate", req.Rate)
	day := a.now().UTC().Truncate(24 * time.Hour)
	if err == nil && req.EffectiveOn != "" {
		day, err = parseDate("effective_on", req.EffectiveOn)
	}
	if writeBadRequest(w, err) {
		return
	}
	if err := store.SaveRate(ctx, a.pool, m.ID, req.Currency, day, rate); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rateDTO{Currency: req.Currency, Rate: rate.String(), EffectiveOn: day.Format(time.DateOnly)})
}
