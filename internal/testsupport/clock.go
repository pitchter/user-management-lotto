// Package testsupport holds the port fakes shared by the whole test suite.
//
// They are hand-written rather than generated, and mock the ports rather than
// the driver, because mtest moved under internal/ in mongo-driver v2 and can
// no longer be imported. docs/design-decisions.md gives the full reasoning.
// The real MongoDB adapter is covered by the integration tests instead.
package testsupport

import (
	"sync"
	"time"

	"github.com/pitchter/7-solution/internal/domain"
)

// FakeClock is a Clock that never advances on its own; tests call Advance.
// That makes anything time-dependent (token lifetime, timestamps) exact and
// repeatable.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

var _ domain.Clock = (*FakeClock)(nil)

// NewFakeClock returns a clock frozen at the given instant.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t.UTC()}
}

// FixedTime is the starting instant shared by the tests.
func FixedTime() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

// Now returns this clock's current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves time forward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
