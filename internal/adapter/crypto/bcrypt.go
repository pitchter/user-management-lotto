// Package crypto is the driven adapter for the domain.PasswordHasher port,
// implemented with bcrypt from golang.org/x/crypto.
package crypto

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/pitchter/7-solution/internal/domain"
)

// DummyHash is a precomputed cost-12 hash of a password nobody uses.
// domain.PasswordHasher explains what it is for; it is precomputed so that
// serving it costs nothing.
const DummyHash = "$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// BcryptHasher hashes and verifies passwords with bcrypt.
type BcryptHasher struct {
	cost int
}

var _ domain.PasswordHasher = (*BcryptHasher)(nil)

// NewBcryptHasher builds a hasher with the given cost.
//
// A cost outside the range bcrypt accepts is pulled back to bcrypt.DefaultCost so
// the application cannot fail at run time; the configured value is validated too.
func NewBcryptHasher(cost int) *BcryptHasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return &BcryptHasher{cost: cost}
}

// Cost reports the cost actually in use.
func (h *BcryptHasher) Cost() int { return h.cost }

// DummyHash returns a bcrypt hash that no live password matches.
func (h *BcryptHasher) DummyHash() string { return DummyHash }

// Hash turns a plaintext password into a bcrypt hash.
//
// bcrypt already mixes in a random salt on every call, so the same password never
// yields the same hash twice, and the salt does not need to be stored separately
// (it is embedded in the result string).
func (h *BcryptHasher) Hash(plain string) (string, error) {
	if err := domain.ValidatePassword(plain); err != nil {
		return "", err
	}

	b, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		// This can only happen for passwords over 72 bytes, which
		// ValidatePassword already rejects; anything else is a real fault.
		return "", fmt.Errorf("could not hash the password: %w", err)
	}
	return string(b), nil
}

// Compare checks a plaintext password against a hash.
//
// It returns domain.ErrInvalidCredentials on a mismatch (the same error in
// every case, so the caller cannot tell the cases apart) and a real error
// when the stored hash is corrupt, which is a system fault, not a user one.
func (h *BcryptHasher) Compare(hash, plain string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return domain.ErrInvalidCredentials
	case errors.Is(err, bcrypt.ErrHashTooShort) || isInvalidHashVersion(err):
		return fmt.Errorf("the stored hash is not a valid bcrypt hash: %w", err)
	default:
		return domain.ErrInvalidCredentials
	}
}

// isInvalidHashVersion detects bcrypt.HashVersionTooNewError, which has no
// sentinel value to compare against directly.
func isInvalidHashVersion(err error) bool {
	var v bcrypt.HashVersionTooNewError
	return errors.As(err, &v)
}
