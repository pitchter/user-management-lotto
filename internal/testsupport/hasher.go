package testsupport

import (
	"errors"
	"strings"
	"sync/atomic"

	"user-management-api/internal/domain"
)

// FakeHasher stands in for bcrypt in use-case tests.
//
// bcrypt at cost 12 deliberately costs hundreds of milliseconds per call (to
// make brute force expensive), which would visibly slow a suite with dozens
// of cases. This fake uses a reversible prefix instead, so it is fast while
// still proving the code stores a hash rather than the raw password.
type FakeHasher struct {
	// HashErr, when set, makes Hash fail (to exercise the hashing error path).
	HashErr error
	// CompareCalls counts Compare calls, which is how the tests confirm the
	// login timing-attack defence still runs even when no user is found.
	CompareCalls atomic.Int64
}

var _ domain.PasswordHasher = (*FakeHasher)(nil)

// FakeHashPrefix is the prefix FakeHasher puts in front of the password.
const FakeHashPrefix = "fake-hash:"

// NewFakeHasher builds the fake hasher.
func NewFakeHasher() *FakeHasher { return &FakeHasher{} }

// Hash returns prefix + password, still applying the domain password policy.
func (h *FakeHasher) Hash(plain string) (string, error) {
	if h.HashErr != nil {
		return "", h.HashErr
	}
	if err := domain.ValidatePassword(plain); err != nil {
		return "", err
	}
	return FakeHashPrefix + plain, nil
}

// DummyHash returns a hash no password matches (used to even out login timing).
func (h *FakeHasher) DummyHash() string { return FakeHashPrefix + "\x00no-password-matches-this" }

// Compare checks a hash against a password.
func (h *FakeHasher) Compare(hash, plain string) error {
	h.CompareCalls.Add(1)

	if !strings.HasPrefix(hash, FakeHashPrefix) {
		return errors.New("testsupport: the hash was not produced by FakeHasher")
	}
	if strings.TrimPrefix(hash, FakeHashPrefix) != plain {
		return domain.ErrInvalidCredentials
	}
	return nil
}
