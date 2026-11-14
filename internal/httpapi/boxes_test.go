package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

const boxToday = "?today=2026-11-14"

func (ta *testAPI) boxes(token, ws string) resp {
	ta.t.Helper()
	return ta.expect(ta.do("GET", ws+"/boxes"+boxToday, token, nil), http.StatusOK)
}

// wallet returns the unassigned amount for currency.
func (ta *testAPI) wallet(token, ws, currency string) string {
	ta.t.Helper()
	for _, w := range ta.boxes(token, ws).Body["wallets"].([]any) {
		m := w.(map[string]any)
		if m["currency"] == currency {
			return m["unassigned"].(string)
		}
	}
	return ""
}

func (ta *testAPI) boxBalance(token, ws, id string) string {
	ta.t.Helper()
	return ta.expect(ta.do("GET", ws+"/boxes/"+id+boxToday, token, nil), http.StatusOK).str("balance")
}

func (ta *testAPI) createBox(token, ws string, body map[string]any) string {
	ta.t.Helper()
	return ta.expect(ta.do("POST", ws+"/boxes"+boxToday, token, body), http.StatusCreated).str("id")
}

// The plan's example: wallet + boxes = accounts after every step.
func TestBoxesWalletExample(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("boxes@example.com")
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK", "opening_balance": "1000"})
	ta.createAccount(s.access, ws, map[string]any{"name": "Efectivo", "type": "CASH", "opening_balance": "200"})
	groceries := ta.categoryID(s.access, ws, "EXPENSE", "Comida")

	food := ta.createBox(s.access, ws, map[string]any{"kind": "BUDGET", "name": "Comida", "icon": "utensils", "monthly_amount": "300", "category_ids": []string{groceries}})
	car := ta.createBox(s.access, ws, map[string]any{"kind": "SAVINGS", "name": "Carro", "goal_amount": "5000", "goal_date": "2027-10-31"})
	if r := ta.do("POST", ws+"/boxes", s.access, map[string]any{"kind": "SAVINGS", "name": "carro"}); r.errCode() != "box_name_taken" {
		t.Fatalf("duplicate name: %s", r.Raw)
	}
	if r := ta.do("POST", ws+"/boxes", s.access, map[string]any{"kind": "SAVINGS", "name": "X", "monthly_amount": "1"}); r.Code != http.StatusBadRequest {
		t.Fatalf("savings with monthly amount: %s", r.Raw)
	}
	if w := ta.wallet(s.access, ws, "USD"); w != "1200.00" {
		t.Fatalf("start wallet %s", w)
	}

	ta.expect(ta.do("POST", ws+"/boxes/"+food+"/allocations"+boxToday, s.access, map[string]any{"amount": "300"}), http.StatusCreated)
	r := ta.expect(ta.do("POST", ws+"/boxes/"+car+"/allocations"+boxToday, s.access, map[string]any{"amount": "500", "idempotency_key": "a1"}), http.StatusCreated)
	ta.expect(ta.do("POST", ws+"/boxes/"+car+"/allocations"+boxToday, s.access, map[string]any{"amount": "500", "idempotency_key": "a1"}), http.StatusOK)
	if g := obj(r, "goal"); g["missing"] != "4500.00" || g["per_month"] != "409.10" || g["months"] != 11.0 {
		t.Fatalf("goal: %s", r.Raw)
	}
	if w := ta.wallet(s.access, ws, "USD"); w != "400.00" {
		t.Fatalf("wallet after assigning %s", w)
	}

	// A linked expense comes out of the box, not the wallet.
	tx := ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-14", "account_id": bank, "amount": "40", "category_id": groceries, "box_id": food,
	}), http.StatusCreated)
	if tx.str("box_id") != food {
		t.Fatalf("box_id on transaction: %s", tx.Raw)
	}
	if b, w := ta.boxBalance(s.access, ws, food), ta.wallet(s.access, ws, "USD"); b != "260.00" || w != "400.00" {
		t.Fatalf("after groceries: box %s wallet %s", b, w)
	}
	// An unlinked one comes out of the wallet.
	ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-14", "account_id": bank, "amount": "5",
	}), http.StatusCreated)
	ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "INCOME", "occurred_on": "2026-11-14", "account_id": bank, "amount": "800",
	}), http.StatusCreated)
	ov := ta.boxes(s.access, ws)
	wl := ov.Body["wallets"].([]any)[0].(map[string]any)
	if wl["accounts_total"] != "1955.00" || wl["in_boxes"] != "760.00" || wl["unassigned"] != "1195.00" {
		t.Fatalf("overview: %s", ov.Raw)
	}
	fb := ov.list()[0]
	month := fb["month"].(map[string]any)
	if fb["id"] != food || month["spent"] != "40.00" || month["needs_fill"] != true || fmt.Sprint(month["progress"]) != "0.1333" {
		t.Fatalf("food box: %v", fb)
	}
	if ids := fb["category_ids"].([]any); len(ids) != 1 || ids[0] != groceries {
		t.Fatalf("category ids: %v", fb)
	}

	// Editing the linked expense updates the box; filtering by box works.
	ta.expect(ta.do("PUT", ws+"/transactions/"+tx.str("id"), s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-14", "account_id": bank, "amount": "50", "box_id": food,
	}), http.StatusOK)
	if b := ta.boxBalance(s.access, ws, food); b != "250.00" {
		t.Fatalf("after edit %s", b)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/transactions?box_id="+food, s.access, nil), http.StatusOK).list()); n != 1 {
		t.Fatalf("filter by box: %d", n)
	}

	// Move 100 from the car to food, then undo it.
	mv := ta.expect(ta.do("POST", ws+"/boxes/moves"+boxToday, s.access, map[string]any{"from_box_id": car, "to_box_id": food, "amount": "100"}), http.StatusCreated)
	if len(mv.Body["wallets"].([]any)) != 1 {
		t.Fatalf("move answer: %s", mv.Raw)
	}
	if b := ta.boxBalance(s.access, ws, car); b != "400.00" {
		t.Fatalf("car after move %s", b)
	}
	detail := ta.expect(ta.do("GET", ws+"/boxes/"+food+boxToday, s.access, nil), http.StatusOK)
	hist := detail.Body["history"].([]any)
	if len(hist) != 3 {
		t.Fatalf("history: %s", detail.Raw)
	}
	var moveID string
	for _, h := range hist {
		m := h.(map[string]any)
		if m["kind"] == "MOVE" {
			moveID = m["id"].(string)
			if m["other_box_id"] != car || m["amount"] != "100.00" {
				t.Fatalf("move row: %v", m)
			}
		}
		if m["type"] == "TRANSACTION" && m["amount"] != "-50.00" {
			t.Fatalf("tx row: %v", m)
		}
	}
	ta.expect(ta.do("DELETE", ws+"/boxes/"+food+"/allocations/"+moveID, s.access, nil), http.StatusOK)
	if b, c := ta.boxBalance(s.access, ws, food), ta.boxBalance(s.access, ws, car); b != "250.00" || c != "500.00" {
		t.Fatalf("after undoing move: food %s car %s", b, c)
	}

	// Fill tops food back up to 300; a second fill has nothing to do.
	pre := ta.expect(ta.do("POST", ws+"/boxes/fill"+boxToday, s.access, map[string]any{"dry_run": true}), http.StatusOK)
	items := pre.Body["items"].([]any)
	totals := pre.Body["totals"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["amount"] != "50.00" || totals[0].(map[string]any)["wallet_after"] != "1145.00" {
		t.Fatalf("fill preview: %s", pre.Raw)
	}
	if b := ta.boxBalance(s.access, ws, food); b != "250.00" {
		t.Fatalf("dry run changed the box: %s", b)
	}
	ta.expect(ta.do("POST", ws+"/boxes/fill"+boxToday, s.access, map[string]any{}), http.StatusCreated)
	if b := ta.boxBalance(s.access, ws, food); b != "300.00" {
		t.Fatalf("after fill %s", b)
	}
	if again := ta.expect(ta.do("POST", ws+"/boxes/fill"+boxToday, s.access, map[string]any{}), http.StatusOK); len(again.Body["items"].([]any)) != 0 {
		t.Fatalf("second fill: %s", again.Raw)
	}

	// Release 20 from food back to the wallet.
	ta.expect(ta.do("POST", ws+"/boxes/"+food+"/allocations"+boxToday, s.access, map[string]any{"amount": "-20"}), http.StatusCreated)
	if w := ta.wallet(s.access, ws, "USD"); w != "1165.00" {
		t.Fatalf("after release %s", w)
	}

	// Archiving returns the money and frees the category; restoring is empty.
	arch := ta.expect(ta.do("POST", ws+"/boxes/"+car+"/archive"+boxToday, s.access, nil), http.StatusOK)
	if arch.Body["archived"] != true || arch.str("balance") != "0.00" {
		t.Fatalf("archive: %s", arch.Raw)
	}
	if w := ta.wallet(s.access, ws, "USD"); w != "1665.00" {
		t.Fatalf("after archive %s", w)
	}
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-14", "account_id": bank, "amount": "1", "box_id": car,
	}); r.errCode() != "box_archived" {
		t.Fatalf("link to archived: %s", r.Raw)
	}
	if n := len(ta.boxes(s.access, ws).list()); n != 1 {
		t.Fatalf("active boxes: %d", n)
	}
	ta.expect(ta.do("POST", ws+"/boxes/"+car+"/restore", s.access, nil), http.StatusOK)
	if b := ta.boxBalance(s.access, ws, car); b != "0.00" {
		t.Fatalf("restored %s", b)
	}

	// Deleting the linked expense gives the money back to the box.
	ta.expect(ta.do("DELETE", ws+"/transactions/"+tx.str("id"), s.access, nil), http.StatusNoContent)
	if b := ta.boxBalance(s.access, ws, food); b != "330.00" {
		t.Fatalf("after delete %s", b)
	}

	// Transfers can't be linked.
	cash2 := ta.createAccount(s.access, ws, map[string]any{"name": "Caja chica", "type": "CASH"})
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "TRANSFER", "occurred_on": "2026-11-14", "from_account_id": bank, "to_account_id": cash2, "amount": "1", "box_id": food,
	}); r.Code != http.StatusBadRequest {
		t.Fatalf("transfer with box: %s", r.Raw)
	}
}

func TestBoxesCurrencyAndDebtPayments(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("boxfx@example.com")
	usd := ta.createAccount(s.access, ws, map[string]any{"name": "USD", "type": "BANK", "opening_balance": "100"})
	eur := ta.createAccount(s.access, ws, map[string]any{"name": "EUR", "type": "BANK", "currency": "EUR", "opening_balance": "50"})
	card := ta.createAccount(s.access, ws, map[string]any{"name": "Tarjeta", "type": "CREDIT_CARD", "opening_balance": "-30"})
	_ = card

	bills := ta.createBox(s.access, ws, map[string]any{"kind": "BUDGET", "name": "Servicios", "monthly_amount": "60"})
	trip := ta.createBox(s.access, ws, map[string]any{"kind": "SAVINGS", "name": "Viaje", "currency": "EUR"})

	// Two currencies, two wallets, base first; the card lowers the USD one.
	ov := ta.boxes(s.access, ws)
	wl := ov.Body["wallets"].([]any)
	if len(wl) != 2 || wl[0].(map[string]any)["currency"] != "USD" || wl[0].(map[string]any)["unassigned"] != "70.00" || wl[1].(map[string]any)["unassigned"] != "50.00" {
		t.Fatalf("wallets: %s", ov.Raw)
	}
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "EXPENSE", "occurred_on": "2026-11-14", "account_id": usd, "amount": "1", "box_id": trip,
	}); r.errCode() != "box_currency_mismatch" {
		t.Fatalf("currency mismatch: %s", r.Raw)
	}
	if r := ta.do("POST", ws+"/boxes/moves", s.access, map[string]any{"from_box_id": bills, "to_box_id": trip, "amount": "1"}); r.errCode() != "box_currency_mismatch" {
		t.Fatalf("move across currencies: %s", r.Raw)
	}
	ta.expect(ta.do("POST", ws+"/boxes/"+trip+"/allocations", s.access, map[string]any{"amount": "20"}), http.StatusCreated)
	if r := ta.do("PUT", ws+"/boxes/"+trip, s.access, map[string]any{"kind": "SAVINGS", "name": "Viaje", "currency": "USD"}); r.errCode() != "box_has_history" {
		t.Fatalf("currency change with history: %s", r.Raw)
	}
	_ = eur

	// Paying a debt from a box.
	ta.expect(ta.do("POST", ws+"/boxes/"+bills+"/allocations", s.access, map[string]any{"amount": "60"}), http.StatusCreated)
	ob := ta.expect(ta.do("POST", ws+"/obligations", s.access, map[string]any{
		"origin": "OTHER", "direction": "PAYABLE", "amount": "45", "contact_name": "Empresa eléctrica", "occurred_on": "2026-11-01",
	}), http.StatusCreated)
	obPath := ws + "/obligations/" + ob.str("id")
	if r := ta.do("POST", obPath+"/payments", s.access, map[string]any{"account_id": usd, "amount": "40", "paid_on": "2026-11-14", "box_id": trip}); r.errCode() != "box_currency_mismatch" {
		t.Fatalf("payment box currency: %s", r.Raw)
	}
	paid := ta.expect(ta.do("POST", obPath+"/payments", s.access, map[string]any{"account_id": usd, "amount": "40", "paid_on": "2026-11-14", "box_id": bills}), http.StatusCreated)
	pm := paid.Body["payments"].([]any)[0].(map[string]any)
	if pm["box_id"] != bills {
		t.Fatalf("payment box: %s", paid.Raw)
	}
	if b, w := ta.boxBalance(s.access, ws, bills), ta.wallet(s.access, ws, "USD"); b != "20.00" || w != "10.00" {
		t.Fatalf("after paying from the box: box %s wallet %s", b, w)
	}
}
