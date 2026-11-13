package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestLocale(t *testing.T) {
	for header, want := range map[string]string{
		"":               "es",
		"en":             "en",
		"en-US,en;q=0.9": "en",
		"EN-gb":          "en",
		"es-EC,en;q=0.8": "es",
		"fr-FR,en;q=0.5": "es",
		"  en ; q=1":     "en",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", header)
		if got := requestLocale(r); got != want {
			t.Errorf("%q: got %s, want %s", header, got, want)
		}
	}
}

// Sign-up in English creates an English user with English default categories;
// the locale can be changed later and only affects new workspaces.
func TestUserLocale(t *testing.T) {
	ta := newTestAPI(t, nil)
	b, _ := json.Marshal(map[string]any{"email": "en@example.com", "password": "s3cret-pass"})
	req := httptest.NewRequest("POST", "/v1/auth/register", bytes.NewReader(b))
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rec := httptest.NewRecorder()
	ta.handler.ServeHTTP(rec, req)
	var body struct {
		AccessToken string         `json:"access_token"`
		User        map[string]any `json:"user"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.User["locale"] != "en" {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	token := body.AccessToken
	ws := ta.expect(ta.do("GET", "/v1/workspaces", token, nil), http.StatusOK).list()
	personal := "/v1/workspaces/" + ws[0]["id"].(string)
	ta.categoryID(token, personal, "EXPENSE", "Groceries")
	ta.categoryID(token, personal, "INCOME", "Salary")

	// Spanish is the default without the header.
	es := ta.register("es@example.com")
	if me := ta.expect(ta.do("GET", "/v1/users/me", es.access, nil), http.StatusOK); me.str("locale") != "es" {
		t.Fatalf("default locale: %s", me.Raw)
	}

	// Switching to Spanish: new workspaces get Spanish names, old ones stay.
	if r := ta.do("PATCH", "/v1/users/me", token, map[string]any{"locale": "fr"}); r.errCode() != "invalid_request" {
		t.Fatalf("bad locale: %s", r.Raw)
	}
	ta.expect(ta.do("PATCH", "/v1/users/me", token, map[string]any{}), http.StatusBadRequest)
	me := ta.expect(ta.do("PATCH", "/v1/users/me", token, map[string]any{"locale": "es"}), http.StatusOK)
	if me.str("locale") != "es" {
		t.Fatalf("patch locale: %s", me.Raw)
	}
	biz := ta.expect(ta.do("POST", "/v1/workspaces", token, map[string]any{"name": "Shop", "type": "BUSINESS"}), http.StatusCreated)
	ta.categoryID(token, "/v1/workspaces/"+biz.str("id"), "INCOME", "Ventas")
	ta.categoryID(token, personal, "EXPENSE", "Groceries")

	// Changing only the name keeps the locale.
	me = ta.expect(ta.do("PATCH", "/v1/users/me", token, map[string]any{"name": "Sam"}), http.StatusOK)
	if me.str("name") != "Sam" || me.str("locale") != "es" {
		t.Fatalf("patch name: %s", me.Raw)
	}
}

// Form errors users can cause have their own codes, so the app can translate them.
func TestFormErrorCodes(t *testing.T) {
	ta := newTestAPI(t, nil)
	if r := ta.do("POST", "/v1/auth/register", "", map[string]any{"email": "nope", "password": "s3cret-pass"}); r.errCode() != "invalid_email" {
		t.Fatalf("email: %s", r.Raw)
	}
	if r := ta.do("POST", "/v1/auth/register", "", map[string]any{"email": "a@example.com", "password": "short"}); r.errCode() != "invalid_password" {
		t.Fatalf("password: %s", r.Raw)
	}
}
