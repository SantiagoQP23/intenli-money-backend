package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
	RevokedAt *time.Time
}

func CreateRefreshToken(ctx context.Context, db DB, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := db.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, familyID, hash, expiresAt)
	return err
}

// GetRefreshTokenForUpdate locks the token row for rotation.
func GetRefreshTokenForUpdate(ctx context.Context, db DB, hash []byte) (RefreshToken, error) {
	var t RefreshToken
	err := db.QueryRow(ctx,
		`SELECT id, user_id, family_id, expires_at, revoked_at
		 FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`, hash).
		Scan(&t.ID, &t.UserID, &t.FamilyID, &t.ExpiresAt, &t.RevokedAt)
	return t, notFound(err)
}

func RevokeRefreshToken(ctx context.Context, db DB, id uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

func RevokeRefreshFamily(ctx context.Context, db DB, familyID uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`, familyID)
	return err
}

func RevokeAllRefreshTokens(ctx context.Context, db DB, userID uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}
