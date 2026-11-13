package httpapi

import (
	"net/http"
	"testing"
)

func (ta *testAPI) summary(token, ws string) resp {
	ta.t.Helper()
	return ta.expect(ta.do("GET", ws+"/summary?today=2026-11-25", token, nil), http.StatusOK)
}

func obj(r resp, key string) map[string]any { m, _ := r.Body[key].(map[string]any); return m }

func TestPurchaseOnCreditIsOneExpense(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("debts@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH", "opening_balance": "500"})
	food := ta.categoryID(s.access, ws, "EXPENSE", "Comida")

	// Buy 100, pay 30 now, owe 70.
	ob := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "PURCHASE", "amount": "100", "paid_now": "30", "account_id": cash, "category_id": food,
		"contact_name": "Proveedor A", "description": "Mercadería", "occurred_on": "2026-11-05", "due_on": "2026-11-20",
	}), http.StatusCreated)
	obPath := ws + "/obligations/" + ob.str("id")
	if ob.str("direction") != "PAYABLE" || ob.str("original_amount") != "70.00" || ob.str("outstanding") != "70.00" || ob.str("status") != "UNPAID" {
		t.Fatalf("obligation: %s", ob.Raw)
	}
	if c := obj(ob, "contact"); c["name"] != "Proveedor A" {
		t.Fatalf("contact: %s", ob.Raw)
	}
	if b := ta.balance(s.access, ws, cash); b != "470.00" {
		t.Fatalf("cash after purchase %s", b)
	}
	origin := ta.expect(ta.do("GET", ws+"/transactions/"+ob.str("origin_transaction_id"), s.access, nil), http.StatusOK)
	if origin.str("type") != "EXPENSE" || origin.str("amount") != "100.00" || origin.str("obligation_id") != ob.str("id") {
		t.Fatalf("origin transaction: %s", origin.Raw)
	}

	sum := ta.summary(s.access, ws)
	if sum.str("expenses") != "100.00" || obj(sum, "payables")["total"] != "70.00" || obj(sum, "payables")["overdue_count"] != 1.0 {
		t.Fatalf("summary after purchase: %s", sum.Raw)
	}

	// Pay 50: settles part of the debt, and is not a second expense.
	pay := func(amount, key string, code int) resp {
		body := map[string]any{"account_id": cash, "amount": amount, "paid_on": "2026-11-10"}
		if key != "" {
			body["idempotency_key"] = key
		}
		return ta.expect(ta.do("POST", obPath+"/payments", s.access, body), code)
	}
	p1 := pay("50", "", http.StatusCreated)
	if p1.str("status") != "PARTIALLY_PAID" || p1.str("outstanding") != "20.00" || p1.str("paid") != "50.00" {
		t.Fatalf("after payment: %s", p1.Raw)
	}
	if r := ta.do("POST", obPath+"/payments", s.access, map[string]any{"account_id": cash, "amount": "20.01", "paid_on": "2026-11-10"}); r.errCode() != "overpayment" {
		t.Fatalf("overpayment: %s", r.Raw)
	}
	pay("20", "pay-k1", http.StatusCreated)
	retry := pay("20", "pay-k1", http.StatusOK) // same key: no second payment
	if retry.str("status") != "PAID" || len(retry.Body["payments"].([]any)) != 2 {
		t.Fatalf("idempotent retry: %s", retry.Raw)
	}
	if b := ta.balance(s.access, ws, cash); b != "400.00" { // 500 - 30 - 50 - 20
		t.Fatalf("cash after payments %s", b)
	}
	sum = ta.summary(s.access, ws)
	if sum.str("expenses") != "100.00" || obj(sum, "payables")["total"] != "0.00" || sum.str("net_worth") != "400.00" {
		t.Fatalf("summary after paying: %s", sum.Raw)
	}

	// Payments are SETTLEMENT transactions managed by the debt.
	settlements := ta.expect(ta.do("GET", ws+"/transactions?type=SETTLEMENT", s.access, nil), http.StatusOK).list()
	if len(settlements) != 2 {
		t.Fatalf("settlements: %d", len(settlements))
	}
	stPath := ws + "/transactions/" + settlements[0]["id"].(string)
	if r := ta.do("DELETE", stPath, s.access, nil); r.errCode() != "managed_by_obligation" {
		t.Fatalf("delete managed: %s", r.Raw)
	}
	if r := ta.do("PUT", ws+"/transactions/"+ob.str("origin_transaction_id"), s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-05", "account_id": cash, "amount": "1",
	}); r.errCode() != "managed_by_obligation" {
		t.Fatalf("edit managed: %s", r.Raw)
	}

	// Undo the 50 payment: owed again, money back.
	payments := p1.Body["payments"].([]any)
	pmID := payments[0].(map[string]any)["id"].(string)
	undone := ta.expect(ta.do("DELETE", obPath+"/payments/"+pmID, s.access, nil), http.StatusOK)
	if undone.str("outstanding") != "50.00" || undone.str("status") != "PARTIALLY_PAID" {
		t.Fatalf("after undo: %s", undone.Raw)
	}
	if b := ta.balance(s.access, ws, cash); b != "450.00" {
		t.Fatalf("cash after undo %s", b)
	}
	if r := ta.do("DELETE", obPath, s.access, nil); r.errCode() != "obligation_has_payments" {
		t.Fatalf("delete with payments: %s", r.Raw)
	}

	// Details can change; the list filters by status.
	upd := ta.expect(ta.do("PUT", obPath, s.access, map[string]any{"description": "Mercadería noviembre", "due_on": "2026-12-01", "contact_name": "Proveedor A"}), http.StatusOK)
	if upd.str("description") != "Mercadería noviembre" || upd.Body["overdue"] != false || obj(upd, "contact")["id"] != obj(ob, "contact")["id"] {
		t.Fatalf("update: %s", upd.Raw)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/obligations?status=open&direction=PAYABLE", s.access, nil), http.StatusOK).list()); n != 1 {
		t.Fatalf("open payables: %d", n)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/contacts", s.access, nil), http.StatusOK).list()); n != 1 {
		t.Fatalf("contacts: %d", n)
	}
}

func TestSalesLoansAndReports(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("loans@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH"})
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK"})
	food := ta.categoryID(s.access, ws, "EXPENSE", "Comida")

	ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-02", "account_id": bank, "amount": "40", "category_id": food,
	}), http.StatusCreated)

	// A loan of 1000 into the bank is money in, but not income.
	loan := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "LOAN_BORROWED", "amount": "1000", "account_id": bank, "contact_name": "Banco Pichincha", "occurred_on": "2026-11-03",
	}), http.StatusCreated)
	if loan.str("direction") != "PAYABLE" || ta.balance(s.access, ws, bank) != "960.00" {
		t.Fatalf("loan: %s", loan.Raw)
	}

	// A sale of 200 fully on credit is income today; collecting it later isn't.
	sale := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "SALE", "amount": "200", "contact_name": "Cliente B", "occurred_on": "2026-11-04",
	}), http.StatusCreated)
	if sale.str("currency") != "USD" || sale.str("outstanding") != "200.00" {
		t.Fatalf("sale: %s", sale.Raw)
	}
	ta.expect(ta.do("POST", ws+"/obligations/"+sale.str("id")+"/payments", s.access, map[string]any{
		"account_id": cash, "amount": "150", "paid_on": "2026-11-15",
	}), http.StatusCreated)

	// A debt that already existed moves no money.
	other := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "OTHER", "direction": "RECEIVABLE", "amount": "25", "contact_name": "Primo", "occurred_on": "2026-10-01",
	}), http.StatusCreated)
	if other.Body["origin_transaction_id"] != nil {
		t.Fatalf("other: %s", other.Raw)
	}

	sum := ta.summary(s.access, ws)
	// income 200 (sale), expenses 40; balance 960 + 150; receivables 50 + 25; payables 1000.
	if sum.str("income") != "200.00" || sum.str("expenses") != "40.00" || obj(sum, "balance")["total"] != "1110.00" ||
		obj(sum, "receivables")["total"] != "75.00" || obj(sum, "payables")["total"] != "1000.00" || sum.str("net_worth") != "185.00" {
		t.Fatalf("summary: %s", sum.Raw)
	}

	// Deleting a debt with no payments removes its transaction too.
	purchase := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "PURCHASE", "amount": "60", "category_id": food, "occurred_on": "2026-11-06",
	}), http.StatusCreated)
	if ta.summary(s.access, ws).str("expenses") != "100.00" {
		t.Fatal("purchase on credit should count as an expense")
	}
	ta.expect(ta.do("DELETE", ws+"/obligations/"+purchase.str("id"), s.access, nil), http.StatusNoContent)
	if ta.summary(s.access, ws).str("expenses") != "40.00" {
		t.Fatal("deleted purchase still counted")
	}
	ta.expect(ta.do("GET", ws+"/obligations/"+purchase.str("id"), s.access, nil), http.StatusNotFound)

	cats := ta.expect(ta.do("GET", ws+"/reports/categories?type=EXPENSE&from=2026-11-01&to=2026-11-30", s.access, nil), http.StatusOK)
	if rows := cats.list(); len(rows) != 1 || rows[0]["name"] != "Comida" || rows[0]["total"] != "40.00" || rows[0]["share"] != "1.0000" {
		t.Fatalf("category report: %s", cats.Raw)
	}
	monthly := ta.expect(ta.do("GET", ws+"/reports/monthly?months=2&today=2026-11-25", s.access, nil), http.StatusOK).list()
	if len(monthly) != 2 || monthly[0]["month"] != "2026-10" || monthly[0]["income"] != "0.00" ||
		monthly[1]["income"] != "200.00" || monthly[1]["expenses"] != "40.00" {
		t.Fatalf("monthly: %v", monthly)
	}
}

func TestObligationValidation(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("obval@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH"})
	eur := ta.createAccount(s.access, ws, map[string]any{"name": "Euros", "type": "CASH", "currency": "EUR"})
	salary := ta.categoryID(s.access, ws, "INCOME", "Salario")

	for _, tc := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"origin": "GIFT", "amount": "1", "occurred_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "PURCHASE", "direction": "RECEIVABLE", "amount": "1", "occurred_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "PURCHASE", "amount": "10", "paid_now": "10", "account_id": cash, "occurred_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "PURCHASE", "amount": "10", "paid_now": "5", "occurred_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "PURCHASE", "amount": "10", "category_id": salary, "occurred_on": "2026-11-01"}, "category_kind_mismatch"},
		{map[string]any{"origin": "LOAN_LENT", "amount": "10", "occurred_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "OTHER", "amount": "10", "occurred_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "SALE", "amount": "10", "occurred_on": "2026-11-05", "due_on": "2026-11-01"}, "invalid_request"},
		{map[string]any{"origin": "SALE", "amount": "10", "currency": "EUR", "occurred_on": "2026-11-05"}, "exchange_rate_required"},
	} {
		if r := ta.do("POST", ws+"/obligations", s.access, tc.body); r.errCode() != tc.code {
			t.Errorf("%v: got %s", tc.body, r.Raw)
		}
	}

	// A payment must use an account in the debt's currency.
	ob := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "LOAN_LENT", "amount": "10", "account_id": cash, "occurred_on": "2026-11-01",
	}), http.StatusCreated)
	if r := ta.do("POST", ws+"/obligations/"+ob.str("id")+"/payments", s.access, map[string]any{
		"account_id": eur, "amount": "5", "paid_on": "2026-11-02", "exchange_rate": "1.1",
	}); r.errCode() != "currency_mismatch" {
		t.Fatalf("currency mismatch: %s", r.Raw)
	}
	if ta.balance(s.access, ws, cash) != "-10.00" {
		t.Fatal("lending should take money out")
	}

	// Another workspace can't see it.
	other, _ := ta.setupWorkspace("stranger@example.com")
	ta.expect(ta.do("GET", ws+"/obligations/"+ob.str("id"), other.access, nil), http.StatusNotFound)
}

func TestCurrenciesArePublic(t *testing.T) {
	ta := newTestAPI(t, nil)
	if n := len(ta.expect(ta.do("GET", "/v1/currencies", "", nil), http.StatusOK).list()); n < 10 {
		t.Fatalf("currencies: %d", n)
	}
}
