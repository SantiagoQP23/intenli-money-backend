package httpapi

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/SantiagoQP23/intenli-money-backend/internal/auth"
	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

// GET /v1/workspaces
func (a *api) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ms, err := store.ListMemberships(r.Context(), a.pool, userIDFrom(r.Context()))
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]workspaceDTO, len(ms))
	for i, m := range ms {
		out[i] = toWorkspaceDTO(m)
	}
	writeJSON(w, http.StatusOK, listDTO[workspaceDTO]{Data: out})
}

// POST /v1/workspaces creates a BUSINESS or SHARED workspace.
// Personal workspaces are only created at sign-up.
func (a *api) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string              `json:"name"`
		Type         store.WorkspaceType `json:"type"`
		BaseCurrency string              `json:"base_currency"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil {
		req.Name, err = cleanName(req.Name, "name", true, 100)
	}
	if err == nil && req.Type != store.WorkspaceBusiness && req.Type != store.WorkspaceShared {
		err = badRequest("type must be BUSINESS or SHARED")
	}
	if err == nil {
		req.BaseCurrency, err = currencyOrDefault(req.BaseCurrency)
	}
	if writeBadRequest(w, err) {
		return
	}

	var m store.Membership
	err = store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		var err error
		m, err = store.CreateWorkspace(r.Context(), tx, req.Name, req.Type, req.BaseCurrency, userIDFrom(r.Context()))
		return err
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWorkspaceDTO(m))
}

// GET /v1/workspaces/{workspaceID}
func (a *api) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toWorkspaceDTO(membershipFrom(r.Context())))
}

// PATCH /v1/workspaces/{workspaceID} (owner)
func (a *api) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil {
		req.Name, err = cleanName(req.Name, "name", true, 100)
	}
	if writeBadRequest(w, err) {
		return
	}
	m := membershipFrom(r.Context())
	if err := store.UpdateWorkspaceName(r.Context(), a.pool, m.ID, req.Name); err != nil {
		a.internalError(w, r, err)
		return
	}
	m.Name = req.Name
	writeJSON(w, http.StatusOK, toWorkspaceDTO(m))
}

// DELETE /v1/workspaces/{workspaceID} (owner) archives the workspace.
func (a *api) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	m := membershipFrom(r.Context())
	if m.Type == store.WorkspacePersonal {
		writeError(w, http.StatusConflict, "personal_workspace", "your personal workspace can't be deleted")
		return
	}
	if err := store.ArchiveWorkspace(r.Context(), a.pool, m.ID); err != nil {
		a.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/workspaces/{workspaceID}/members
func (a *api) handleListMembers(w http.ResponseWriter, r *http.Request) {
	members, err := store.ListMembers(r.Context(), a.pool, membershipFrom(r.Context()).ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]memberDTO, len(members))
	for i, m := range members {
		out[i] = memberDTO(m)
	}
	writeJSON(w, http.StatusOK, listDTO[memberDTO]{Data: out})
}

var (
	errLastOwner      = errors.New("last owner")
	errMemberNotFound = errors.New("member not found")
)

func (a *api) writeMemberChangeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errMemberNotFound):
		writeError(w, http.StatusNotFound, "member_not_found", "member not found")
	case errors.Is(err, errLastOwner):
		writeError(w, http.StatusConflict, "last_owner", "a workspace needs at least one owner")
	default:
		a.internalError(w, r, err)
	}
}

// PATCH /v1/workspaces/{workspaceID}/members/{userID} (owner)
func (a *api) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	m := membershipFrom(r.Context())
	targetID, ok := uuidParam(r, "userID")
	if !ok {
		writeError(w, http.StatusNotFound, "member_not_found", "member not found")
		return
	}
	var req struct {
		Role store.Role `json:"role"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil && !req.Role.Valid() {
		err = badRequest("role must be OWNER, EDITOR or VIEWER")
	}
	if writeBadRequest(w, err) {
		return
	}

	err = store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := store.LockWorkspace(ctx, tx, m.ID); err != nil {
			return err
		}
		current, err := store.GetMemberRole(ctx, tx, m.ID, targetID)
		if errors.Is(err, store.ErrNotFound) {
			return errMemberNotFound
		}
		if err != nil {
			return err
		}
		if current == store.RoleOwner && req.Role != store.RoleOwner {
			n, err := store.CountOwners(ctx, tx, m.ID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errLastOwner
			}
		}
		return store.UpdateMemberRole(ctx, tx, m.ID, targetID, req.Role)
	})
	if err != nil {
		a.writeMemberChangeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /v1/workspaces/{workspaceID}/members/{userID}
// Owners can remove anyone; any member can remove themselves (leave).
func (a *api) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	m := membershipFrom(r.Context())
	targetID, ok := uuidParam(r, "userID")
	if !ok {
		writeError(w, http.StatusNotFound, "member_not_found", "member not found")
		return
	}
	self := targetID == userIDFrom(r.Context())
	if !self && m.Role != store.RoleOwner {
		writeError(w, http.StatusForbidden, "forbidden", "your role in this workspace does not allow this")
		return
	}
	if m.Type == store.WorkspacePersonal {
		writeError(w, http.StatusConflict, "personal_workspace", "you can't leave your personal workspace")
		return
	}

	err := store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := store.LockWorkspace(ctx, tx, m.ID); err != nil {
			return err
		}
		current, err := store.GetMemberRole(ctx, tx, m.ID, targetID)
		if errors.Is(err, store.ErrNotFound) {
			return errMemberNotFound
		}
		if err != nil {
			return err
		}
		if current == store.RoleOwner {
			n, err := store.CountOwners(ctx, tx, m.ID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errLastOwner
			}
		}
		return store.RemoveMember(ctx, tx, m.ID, targetID)
	})
	if err != nil {
		a.writeMemberChangeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/workspaces/{workspaceID}/invitations (owner)
func (a *api) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	invs, err := store.ListOpenInvitations(r.Context(), a.pool, membershipFrom(r.Context()).ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]invitationDTO, len(invs))
	for i, inv := range invs {
		out[i] = toInvitationDTO(inv)
	}
	writeJSON(w, http.StatusOK, listDTO[invitationDTO]{Data: out})
}

// POST /v1/workspaces/{workspaceID}/invitations (owner)
//
// Returns a one-time token. Until email sending exists, the owner shares
// it with the invitee, who accepts it signed in with the invited email.
func (a *api) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	m := membershipFrom(r.Context())
	if m.Type == store.WorkspacePersonal {
		writeError(w, http.StatusConflict, "personal_workspace", "personal workspaces can't have other members")
		return
	}
	var req struct {
		Email string     `json:"email"`
		Role  store.Role `json:"role"`
	}
	err := decodeJSON(w, r, &req)
	if err == nil {
		req.Email, err = normalizeEmail(req.Email)
	}
	if err == nil && !req.Role.Valid() {
		err = badRequest("role must be OWNER, EDITOR or VIEWER")
	}
	if writeBadRequest(w, err) {
		return
	}

	token, hash, err := auth.NewOpaqueToken()
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	var inv store.Invitation
	err = store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		var err error
		inv, err = store.CreateInvitation(r.Context(), tx, m.ID, req.Email, req.Role,
			userIDFrom(r.Context()), hash, a.now().Add(a.invitationTTL))
		return err
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	dto := toInvitationDTO(inv)
	dto.Token = token
	writeJSON(w, http.StatusCreated, dto)
}

// DELETE /v1/workspaces/{workspaceID}/invitations/{invitationID} (owner)
func (a *api) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	invID, ok := uuidParam(r, "invitationID")
	if !ok {
		writeError(w, http.StatusNotFound, "invitation_not_found", "invitation not found")
		return
	}
	err := store.RevokeInvitation(r.Context(), a.pool, membershipFrom(r.Context()).ID, invID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invitation_not_found", "invitation not found")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var (
	errInvitationInvalid  = errors.New("invitation invalid")
	errInvitationMismatch = errors.New("invitation email mismatch")
)

// POST /v1/invitations/accept
func (a *api) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if writeBadRequest(w, decodeJSON(w, r, &req)) {
		return
	}
	userID := userIDFrom(r.Context())

	var m store.Membership
	err := store.InTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		inv, err := store.GetInvitationByTokenForUpdate(ctx, tx, auth.HashOpaqueToken(req.Token))
		if errors.Is(err, store.ErrNotFound) {
			return errInvitationInvalid
		}
		if err != nil {
			return err
		}
		if inv.AcceptedAt != nil || inv.RevokedAt != nil || !a.now().Before(inv.ExpiresAt) {
			return errInvitationInvalid
		}
		user, err := store.GetUserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if !emailEqual(user.Email, inv.Email) {
			return errInvitationMismatch
		}
		err = store.AddMember(ctx, tx, inv.WorkspaceID, userID, inv.Role)
		if err != nil && !errors.Is(err, store.ErrConflict) { // already a member: keep their role
			return err
		}
		if err := store.MarkInvitationAccepted(ctx, tx, inv.ID, userID); err != nil {
			return err
		}
		m, err = store.GetMembership(ctx, tx, inv.WorkspaceID, userID)
		if errors.Is(err, store.ErrNotFound) { // workspace archived
			return errInvitationInvalid
		}
		return err
	})
	switch {
	case errors.Is(err, errInvitationInvalid):
		writeError(w, http.StatusNotFound, "invitation_invalid", "this invitation is invalid, expired or already used")
	case errors.Is(err, errInvitationMismatch):
		writeError(w, http.StatusForbidden, "invitation_email_mismatch", "this invitation was sent to a different email")
	case err != nil:
		a.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, toWorkspaceDTO(m))
	}
}
