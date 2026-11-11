package crypto_test

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"user-management-api/internal/adapter/crypto"
	"user-management-api/internal/domain"
)

// The lowest cost bcrypt accepts, used to keep the suite fast; the real
// cost of 12 takes hundreds of milliseconds per call.
const testCost = bcrypt.MinCost

func TestHashAndCompare(t *testing.T) {
	t.Parallel()

	h := crypto.NewBcryptHasher(testCost)
	const password = "correct horse battery"

	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	if hash == password {
		t.Fatal("the hash must not equal the plaintext password")
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("the result must be in bcrypt form, got %q", hash)
	}
	if err := h.Compare(hash, password); err != nil {
		t.Errorf("the correct password must verify, got: %v", err)
	}
}

func TestHashUsesRandomSalt(t *testing.T) {
	t.Parallel()

	h := crypto.NewBcryptHasher(testCost)

	first, err := h.Hash("same-password-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Hash("same-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("the same password must hash differently, since the salt is fresh each time")
	}
	// Both values must still verify.
	if err := h.Compare(first, "same-password-1"); err != nil {
		t.Error(err)
	}
	if err := h.Compare(second, "same-password-1"); err != nil {
		t.Error(err)
	}
}

func TestCompareWrongPassword(t *testing.T) {
	t.Parallel()

	h := crypto.NewBcryptHasher(testCost)
	hash, err := h.Hash("the-right-one")
	if err != nil {
		t.Fatal(err)
	}

	err = h.Compare(hash, "the-wrong-one")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("a wrong password must give ErrInvalidCredentials, got: %v", err)
	}
	// The message must not hint at whether the password or the email was wrong.
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("the message gives too much away: %v", err)
	}
}

func TestCompareCorruptedHash(t *testing.T) {
	t.Parallel()

	h := crypto.NewBcryptHasher(testCost)

	tests := []struct {
		name string
		hash string
	}{
		{name: "empty", hash: ""},
		{name: "not bcrypt at all", hash: "plain-text-password"},
		{name: "too short", hash: "$2a$"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := h.Compare(tc.hash, "anything"); err == nil {
				t.Error("a corrupt hash must not verify")
			}
		})
	}
}

func TestHashRejectsBadPasswords(t *testing.T) {
	t.Parallel()

	h := crypto.NewBcryptHasher(testCost)

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "too short", input: "1234"},
		{name: "longer than 72 bytes", input: strings.Repeat("x", 73)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := h.Hash(tc.input)
			if !errors.Is(err, domain.ErrValidation) {
				t.Errorf("want ErrValidation, got: %v", err)
			}
		})
	}
}

// TestNewBcryptHasherClampsCost confirms an unusable cost is pulled back to
// the default rather than breaking the application at run time.
func TestNewBcryptHasherClampsCost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cost int
		want int
	}{
		{name: "below the minimum", cost: 1, want: bcrypt.DefaultCost},
		{name: "above the maximum", cost: 99, want: bcrypt.DefaultCost},
		{name: "a usable value", cost: 6, want: 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := crypto.NewBcryptHasher(tc.cost).Cost(); got != tc.want {
				t.Errorf("Cost() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHashRespectsConfiguredCost(t *testing.T) {
	t.Parallel()

	h := crypto.NewBcryptHasher(6)
	hash, err := h.Hash("some-password")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatal(err)
	}
	if cost != 6 {
		t.Errorf("cost recorded in the hash = %d, want 6", cost)
	}
}

// TestDummyHashIsUsable confirms the constant behind the timing-attack
// defence really is a valid bcrypt hash. If it were corrupt, Compare would
// return a system error instead of spending the usual time, and the defence
// would not work.
func TestDummyHashIsUsable(t *testing.T) {
	t.Parallel()

	if _, err := bcrypt.Cost([]byte(crypto.DummyHash)); err != nil {
		t.Fatalf("DummyHash is not a valid bcrypt hash: %v", err)
	}

	h := crypto.NewBcryptHasher(testCost)
	err := h.Compare(crypto.DummyHash, "any-password-at-all")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("comparing against DummyHash must give ErrInvalidCredentials, got: %v", err)
	}
}

func TestHasherSatisfiesPort(t *testing.T) {
	t.Parallel()

	var _ domain.PasswordHasher = crypto.NewBcryptHasher(testCost)
}
