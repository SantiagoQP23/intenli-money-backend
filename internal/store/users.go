package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type User struct {
	ID            uuid.UUID
	Email         string
	EmailVerified bool
	Name          string
	PasswordHash  *string
	Locale        string // "es" or "en"
	CreatedAt     time.Time
}

// ValidLocale reports whether locale is a language the app supports.
func ValidLocale(locale string) bool { return locale == "es" || locale == "en" }

const userColumns = `id, email, email_verified, name, password_hash, locale, created_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.EmailVerified, &u.Name, &u.PasswordHash, &u.Locale, &u.CreatedAt)
	return u, notFound(err)
}

// CreateUser returns ErrConflict if the email is already registered.
func CreateUser(ctx context.Context, db DB, email, name string, passwordHash *string, emailVerified bool, locale string) (User, error) {
	u, err := scanUser(db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, email_verified, locale)
		 VALUES ($1, $2, $3, $4, $5) RETURNING `+userColumns,
		email, name, passwordHash, emailVerified, locale))
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	return u, err
}

func GetUserByID(ctx context.Context, db DB, id uuid.UUID) (User, error) {
	return scanUser(db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func GetUserByEmail(ctx context.Context, db DB, email string) (User, error) {
	return scanUser(db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

// GetUserByEmailForUpdate locks the row so concurrent sign-ins can't race.
func GetUserByEmailForUpdate(ctx context.Context, db DB, email string) (User, error) {
	return scanUser(db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1 FOR UPDATE`, email))
}

func GetUserByIdentity(ctx context.Context, db DB, provider, subject string) (User, error) {
	return scanUser(db.QueryRow(ctx,
		`SELECT u.id, u.email, u.email_verified, u.name, u.password_hash, u.locale, u.created_at
		 FROM user_identities i JOIN users u ON u.id = i.user_id
		 WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
}

func CreateIdentity(ctx context.Context, db DB, userID uuid.UUID, provider, subject string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO user_identities (user_id, provider, subject) VALUES ($1, $2, $3)`,
		userID, provider, subject)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// MarkEmailVerified sets email_verified. When dropPassword is true it also
// clears the password: used when Google proves ownership of an email whose
// password account was never verified, so whoever set that password can't
// keep access.
func MarkEmailVerified(ctx context.Context, db DB, userID uuid.UUID, dropPassword bool) error {
	_, err := db.Exec(ctx,
		`UPDATE users SET email_verified = true,
		   password_hash = CASE WHEN $2 THEN NULL ELSE password_hash END,
		   updated_at = now()
		 WHERE id = $1`, userID, dropPassword)
	return err
}

// UpdateUser changes the name and/or locale; nil leaves a field as it is.
func UpdateUser(ctx context.Context, db DB, userID uuid.UUID, name, locale *string) (User, error) {
	return scanUser(db.QueryRow(ctx,
		`UPDATE users SET name = coalesce($2, name), locale = coalesce($3, locale), updated_at = now()
		 WHERE id = $1 RETURNING `+userColumns,
		userID, name, locale))
}
