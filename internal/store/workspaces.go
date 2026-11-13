package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type WorkspaceType string

const (
	WorkspacePersonal WorkspaceType = "PERSONAL"
	WorkspaceBusiness WorkspaceType = "BUSINESS"
	WorkspaceShared   WorkspaceType = "SHARED"
)

type Role string

const (
	RoleOwner  Role = "OWNER"
	RoleEditor Role = "EDITOR"
	RoleViewer Role = "VIEWER"
)

// rank orders roles so a check can ask for "at least EDITOR".
func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// AtLeast reports whether r grants everything min grants.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() && r.rank() > 0 }

func (r Role) Valid() bool { return r.rank() > 0 }

type Workspace struct {
	ID           uuid.UUID
	Name         string
	Type         WorkspaceType
	BaseCurrency string
	CreatedBy    uuid.UUID
	CreatedAt    time.Time
}

// Membership is a workspace as seen by one of its members.
type Membership struct {
	Workspace
	Role Role
}

const workspaceColumns = `w.id, w.name, w.type, w.base_currency, w.created_by, w.created_at`

func scanMembership(row pgx.Row) (Membership, error) {
	var m Membership
	err := row.Scan(&m.ID, &m.Name, &m.Type, &m.BaseCurrency, &m.CreatedBy, &m.CreatedAt, &m.Role)
	return m, notFound(err)
}

// CreateWorkspace inserts a workspace and makes creator its OWNER.
// A second PERSONAL workspace for the same user returns ErrConflict.
func CreateWorkspace(ctx context.Context, db DB, name string, typ WorkspaceType, currency string, creator uuid.UUID) (Membership, error) {
	var m Membership
	err := db.QueryRow(ctx,
		`INSERT INTO workspaces (name, type, base_currency, created_by) VALUES ($1, $2, $3, $4)
		 RETURNING id, name, type, base_currency, created_by, created_at`,
		name, typ, currency, creator).
		Scan(&m.ID, &m.Name, &m.Type, &m.BaseCurrency, &m.CreatedBy, &m.CreatedAt)
	if isUniqueViolation(err) {
		return Membership{}, ErrConflict
	}
	if err != nil {
		return Membership{}, err
	}
	if err := AddMember(ctx, db, m.ID, creator, RoleOwner); err != nil {
		return Membership{}, err
	}
	if err := CreateDefaultCategories(ctx, db, m.ID, typ); err != nil {
		return Membership{}, err
	}
	m.Role = RoleOwner
	return m, nil
}

// ListMemberships returns the user's active workspaces, personal first.
func ListMemberships(ctx context.Context, db DB, userID uuid.UUID) ([]Membership, error) {
	rows, err := db.Query(ctx,
		`SELECT `+workspaceColumns+`, m.role
		 FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
		 WHERE m.user_id = $1 AND w.archived_at IS NULL
		 ORDER BY (w.type = 'PERSONAL') DESC, w.name, w.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMembership returns ErrNotFound when the workspace doesn't exist, is
// archived, or the user isn't a member: callers must not tell these apart.
func GetMembership(ctx context.Context, db DB, workspaceID, userID uuid.UUID) (Membership, error) {
	return scanMembership(db.QueryRow(ctx,
		`SELECT `+workspaceColumns+`, m.role
		 FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
		 WHERE m.workspace_id = $1 AND m.user_id = $2 AND w.archived_at IS NULL`,
		workspaceID, userID))
}

func UpdateWorkspaceName(ctx context.Context, db DB, workspaceID uuid.UUID, name string) error {
	_, err := db.Exec(ctx, `UPDATE workspaces SET name = $2, updated_at = now() WHERE id = $1`, workspaceID, name)
	return err
}

// ArchiveWorkspace hides a workspace without deleting its financial history.
func ArchiveWorkspace(ctx context.Context, db DB, workspaceID uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE workspaces SET archived_at = now(), updated_at = now() WHERE id = $1`, workspaceID)
	return err
}

// LockWorkspace serializes membership changes on one workspace.
func LockWorkspace(ctx context.Context, db DB, workspaceID uuid.UUID) error {
	_, err := db.Exec(ctx, `SELECT 1 FROM workspaces WHERE id = $1 FOR UPDATE`, workspaceID)
	return err
}

type Member struct {
	UserID   uuid.UUID
	Email    string
	Name     string
	Role     Role
	JoinedAt time.Time
}

func AddMember(ctx context.Context, db DB, workspaceID, userID uuid.UUID, role Role) error {
	// ON CONFLICT keeps a surrounding transaction usable when the user is
	// already a member.
	tag, err := db.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (workspace_id, user_id) DO NOTHING`,
		workspaceID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func ListMembers(ctx context.Context, db DB, workspaceID uuid.UUID) ([]Member, error) {
	rows, err := db.Query(ctx,
		`SELECT u.id, u.email, u.name, m.role, m.created_at
		 FROM workspace_members m JOIN users u ON u.id = m.user_id
		 WHERE m.workspace_id = $1 ORDER BY m.created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func GetMemberRole(ctx context.Context, db DB, workspaceID, userID uuid.UUID) (Role, error) {
	var r Role
	err := db.QueryRow(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID).Scan(&r)
	return r, notFound(err)
}

func CountOwners(ctx context.Context, db DB, workspaceID uuid.UUID) (int, error) {
	var n int
	err := db.QueryRow(ctx,
		`SELECT count(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'OWNER'`,
		workspaceID).Scan(&n)
	return n, err
}

func UpdateMemberRole(ctx context.Context, db DB, workspaceID, userID uuid.UUID, role Role) error {
	_, err := db.Exec(ctx,
		`UPDATE workspace_members SET role = $3 WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID, role)
	return err
}

func RemoveMember(ctx context.Context, db DB, workspaceID, userID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
	return err
}
