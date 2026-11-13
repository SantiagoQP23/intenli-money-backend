package httpapi

import (
	"time"

	"github.com/google/uuid"

	"github.com/SantiagoQP23/intenli-money-backend/internal/store"
)

type userDTO struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	EmailVerified bool      `json:"email_verified"`
	Name          string    `json:"name"`
	HasPassword   bool      `json:"has_password"`
	Locale        string    `json:"locale"`
	CreatedAt     time.Time `json:"created_at"`
}

func toUserDTO(u store.User) userDTO {
	return userDTO{
		ID:            u.ID,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		Name:          u.Name,
		HasPassword:   u.PasswordHash != nil,
		Locale:        u.Locale,
		CreatedAt:     u.CreatedAt,
	}
}

type sessionDTO struct {
	User         userDTO `json:"user"`
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int     `json:"expires_in"` // access token lifetime, seconds
}

type workspaceDTO struct {
	ID           uuid.UUID           `json:"id"`
	Name         string              `json:"name"`
	Type         store.WorkspaceType `json:"type"`
	BaseCurrency string              `json:"base_currency"`
	Role         store.Role          `json:"role"`
	CreatedAt    time.Time           `json:"created_at"`
}

func toWorkspaceDTO(m store.Membership) workspaceDTO {
	return workspaceDTO{
		ID:           m.ID,
		Name:         m.Name,
		Type:         m.Type,
		BaseCurrency: m.BaseCurrency,
		Role:         m.Role,
		CreatedAt:    m.CreatedAt,
	}
}

type memberDTO struct {
	UserID   uuid.UUID  `json:"user_id"`
	Email    string     `json:"email"`
	Name     string     `json:"name"`
	Role     store.Role `json:"role"`
	JoinedAt time.Time  `json:"joined_at"`
}

type invitationDTO struct {
	ID        uuid.UUID  `json:"id"`
	Email     string     `json:"email"`
	Role      store.Role `json:"role"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	// Token is only returned when the invitation is created.
	Token string `json:"token,omitempty"`
}

func toInvitationDTO(i store.Invitation) invitationDTO {
	return invitationDTO{ID: i.ID, Email: i.Email, Role: i.Role, ExpiresAt: i.ExpiresAt, CreatedAt: i.CreatedAt}
}

type listDTO[T any] struct {
	Data []T `json:"data"`
}
