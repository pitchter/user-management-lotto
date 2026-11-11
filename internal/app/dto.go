// Package app is the application (use-case) layer that orchestrates the
// business rules.
//
// This is the only place that knows the steps of each operation (signing up
// = check the password, hash it, build the entity, store it) and the only
// place that decides permissions, which is how REST and gRPC end up with
// identical behaviour without the rules being written twice.
//
// It knows nothing about HTTP, gRPC or MongoDB: plain DTOs in and out.
package app

import (
	"time"

	"user-management-api/internal/domain"
)

// RegisterInput is the data for self-service sign-up.
//
// There is deliberately no role field: anyone signing up gets the user role,
// which closes the privilege-escalation path of posting "role": "admin".
type RegisterInput struct {
	Name     string
	Email    string
	Password string
}

// LoginInput is the data for signing in.
type LoginInput struct {
	Email    string
	Password string
}

// CreateUserInput is the data for an admin creating a user (role included).
type CreateUserInput struct {
	Name     string
	Email    string
	Password string
	// Role may be left empty, which means user.
	Role string
}

// UpdateUserInput is the data for editing a user. A nil field was not sent
// and is left alone, as PATCH requires.
type UpdateUserInput struct {
	Name  *string
	Email *string
}

// HasChanges reports whether any field was actually submitted.
func (in UpdateUserInput) HasChanges() bool {
	return in.Name != nil || in.Email != nil
}

// ListInput holds the parameters of a user listing.
type ListInput struct {
	Limit  int
	Cursor string
}

// UserOutput is user data that is safe to send outside.
//
// There is no PasswordHash field at all — not merely a json:"-" tag, but
// absent from the type — so it cannot leak with a response no matter how an
// outer layer serialises it (JSON, protobuf or a log line).
type UserOutput struct {
	ID        string
	Name      string
	Email     string
	Role      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListOutput is the result of a user listing.
type ListOutput struct {
	Users      []UserOutput
	NextCursor string
	Total      int64
}

// AuthOutput is the result of signing in.
type AuthOutput struct {
	AccessToken string
	TokenType   string
	ExpiresIn   int64
	ExpiresAt   time.Time
	User        UserOutput
}

// newUserOutput converts an entity into the outgoing DTO.
func newUserOutput(u domain.User) UserOutput {
	return UserOutput{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		Role:      u.Role.String(),
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}
