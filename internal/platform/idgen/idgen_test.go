package idgen_test

import (
	"encoding/hex"
	"testing"

	"user-management-api/internal/platform/idgen"
)

func TestNewFormat(t *testing.T) {
	t.Parallel()

	id := idgen.New()
	if want := idgen.Size * 2; len(id) != want {
		t.Errorf("length = %d, want %d", len(id), want)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Errorf("must be decodable hex: %v", err)
	}
}

// TestNewDoesNotRepeat is a smoke check on the wiring, not on crypto/rand:
// it would catch New() returning a fixed value or a shared buffer. The
// unguessability of the bytes themselves is the standard library's guarantee,
// so there is no test here asserting it.
func TestNewDoesNotRepeat(t *testing.T) {
	t.Parallel()

	first, second := idgen.New(), idgen.New()
	if first == second {
		t.Errorf("two calls returned the same value: %s", first)
	}
}
