package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"testing"

	"github.com/SantiagoQP23/intenli-money-backend/internal/defaults"
	"github.com/SantiagoQP23/intenli-money-backend/migrations"
)

func countByKind(cats []map[string]any) (income, expense int) {
	for _, c := range cats {
		if c["kind"] == "INCOME" {
			income++
		} else {
			expense++
		}
	}
	return
}

func TestDefaultCategoriesByWorkspaceType(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, personal := ta.setupWorkspace("defaults@example.com")

	cats := ta.expect(ta.do("GET", personal+"/categories", s.access, nil), http.StatusOK).list()
	if in, ex := countByKind(cats); in != 5 || ex != 15 {
		t.Fatalf("personal: %d income, %d expense", in, ex)
	}
	for _, c := range cats {
		if icon, _ := c["icon"].(string); !defaults.ValidIcon(icon) {
			t.Errorf("%v has no valid icon", c["name"])
		}
	}

	biz := ta.expect(ta.do("POST", "/v1/workspaces", s.access, map[string]any{"name": "Tienda", "type": "BUSINESS"}), http.StatusCreated)
	bizCats := ta.expect(ta.do("GET", "/v1/workspaces/"+biz.str("id")+"/categories", s.access, nil), http.StatusOK).list()
	if in, ex := countByKind(bizCats); in != 4 || ex != 13 {
		t.Fatalf("business: %d income, %d expense", in, ex)
	}
	ta.categoryID(s.access, "/v1/workspaces/"+biz.str("id"), "EXPENSE", "Mercadería")

	shared := ta.expect(ta.do("POST", "/v1/workspaces", s.access, map[string]any{"name": "Casa", "type": "SHARED"}), http.StatusCreated)
	ta.categoryID(s.access, "/v1/workspaces/"+shared.str("id"), "EXPENSE", "Mascotas")
}

func TestCategoryIcons(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("icons@example.com")
	if n := len(ta.expect(ta.do("GET", "/v1/category-icons", "", nil), http.StatusOK).list()); n != len(defaults.Icons) {
		t.Fatalf("icons: %d", n)
	}
	ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"kind": "EXPENSE", "name": "Bebé", "icon": "rocket"}), http.StatusBadRequest)
	c := ta.expect(ta.do("POST", ws+"/categories", s.access, map[string]any{"kind": "EXPENSE", "name": "Bebé", "icon": "baby"}), http.StatusCreated)
	if c.str("icon") != "baby" {
		t.Fatalf("create: %s", c.Raw)
	}
	path := ws + "/categories/" + c.str("id")
	iconOf := func() any {
		for _, x := range ta.expect(ta.do("GET", ws+"/categories", s.access, nil), http.StatusOK).list() {
			if x["id"] == c.str("id") {
				return x["icon"]
			}
		}
		return "missing"
	}
	ta.expect(ta.do("PATCH", path, s.access, map[string]any{"name": "Bebés"}), http.StatusNoContent)
	if iconOf() != "baby" {
		t.Fatal("rename without icon should keep it")
	}
	ta.expect(ta.do("PATCH", path, s.access, map[string]any{"name": "Bebés", "icon": "gift"}), http.StatusNoContent)
	if iconOf() != "gift" {
		t.Fatal("icon not changed")
	}
	ta.expect(ta.do("PATCH", path, s.access, map[string]any{"name": "Bebés", "icon": ""}), http.StatusNoContent)
	if iconOf() != nil {
		t.Fatal("icon not cleared")
	}
}

// A workspace created before the new defaults: the backfill adds what's
// missing, sets icons, and leaves renamed or archived categories alone.
func TestBackfillDefaultCategories(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("old@example.com")
	ctx := context.Background()
	wsID := ws[len("/v1/workspaces/"):]
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := ta.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Rewind to the old 12 defaults, without icons.
	exec(`DELETE FROM categories WHERE workspace_id = $1`, wsID)
	for _, c := range [][2]string{
		{"INCOME", "Salario"}, {"INCOME", "Ventas"}, {"INCOME", "Otros ingresos"},
		{"EXPENSE", "Comida"}, {"EXPENSE", "Transporte"}, {"EXPENSE", "Vivienda"}, {"EXPENSE", "Servicios"},
		{"EXPENSE", "Salud"}, {"EXPENSE", "Compras"}, {"EXPENSE", "Entretenimiento"}, {"EXPENSE", "Proveedores"},
		{"EXPENSE", "Otros gastos"},
	} {
		exec(`INSERT INTO categories (workspace_id, kind, name) VALUES ($1, $2, $3)`, wsID, c[0], c[1])
	}
	// The person archived Mascotas once and renamed Comida.
	exec(`INSERT INTO categories (workspace_id, kind, name, archived_at) VALUES ($1, 'EXPENSE', 'mascotas', now())`, wsID)
	exec(`UPDATE categories SET name = 'Súper' WHERE workspace_id = $1 AND name = 'Comida'`, wsID)

	db, err := sql.Open("pgx", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := func() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrations.BackfillDefaultCategories(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run() // a second run changes nothing

	cats := ta.expect(ta.do("GET", ws+"/categories?include_archived=true", s.access, nil), http.StatusOK).list()
	byName := map[string]map[string]any{}
	for _, c := range cats {
		name := c["name"].(string)
		if byName[name] != nil {
			t.Fatalf("duplicate %s", name)
		}
		byName[name] = c
	}
	for _, want := range []string{"Restaurantes", "Servicios básicos", "Comida", "Negocio propio", "Servicios", "Ventas", "Súper"} {
		if byName[want] == nil {
			t.Errorf("missing %s", want)
		}
	}
	if byName["Mascotas"] != nil || byName["mascotas"]["archived"] != true {
		t.Error("archived Mascotas should stay archived and not come back")
	}
	if byName["Transporte"]["icon"] != "bus" || byName["Servicios"]["icon"] != nil || byName["Súper"]["icon"] != nil {
		t.Errorf("icons: transporte=%v servicios=%v súper=%v", byName["Transporte"]["icon"], byName["Servicios"]["icon"], byName["Súper"]["icon"])
	}
	// 12 old + archived mascotas + the 12 defaults not already there. Comida
	// counts as new because the person renamed theirs to Súper.
	if len(cats) != 12+1+(20-8) {
		t.Errorf("got %d categories", len(cats))
	}
}
