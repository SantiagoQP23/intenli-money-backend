package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// currencyCache holds the currencies table, which only changes with migrations.
type currencyCache struct {
	mu    sync.Mutex
	byKey map[string]store.Currency
}

func (c *currencyCache) get(ctx context.Context, db store.DB) (map[string]store.Currency, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKey != nil {
		return c.byKey, nil
	}
	list, err := store.ListCurrencies(ctx, db)
	if err != nil {
		return nil, err
	}
	m := make(map[string]store.Currency, len(list))
	for _, cur := range list {
		m[cur.Code] = cur
	}
	c.byKey = m
	return m, nil
}

// parseAmount parses a JSON money string. It must be positive (unless
// allowZeroOrNegative) and fit the currency's decimal places.
func parseAmount(field, raw string, cur store.Currency, allowZeroOrNegative bool) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Decimal{}, badRequest("%s must be a decimal number in a string, like \"12.50\"", field)
	}
	if !allowZeroOrNegative && !d.IsPositive() {
		return decimal.Decimal{}, badRequest("%s must be greater than zero", field)
	}
	if !d.Equal(d.Round(cur.Precision)) {
		return decimal.Decimal{}, badRequest("%s has more than %d decimal places for %s", field, cur.Precision, cur.Code)
	}
	if d.Abs().GreaterThanOrEqual(decimal.New(1, 15)) {
		return decimal.Decimal{}, badRequest("%s is too large", field)
	}
	return d, nil
}

func parseRate(field, raw string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(raw)
	if err != nil || !d.IsPositive() {
		return decimal.Decimal{}, badRequest("%s must be a positive decimal number in a string", field)
	}
	if d.GreaterThanOrEqual(decimal.New(1, 13)) {
		return decimal.Decimal{}, badRequest("%s is too large", field)
	}
	return d.Round(10), nil
}

func parseDate(field, raw string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return time.Time{}, badRequest("%s must be a date like 2026-11-13", field)
	}
	return t, nil
}

// parseTimeOfDay accepts an optional "15:04" time of day; nil or "" means none.
func parseTimeOfDay(field string, raw *string) (*string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	t, err := time.Parse("15:04", *raw)
	if err != nil {
		return nil, badRequest("%s must be a time like 14:30", field)
	}
	s := t.Format("15:04")
	return &s, nil
}

func formatAmount(d decimal.Decimal, precision int32) string {
	return d.StringFixed(precision)
}
