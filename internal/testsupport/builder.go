package testsupport

import (
	"testing"

	"user-management-api/internal/domain"
)

// UserOption customises the user built by NewUser.
type UserOption func(*domain.User)

// WithName sets the display name.
func WithName(name string) UserOption {
	return func(u *domain.User) { u.Name = name }
}

// WithEmail sets the email, normalised the way the repository would.
func WithEmail(email string) UserOption {
	return func(u *domain.User) { u.Email = domain.NormalizeEmail(email) }
}

// WithRole sets the role.
func WithRole(role domain.Role) UserOption {
	return func(u *domain.User) { u.Role = role }
}

// WithPassword takes a plaintext password and stores FakeHasher's hash of it.
func WithPassword(plain string) UserOption {
	return func(u *domain.User) { u.PasswordHash = FakeHashPrefix + plain }
}

// NewUser builds a user for tests with sane values in every field, so each
// test only has to override the part it actually cares about.
func NewUser(t *testing.T, opts ...UserOption) domain.User {
	t.Helper()

	now := FixedTime()
	u := domain.User{
		Name:         "Test User",
		Email:        "test.user@example.com",
		PasswordHash: FakeHashPrefix + "password123",
		Role:         domain.RoleUser,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	for _, opt := range opts {
		opt(&u)
	}
	return u
}

// SeedUser inserts one user into the repository and returns it with its real id.
func SeedUser(t *testing.T, repo *FakeUserRepository, opts ...UserOption) domain.User {
	t.Helper()
	return repo.Seed(NewUser(t, opts...))[0]
}
