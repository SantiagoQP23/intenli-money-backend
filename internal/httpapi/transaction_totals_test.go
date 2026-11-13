package httpapi

import (
	"net/http"
	"testing"
)

// The totals strip: workspace income/expenses in the base currency, or money
// in and out of one account, for a date range and optional type.
func TestTransactionTotals(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("totals@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Cash", "type": "CASH", "currency": "USD", "opening_balance": "100"})
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Bank", "type": "BANK", "currency": "USD", "opening_balance": "0"})
	post := func(body map[string]any) {
		ta.expect(ta.do("POST", ws+"/transactions", s.access, body), http.StatusCreated)
	}
	post(map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-05", "account_id": cash, "amount": "10"})
	post(map[string]any{"type": "INCOME", "occurred_on": "2026-11-06", "account_id": bank, "amount": "50"})
	post(map[string]any{"type": "TRANSFER", "occurred_on": "2026-11-07", "from_account_id": cash, "to_account_id": bank, "amount": "20"})
	post(map[string]any{"type": "EXPENSE", "occurred_on": "2026-10-30", "account_id": cash, "amount": "5"}) // outside the period

	totals := func(query string) resp {
		return ta.expect(ta.do("GET", ws+"/transactions/totals?from=2026-11-01&to=2026-11-30"+query, s.access, nil), http.StatusOK)
	}
	check := func(name string, r resp, in, out, net string) {
		t.Helper()
		if r.str("income") != in || r.str("expenses") != out || r.str("net") != net {
			t.Fatalf("%s: want %s/%s/%s, got %s", name, in, out, net, r.Raw)
		}
	}
	check("workspace", totals(""), "50.00", "10.00", "40.00")
	check("workspace expenses", totals("&type=EXPENSE"), "0.00", "10.00", "-10.00")
	check("workspace transfers", totals("&type=TRANSFER"), "0.00", "0.00", "0.00")
	cashTotals := totals("&account_id=" + cash)
	check("cash", cashTotals, "0.00", "30.00", "-30.00")
	if cashTotals.str("currency") != "USD" || cashTotals.str("from") != "2026-11-01" {
		t.Fatalf("cash meta: %s", cashTotals.Raw)
	}
	check("bank", totals("&account_id="+bank), "70.00", "0.00", "70.00")
	check("cash transfers", totals("&account_id="+cash+"&type=TRANSFER"), "0.00", "20.00", "-20.00")
	check("one day", ta.expect(ta.do("GET", ws+"/transactions/totals?from=2026-10-30&to=2026-10-30", s.access, nil), http.StatusOK), "0.00", "5.00", "-5.00")

	if r := ta.do("GET", ws+"/transactions/totals?account_id=00000000-0000-0000-0000-000000000000", s.access, nil); r.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d %s", r.Code, r.Raw)
	}
	for _, q := range []string{"type=NOPE", "account_id=x", "from=2026-11-30&to=2026-11-01"} {
		if r := ta.do("GET", ws+"/transactions/totals?"+q, s.access, nil); r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", q, r.Code, r.Raw)
		}
	}
}
