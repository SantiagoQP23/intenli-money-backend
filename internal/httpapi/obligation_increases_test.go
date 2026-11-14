package httpapi

import (
	"net/http"
	"testing"
)

func TestDebtIncreases(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("increases@example.com")
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK", "opening_balance": "100"})
	food := ta.categoryID(s.access, ws, "EXPENSE", "Comida")

	// Borrow 300, then borrow 200 more: cash comes in, no expense.
	loan := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "LOAN_BORROWED", "amount": "300", "account_id": bank, "contact_name": "Juan", "occurred_on": "2026-11-01",
	}), http.StatusCreated)
	loanPath := ws + "/obligations/" + loan.str("id")
	inc := func(path string, body map[string]any, code int) resp {
		return ta.expect(ta.do("POST", path+"/increases", s.access, body), code)
	}
	if r := ta.do("POST", loanPath+"/increases", s.access, map[string]any{"kind": "MONEY", "amount": "200", "occurred_on": "2026-11-05"}); r.Code != http.StatusBadRequest {
		t.Fatalf("money without account: %s", r.Raw)
	}
	l := inc(loanPath, map[string]any{"kind": "MONEY", "amount": "200", "account_id": bank, "occurred_on": "2026-11-05",
		"occurred_time": "09:30", "idempotency_key": "inc-1"}, http.StatusCreated)
	if l.str("original_amount") != "300.00" || l.str("increased") != "200.00" || l.str("total") != "500.00" || l.str("outstanding") != "500.00" {
		t.Fatalf("after money increase: %s", l.Raw)
	}
	incs := l.Body["increases"].([]any)
	first := incs[0].(map[string]any)
	if len(incs) != 1 || first["kind"] != "MONEY" || first["account_id"] != bank || first["occurred_time"] != "09:30" {
		t.Fatalf("increases: %s", l.Raw)
	}
	if retry := inc(loanPath, map[string]any{"kind": "MONEY", "amount": "200", "account_id": bank, "occurred_on": "2026-11-05",
		"idempotency_key": "inc-1"}, http.StatusOK); len(retry.Body["increases"].([]any)) != 1 {
		t.Fatalf("idempotent retry: %s", retry.Raw)
	}
	if b := ta.balance(s.access, ws, bank); b != "600.00" {
		t.Fatalf("bank after borrowing more: %s", b)
	}
	incTx := ta.expect(ta.do("GET", ws+"/transactions/"+first["transaction_id"].(string), s.access, nil), http.StatusOK)
	if incTx.str("type") != "LOAN" || incTx.str("obligation_id") != loan.str("id") {
		t.Fatalf("increase transaction: %s", incTx.Raw)
	}
	if sum := ta.summary(s.access, ws); sum.str("expenses") != "0.00" || obj(sum, "payables")["total"] != "500.00" {
		t.Fatalf("summary after money increase: %s", sum.Raw)
	}

	// Edit it to 150: cash and the debt follow; the transaction keeps its id.
	incPath := loanPath + "/increases/" + first["id"].(string)
	l = ta.expect(ta.do("PUT", incPath, s.access, map[string]any{"amount": "150", "account_id": bank, "occurred_on": "2026-11-06"}), http.StatusOK)
	if l.str("total") != "450.00" || ta.balance(s.access, ws, bank) != "550.00" {
		t.Fatalf("after editing increase: %s", l.Raw)
	}
	if r := ta.do("PUT", incPath, s.access, map[string]any{"kind": "ADJUSTMENT", "amount": "150", "occurred_on": "2026-11-06"}); r.Code != http.StatusBadRequest {
		t.Fatalf("kind change: %s", r.Raw)
	}
	ta.expect(ta.do("GET", ws+"/transactions/"+first["transaction_id"].(string), s.access, nil), http.StatusOK)

	// Pay 400; the increase can't then shrink below what's paid.
	ta.expect(ta.do("POST", loanPath+"/payments", s.access, map[string]any{"account_id": bank, "amount": "400", "paid_on": "2026-11-10"}), http.StatusCreated)
	if r := ta.do("PUT", incPath, s.access, map[string]any{"amount": "50", "account_id": bank, "occurred_on": "2026-11-06"}); r.errCode() != "below_paid" {
		t.Fatalf("shrink below paid: %s", r.Raw)
	}
	if r := ta.do("DELETE", incPath, s.access, nil); r.errCode() != "below_paid" {
		t.Fatalf("delete below paid: %s", r.Raw)
	}
	ta.expect(ta.do("PUT", incPath, s.access, map[string]any{"amount": "100", "account_id": bank, "occurred_on": "2026-11-06"}), http.StatusOK)
	l = ta.expect(ta.do("GET", loanPath, s.access, nil), http.StatusOK)
	if l.str("status") != "PAID" || l.str("outstanding") != "0.00" {
		t.Fatalf("paid after editing: %s", l.Raw)
	}

	// An adjustment reopens a paid debt and moves no money.
	l = inc(loanPath, map[string]any{"kind": "ADJUSTMENT", "amount": "25", "occurred_on": "2026-11-12", "description": "Intereses"}, http.StatusCreated)
	if l.str("status") != "PARTIALLY_PAID" || l.str("outstanding") != "25.00" || ta.balance(s.access, ws, bank) != "100.00" {
		t.Fatalf("after adjustment: %s", l.Raw)
	}
	adj := l.Body["increases"].([]any)[0].(map[string]any)
	if adj["kind"] != "ADJUSTMENT" || adj["transaction_id"] != nil {
		t.Fatalf("adjustment: %s", l.Raw)
	}
	l = ta.expect(ta.do("DELETE", loanPath+"/increases/"+adj["id"].(string), s.access, nil), http.StatusOK)
	if l.str("status") != "PAID" || len(l.Body["increases"].([]any)) != 1 {
		t.Fatalf("after deleting adjustment: %s", l.Raw)
	}

	// Buying more on credit is a CHARGE: an expense today, no cash.
	pur := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "PURCHASE", "amount": "80", "contact_name": "Proveedor", "category_id": food, "occurred_on": "2026-11-02",
	}), http.StatusCreated)
	purPath := ws + "/obligations/" + pur.str("id")
	if r := ta.do("POST", purPath+"/increases", s.access, map[string]any{"kind": "CHARGE", "amount": "20", "occurred_on": "2026-11-03", "account_id": bank}); r.Code != http.StatusBadRequest {
		t.Fatalf("charge with account: %s", r.Raw)
	}
	p := inc(purPath, map[string]any{"kind": "CHARGE", "amount": "20", "category_id": food, "occurred_on": "2026-11-03"}, http.StatusCreated)
	if p.str("total") != "100.00" || ta.balance(s.access, ws, bank) != "100.00" {
		t.Fatalf("after charge: %s", p.Raw)
	}
	charge := p.Body["increases"].([]any)[0].(map[string]any)
	if charge["category_id"] != food {
		t.Fatalf("charge category: %s", p.Raw)
	}
	if sum := ta.summary(s.access, ws); sum.str("expenses") != "100.00" {
		t.Fatalf("summary after charge: %s", sum.Raw)
	}
	chargeTx := ta.expect(ta.do("GET", ws+"/transactions/"+charge["transaction_id"].(string), s.access, nil), http.StatusOK)
	if chargeTx.str("type") != "EXPENSE" {
		t.Fatalf("charge transaction: %s", chargeTx.Raw)
	}
	if r := ta.do("DELETE", ws+"/transactions/"+charge["transaction_id"].(string), s.access, nil); r.errCode() != "managed_by_obligation" {
		t.Fatalf("charge transaction should be managed: %s", r.Raw)
	}

	// Deleting the debt takes its increases and their transactions with it.
	ta.expect(ta.do("DELETE", purPath, s.access, nil), http.StatusNoContent)
	if sum := ta.summary(s.access, ws); sum.str("expenses") != "0.00" {
		t.Fatalf("summary after deleting debt: %s", sum.Raw)
	}
	ta.expect(ta.do("GET", ws+"/transactions/"+charge["transaction_id"].(string), s.access, nil), http.StatusNotFound)
}

func TestEditPayment(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("editpay@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH", "opening_balance": "500"})
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK", "opening_balance": "500"})
	eur := ta.createAccount(s.access, ws, map[string]any{"name": "Euros", "type": "BANK", "currency": "EUR"})

	ob := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "PURCHASE", "amount": "100", "contact_name": "Proveedor", "occurred_on": "2026-11-01",
	}), http.StatusCreated)
	obPath := ws + "/obligations/" + ob.str("id")
	ta.expect(ta.do("POST", obPath+"/payments", s.access, map[string]any{"account_id": cash, "amount": "30", "paid_on": "2026-11-02"}), http.StatusCreated)
	o := ta.expect(ta.do("POST", obPath+"/payments", s.access, map[string]any{"account_id": cash, "amount": "40", "paid_on": "2026-11-03"}), http.StatusCreated)
	pm := o.Body["payments"].([]any)[0].(map[string]any) // newest: the 40
	pmPath := obPath + "/payments/" + pm["id"].(string)

	// Move it to the bank, change amount, date and time.
	o = ta.expect(ta.do("PUT", pmPath, s.access, map[string]any{
		"account_id": bank, "amount": "70", "paid_on": "2026-11-04", "paid_time": "18:15", "description": "Abono",
	}), http.StatusOK)
	got := o.Body["payments"].([]any)[0].(map[string]any)
	if o.str("outstanding") != "0.00" || o.str("status") != "PAID" || got["amount"] != "70.00" || got["account_id"] != bank ||
		got["paid_on"] != "2026-11-04" || got["paid_time"] != "18:15" || got["description"] != "Abono" || got["transaction_id"] != pm["transaction_id"] {
		t.Fatalf("after edit: %s", o.Raw)
	}
	if c, b := ta.balance(s.access, ws, cash), ta.balance(s.access, ws, bank); c != "470.00" || b != "430.00" {
		t.Fatalf("balances after edit: cash %s bank %s", c, b)
	}
	tx := ta.expect(ta.do("GET", ws+"/transactions/"+pm["transaction_id"].(string), s.access, nil), http.StatusOK)
	if tx.str("type") != "SETTLEMENT" || tx.str("occurred_on") != "2026-11-04" || tx.str("description") != "Abono" {
		t.Fatalf("settlement after edit: %s", tx.Raw)
	}

	// Too much, wrong currency, unknown payment.
	if r := ta.do("PUT", pmPath, s.access, map[string]any{"account_id": bank, "amount": "70.01", "paid_on": "2026-11-04"}); r.errCode() != "overpayment" {
		t.Fatalf("overpayment: %s", r.Raw)
	}
	if r := ta.do("PUT", pmPath, s.access, map[string]any{"account_id": eur, "amount": "10", "paid_on": "2026-11-04"}); r.errCode() != "currency_mismatch" {
		t.Fatalf("currency mismatch: %s", r.Raw)
	}
	if r := ta.do("PUT", obPath+"/payments/00000000-0000-0000-0000-000000000000", s.access,
		map[string]any{"account_id": bank, "amount": "1", "paid_on": "2026-11-04"}); r.errCode() != "payment_not_found" {
		t.Fatalf("unknown payment: %s", r.Raw)
	}

	// Lower it again: the debt reopens.
	o = ta.expect(ta.do("PUT", pmPath, s.access, map[string]any{"account_id": bank, "amount": "10", "paid_on": "2026-11-04"}), http.StatusOK)
	if o.str("outstanding") != "60.00" || o.str("status") != "PARTIALLY_PAID" || ta.balance(s.access, ws, bank) != "490.00" {
		t.Fatalf("after lowering: %s", o.Raw)
	}
}
