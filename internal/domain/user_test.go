package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pitchter/7-solution/internal/domain"
)

var testNow = time.Date(2026, 11, 11, 10, 30, 0, 0, time.UTC)

func TestNewUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		inputName    string
		inputEmail   string
		passwordHash string
		role         domain.Role
		wantErr      bool
		wantFields   []string
		wantEmail    string
		wantName     string
	}{
		{
			name:         "all fields valid",
			inputName:    "Somchai Jaidee",
			inputEmail:   "somchai@example.com",
			passwordHash: "$2a$12$hash",
			role:         domain.RoleUser,
			wantEmail:    "somchai@example.com",
			wantName:     "Somchai Jaidee",
		},
		{
			name:         "email is lower-cased and trimmed",
			inputName:    "  Alice  ",
			inputEmail:   "  Alice@Example.COM  ",
			passwordHash: "$2a$12$hash",
			role:         domain.RoleAdmin,
			wantEmail:    "alice@example.com",
			wantName:     "Alice",
		},
		{
			name:         "blank name",
			inputName:    "   ",
			inputEmail:   "a@example.com",
			passwordHash: "h",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"name"},
		},
		{
			name:         "name too short",
			inputName:    "A",
			inputEmail:   "a@example.com",
			passwordHash: "h",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"name"},
		},
		{
			name:         "name longer than 100 characters",
			inputName:    strings.Repeat("a", 101),
			inputEmail:   "a@example.com",
			passwordHash: "h",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"name"},
		},
		{
			name:         "email without @",
			inputName:    "Alice",
			inputEmail:   "not-an-email",
			passwordHash: "h",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"email"},
		},
		{
			name:         "email domain without a dot",
			inputName:    "Alice",
			inputEmail:   "alice@localhost",
			passwordHash: "h",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"email"},
		},
		{
			name:         "display-name form of an address is rejected",
			inputName:    "Alice",
			inputEmail:   "Alice <alice@example.com>",
			passwordHash: "h",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"email"},
		},
		{
			name:         "missing password hash",
			inputName:    "Alice",
			inputEmail:   "alice@example.com",
			passwordHash: "",
			role:         domain.RoleUser,
			wantErr:      true,
			wantFields:   []string{"password"},
		},
		{
			name:         "unknown role",
			inputName:    "Alice",
			inputEmail:   "alice@example.com",
			passwordHash: "h",
			role:         domain.Role("superuser"),
			wantErr:      true,
			wantFields:   []string{"role"},
		},
		{
			name:         "several invalid fields are all reported",
			inputName:    "",
			inputEmail:   "bad",
			passwordHash: "",
			role:         domain.Role("x"),
			wantErr:      true,
			wantFields:   []string{"name", "email", "password", "role"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			u, err := domain.NewUser(tc.inputName, tc.inputEmail, tc.passwordHash, tc.role, testNow)

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if u.Email != tc.wantEmail {
					t.Errorf("Email = %q, want %q", u.Email, tc.wantEmail)
				}
				if u.Name != tc.wantName {
					t.Errorf("Name = %q, want %q", u.Name, tc.wantName)
				}
				if u.ID != "" {
					t.Errorf("ID must be left empty for the repository to fill, got %q", u.ID)
				}
				if !u.CreatedAt.Equal(testNow) || !u.UpdatedAt.Equal(testNow) {
					t.Errorf("timestamps = %v/%v, want %v", u.CreatedAt, u.UpdatedAt, testNow)
				}
				if u.CreatedAt.Location() != time.UTC {
					t.Errorf("CreatedAt must be UTC, got %v", u.CreatedAt.Location())
				}
				return
			}

			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !errors.Is(err, domain.ErrValidation) {
				t.Errorf("error must match ErrValidation, got %v", err)
			}
			assertFields(t, err, tc.wantFields)
		})
	}
}

func TestUserRename(t *testing.T) {
	t.Parallel()

	later := testNow.Add(time.Hour)

	t.Run("renames and bumps UpdatedAt", func(t *testing.T) {
		t.Parallel()
		u := mustUser(t)
		if err := u.Rename("  New Name  ", later); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if u.Name != "New Name" {
			t.Errorf("Name = %q", u.Name)
		}
		if !u.UpdatedAt.Equal(later) {
			t.Errorf("UpdatedAt = %v, want %v", u.UpdatedAt, later)
		}
		if !u.CreatedAt.Equal(testNow) {
			t.Errorf("CreatedAt must not change, got %v", u.CreatedAt)
		}
	})

	t.Run("an invalid name leaves the entity untouched", func(t *testing.T) {
		t.Parallel()
		u := mustUser(t)
		err := u.Rename("", later)
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("want ErrValidation, got %v", err)
		}
		if u.Name != "Alice" {
			t.Errorf("name must not change, got %q", u.Name)
		}
		if !u.UpdatedAt.Equal(testNow) {
			t.Errorf("UpdatedAt must not change when validation fails")
		}
	})
}

func TestUserChangeEmail(t *testing.T) {
	t.Parallel()

	later := testNow.Add(time.Hour)

	t.Run("changes the email and normalises it", func(t *testing.T) {
		t.Parallel()
		u := mustUser(t)
		if err := u.ChangeEmail("NEW@Example.com", later); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if u.Email != "new@example.com" {
			t.Errorf("Email = %q", u.Email)
		}
		if !u.UpdatedAt.Equal(later) {
			t.Errorf("UpdatedAt = %v", u.UpdatedAt)
		}
	})

	t.Run("an invalid email leaves the entity untouched", func(t *testing.T) {
		t.Parallel()
		u := mustUser(t)
		if err := u.ChangeEmail("nope", later); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("want ErrValidation, got %v", err)
		}
		if u.Email != "alice@example.com" {
			t.Errorf("email must not change, got %q", u.Email)
		}
	})
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "long enough", input: "secret123"},
		{name: "exactly 8 characters", input: "12345678"},
		// 8 runes but 10 bytes: the length rule counts runes, not bytes
		{name: "8 multi-byte characters", input: "pässwörd"},
		{name: "empty", input: "", wantErr: true},
		{name: "too short", input: "short", wantErr: true},
		{name: "longer than 72 bytes", input: strings.Repeat("a", 73), wantErr: true},
		{name: "exactly 72 bytes", input: strings.Repeat("a", 72)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidatePassword(tc.input)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrValidation) {
					t.Errorf("want ErrValidation, got %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    domain.Role
		wantErr bool
	}{
		{input: "admin", want: domain.RoleAdmin},
		{input: "ADMIN", want: domain.RoleAdmin},
		{input: " user ", want: domain.RoleUser},
		{input: "", want: domain.RoleUser},
		{input: "root", wantErr: true},
	}

	for _, tc := range tests {
		t.Run("input="+tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := domain.ParseRole(tc.input)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrValidation) {
					t.Errorf("want ErrValidation, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRoleValid(t *testing.T) {
	t.Parallel()

	if !domain.RoleAdmin.Valid() || !domain.RoleUser.Valid() {
		t.Error("admin and user must both be valid")
	}
	if domain.Role("other").Valid() {
		t.Error(`"other" must not be valid`)
	}
	if domain.RoleAdmin.String() != "admin" {
		t.Error("String() must return admin")
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	if got := domain.NormalizeEmail("  Foo@BAR.com "); got != "foo@bar.com" {
		t.Errorf("got %q", got)
	}
}

func TestUserIsAdmin(t *testing.T) {
	t.Parallel()

	admin := &domain.User{Role: domain.RoleAdmin}
	user := &domain.User{Role: domain.RoleUser}
	if !admin.IsAdmin() {
		t.Error("admin.IsAdmin() must be true")
	}
	if user.IsAdmin() {
		t.Error("user.IsAdmin() must be false")
	}
}

func mustUser(t *testing.T) *domain.User {
	t.Helper()
	u, err := domain.NewUser("Alice", "alice@example.com", "$2a$12$hash", domain.RoleUser, testNow)
	if err != nil {
		t.Fatalf("could not build the starting user: %v", err)
	}
	return u
}

func assertFields(t *testing.T, err error, want []string) {
	t.Helper()

	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error must be a *ValidationError, got %T", err)
	}
	got := make(map[string]bool, len(ve.Fields))
	for _, f := range ve.Fields {
		got[f.Field] = true
		if f.Issue == "" {
			t.Errorf("field %q carries no explanation", f.Field)
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("field %q must be reported too, got %+v", w, ve.Fields)
		}
	}
	if len(ve.Fields) != len(want) {
		t.Errorf("invalid field count = %d, want %d (%+v)", len(ve.Fields), len(want), ve.Fields)
	}
}
