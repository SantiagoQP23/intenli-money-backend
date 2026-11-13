package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Invitation struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Email       string
	Role        Role
	InvitedBy   uuid.UUID
	ExpiresAt   time.Time
	AcceptedAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

const invitationColumns = `id, workspace_id, email, role, invited_by, expires_at, accepted_at, revoked_at, created_at`

func scanInvitation(row interface{ Scan(...any) error }) (Invitation, error) {
	var i Invitation
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.Email, &i.Role, &i.InvitedBy, &i.ExpiresAt, &i.AcceptedAt, &i.RevokedAt, &i.CreatedAt)
	return i, notFound(err)
}

// CreateInvitation replaces any open invitation for the same email.
func CreateInvitation(ctx context.Context, db DB, workspaceID uuid.UUID, email string, role Role, invitedBy uuid.UUID, tokenHash []byte, expiresAt time.Time) (Invitation, error) {
	if _, err := db.Exec(ctx,
		`UPDATE workspace_invitations SET revoked_at = now()
		 WHERE workspace_id = $1 AND email = $2 AND accepted_at IS NULL AND revoked_at IS NULL`,
		workspaceID, email); err != nil {
		return Invitation{}, err
	}
	return scanInvitation(db.QueryRow(ctx,
		`INSERT INTO workspace_invitations (workspace_id, email, role, invited_by, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+invitationColumns,
		workspaceID, email, role, invitedBy, tokenHash, expiresAt))
}

// ListOpenInvitations returns invitations not yet accepted, revoked or expired.
func ListOpenInvitations(ctx context.Context, db DB, workspaceID uuid.UUID) ([]Invitation, error) {
	rows, err := db.Query(ctx,
		`SELECT `+invitationColumns+` FROM workspace_invitations
		 WHERE workspace_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
		 ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		i, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RevokeInvitation returns ErrNotFound if no open invitation matched.
func RevokeInvitation(ctx context.Context, db DB, workspaceID, invitationID uuid.UUID) error {
	tag, err := db.Exec(ctx,
		`UPDATE workspace_invitations SET revoked_at = now()
		 WHERE id = $1 AND workspace_id = $2 AND accepted_at IS NULL AND revoked_at IS NULL`,
		invitationID, workspaceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func GetInvitationByTokenForUpdate(ctx context.Context, db DB, tokenHash []byte) (Invitation, error) {
	return scanInvitation(db.QueryRow(ctx,
		`SELECT `+invitationColumns+` FROM workspace_invitations WHERE token_hash = $1 FOR UPDATE`, tokenHash))
}

func MarkInvitationAccepted(ctx context.Context, db DB, invitationID, userID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE workspace_invitations SET accepted_at = now(), accepted_by = $2 WHERE id = $1`,
		invitationID, userID)
	return err
}
