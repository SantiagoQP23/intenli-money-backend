package migrations

import (
	"context"
	"database/sql"

	"github.com/SantiagoQP23/intenli-money-backend/internal/defaults"
)

// BackfillDefaultCategories gives every existing workspace the defaults for
// its type that it doesn't have yet, and an icon to its categories whose
// name matches a default. A default is skipped when the workspace already
// has a category of that kind and name, active or archived, so nothing
// someone renamed, archived or set up by hand changes. Nothing is deleted.
func BackfillDefaultCategories(ctx context.Context, tx *sql.Tx) error {
	for _, typ := range []string{"PERSONAL", "SHARED", "BUSINESS"} {
		for _, c := range defaults.For(typ) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO categories (workspace_id, kind, name, icon)
				SELECT w.id, $2, $3, $4 FROM workspaces w
				WHERE w.type = $1 AND NOT EXISTS (
					SELECT 1 FROM categories c
					WHERE c.workspace_id = w.id AND c.kind = $2 AND lower(c.name) = lower($3))`,
				typ, c.Kind, c.Name, c.Icon); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE categories c SET icon = $4 FROM workspaces w
				WHERE w.id = c.workspace_id AND w.type = $1
				  AND c.kind = $2 AND lower(c.name) = lower($3) AND c.icon IS NULL`,
				typ, c.Kind, c.Name, c.Icon); err != nil {
				return err
			}
		}
	}
	return nil
}
