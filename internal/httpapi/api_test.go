package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SantiagoQP23/intenli-money-backend/internal/auth"
	"github.com/SantiagoQP23/intenli-money-backend/internal/database"
	"github.com/SantiagoQP23/intenli-money-backend/migrations"
)

// These tests hit a real Postgres. They are skipped unless TEST_DATABASE_URL
// is set (CI sets it; locally run `make db-up && make test-db`).

type fakeGoogle map[string]auth.GoogleIdentity

func (f fakeGoogle) Verify(_ context.Context, token string) (auth.GoogleIdentity, error) {
	id, ok := f[token]
	if !ok {
		return auth.GoogleIdentity{}, auth.ErrInvalidToken
	}
	return id, nil
}

type testAPI struct {
	t       *testing.T
	handler http.Handler
	pool    *pgxpool.Pool
}

func newTestAPI(t *testing.T, google fakeGoogle) *testAPI {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	if err := migrations.Up(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE users, workspaces CASCADE`); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            pool,
		Pool:          pool,
		AccessTokens:  auth.NewAccessTokens([]byte("test-secret-test-secret-test-secret"), 15*time.Minute),
		Google:        google,
		RefreshTTL:    24 * time.Hour,
		InvitationTTL: 24 * time.Hour,
	})
	return &testAPI{t: t, handler: h, pool: pool}
}

type resp struct {
	Code int
	Body map[string]any
	Raw  string
}

func (r resp) str(key string) string { s, _ := r.Body[key].(string); return s }

func (r resp) errCode() string {
	e, _ := r.Body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (r resp) list() []map[string]any {
	items, _ := r.Body["data"].([]any)
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i], _ = it.(map[string]any)
	}
	return out
}

func (ta *testAPI) do(method, path, token string, body any) resp {
	ta.t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	ta.handler.ServeHTTP(rec, req)
	out := resp{Code: rec.Code, Raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.Body)
	return out
}

func (ta *testAPI) expect(r resp, code int) resp {
	ta.t.Helper()
	if r.Code != code {
		ta.t.Fatalf("got %d, want %d: %s", r.Code, code, r.Raw)
	}
	return r
}

type session struct {
	access, refresh, userID string
}

func (ta *testAPI) register(email string) session {
	ta.t.Helper()
	r := ta.expect(ta.do("POST", "/v1/auth/register", "", map[string]any{
		"email": email, "password": "s3cret-pass", "name": "Test",
	}), http.StatusCreated)
	user, _ := r.Body["user"].(map[string]any)
	return session{access: r.str("access_token"), refresh: r.str("refresh_token"), userID: user["id"].(string)}
}

func TestRegisterLoginAndPersonalWorkspace(t *testing.T) {
	ta := newTestAPI(t, nil)
	s := ta.register("Ana@Example.com")

	// Email is normalized; duplicates (any case) are rejected.
	me := ta.expect(ta.do("GET", "/v1/users/me", s.access, nil), http.StatusOK)
	if me.str("email") != "ana@example.com" || me.Body["has_password"] != true {
		t.Fatalf("me: %s", me.Raw)
	}
	if r := ta.do("POST", "/v1/auth/register", "", map[string]any{"email": "ANA@example.com", "password": "another-pass"}); r.errCode() != "email_taken" {
		t.Fatalf("duplicate register: %s", r.Raw)
	}

	// A personal workspace exists, owned by the user, in USD.
	ws := ta.expect(ta.do("GET", "/v1/workspaces", s.access, nil), http.StatusOK).list()
	if len(ws) != 1 || ws[0]["type"] != "PERSONAL" || ws[0]["role"] != "OWNER" || ws[0]["base_currency"] != "USD" {
		t.Fatalf("workspaces: %v", ws)
	}

	// Login works with the right password only, with the same error for an unknown email.
	ta.expect(ta.do("POST", "/v1/auth/login", "", map[string]any{"email": "ana@example.com", "password": "s3cret-pass"}), http.StatusOK)
	wrong := ta.expect(ta.do("POST", "/v1/auth/login", "", map[string]any{"email": "ana@example.com", "password": "nope-nope"}), http.StatusUnauthorized)
	unknown := ta.expect(ta.do("POST", "/v1/auth/login", "", map[string]any{"email": "who@example.com", "password": "nope-nope"}), http.StatusUnauthorized)
	if wrong.errCode() != "invalid_credentials" || unknown.errCode() != "invalid_credentials" {
		t.Fatalf("login errors differ: %s / %s", wrong.Raw, unknown.Raw)
	}
}

func TestRegisterValidation(t *testing.T) {
	ta := newTestAPI(t, nil)
	cases := []map[string]any{
		{"email": "not-an-email", "password": "s3cret-pass"},
		{"email": "a@example.com", "password": "short"},
		{"email": "a@example.com", "password": "s3cret-pass", "base_currency": "usd"},
		{"email": "a@example.com", "password": "s3cret-pass", "unexpected": true},
	}
	for i, body := range cases {
		if r := ta.do("POST", "/v1/auth/register", "", body); r.Code != http.StatusBadRequest {
			t.Errorf("case %d: got %d %s", i, r.Code, r.Raw)
		}
	}
}

func TestProtectedRoutesNeedToken(t *testing.T) {
	ta := newTestAPI(t, nil)
	ta.expect(ta.do("GET", "/v1/users/me", "", nil), http.StatusUnauthorized)
	ta.expect(ta.do("GET", "/v1/workspaces", "garbage", nil), http.StatusUnauthorized)
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	ta := newTestAPI(t, nil)
	s := ta.register("rot@example.com")

	r1 := ta.expect(ta.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": s.refresh}), http.StatusOK)
	newRefresh := r1.str("refresh_token")
	if newRefresh == "" || newRefresh == s.refresh {
		t.Fatalf("refresh token not rotated: %s", r1.Raw)
	}

	// Replaying the old token is rejected and kills the whole family...
	ta.expect(ta.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": s.refresh}), http.StatusUnauthorized)
	// ...including the token that replaced it.
	ta.expect(ta.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": newRefresh}), http.StatusUnauthorized)

	// Logout revokes a fresh session.
	l := ta.expect(ta.do("POST", "/v1/auth/login", "", map[string]any{"email": "rot@example.com", "password": "s3cret-pass"}), http.StatusOK)
	ta.expect(ta.do("POST", "/v1/auth/logout", "", map[string]any{"refresh_token": l.str("refresh_token")}), http.StatusNoContent)
	ta.expect(ta.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": l.str("refresh_token")}), http.StatusUnauthorized)
}

func TestGoogleSignIn(t *testing.T) {
	google := fakeGoogle{
		"new-user":   {Subject: "g-1", Email: "new@example.com", EmailVerified: true, Name: "New Person"},
		"link-user":  {Subject: "g-2", Email: "existing@example.com", EmailVerified: true},
		"unverified": {Subject: "g-3", Email: "x@example.com", EmailVerified: false},
	}
	ta := newTestAPI(t, google)

	// New user: account + personal workspace, no password.
	r := ta.expect(ta.do("POST", "/v1/auth/google", "", map[string]any{"id_token": "new-user"}), http.StatusOK)
	user, _ := r.Body["user"].(map[string]any)
	if user["email"] != "new@example.com" || user["name"] != "New Person" || user["email_verified"] != true || user["has_password"] != false {
		t.Fatalf("google user: %s", r.Raw)
	}
	ws := ta.expect(ta.do("GET", "/v1/workspaces", r.str("access_token"), nil), http.StatusOK).list()
	if len(ws) != 1 || ws[0]["type"] != "PERSONAL" {
		t.Fatalf("google workspaces: %v", ws)
	}
	// Signing in again returns the same user, not a new one.
	again := ta.expect(ta.do("POST", "/v1/auth/google", "", map[string]any{"id_token": "new-user"}), http.StatusOK)
	if again.Body["user"].(map[string]any)["id"] != user["id"] {
		t.Fatal("second Google sign-in created another user")
	}

	// Existing unverified password account with the same email: Google links
	// to it, verifies the email and drops the unproven password + sessions.
	pw := ta.register("existing@example.com")
	linked := ta.expect(ta.do("POST", "/v1/auth/google", "", map[string]any{"id_token": "link-user"}), http.StatusOK)
	lu := linked.Body["user"].(map[string]any)
	if lu["id"] != pw.userID || lu["email_verified"] != true || lu["has_password"] != false {
		t.Fatalf("linking: %s", linked.Raw)
	}
	ta.expect(ta.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": pw.refresh}), http.StatusUnauthorized)
	ta.expect(ta.do("POST", "/v1/auth/login", "", map[string]any{"email": "existing@example.com", "password": "s3cret-pass"}), http.StatusUnauthorized)

	// Rejected tokens.
	if r := ta.do("POST", "/v1/auth/google", "", map[string]any{"id_token": "unverified"}); r.errCode() != "email_not_verified" {
		t.Fatalf("unverified: %s", r.Raw)
	}
	if r := ta.do("POST", "/v1/auth/google", "", map[string]any{"id_token": "forged"}); r.errCode() != "invalid_google_token" {
		t.Fatalf("forged: %s", r.Raw)
	}
}

func TestGoogleNotConfigured(t *testing.T) {
	ta := newTestAPI(t, nil)
	ta.handler = NewRouter(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: ta.pool, Pool: ta.pool,
		AccessTokens: auth.NewAccessTokens([]byte("test-secret-test-secret-test-secret"), time.Minute),
		Google:       auth.NewGoogleVerifier(nil),
	})
	if r := ta.do("POST", "/v1/auth/google", "", map[string]any{"id_token": "x"}); r.Code != http.StatusNotImplemented {
		t.Fatalf("got %d %s", r.Code, r.Raw)
	}
}

func TestWorkspacePermissions(t *testing.T) {
	ta := newTestAPI(t, nil)
	owner := ta.register("owner@example.com")
	editor := ta.register("editor@example.com")
	outsider := ta.register("outsider@example.com")

	ws := ta.expect(ta.do("POST", "/v1/workspaces", owner.access, map[string]any{
		"name": "Camaronera", "type": "BUSINESS", "base_currency": "USD",
	}), http.StatusCreated)
	wsPath := "/v1/workspaces/" + ws.str("id")

	// Personal workspaces can't be created by hand.
	ta.expect(ta.do("POST", "/v1/workspaces", owner.access, map[string]any{"name": "x", "type": "PERSONAL"}), http.StatusBadRequest)

	// Outsiders can't see it, and get 404 rather than 403.
	if r := ta.do("GET", wsPath, outsider.access, nil); r.errCode() != "workspace_not_found" {
		t.Fatalf("outsider: %s", r.Raw)
	}
	ta.expect(ta.do("GET", "/v1/workspaces/not-a-uuid", owner.access, nil), http.StatusNotFound)

	// Invite the editor; the token only works for the invited email.
	inv := ta.expect(ta.do("POST", wsPath+"/invitations", owner.access, map[string]any{"email": "Editor@example.com", "role": "EDITOR"}), http.StatusCreated)
	token := inv.str("token")
	if token == "" {
		t.Fatalf("no token: %s", inv.Raw)
	}
	if r := ta.do("POST", "/v1/invitations/accept", outsider.access, map[string]any{"token": token}); r.errCode() != "invitation_email_mismatch" {
		t.Fatalf("wrong user accepted: %s", r.Raw)
	}
	acc := ta.expect(ta.do("POST", "/v1/invitations/accept", editor.access, map[string]any{"token": token}), http.StatusOK)
	if acc.str("role") != "EDITOR" {
		t.Fatalf("accept: %s", acc.Raw)
	}
	// Single use.
	ta.expect(ta.do("POST", "/v1/invitations/accept", editor.access, map[string]any{"token": token}), http.StatusNotFound)

	// The editor sees the workspace but can't manage it.
	ta.expect(ta.do("GET", wsPath, editor.access, nil), http.StatusOK)
	ta.expect(ta.do("PATCH", wsPath, editor.access, map[string]any{"name": "Mine now"}), http.StatusForbidden)
	ta.expect(ta.do("POST", wsPath+"/invitations", editor.access, map[string]any{"email": "z@example.com", "role": "VIEWER"}), http.StatusForbidden)
	ta.expect(ta.do("DELETE", wsPath+"/members/"+owner.userID, editor.access, nil), http.StatusForbidden)
	members := ta.expect(ta.do("GET", wsPath+"/members", editor.access, nil), http.StatusOK).list()
	if len(members) != 2 {
		t.Fatalf("members: %v", members)
	}

	// The last owner can't demote themselves or leave.
	if r := ta.do("PATCH", wsPath+"/members/"+owner.userID, owner.access, map[string]any{"role": "VIEWER"}); r.errCode() != "last_owner" {
		t.Fatalf("demote last owner: %s", r.Raw)
	}
	if r := ta.do("DELETE", wsPath+"/members/"+owner.userID, owner.access, nil); r.errCode() != "last_owner" {
		t.Fatalf("last owner left: %s", r.Raw)
	}

	// Promote the editor; now the original owner can step down.
	ta.expect(ta.do("PATCH", wsPath+"/members/"+editor.userID, owner.access, map[string]any{"role": "OWNER"}), http.StatusNoContent)
	ta.expect(ta.do("PATCH", wsPath+"/members/"+owner.userID, owner.access, map[string]any{"role": "VIEWER"}), http.StatusNoContent)
	ta.expect(ta.do("PATCH", wsPath, owner.access, map[string]any{"name": "x"}), http.StatusForbidden)

	// A member can leave; afterwards the workspace is invisible to them.
	ta.expect(ta.do("DELETE", wsPath+"/members/"+owner.userID, owner.access, nil), http.StatusNoContent)
	ta.expect(ta.do("GET", wsPath, owner.access, nil), http.StatusNotFound)

	// Deleting archives it for everyone.
	ta.expect(ta.do("DELETE", wsPath, editor.access, nil), http.StatusNoContent)
	ta.expect(ta.do("GET", wsPath, editor.access, nil), http.StatusNotFound)
}

func TestPersonalWorkspaceRules(t *testing.T) {
	ta := newTestAPI(t, nil)
	s := ta.register("solo@example.com")
	ws := ta.expect(ta.do("GET", "/v1/workspaces", s.access, nil), http.StatusOK).list()
	path := fmt.Sprintf("/v1/workspaces/%s", ws[0]["id"])

	if r := ta.do("POST", path+"/invitations", s.access, map[string]any{"email": "a@example.com", "role": "VIEWER"}); r.errCode() != "personal_workspace" {
		t.Fatalf("invite into personal: %s", r.Raw)
	}
	if r := ta.do("DELETE", path, s.access, nil); r.errCode() != "personal_workspace" {
		t.Fatalf("delete personal: %s", r.Raw)
	}
	ta.expect(ta.do("PATCH", path, s.access, map[string]any{"name": "Mis finanzas"}), http.StatusOK)
}

func TestInvitationRevokeAndReinvite(t *testing.T) {
	ta := newTestAPI(t, nil)
	owner := ta.register("o@example.com")
	guest := ta.register("g@example.com")
	ws := ta.expect(ta.do("POST", "/v1/workspaces", owner.access, map[string]any{"name": "Casa", "type": "SHARED"}), http.StatusCreated)
	path := "/v1/workspaces/" + ws.str("id") + "/invitations"

	first := ta.expect(ta.do("POST", path, owner.access, map[string]any{"email": "g@example.com", "role": "VIEWER"}), http.StatusCreated)
	// Re-inviting replaces the open invitation, invalidating the first token.
	second := ta.expect(ta.do("POST", path, owner.access, map[string]any{"email": "g@example.com", "role": "EDITOR"}), http.StatusCreated)
	if open := ta.expect(ta.do("GET", path, owner.access, nil), http.StatusOK).list(); len(open) != 1 || open[0]["role"] != "EDITOR" {
		t.Fatalf("open invitations: %v", open)
	}
	ta.expect(ta.do("POST", "/v1/invitations/accept", guest.access, map[string]any{"token": first.str("token")}), http.StatusNotFound)

	ta.expect(ta.do("DELETE", path+"/"+second.str("id"), owner.access, nil), http.StatusNoContent)
	ta.expect(ta.do("POST", "/v1/invitations/accept", guest.access, map[string]any{"token": second.str("token")}), http.StatusNotFound)
}
