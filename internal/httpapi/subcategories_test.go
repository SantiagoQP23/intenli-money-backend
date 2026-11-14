package httpapi

import (
	"net/http"
	"testing"
)

func TestSubcategories(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("subcats@example.com")
	food := ta.categoryID(s.access, ws, "EXPENSE", "Comida")
	salary := ta.categoryID(s.access, ws, "INCOME", "Salario")
	transport := ta.categoryID(s.access, ws, "EXPENSE", "Transporte")

	// Kind comes from the parent when omitted.
	sub := ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"name": "Supermercado", "parent_id": food}), http.StatusCreated)
	if sub.str("kind") != "EXPENSE" || sub.str("parent_id") != food {
		t.Fatalf("create: %s", sub.Raw)
	}
	super := sub.str("id")
	cafe := ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"kind": "EXPENSE", "name": "Café", "parent_id": food, "icon": "coffee"}), http.StatusCreated).str("id")

	// Names are unique among siblings only.
	ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"name": "Otros", "parent_id": food}), http.StatusCreated)
	ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"name": "Otros", "parent_id": transport}), http.StatusCreated)
	if r := ta.do("POST", ws+"/categories", s.access, map[string]any{"name": "otros", "parent_id": food}); r.errCode() != "category_exists" {
		t.Fatalf("duplicate sibling: %s", r.Raw)
	}

	// Parent rules.
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"name": "Nieto", "parent_id": super}, "parent_is_subcategory"},
		{map[string]any{"kind": "INCOME", "name": "Bono", "parent_id": food}, "parent_kind_mismatch"},
		{map[string]any{"name": "X", "parent_id": "00000000-0000-0000-0000-000000000001"}, "parent_not_found"},
	} {
		if r := ta.do("POST", ws+"/categories", s.access, c.body); r.errCode() != c.code {
			t.Errorf("%v: %s", c.body, r.Raw)
		}
	}

	// Moving: a leaf can go under a parent and back; a parent can't nest.
	path := func(id string) string { return ws + "/categories/" + id }
	ta.expect(ta.do("PATCH", path(transport), s.access, map[string]any{"name": "Transporte", "parent_id": food}), http.StatusConflict)
	restaurants := ta.categoryID(s.access, ws, "EXPENSE", "Restaurantes")
	ta.expect(ta.do("PATCH", path(restaurants), s.access, map[string]any{"name": "Restaurantes", "parent_id": food}), http.StatusNoContent)
	if r := ta.do("PATCH", path(restaurants), s.access, map[string]any{"name": "Restaurantes", "parent_id": salary}); r.errCode() != "parent_kind_mismatch" {
		t.Fatalf("move across kinds: %s", r.Raw)
	}
	if r := ta.do("PATCH", path(food), s.access, map[string]any{"name": "Comida", "parent_id": food}); r.errCode() != "parent_is_self" {
		t.Fatalf("self parent: %s", r.Raw)
	}
	ta.expect(ta.do("PATCH", path(cafe), s.access, map[string]any{"name": "Cafetería"}), http.StatusNoContent) // keeps its parent
	parentOf := func(id string) any {
		for _, c := range ta.expect(ta.do("GET", ws+"/categories?include_archived=true", s.access, nil), http.StatusOK).list() {
			if c["id"] == id {
				return c["parent_id"]
			}
		}
		return "missing"
	}
	if parentOf(cafe) != food || parentOf(restaurants) != food {
		t.Fatal("parents not kept")
	}
	ta.expect(ta.do("PATCH", path(cafe), s.access, map[string]any{"name": "Cafetería", "parent_id": nil}), http.StatusNoContent)
	if parentOf(cafe) != nil {
		t.Fatal("null parent should make it top level")
	}
	ta.expect(ta.do("PATCH", path(cafe), s.access, map[string]any{"name": "Cafetería", "parent_id": food}), http.StatusNoContent)

	// Transactions can use a subcategory; the parent filter and the report
	// roll them up.
	bank := ta.createAccount(s.access, ws, map[string]any{"name": "Banco", "type": "BANK", "opening_balance": "500"})
	spend := func(amount, category string) {
		ta.expect(ta.do("POST", ws+"/transactions", s.access, map[string]any{
			"type": "EXPENSE", "occurred_on": "2026-11-03", "account_id": bank, "amount": amount, "category_id": category,
		}), http.StatusCreated)
	}
	spend("30", super)
	spend("10", cafe)
	spend("5", food)
	spend("20", transport)
	if r := ta.do("POST", ws+"/transactions", s.access, map[string]any{
		"type": "INCOME", "occurred_on": "2026-11-03", "account_id": bank, "amount": "1", "category_id": super,
	}); r.errCode() != "category_kind_mismatch" {
		t.Fatalf("income with expense subcategory: %s", r.Raw)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/transactions?category_id="+food, s.access, nil), http.StatusOK).list()); n != 3 {
		t.Fatalf("parent filter: %d transactions", n)
	}
	if n := len(ta.expect(ta.do("GET", ws+"/transactions?category_id="+super, s.access, nil), http.StatusOK).list()); n != 1 {
		t.Fatalf("subcategory filter: %d transactions", n)
	}
	report := ta.expect(ta.do("GET", ws+"/reports/categories?type=EXPENSE&from=2026-11-01&to=2026-11-30", s.access, nil), http.StatusOK).list()
	if len(report) != 2 || report[0]["category_id"] != food || report[0]["total"] != "45.00" || report[0]["count"] != float64(3) ||
		report[1]["name"] != "Transporte" {
		t.Fatalf("report: %v", report)
	}
	inner := ta.expect(ta.do("GET", ws+"/reports/categories?type=EXPENSE&from=2026-11-01&to=2026-11-30&parent_id="+food, s.access, nil), http.StatusOK)
	if rows := inner.list(); len(rows) != 3 || rows[0]["name"] != "Supermercado" || rows[2]["name"] != "Comida" || inner.str("total") != "45.00" {
		t.Fatalf("breakdown: %s", inner.Raw)
	}

	// Archiving a parent archives its subcategories; restoring brings back
	// those archived with it, not one archived on its own before.
	ta.expect(ta.do("DELETE", path(restaurants), s.access, nil), http.StatusNoContent)
	ta.expect(ta.do("DELETE", path(food), s.access, nil), http.StatusNoContent)
	active := map[string]bool{}
	for _, c := range ta.expect(ta.do("GET", ws+"/categories", s.access, nil), http.StatusOK).list() {
		active[c["id"].(string)] = true
	}
	if active[food] || active[super] || active[cafe] {
		t.Fatal("subcategories should be archived with the parent")
	}
	if r := ta.do("POST", path(super)+"/restore", s.access, nil); r.errCode() != "parent_archived" {
		t.Fatalf("restore child first: %s", r.Raw)
	}
	if r := ta.do("POST", ws+"/categories", s.access, map[string]any{"name": "Nuevo", "parent_id": food}); r.errCode() != "parent_archived" {
		t.Fatalf("create under archived: %s", r.Raw)
	}
	ta.expect(ta.do("POST", path(food)+"/restore", s.access, nil), http.StatusNoContent)
	active = map[string]bool{}
	for _, c := range ta.expect(ta.do("GET", ws+"/categories", s.access, nil), http.StatusOK).list() {
		active[c["id"].(string)] = true
	}
	if !active[food] || !active[super] || !active[cafe] || active[restaurants] {
		t.Fatalf("restore: %v", active)
	}
	// Restoring a subcategory on its own works once the parent is active.
	ta.expect(ta.do("POST", path(restaurants)+"/restore", s.access, nil), http.StatusNoContent)
}
