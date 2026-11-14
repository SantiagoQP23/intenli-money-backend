package httpapi

import (
	"net/http"
	"net/url"
	"testing"
)

// setupWorkspace registers an owner and returns their personal workspace path.
func (ta *testAPI) setupWorkspace(email string) (session, string) {
	ta.t.Helper()
	s := ta.register(email)
	ws := ta.expect(ta.do("GET", "/v1/workspaces", s.access, nil), http.StatusOK).list()
	return s, "/v1/workspaces/" + ws[0]["id"].(string)
}

func (ta *testAPI) createAccount(token, ws string, body map[string]any) string {
	ta.t.Helper()
	return ta.expect(ta.do("POST", ws+"/accounts", token, body), http.StatusCreated).str("id")
}

func (ta *testAPI) balance(token, ws, accountID string) string {
	ta.t.Helper()
	return ta.expect(ta.do("GET", ws+"/accounts/"+accountID, token, nil), http.StatusOK).str("balance")
}

func (ta *testAPI) categoryID(token, ws, kind, name string) string {
	ta.t.Helper()
	for _, c := range ta.expect(ta.do("GET", ws+"/categories", token, nil), http.StatusOK).list() {
		if c["kind"] == kind && c["name"] == name {
			return c["id"].(string)
		}
	}
	ta.t.Fatalf("category %s/%s not found", kind, name)
	return ""
}

func TestAccountsAndBalances(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("acc@example.com")

	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH", "opening_balance": "100.00"})
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK"})
	card := ta.createAccount(s.access, ws, map[string]any{"name": "Tarjeta", "type": "CREDIT_CARD", "opening_balance": "-50.5"})

	acc := ta.expect(ta.do("GET", ws+"/accounts/"+cash, s.access, nil), http.StatusOK)
	if acc.str("currency") != "USD" || acc.str("balance") != "100.00" {
		t.Fatalf("cash: %s", acc.Raw)
	}
	if b := ta.balance(s.access, ws, card); b != "-50.50" {
		t.Fatalf("card balance %s", b)
	}

	// Validation.
	for _, body := range []map[string]any{
		{"name": "x", "type": "PIGGY"},
		{"name": "", "type": "CASH"},
		{"name": "x", "type": "CASH", "opening_balance": "1.001"},
		{"name": "x", "type": "CASH", "opening_balance": 5},
	} {
		ta.expect(ta.do("POST", ws+"/accounts", s.access, body), http.StatusBadRequest)
	}
	if r := ta.do("POST", ws+"/accounts", s.access, map[string]any{"name": "x", "type": "CASH", "currency": "XXX"}); r.errCode() != "unsupported_currency" {
		t.Fatalf("unsupported currency: %s", r.Raw)
	}

	food := ta.categoryID(s.access, ws, "EXPENSE", "Comida")
	salary := ta.categoryID(s.access, ws, "INCOME", "Salario")

	// Income +1000 to bank, expense -25.50 from cash, transfer 200 bank -> cash.
	ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "INCOME", "occurred_on": "2026-11-01", "account_id": bank, "amount": "1000", "category_id": salary,
	}), http.StatusCreated)
	exp := ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-02", "account_id": cash, "amount": "25.50", "category_id": food, "description": "Almuerzo",
	}), http.StatusCreated)
	if exp.str("amount") != "25.50" || exp.str("base_amount") != "25.50" {
		t.Fatalf("expense: %s", exp.Raw)
	}
	ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "TRANSFER", "occurred_on": "2026-11-03", "from_account_id": bank, "to_account_id": cash, "amount": "200",
	}), http.StatusCreated)

	if b := ta.balance(s.access, ws, cash); b != "274.50" { // 100 - 25.50 + 200
		t.Fatalf("cash balance %s", b)
	}
	if b := ta.balance(s.access, ws, bank); b != "800.00" {
		t.Fatalf("bank balance %s", b)
	}

	// Edit the expense: 25.50 -> 30; then delete it.
	expPath := ws + "/transactions/" + exp.str("id")
	ta.expect(ta.do("PUT", expPath, s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-02", "account_id": cash, "amount": "30", "category_id": food,
	}), http.StatusOK)
	if b := ta.balance(s.access, ws, cash); b != "270.00" {
		t.Fatalf("after edit %s", b)
	}
	ta.expect(ta.do("DELETE", expPath, s.access, nil), http.StatusNoContent)
	if b := ta.balance(s.access, ws, cash); b != "300.00" {
		t.Fatalf("after delete %s", b)
	}
	ta.expect(ta.do("GET", expPath, s.access, nil), http.StatusNotFound)

	// Archived accounts keep history but take no new transactions.
	ta.expect(ta.do("PATCH", ws+"/accounts/"+card, s.access, map[string]any{"archived": true}), http.StatusOK)
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-04", "account_id": card, "amount": "1",
	}); r.errCode() != "account_archived" {
		t.Fatalf("archived account: %s", r.Raw)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/accounts", s.access, nil), http.StatusOK).list()); n != 2 {
		t.Fatalf("active accounts: %d", n)
	}
}

func TestTransactionValidation(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("val@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH"})
	food := ta.categoryID(s.access, ws, "EXPENSE", "Comida")
	salary := ta.categoryID(s.access, ws, "INCOME", "Salario")

	cases := []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "-5"}, "invalid_request"},
		{map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "0"}, "invalid_request"},
		{map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "1.234"}, "invalid_request"},
		{map[string]any{"type": "EXPENSE", "occurred_on": "13/11/2026", "account_id": cash, "amount": "1"}, "invalid_request"},
		{map[string]any{"type": "LOAN", "occurred_on": "2026-11-01", "account_id": cash, "amount": "1"}, "invalid_request"},
		{map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-01", "amount": "1"}, "invalid_request"},
		{map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "1", "category_id": salary}, "category_kind_mismatch"},
		{map[string]any{"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": "00000000-0000-0000-0000-000000000001", "amount": "1"}, "account_not_found"},
		{map[string]any{"type": "TRANSFER", "occurred_on": "2026-11-01", "from_account_id": cash, "to_account_id": cash, "amount": "1"}, "invalid_request"},
		{map[string]any{"type": "TRANSFER", "occurred_on": "2026-11-01", "from_account_id": cash, "to_account_id": cash, "amount": "1", "category_id": food}, "invalid_request"},
	}
	for i, c := range cases {
		if r := ta.do("POST", ws+"/transactions", s.access, c.body); r.errCode() != c.code {
			t.Errorf("case %d: want %s, got %d %s", i, c.code, r.Code, r.Raw)
		}
	}

	// Another workspace's account or category can't be used.
	other, otherPath := ta.setupWorkspace("third@example.com")
	otherCash := ta.createAccount(other.access, otherPath, map[string]any{"name": "Cash", "type": "CASH"})
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": otherCash, "amount": "1",
	}); r.errCode() != "account_not_found" {
		t.Fatalf("cross-workspace account: %s", r.Raw)
	}
	if r := ta.do("GET", otherPath+"/transactions", s.access, nil); r.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace read: %s", r.Raw)
	}
}

func TestMultiCurrency(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("fx@example.com") // base USD
	usd := ta.createAccount(s.access, ws, map[string]any{"name": "USD", "type": "BANK"})
	eur := ta.createAccount(s.access, ws, map[string]any{"name": "EUR", "type": "BANK", "currency": "EUR"})
	clp := ta.createAccount(s.access, ws, map[string]any{"name": "CLP", "type": "CASH", "currency": "CLP"})

	// A foreign-currency expense needs a rate the first time...
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-05", "account_id": eur, "amount": "10",
	}); r.errCode() != "exchange_rate_required" {
		t.Fatalf("missing rate: %s", r.Raw)
	}
	// ...which is then saved and reused for later dates.
	r := ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "INCOME", "occurred_on": "2026-11-05", "account_id": eur, "amount": "100", "exchange_rate": "1.08",
	}), http.StatusCreated)
	if r.str("amount") != "100.00" || r.str("currency") != "EUR" || r.str("base_amount") != "108.00" {
		t.Fatalf("eur income: %s", r.Raw)
	}
	r = ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-06", "account_id": eur, "amount": "10",
	}), http.StatusCreated)
	if r.str("base_amount") != "10.80" {
		t.Fatalf("saved rate not reused: %s", r.Raw)
	}
	rates := ta.expect(ta.do("GET", ws+"/exchange-rates", s.access, nil), http.StatusOK).list()
	if len(rates) != 1 || rates[0]["currency"] != "EUR" || rates[0]["rate"] != "1.08" {
		t.Fatalf("rates: %v", rates)
	}

	// CLP has no decimals.
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "INCOME", "occurred_on": "2026-11-05", "account_id": clp, "amount": "1000.5", "exchange_rate": "0.0011",
	}); r.Code != http.StatusBadRequest {
		t.Fatalf("clp decimals: %s", r.Raw)
	}

	// Cross-currency transfer needs to_amount; both sides carry the same base value.
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "TRANSFER", "occurred_on": "2026-11-07", "from_account_id": usd, "to_account_id": eur, "amount": "54",
	}); r.Code != http.StatusBadRequest {
		t.Fatalf("missing to_amount: %s", r.Raw)
	}
	tr := ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "TRANSFER", "occurred_on": "2026-11-07", "from_account_id": usd, "to_account_id": eur, "amount": "54", "to_amount": "50",
	}), http.StatusCreated)
	entries, _ := tr.Body["entries"].([]any)
	from, to := entries[0].(map[string]any), entries[1].(map[string]any)
	if from["amount"] != "-54.00" || from["base_amount"] != "-54.00" || to["amount"] != "50.00" || to["base_amount"] != "54.00" || to["exchange_rate"] != "1.08" {
		t.Fatalf("transfer entries: %s", tr.Raw)
	}
	if b := ta.balance(s.access, ws, eur); b != "140.00" { // 100 - 10 + 50
		t.Fatalf("eur balance %s", b)
	}
	if b := ta.balance(s.access, ws, usd); b != "-54.00" {
		t.Fatalf("usd balance %s", b)
	}
}

func TestTransactionListAndPaging(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("page@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH"})
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK"})
	for i, day := range []string{"2026-11-01", "2026-11-02", "2026-11-02", "2026-11-03", "2026-11-04"} {
		acct := cash
		if i%2 == 1 {
			acct = bank
		}
		ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
			"type": "EXPENSE", "occurred_on": day, "account_id": acct, "amount": "1",
		}), http.StatusCreated)
	}

	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		path := ws + "/transactions?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		r := ta.expect(ta.do("GET", path, s.access, nil), http.StatusOK)
		for _, tx := range r.list() {
			seen = append(seen, tx["occurred_on"].(string))
		}
		next, _ := r.Body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	want := []string{"2026-11-04", "2026-11-03", "2026-11-02", "2026-11-02", "2026-11-01"}
	if len(seen) != len(want) {
		t.Fatalf("paged: %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("order: %v", seen)
		}
	}

	if n := len(ta.expect(ta.do("GET", ws+"/transactions?account_id="+bank, s.access, nil), http.StatusOK).list()); n != 2 {
		t.Fatalf("filter by account: %d", n)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/transactions?from=2026-11-02&to=2026-11-03", s.access, nil), http.StatusOK).list()); n != 3 {
		t.Fatalf("filter by date: %d", n)
	}
	ta.expect(ta.do("GET", ws+"/transactions?cursor=garbage", s.access, nil), http.StatusBadRequest)
}

func TestViewerCannotWrite(t *testing.T) {
	ta := newTestAPI(t, nil)
	owner := ta.register("own@example.com")
	viewer := ta.register("view@example.com")
	ws := ta.expect(ta.do("POST", "/v1/workspaces", owner.access, map[string]any{"name": "Casa", "type": "SHARED"}), http.StatusCreated)
	path := "/v1/workspaces/" + ws.str("id")
	inv := ta.expect(ta.do("POST", path+"/invitations", owner.access, map[string]any{"email": "view@example.com", "role": "VIEWER"}), http.StatusCreated)
	ta.expect(ta.do("POST", "/v1/invitations/accept", viewer.access, map[string]any{"token": inv.str("token")}), http.StatusOK)

	cash := ta.createAccount(owner.access, path, map[string]any{"name": "Efectivo", "type": "CASH"})
	ta.expect(ta.do("GET", path+"/accounts", viewer.access, nil), http.StatusOK)
	ta.expect(ta.do("GET", path+"/transactions", viewer.access, nil), http.StatusOK)
	ta.expect(ta.do("POST", path+"/accounts", viewer.access, map[string]any{"name": "x", "type": "CASH"}), http.StatusForbidden)
	ta.expect(ta.do("POST", path+"/transactions", viewer.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "1",
	}), http.StatusForbidden)
	ta.expect(ta.do("POST", path+"/categories", viewer.access, map[string]any{"kind": "EXPENSE", "name": "x"}), http.StatusForbidden)
	ta.expect(ta.do("GET", path+"/boxes", viewer.access, nil), http.StatusOK)
	ta.expect(ta.do("POST", path+"/boxes", viewer.access, map[string]any{"kind": "BUDGET", "name": "x"}), http.StatusForbidden)
}

func TestCategories(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("cat@example.com")
	c := ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"kind": "EXPENSE", "name": "Camarón"}), http.StatusCreated)
	if r := ta.do("POST", ws+"/categories", s.access, map[string]any{"kind": "EXPENSE", "name": "camarón"}); r.errCode() != "category_exists" {
		t.Fatalf("duplicate: %s", r.Raw)
	}
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH"})
	tx := ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "5", "category_id": c.str("id"),
	}), http.StatusCreated)

	ta.expect(ta.do("DELETE", ws+"/categories/"+c.str("id"), s.access, nil), http.StatusNoContent)
	// Archived: not offered for new transactions...
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "5", "category_id": c.str("id"),
	}); r.errCode() != "category_archived" {
		t.Fatalf("archived category: %s", r.Raw)
	}
	// ...but editing an old transaction that already uses it still works.
	ta.expect(ta.do("PUT", ws+"/transactions/"+tx.str("id"), s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-01", "account_id": cash, "amount": "6", "category_id": c.str("id"),
	}), http.StatusOK)
	// The name is free again, so restoring the old one now conflicts.
	again := ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"kind": "EXPENSE", "name": "Camarón"}), http.StatusCreated)
	if r := ta.do("POST", ws+"/categories/"+c.str("id")+"/restore", s.access, nil); r.errCode() != "category_exists" {
		t.Fatalf("restore with clash: %s", r.Raw)
	}
	ta.expect(ta.do("DELETE", ws+"/categories/"+again.str("id"), s.access, nil), http.StatusNoContent)
	ta.expect(ta.do("POST", ws+"/categories/"+c.str("id")+"/restore", s.access, nil), http.StatusNoContent)
	ta.expect(ta.do("POST", ws+"/categories/"+c.str("id")+"/restore", s.access, nil), http.StatusNotFound) // not archived
	ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-02", "account_id": cash, "amount": "5", "category_id": c.str("id"),
	}), http.StatusCreated)
}
