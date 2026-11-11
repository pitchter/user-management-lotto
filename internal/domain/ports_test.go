package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/pitchter/7-solution/internal/domain"
)

func TestIdentityCanManage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity domain.Identity
		target   string
		want     bool
	}{
		{
			name:     "admin may manage others",
			identity: domain.Identity{UserID: "admin-1", Role: domain.RoleAdmin},
			target:   "user-9",
			want:     true,
		},
		{
			name:     "admin may manage itself",
			identity: domain.Identity{UserID: "admin-1", Role: domain.RoleAdmin},
			target:   "admin-1",
			want:     true,
		},
		{
			name:     "user may manage itself",
			identity: domain.Identity{UserID: "user-1", Role: domain.RoleUser},
			target:   "user-1",
			want:     true,
		},
		{
			name:     "user may not manage others",
			identity: domain.Identity{UserID: "user-1", Role: domain.RoleUser},
			target:   "user-2",
			want:     false,
		},
		{
			name:     "an empty identity matches no target, not even an empty one",
			identity: domain.Identity{},
			target:   "",
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.identity.CanManage(tc.target); got != tc.want {
				t.Errorf("CanManage(%q) = %v, want %v", tc.target, got, tc.want)
			}

			err := tc.identity.Authorize(tc.target)
			if tc.want && err != nil {
				t.Errorf("Authorize must pass, got %v", err)
			}
			if !tc.want && !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("Authorize must give ErrForbidden, got %v", err)
			}
		})
	}
}

func TestIdentityIsAdmin(t *testing.T) {
	t.Parallel()

	if !(domain.Identity{Role: domain.RoleAdmin}).IsAdmin() {
		t.Error("the admin role must report IsAdmin")
	}
	if (domain.Identity{Role: domain.RoleUser}).IsAdmin() {
		t.Error("the user role must not report IsAdmin")
	}
}

func TestListParamsNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "unset falls back to the default", in: 0, want: domain.DefaultListLimit},
		{name: "negative falls back to the default", in: -5, want: domain.DefaultListLimit},
		{name: "a sensible value is kept", in: 7, want: 7},
		{name: "above the ceiling is clamped to max", in: 5000, want: domain.MaxListLimit},
		{name: "exactly at the ceiling", in: domain.MaxListLimit, want: domain.MaxListLimit},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := domain.ListParams{Limit: tc.in, Cursor: "abc"}.Normalize()
			if got.Limit != tc.want {
				t.Errorf("Limit = %d, want %d", got.Limit, tc.want)
			}
			if got.Cursor != "abc" {
				t.Errorf("Normalize must leave Cursor alone, got %q", got.Cursor)
			}
		})
	}
}

func TestSystemClockNow(t *testing.T) {
	t.Parallel()

	var c domain.Clock = domain.SystemClock{}
	now := c.Now()
	if now.Location() != time.UTC {
		t.Errorf("SystemClock must return UTC, got %v", now.Location())
	}
	if time.Since(now) > time.Minute {
		t.Errorf("the returned time is too far from now: %v", now)
	}
}
