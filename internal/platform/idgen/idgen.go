// Package idgen produces unguessable random identifiers for request ids and JWT jti claims.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// Size is how many random bytes are used (16 bytes = 128 bits, like a UUID).
const Size = 16

// New returns a random value as a 32-character hex string.
//
// It uses crypto/rand rather than math/rand because the value doubles as a token's
// jti, which must not be predictable. Since Go 1.24 rand.Read no longer returns an
// error (it panics if the system cannot provide entropy), so none is needed here.
func New() string {
	b := make([]byte, Size)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
