package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// period reads ?from and ?to (dates), defaulting to the month of today.
func (a *api) period(r *http.Request) (time.Time, time.Time, error) {
	today := a.today(r)
	from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	q := r.URL.Query()
	var err error
	if v := q.Get("from"); v != "" {
		if from, err = parseDate("from", v); err != nil {
			return from, to, err
		}
	}
	if v := q.Get("to"); v != "" {
		if to, err = parseDate("to", v); err != nil {
			return from, to, err
		}
	}
	if to.Before(from) {
		return from, to, badRequest("to cannot be before from")
	}
	return from, to, nil
}

type currencyAmountDTO struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// converter turns amounts into the base currency with the latest saved rates.
type converter struct {
	base    string
	rates   map[string]decimal.Decimal
	curs    map[string]store.Currency
	missing map[string]bool
}

func (c *converter) toBase(currency string, amount decimal.Decimal) decimal.Decimal {
	if currency == c.base {
		return amount
	}
	rate, ok := c.rates[currency]
	if !ok {
		c.missing[currency] = true
		return decimal.Zero
	}
	return amount.Mul(rate).Round(c.curs[c.base].Precision)
}

func (c *converter) byCurrency(totals map[string]decimal.Decimal) ([]currencyAmountDTO, decimal.Decimal) {
	out := []currencyAmountDTO{}
	sum := decimal.Zero
	for code, amount := range totals {
		out = append(out, currencyAmountDTO{code, formatAmount(amount, c.curs[code].Precision)})
		sum = sum.Add(c.toBase(code, amount))
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Currency == c.base) != (out[j].Currency == c.base) {
			return out[i].Currency == c.base
		}
		return out[i].Currency < out[j].Currency
	})
	return out, sum
}

type debtSideDTO struct {
	Total        string              `json:"total"` // base currency
	ByCurrency   []currencyAmountDTO `json:"by_currency"`
	Count        int                 `json:"count"`
	OverdueCount int                 `json:"overdue_count"`
}

// GET /v1/workspaces/{workspaceID}/summary?from&to&today
//
// The home dashboard: money in accounts, income and expenses in the period
// (default: this month), open payables and receivables, and the next debts
// due. Totals are in the base currency using the latest saved rates;
// currencies without a rate are listed in missing_rates and left out.
func (a *api) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	from, to, err := a.period(r)
	if writeBadRequest(w, err) {
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	rates, err := store.ListLatestRates(ctx, a.db, m.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	conv := &converter{base: m.BaseCurrency, rates: map[string]decimal.Decimal{}, curs: curs, missing: map[string]bool{}}
	for _, rt := range rates {
		conv.rates[rt.FromCurrency] = rt.Rate
	}

	accounts, err := store.ListAccounts(ctx, a.db, m.ID, false)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	balances := map[string]decimal.Decimal{}
	for _, acc := range accounts {
		balances[acc.Currency] = balances[acc.Currency].Add(acc.Balance)
	}
	balanceList, balanceTotal := conv.byCurrency(balances)

	period, err := store.PeriodIncomeExpense(ctx, a.db, m.ID, from, to)
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	open, err := store.ListObligations(ctx, a.db, m.ID, store.ObligationFilter{Open: true})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	today := a.today(r)
	side := func(dir store.Direction) (debtSideDTO, decimal.Decimal) {
		totals := map[string]decimal.Decimal{}
		dto := debtSideDTO{}
		for _, o := range open {
			if o.Direction != dir {
				continue
			}
			totals[o.Currency] = totals[o.Currency].Add(o.Outstanding())
			dto.Count++
			if o.DueOn != nil && o.DueOn.Before(today) {
				dto.OverdueCount++
			}
		}
		var sum decimal.Decimal
		dto.ByCurrency, sum = conv.byCurrency(totals)
		dto.Total = formatAmount(sum, curs[m.BaseCurrency].Precision)
		return dto, sum
	}
	payables, payTotal := side(store.Payable)
	receivables, recTotal := side(store.Receivable)

	// Next debts due: open ones with a due date, soonest (and overdue) first.
	upcoming := []obligationDTO{}
	for _, o := range open { // already ordered by due_on
		if o.DueOn != nil && len(upcoming) < 5 {
			upcoming = append(upcoming, toObligationDTO(o, curs[o.Currency], today))
		}
	}

	missing := []string{}
	for code := range conv.missing {
		missing = append(missing, code)
	}
	sort.Strings(missing)
	p := curs[m.BaseCurrency].Precision
	writeJSON(w, http.StatusOK, map[string]any{
		"base_currency": m.BaseCurrency,
		"from":          from.Format(time.DateOnly),
		"to":            to.Format(time.DateOnly),
		"balance": map[string]any{
			"total":       formatAmount(balanceTotal, p),
			"by_currency": balanceList,
		},
		"income":      formatAmount(period.Income, p),
		"expenses":    formatAmount(period.Expenses, p),
		"net":         formatAmount(period.Income.Sub(period.Expenses), p),
		"payables":    payables,
		"receivables": receivables,
		// What the workspace has after paying what it owes and collecting
		// what it's owed.
		"net_worth":     formatAmount(balanceTotal.Add(recTotal).Sub(payTotal), p),
		"upcoming_due":  upcoming,
		"missing_rates": missing,
	})
}

// GET /v1/workspaces/{workspaceID}/reports/categories?type=EXPENSE|INCOME&from&to&parent_id
//
// Subcategories count toward their parent. parent_id breaks one parent down
// into its subcategories (and itself, for transactions filed under it).
func (a *api) handleCategoryReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	typ := store.TxType(r.URL.Query().Get("type"))
	if typ == "" {
		typ = store.TxExpense
	}
	if typ != store.TxExpense && typ != store.TxIncome {
		writeBadRequest(w, badRequest("type must be EXPENSE or INCOME"))
		return
	}
	from, to, err := a.period(r)
	if writeBadRequest(w, err) {
		return
	}
	var within *uuid.UUID
	if v := r.URL.Query().Get("parent_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeBadRequest(w, badRequest("parent_id must be a category id"))
			return
		}
		within = &id
	}
	rows, err := store.CategoryTotals(ctx, a.db, m.ID, typ, from, to, within)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	p := curs[m.BaseCurrency].Precision
	type row struct {
		CategoryID *uuid.UUID `json:"category_id"`
		Name       *string    `json:"name"`
		Total      string     `json:"total"`
		Share      string     `json:"share"` // 0..1 of the period total
		Count      int        `json:"count"`
	}
	sum := decimal.Zero
	for _, c := range rows {
		sum = sum.Add(c.Total)
	}
	out := make([]row, len(rows))
	for i, c := range rows {
		share := decimal.Zero
		if sum.IsPositive() {
			share = c.Total.DivRound(sum, 4)
		}
		out[i] = row{c.CategoryID, c.Name, formatAmount(c.Total, p), share.StringFixed(4), c.Count}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": typ, "from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly),
		"base_currency": m.BaseCurrency, "total": formatAmount(sum, p), "data": out,
	})
}

// GET /v1/workspaces/{workspaceID}/reports/monthly?months=6&today
// Income and expenses per month for the last N months (1-24), oldest first,
// including months with nothing.
func (a *api) handleMonthlyReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	months := 6
	if v := r.URL.Query().Get("months"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 24 {
			writeBadRequest(w, badRequest("months must be between 1 and 24"))
			return
		}
		months = n
	}
	today := a.today(r)
	end := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, -(months - 1), 0)
	rows, err := store.MonthlyIncomeExpense(ctx, a.db, m.ID, start, end.AddDate(0, 1, -1))
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	p := curs[m.BaseCurrency].Precision
	byMonth := map[string]store.IncomeExpense{}
	for _, row := range rows {
		byMonth[row.Month.Format("2006-01")] = row.IncomeExpense
	}
	type monthDTO struct {
		Month    string `json:"month"`
		Income   string `json:"income"`
		Expenses string `json:"expenses"`
		Net      string `json:"net"`
	}
	out := make([]monthDTO, months)
	for i := range months {
		key := start.AddDate(0, i, 0).Format("2006-01")
		ie := byMonth[key]
		out[i] = monthDTO{key, formatAmount(ie.Income, p), formatAmount(ie.Expenses, p), formatAmount(ie.Income.Sub(ie.Expenses), p)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"base_currency": m.BaseCurrency, "data": out})
}

// GET /v1/workspaces/{workspaceID}/transactions/totals?from&to&type&account_id
//
// The totals strip above a transaction list (default period: this month).
// Without account_id: income and expenses in the base currency (type may
// narrow it to INCOME or EXPENSE). With account_id: money in and out of that
// account in its currency, every type counted unless type narrows it; "net"
// is then the balance change over the period.
func (a *api) handleTransactionTotals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := membershipFrom(ctx)
	q := r.URL.Query()
	from, to, err := a.period(r)
	if writeBadRequest(w, err) {
		return
	}
	var typ *store.TxType
	if v := q.Get("type"); v != "" {
		t := store.TxType(v)
		if !t.Known() {
			writeBadRequest(w, badRequest("type must be INCOME, EXPENSE, TRANSFER, SETTLEMENT or LOAN"))
			return
		}
		typ = &t
	}
	currency := m.BaseCurrency
	var accountID *uuid.UUID
	if v := q.Get("account_id"); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			writeBadRequest(w, badRequest("account_id must be a UUID"))
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
		accountID, currency = &id, acc.Currency
	}
	totals, err := store.PeriodTotals(ctx, a.db, m.ID, accountID, typ, from, to)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	curs, err := a.currencies.get(ctx, a.db)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	p := curs[currency].Precision
	writeJSON(w, http.StatusOK, map[string]any{
		"from":     from.Format(time.DateOnly),
		"to":       to.Format(time.DateOnly),
		"currency": currency,
		"income":   formatAmount(totals.Income, p),
		"expenses": formatAmount(totals.Expenses, p),
		"net":      formatAmount(totals.Income.Sub(totals.Expenses), p),
	})
}
