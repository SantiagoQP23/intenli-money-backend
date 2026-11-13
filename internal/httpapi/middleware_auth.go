package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxMembership
)

// requireAuth verifies the Bearer access token and stores the user ID.
func (a *api) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing access token")
			return
		}
		userID, err := a.access.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_token", "access token is invalid or expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUserID, userID)))
	})
}

func userIDFrom(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(ctxUserID).(uuid.UUID)
	return id
}

// loadWorkspace resolves {workspaceID} and checks the caller is a member.
// Non-members get 404, so workspace IDs can't be probed.
func (a *api) loadWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workspaceID, ok := uuidParam(r, "workspaceID")
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found", "workspace not found")
			return
		}
		m, err := store.GetMembership(r.Context(), a.db, workspaceID, userIDFrom(r.Context()))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace_not_found", "workspace not found")
			return
		}
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxMembership, m)))
	})
}

func membershipFrom(ctx context.Context) store.Membership {
	m, _ := ctx.Value(ctxMembership).(store.Membership)
	return m
}

// requireRole allows the request only if the caller's role is at least min.
// Use after loadWorkspace.
func requireRole(min store.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !membershipFrom(r.Context()).Role.AtLeast(min) {
				writeError(w, http.StatusForbidden, "forbidden", "your role in this workspace does not allow this")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
