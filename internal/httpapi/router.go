// Package httpapi wires the Chi router, middleware and HTTP handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SantiagoQP23/intenli-money-backend/internal/auth"
	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// Pinger is the part of the database pool the health checks need.
type Pinger interface {
	Ping(ctx context.Context) error
}

type Deps struct {
	Logger      *slog.Logger
	DB          Pinger
	Pool        *pgxpool.Pool
	CORSOrigins []string

	AccessTokens  *auth.AccessTokens
	Google        auth.GoogleVerifier
	RefreshTTL    time.Duration
	InvitationTTL time.Duration
	// AuthRateLimit is requests per minute per IP on /v1/auth (0 disables).
	AuthRateLimit int
}

type api struct {
	logger        *slog.Logger
	db            store.DB
	pool          *pgxpool.Pool
	access        *auth.AccessTokens
	google        auth.GoogleVerifier
	refreshTTL    time.Duration
	invitationTTL time.Duration
	now           func() time.Time
	currencies    currencyCache
}

func NewRouter(d Deps) http.Handler {
	a := &api{
		logger:        d.Logger,
		pool:          d.Pool,
		access:        d.AccessTokens,
		google:        d.Google,
		refreshTTL:    d.RefreshTTL,
		invitationTTL: d.InvitationTTL,
		now:           time.Now,
	}
	if d.Pool != nil {
		a.db = d.Pool
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger(d.Logger))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	if len(d.CORSOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: d.CORSOrigins,
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Accept", "Authorization", "Content-Type"},
			MaxAge:         300,
		}))
	}

	r.Get("/healthz", handleLiveness)
	r.Get("/readyz", handleReadiness(d.DB))

	r.Route("/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			if d.AuthRateLimit > 0 {
				r.Use(httprate.Limit(d.AuthRateLimit, time.Minute,
					httprate.WithKeyFuncs(httprate.KeyByIP),
					httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
						writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again in a minute")
					})))
			}
			r.Post("/register", a.handleRegister)
			r.Post("/login", a.handleLogin)
			r.Post("/google", a.handleGoogle)
			r.Post("/refresh", a.handleRefresh)
			r.Post("/logout", a.handleLogout)
		})

		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth)

			r.Get("/users/me", a.handleGetMe)
			r.Patch("/users/me", a.handleUpdateMe)

			r.Post("/invitations/accept", a.handleAcceptInvitation)
			r.Get("/currencies", a.handleListCurrencies)

			r.Get("/workspaces", a.handleListWorkspaces)
			r.Post("/workspaces", a.handleCreateWorkspace)

			r.Route("/workspaces/{workspaceID}", func(r chi.Router) {
				r.Use(a.loadWorkspace)
				owner := requireRole(store.RoleOwner)

				r.Get("/", a.handleGetWorkspace)
				r.With(owner).Patch("/", a.handleUpdateWorkspace)
				r.With(owner).Delete("/", a.handleDeleteWorkspace)

				r.Get("/members", a.handleListMembers)
				r.With(owner).Patch("/members/{userID}", a.handleUpdateMember)
				r.Delete("/members/{userID}", a.handleRemoveMember) // owner, or self to leave

				r.With(owner).Get("/invitations", a.handleListInvitations)
				r.With(owner).Post("/invitations", a.handleCreateInvitation)
				r.With(owner).Delete("/invitations/{invitationID}", a.handleRevokeInvitation)

				// Financial data: every member reads, editors and owners write.
				editor := requireRole(store.RoleEditor)

				r.Get("/accounts", a.handleListAccounts)
				r.With(editor).Post("/accounts", a.handleCreateAccount)
				r.Get("/accounts/{accountID}", a.handleGetAccount)
				r.With(editor).Patch("/accounts/{accountID}", a.handleUpdateAccount)

				r.Get("/categories", a.handleListCategories)
				r.With(editor).Post("/categories", a.handleCreateCategory)
				r.With(editor).Patch("/categories/{categoryID}", a.handleRenameCategory)
				r.With(editor).Delete("/categories/{categoryID}", a.handleArchiveCategory)

				r.Get("/exchange-rates", a.handleListRates)
				r.With(editor).Put("/exchange-rates", a.handleSetRate)

				r.Get("/transactions", a.handleListTransactions)
				r.With(editor).Post("/transactions", a.handleCreateTransaction)
				r.Get("/transactions/{transactionID}", a.handleGetTransaction)
				r.With(editor).Put("/transactions/{transactionID}", a.handleReplaceTransaction)
				r.With(editor).Delete("/transactions/{transactionID}", a.handleDeleteTransaction)
			})
		})
	})

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return r
}

func (a *api) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("internal error",
		"err", err,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", middleware.GetReqID(r.Context()),
	)
	writeError(w, http.StatusInternalServerError, "internal_error", "something went wrong")
}

func emailEqual(a, b string) bool { return strings.EqualFold(a, b) }

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
