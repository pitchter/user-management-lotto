package domain

import (
	"context"
	"time"
)

// UserRepository is the port for user storage.
//
// It is declared here, on the side that needs it, rather than next to the
// implementation — dependency inversion. That is what keeps the business
// logic unaware that MongoDB is behind it, and what makes it testable with an
// in-memory fake instead of a real database.
//
// Contract every implementation must honour:
//   - Create fills in ID and returns ErrEmailTaken when the email is a duplicate.
//   - GetByID, GetByEmail, Update and Delete return ErrUserNotFound when there
//     is no match.
//   - Incoming email addresses are assumed to be normalised already (callers
//     run NormalizeEmail first).
type UserRepository interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	List(ctx context.Context, p ListParams) (ListResult, error)
	Update(ctx context.Context, u *User) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// ListParams are the parameters of a user listing. It is a cursor, not an
// offset; UserRepository.List in the mongodb adapter says why.
type ListParams struct {
	Limit int
	// Cursor is the ID of the last record of the previous page ("" = first page).
	Cursor string
}

// ListResult is the outcome of a user listing.
type ListResult struct {
	Users []User
	// NextCursor should be passed back as Cursor to fetch the next page
	// ("" means there is nothing more).
	NextCursor string
	// Total is the number of users in the system.
	Total int64
}

// Pagination defaults and ceilings.
const (
	DefaultListLimit = 20
	// MaxListLimit caps the page size so a client cannot pull the whole
	// collection in one request.
	MaxListLimit = 100
)

// Normalize clamps Limit into the accepted range.
func (p ListParams) Normalize() ListParams {
	switch {
	case p.Limit <= 0:
		p.Limit = DefaultListLimit
	case p.Limit > MaxListLimit:
		p.Limit = MaxListLimit
	}
	return p
}

// PasswordHasher is the port for hashing and verifying passwords.
//
// It is an interface so that use-case tests do not have to pay for real
// bcrypt work (cost 12 takes hundreds of milliseconds per call) and so that
// switching to argon2 would not touch any business logic.
type PasswordHasher interface {
	// Hash turns a plaintext password into a value safe to store.
	Hash(plain string) (string, error)
	// Compare checks a plaintext password against a hash and returns
	// ErrInvalidCredentials when they do not match.
	Compare(hash, plain string) error
	// DummyHash returns a hash that no password will ever match.
	//
	// It is used on login when the email is not found: Compare is still
	// called against this value so the request takes about as long as one for
	// an existing user. Answering 401 immediately instead would let an
	// attacker time the responses and learn which addresses are registered
	// (timing-based user enumeration).
	//
	// It belongs on the port because the value depends on the algorithm the
	// adapter picked, which the application layer should not know about.
	DummyHash() string
}

// TokenService is the port for issuing and verifying access tokens.
type TokenService interface {
	// Issue mints a token for a user.
	Issue(u *User) (Token, error)
	// Verify validates a token and returns the identity inside it, or
	// ErrUnauthenticated when the token cannot be used.
	Verify(raw string) (Identity, error)
}

// Token is an access token handed to a client.
type Token struct {
	// Value is the token itself (a JWT).
	Value string
	// Type is the token type per RFC 6750 (currently "Bearer").
	Type string
	// ExpiresIn is the remaining lifetime in seconds.
	ExpiresIn int64
	ExpiresAt time.Time
}

// Identity is the caller, as extracted from a token.
type Identity struct {
	UserID string
	Role   Role
}

// IsAdmin reports whether the caller is an administrator.
func (i Identity) IsAdmin() bool { return i.Role == RoleAdmin }

// CanManage reports whether the caller may read or modify the given user.
//
// The rule lives in the domain so REST and gRPC always agree on the answer,
// and so it can be tested without an HTTP request in sight.
func (i Identity) CanManage(targetUserID string) bool {
	return i.IsAdmin() || (i.UserID != "" && i.UserID == targetUserID)
}

// Authorize returns ErrForbidden when the caller may not act on the given user.
func (i Identity) Authorize(targetUserID string) error {
	if !i.CanManage(targetUserID) {
		return ErrForbidden
	}
	return nil
}

// Clock is the port for reading the current time, which lets tests pin it
// down to an exact value.
type Clock interface {
	Now() time.Time
}

// SystemClock is the Clock backed by the machine's real time.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
