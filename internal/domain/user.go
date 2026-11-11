// Package domain is the innermost layer of the architecture (hexagonal /
// ports and adapters). It holds the entities, the business rules and the
// ports (interfaces) that outer layers must satisfy.
//
// Hard rule: nothing here may import anything outside the standard library.
// This is enforced by depguard in .golangci.yml.
package domain

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits applied to user data.
const (
	MinNameLen = 2
	MaxNameLen = 100
	// MaxEmailLen is the maximum email length per RFC 5321.
	MaxEmailLen    = 254
	MinPasswordLen = 8
	// MaxPasswordLen exists because bcrypt only reads the first 72 bytes of a
	// password. Anything longer would be silently truncated, which would make
	// two different passwords interchangeable, so we reject it outright
	// instead of cutting it short.
	MaxPasswordLen = 72
)

// Role is a user's permission level.
type Role string

// Supported permission levels.
const (
	// RoleAdmin can manage every user.
	RoleAdmin Role = "admin"
	// RoleUser can only manage its own record.
	RoleUser Role = "user"
)

// String returns the role as a plain string.
func (r Role) String() string { return string(r) }

// Valid reports whether this is a role the system knows.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleUser
}

// ParseRole converts a string into a Role. An empty value means RoleUser,
// which is the least privileged option (fail closed).
func ParseRole(s string) (Role, error) {
	switch Role(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return RoleUser, nil
	case RoleAdmin:
		return RoleAdmin, nil
	case RoleUser:
		return RoleUser, nil
	default:
		return "", NewValidationError("role", `must be either "admin" or "user"`)
	}
}

// User is the user entity.
//
// ID is a string (the hex form of an ObjectID) so that the domain never has
// to know about MongoDB's own types. The conversion happens only inside
// internal/adapter/mongodb.
type User struct {
	ID           string
	Name         string
	Email        string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewUser creates a user and enforces every rule on the way in.
//
// It takes an already hashed password rather than a plaintext one, because
// the domain should not know how hashing works (that is the PasswordHasher
// port's job). The ID is filled in by the repository when the user is stored.
func NewUser(name, email, passwordHash string, role Role, now time.Time) (*User, error) {
	v := &ValidationError{}

	// Absorb files the offending field away so every problem is reported in
	// one answer. It only refuses an error that is not a validation failure,
	// and such an error has to travel on untouched.
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil && !v.Absorb(err) {
		return nil, err
	}

	email = NormalizeEmail(email)
	if err := validateEmail(email); err != nil && !v.Absorb(err) {
		return nil, err
	}

	if passwordHash == "" {
		v.Add("password", "is required")
	}

	if !role.Valid() {
		v.Add("role", `must be either "admin" or "user"`)
	}

	if v.HasErrors() {
		return nil, v
	}

	ts := now.UTC()
	return &User{
		Name:         name,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         role,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}, nil
}

// Rename changes the user's display name.
func (u *User) Rename(name string, now time.Time) error {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return err
	}
	u.Name = name
	u.UpdatedAt = now.UTC()
	return nil
}

// ChangeEmail changes the email, always normalising it to lower case.
func (u *User) ChangeEmail(email string, now time.Time) error {
	email = NormalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return err
	}
	u.Email = email
	u.UpdatedAt = now.UTC()
	return nil
}

// IsAdmin reports whether this user is an administrator.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// NormalizeEmail puts an address into the one canonical form used for both
// storage and lookups.
//
// Storing it lower-cased lets the unique index on email reject duplicates
// that differ only in case, without relying on a MongoDB collation, and makes
// logging in with "A@b.com" or "a@b.com" behave identically.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidatePassword checks a plaintext password against the system's policy.
//
// This lives in the domain (rather than only as a validator tag on an HTTP
// request) because it is a business rule that must hold for every entry
// point: REST, gRPC and the bootstrap admin alike.
func ValidatePassword(plain string) error {
	switch {
	case plain == "":
		return NewValidationError("password", "is required")
	case utf8.RuneCountInString(plain) < MinPasswordLen:
		return NewValidationError("password",
			fmt.Sprintf("must be at least %d characters long", MinPasswordLen))
	case len(plain) > MaxPasswordLen:
		// Counted in bytes, not runes, because of bcrypt's 72-byte limit.
		return NewValidationError("password",
			fmt.Sprintf("must not exceed %d bytes", MaxPasswordLen))
	default:
		return nil
	}
}

func validateName(name string) error {
	switch n := utf8.RuneCountInString(name); {
	case name == "":
		return NewValidationError("name", "is required")
	case n < MinNameLen:
		return NewValidationError("name",
			fmt.Sprintf("must be at least %d characters long", MinNameLen))
	case n > MaxNameLen:
		return NewValidationError("name",
			fmt.Sprintf("must not exceed %d characters", MaxNameLen))
	default:
		return nil
	}
}

func validateEmail(email string) error {
	if email == "" {
		return NewValidationError("email", "is required")
	}
	if len(email) > MaxEmailLen {
		return NewValidationError("email",
			fmt.Sprintf("must not exceed %d characters", MaxEmailLen))
	}

	// net/mail also accepts forms like `Name <a@b.c>`, so the parsed address
	// has to match the input exactly. The domain part must contain a dot as
	// well, which rejects values such as "user@localhost".
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return NewValidationError("email", "must be a valid email address")
	}
	at := strings.LastIndex(email, "@")
	domainPart := email[at+1:]
	if !strings.Contains(domainPart, ".") ||
		strings.HasPrefix(domainPart, ".") || strings.HasSuffix(domainPart, ".") {
		return NewValidationError("email", "must be a valid email address")
	}
	return nil
}
