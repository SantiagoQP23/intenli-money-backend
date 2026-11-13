package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SantiagoQP23/intenli-money-backend/internal/auth"
	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

const (
	defaultPersonalWorkspaceName = "Personal"
	defaultBaseCurrency          = "USD"
)

// issueSession creates a refresh token in family (a new family when
// familyID is uuid.Nil) plus an access token.
func (a *api) issueSession(ctx context.Context, db store.DB, user store.User, familyID uuid.UUID) (sessionDTO, error) {
	if familyID == uuid.Nil {
		familyID = uuid.New()
	}
	refresh, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return sessionDTO{}, err
	}
	if err := store.CreateRefreshToken(ctx, db, user.ID, familyID, hash, a.now().Add(a.refreshTTL)); err != nil {
		return sessionDTO{}, err
	}
	access, err := a.access.Issue(user.ID)
	if err != nil {
		return sessionDTO{}, err
	}
	return sessionDTO{
		User:         toUserDTO(user),
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(a.access.TTL() / time.Second),
	}, nil
}

// createUserWithPersonalWorkspace is the single place new users are born.
// locale is the user's language ("es" or "en"); the workspace's default
// categories are created in it.
func createUserWithPersonalWorkspace(ctx context.Context, db store.DB, email, name string, passwordHash *string, verified bool, currency, locale string) (store.User, error) {
	user, err := store.CreateUser(ctx, db, email, name, passwordHash, verified, locale)
	if err != nil {
		return store.User{}, err
	}
	if _, err := store.CreateWorkspace(ctx, db, defaultPersonalWorkspaceName, store.WorkspacePersonal, currency, user.ID); err != nil {
		return store.User{}, err
	}
	return user, nil
}

func validatePassword(pw string) error {
	n := len([]rune(pw))
	if n < auth.MinPasswordLength || n > auth.MaxPasswordLength {
		return badRequestCode("invalid_password", "password must be %d to %d characters", auth.MinPasswordLength, auth.MaxPasswordLength)
	}
	return nil
}

func currencyOrDefault(code string) (string, error) {
	if code == "" {
		return defaultBaseCurrency, nil
	}
	if !validCurrency(code) {
		return "", badRequest("base_currency must be a 3-letter ISO 4217 code, like USD")
	}
	return code, nil
}

// POST /v1/auth/register
func (a *api) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email        string `json:"email"`
		Password     string `json:"password"`
		Name         string `json:"name"`
		BaseCurrency string `json:"base_currency"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil {
		req.Email, err = normalizeEmail(req.Email)
	}
	if err == nil {
		err = validatePassword(req.Password)
	}
	if err == nil {
		req.Name, err = cleanName(req.Name, "name", false, 100)
	}
	if err == nil {
		req.BaseCurrency, err = currencyOrDefault(req.BaseCurrency)
	}
	if writeBadRequest(w, err) {
		return
	}

	if !a.currencySupported(w, r, req.BaseCurrency) {
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	var session sessionDTO
	err = store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		user, err := createUserWithPersonalWorkspace(r.Context(), tx, req.Email, req.Name, &hash, false, req.BaseCurrency, requestLocale(r))
		if err != nil {
			return err
		}
		session, err = a.issueSession(r.Context(), tx, user, uuid.Nil)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

// POST /v1/auth/login
func (a *api) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	invalid := func() {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
	}

	email, err := normalizeEmail(req.Email)
	if err != nil {
		auth.CheckDummyPassword(req.Password)
		invalid()
		return
	}
	user, err := store.GetUserByEmail(r.Context(), a.pool, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.internalError(w, r, err)
		return
	}
	if errors.Is(err, store.ErrNotFound) || user.PasswordHash == nil {
		auth.CheckDummyPassword(req.Password)
		invalid()
		return
	}
	ok, err := auth.CheckPassword(req.Password, *user.PasswordHash)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if !ok {
		invalid()
		return
	}
	session, err := a.issueSession(r.Context(), a.pool, user, uuid.Nil)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// POST /v1/auth/google
//
// The app signs in with Google and sends the ID token. We find the user by
// Google identity, else link to an existing account with the same email,
// else create a new account.
func (a *api) handleGoogle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDToken string `json:"id_token"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	if req.IDToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "id_token is required")
		return
	}
	identity, err := a.google.Verify(r.Context(), req.IDToken)
	switch {
	case errors.Is(err, auth.ErrGoogleNotConfigured):
		writeError(w, http.StatusNotImplemented, "google_not_configured", "Google sign-in is not configured on this server")
		return
	case err != nil:
		writeError(w, http.StatusUnauthorized, "invalid_google_token", "Google token is invalid or expired")
		return
	case !identity.EmailVerified:
		writeError(w, http.StatusUnauthorized, "email_not_verified", "your Google email address is not verified")
		return
	}
	email, err := normalizeEmail(identity.Email)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_google_token", "Google token has an unusable email")
		return
	}

	var session sessionDTO
	err = store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		user, err := store.GetUserByIdentity(ctx, tx, "google", identity.Subject)
		switch {
		case err == nil:
			// Returning Google user.
		case errors.Is(err, store.ErrNotFound):
			user, err = store.GetUserByEmailForUpdate(ctx, tx, email)
			switch {
			case err == nil:
				// Link Google to the existing account. If its email was never
				// verified, whoever set the password didn't prove they own the
				// address, so drop the password and its sessions.
				if err := store.CreateIdentity(ctx, tx, user.ID, "google", identity.Subject); err != nil {
					return err
				}
				dropPassword := !user.EmailVerified && user.PasswordHash != nil
				if err := store.MarkEmailVerified(ctx, tx, user.ID, dropPassword); err != nil {
					return err
				}
				if dropPassword {
					if err := store.RevokeAllRefreshTokens(ctx, tx, user.ID); err != nil {
						return err
					}
				}
				if user, err = store.GetUserByID(ctx, tx, user.ID); err != nil {
					return err
				}
			case errors.Is(err, store.ErrNotFound):
				user, err = createUserWithPersonalWorkspace(ctx, tx, email, identity.Name, nil, true, defaultBaseCurrency, requestLocale(r))
				if err != nil {
					return err
				}
				if err := store.CreateIdentity(ctx, tx, user.ID, "google", identity.Subject); err != nil {
					return err
				}
			default:
				return err
			}
		default:
			return err
		}
		session, err = a.issueSession(ctx, tx, user, uuid.Nil)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		// Two first sign-ins raced; the other one won. Retrying succeeds.
		writeError(w, http.StatusConflict, "retry", "please try again")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

var errRefreshRejected = errors.New("refresh rejected")

// POST /v1/auth/refresh rotates the refresh token. Reusing an already
// rotated token revokes every token in its family (likely theft).
func (a *api) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}

	var session sessionDTO
	reused := false
	err := store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		tok, err := store.GetRefreshTokenForUpdate(ctx, tx, auth.HashOpaqueToken(req.RefreshToken))
		if errors.Is(err, store.ErrNotFound) {
			return errRefreshRejected
		}
		if err != nil {
			return err
		}
		if tok.RevokedAt != nil {
			reused = true
			return store.RevokeRefreshFamily(ctx, tx, tok.FamilyID)
		}
		if !a.now().Before(tok.ExpiresAt) {
			return errRefreshRejected
		}
		if err := store.RevokeRefreshToken(ctx, tx, tok.ID); err != nil {
			return err
		}
		user, err := store.GetUserByID(ctx, tx, tok.UserID)
		if err != nil {
			return err
		}
		session, err = a.issueSession(ctx, tx, user, tok.FamilyID)
		return err
	})
	if err == nil && reused {
		err = errRefreshRejected
	}
	if errors.Is(err, errRefreshRejected) {
		writeError(w, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// POST /v1/auth/logout revokes the session the refresh token belongs to.
func (a *api) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	err := store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		tok, err := store.GetRefreshTokenForUpdate(r.Context(), tx, auth.HashOpaqueToken(req.RefreshToken))
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return store.RevokeRefreshFamily(r.Context(), tx, tok.FamilyID)
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/users/me
func (a *api) handleGetMe(w http.ResponseWriter, r *http.Request) {
	user, err := store.GetUserByID(r.Context(), a.pool, userIDFrom(r.Context()))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid_token", "user no longer exists")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(user))
}

// PATCH /v1/users/me
// Changes name and/or locale ("es" or "en"); a field left out stays as it is.
func (a *api) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   *string `json:"name"`
		Locale *string `json:"locale"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil && req.Name == nil && req.Locale == nil {
		err = badRequest("name or locale is required")
	}
	if err == nil && req.Name != nil {
		var name string
		name, err = cleanName(*req.Name, "name", true, 100)
		req.Name = &name
	}
	if err == nil && req.Locale != nil && !store.ValidLocale(*req.Locale) {
		err = badRequest("locale must be es or en")
	}
	if writeBadRequest(w, err) {
		return
	}
	user, err := store.UpdateUser(r.Context(), a.pool, userIDFrom(r.Context()), req.Name, req.Locale)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(user))
}

// requestLocale picks "en" when English is the first language in
// Accept-Language and "es" otherwise. The app sends its active language.
func requestLocale(r *http.Request) string {
	first, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	first, _, _ = strings.Cut(first, ";")
	lang, _, _ := strings.Cut(strings.TrimSpace(strings.ToLower(first)), "-")
	if lang == "en" {
		return "en"
	}
	return "es"
}
