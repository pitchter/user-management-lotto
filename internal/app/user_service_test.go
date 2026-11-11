package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"user-management-api/internal/app"
	"user-management-api/internal/domain"
	"user-management-api/internal/testsupport"
)

// fixture gathers what most tests need in one place.
type fixture struct {
	svc    *app.UserService
	repo   *testsupport.FakeUserRepository
	hasher *testsupport.FakeHasher
	tokens *testsupport.FakeTokenService
	clock  *testsupport.FakeClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	repo := testsupport.NewFakeUserRepository()
	hasher := testsupport.NewFakeHasher()
	tokens := testsupport.NewFakeTokenService()
	clock := testsupport.NewFakeClock(testsupport.FixedTime())

	svc, err := app.NewUserService(app.Deps{
		Repo:   repo,
		Hasher: hasher,
		Tokens: tokens,
		Clock:  clock,
	})
	if err != nil {
		t.Fatalf("could not build the UserService: %v", err)
	}

	return &fixture{svc: svc, repo: repo, hasher: hasher, tokens: tokens, clock: clock}
}

func adminIdentity(id string) domain.Identity {
	return domain.Identity{UserID: id, Role: domain.RoleAdmin}
}

func userIdentity(id string) domain.Identity {
	return domain.Identity{UserID: id, Role: domain.RoleUser}
}

// --- NewUserService ---

func TestNewUserServiceRequiresDeps(t *testing.T) {
	t.Parallel()

	repo := testsupport.NewFakeUserRepository()
	hasher := testsupport.NewFakeHasher()
	tokens := testsupport.NewFakeTokenService()

	tests := []struct {
		name string
		deps app.Deps
	}{
		{name: "no repo", deps: app.Deps{Hasher: hasher, Tokens: tokens}},
		{name: "no hasher", deps: app.Deps{Repo: repo, Tokens: tokens}},
		{name: "no tokens", deps: app.Deps{Repo: repo, Hasher: hasher}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := app.NewUserService(tc.deps); err == nil {
				t.Error("missing dependencies must fail at wiring time")
			}
		})
	}

	// Omitting Clock and Logger must fall back to the defaults.
	svc, err := app.NewUserService(app.Deps{Repo: repo, Hasher: hasher, Tokens: tokens})
	if err != nil {
		t.Fatalf("it should build without a Clock or Logger: %v", err)
	}
	if _, err := svc.Count(context.Background()); err != nil {
		t.Errorf("and still work: %v", err)
	}
}

// --- Register ---

func TestRegisterSuccess(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	out, err := f.svc.Register(context.Background(), app.RegisterInput{
		Name:     "  Somchai  ",
		Email:    "  Somchai@Example.COM ",
		Password: "secret-password",
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if out.ID == "" {
		t.Error("the repository-generated ID must come back")
	}
	if out.Name != "Somchai" {
		t.Errorf("Name = %q (it must be trimmed)", out.Name)
	}
	if out.Email != "somchai@example.com" {
		t.Errorf("Email = %q (it must be lower-cased)", out.Email)
	}
	if out.Role != "user" {
		t.Errorf("Role = %q — a self sign-up must only ever get the user role", out.Role)
	}
	if !out.CreatedAt.Equal(f.clock.Now()) {
		t.Errorf("CreatedAt = %v", out.CreatedAt)
	}

	// The stored value must be a hash, not the raw password.
	stored := f.repo.All()[0]
	if stored.PasswordHash == "secret-password" {
		t.Fatal("the raw password must never be stored")
	}
	if !strings.HasPrefix(stored.PasswordHash, testsupport.FakeHashPrefix) {
		t.Errorf("PasswordHash = %q, it must have gone through the hasher", stored.PasswordHash)
	}
}

// TestRegisterCannotEscalateRole confirms the sign-up API offers no way to
// pick a role: whatever is sent, the result is the user role.
func TestRegisterCannotEscalateRole(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	out, err := f.svc.Register(context.Background(), app.RegisterInput{
		Name:     "Attacker",
		Email:    "attacker@example.com",
		Password: "secret-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Role != domain.RoleUser.String() {
		t.Errorf("Role = %q, want user", out.Role)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	testsupport.SeedUser(t, f.repo, testsupport.WithEmail("taken@example.com"))

	tests := []string{"taken@example.com", "TAKEN@example.com", "  Taken@Example.com  "}
	for _, email := range tests {
		t.Run(email, func(t *testing.T) {
			_, err := f.svc.Register(context.Background(), app.RegisterInput{
				Name:     "Someone",
				Email:    email,
				Password: "secret-password",
			})
			if !errors.Is(err, domain.ErrEmailTaken) {
				t.Errorf("want ErrEmailTaken, got: %v", err)
			}
		})
	}
	if n := f.repo.Len(); n != 1 {
		t.Errorf("no user may have been added, but there are %d", n)
	}
}

func TestRegisterValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    app.RegisterInput
		field string
	}{
		{
			name:  "no name",
			in:    app.RegisterInput{Email: "a@example.com", Password: "secret-password"},
			field: "name",
		},
		{
			name:  "malformed email",
			in:    app.RegisterInput{Name: "Alice", Email: "not-an-email", Password: "secret-password"},
			field: "email",
		},
		{
			name:  "password too short",
			in:    app.RegisterInput{Name: "Alice", Email: "a@example.com", Password: "short"},
			field: "password",
		},
		{
			name:  "no password",
			in:    app.RegisterInput{Name: "Alice", Email: "a@example.com"},
			field: "password",
		},
		{
			name:  "password longer than 72 bytes",
			in:    app.RegisterInput{Name: "Alice", Email: "a@example.com", Password: strings.Repeat("x", 73)},
			field: "password",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			_, err := f.svc.Register(context.Background(), tc.in)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("want ErrValidation, got: %v", err)
			}

			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("the error must be a *ValidationError")
			}
			found := false
			for _, fe := range ve.Fields {
				if fe.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("field %q must be reported, got %+v", tc.field, ve.Fields)
			}
			if f.repo.Len() != 0 {
				t.Error("a request that fails validation must store nothing")
			}
		})
	}
}

// TestRegisterChecksPasswordBeforeHashing confirms no time is wasted hashing
// a password already known to break the policy. A high bcrypt cost is
// expensive, which would make this a cheap way to flood the server.
func TestRegisterChecksPasswordBeforeHashing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.hasher.HashErr = errors.New("must never be called")

	_, err := f.svc.Register(context.Background(), app.RegisterInput{
		Name:     "Alice",
		Email:    "alice@example.com",
		Password: "x",
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("it must fail validation before reaching the hasher, got: %v", err)
	}
}

func TestRegisterRepositoryFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dbErr := errors.New("connection reset by peer")
	f.repo.FailOn(testsupport.MethodCreate, dbErr)

	_, err := f.svc.Register(context.Background(), app.RegisterInput{
		Name:     "Alice",
		Email:    "alice@example.com",
		Password: "secret-password",
	})
	if !errors.Is(err, dbErr) {
		t.Errorf("a database error must propagate, got: %v", err)
	}
}

func TestRegisterRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.svc.Register(ctx, app.RegisterInput{
		Name:     "Alice",
		Email:    "alice@example.com",
		Password: "secret-password",
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got: %v", err)
	}
}

// --- Authenticate ---

func TestAuthenticateSuccess(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	seeded := testsupport.SeedUser(t, f.repo,
		testsupport.WithEmail("alice@example.com"),
		testsupport.WithPassword("secret-password"),
		testsupport.WithRole(domain.RoleAdmin),
	)

	out, err := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "  Alice@Example.com ",
		Password: "secret-password",
	})
	if err != nil {
		t.Fatalf("the login should succeed: %v", err)
	}
	if out.AccessToken == "" {
		t.Error("an access token must come back")
	}
	if out.TokenType != "Bearer" {
		t.Errorf("TokenType = %q", out.TokenType)
	}
	if out.ExpiresIn <= 0 {
		t.Errorf("ExpiresIn = %d", out.ExpiresIn)
	}
	if out.User.ID != seeded.ID {
		t.Errorf("User.ID = %q, want %q", out.User.ID, seeded.ID)
	}
	if out.User.Role != "admin" {
		t.Errorf("Role in the response = %q", out.User.Role)
	}
	// The token must be bound to the user who actually logged in.
	id, err := f.tokens.Verify(out.AccessToken)
	if err != nil {
		t.Fatalf("the issued token must verify: %v", err)
	}
	if id.UserID != seeded.ID || id.Role != domain.RoleAdmin {
		t.Errorf("identity inside the token = %+v", id)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	testsupport.SeedUser(t, f.repo,
		testsupport.WithEmail("alice@example.com"),
		testsupport.WithPassword("the-right-password"),
	)

	_, err := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "alice@example.com",
		Password: "the-wrong-password",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("want ErrInvalidCredentials, got: %v", err)
	}
}

// TestAuthenticateUnknownEmailLooksTheSame is a security test.
//
// When the email is not found the system must (1) return the very same error
// as a wrong password and (2) still call hasher.Compare so the work takes
// about as long. Without (2) an attacker could time responses to discover
// which addresses are registered.
func TestAuthenticateUnknownEmailLooksTheSame(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	testsupport.SeedUser(t, f.repo,
		testsupport.WithEmail("known@example.com"),
		testsupport.WithPassword("the-right-password"),
	)

	_, errUnknown := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "nobody@example.com",
		Password: "whatever-password",
	})
	callsAfterUnknown := f.hasher.CompareCalls.Load()

	_, errWrongPass := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "known@example.com",
		Password: "the-wrong-password",
	})

	if !errors.Is(errUnknown, domain.ErrInvalidCredentials) {
		t.Errorf("an unknown email must give ErrInvalidCredentials, got: %v", errUnknown)
	}
	if errUnknown.Error() != errWrongPass.Error() {
		t.Errorf("the message must be identical in both cases: %q vs %q", errUnknown, errWrongPass)
	}
	if callsAfterUnknown != 1 {
		t.Errorf("Compare must still run to even out the timing (called %d times)", callsAfterUnknown)
	}
}

func TestAuthenticateCorruptedStoredHash(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.repo.Seed(domain.User{
		Name:         "Broken",
		Email:        "broken@example.com",
		PasswordHash: "not a valid hash",
		Role:         domain.RoleUser,
		CreatedAt:    testsupport.FixedTime(),
		UpdatedAt:    testsupport.FixedTime(),
	})

	_, err := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "broken@example.com",
		Password: "secret-password",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	// A corrupt hash is a system fault and must not read as a wrong password.
	if errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("it must not be reported as ErrInvalidCredentials: %v", err)
	}
}

func TestAuthenticateTokenIssueFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	testsupport.SeedUser(t, f.repo,
		testsupport.WithEmail("alice@example.com"),
		testsupport.WithPassword("secret-password"),
	)
	issueErr := errors.New("signing key unavailable")
	f.tokens.IssueErr = issueErr

	_, err := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "alice@example.com",
		Password: "secret-password",
	})
	if !errors.Is(err, issueErr) {
		t.Errorf("the token-issuing error must propagate, got: %v", err)
	}
}

func TestAuthenticateRepositoryFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dbErr := errors.New("server selection timeout")
	f.repo.FailOn(testsupport.MethodGetByEmail, dbErr)

	_, err := f.svc.Authenticate(context.Background(), app.LoginInput{
		Email:    "alice@example.com",
		Password: "secret-password",
	})
	if !errors.Is(err, dbErr) {
		t.Errorf("the database error must propagate, got: %v", err)
	}
	if errors.Is(err, domain.ErrInvalidCredentials) {
		t.Error("a database outage must not turn into a 401")
	}
}

// --- Create (admin) ---

func TestCreateByAdmin(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	out, err := f.svc.Create(context.Background(), adminIdentity("admin-1"), app.CreateUserInput{
		Name:     "New Admin",
		Email:    "new.admin@example.com",
		Password: "secret-password",
		Role:     "admin",
	})
	if err != nil {
		t.Fatalf("an admin must be able to create a user: %v", err)
	}
	if out.Role != "admin" {
		t.Errorf("Role = %q, want admin", out.Role)
	}
	if out.ID == "" {
		t.Error("an ID must come back")
	}
}

func TestCreateDefaultsToUserRole(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	out, err := f.svc.Create(context.Background(), adminIdentity("admin-1"), app.CreateUserInput{
		Name:     "Plain",
		Email:    "plain@example.com",
		Password: "secret-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Role != "user" {
		t.Errorf("with no role given the result must be user, got %q", out.Role)
	}
}

func TestCreateRejectsNonAdmin(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	for _, actor := range []domain.Identity{userIdentity("user-1"), {}} {
		_, err := f.svc.Create(context.Background(), actor, app.CreateUserInput{
			Name:     "X",
			Email:    "x@example.com",
			Password: "secret-password",
		})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("actor %+v must get ErrForbidden, got: %v", actor, err)
		}
	}
	if f.repo.Len() != 0 {
		t.Error("no user may have been created")
	}
}

func TestCreateRejectsUnknownRole(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	_, err := f.svc.Create(context.Background(), adminIdentity("admin-1"), app.CreateUserInput{
		Name:     "X",
		Email:    "x@example.com",
		Password: "secret-password",
		Role:     "superuser",
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("an unknown role must give ErrValidation, got: %v", err)
	}
}

// --- GetByID ---

func TestGetByID(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo, testsupport.WithEmail("target@example.com"))

	tests := []struct {
		name    string
		actor   domain.Identity
		wantErr error
	}{
		{name: "an admin may read it", actor: adminIdentity("admin-1")},
		{name: "the user itself may read it", actor: userIdentity(target.ID)},
		{name: "someone else may not", actor: userIdentity("someone-else"), wantErr: domain.ErrForbidden},
		{name: "an empty identity may not", actor: domain.Identity{}, wantErr: domain.ErrForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := f.svc.GetByID(context.Background(), tc.actor, target.ID)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("want %v, got: %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.ID != target.ID {
				t.Errorf("ID = %q", out.ID)
			}
			if out.Email != target.Email {
				t.Errorf("Email = %q", out.Email)
			}
		})
	}
}

func TestGetByIDNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	_, err := f.svc.GetByID(context.Background(), adminIdentity("admin-1"), "653f1a2b3c4d5e6f70818283")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("want ErrUserNotFound, got: %v", err)
	}
}

// TestGetByIDChecksPermissionBeforeExistence confirms an ordinary user
// cannot use the 403/404 difference to probe which ids exist.
func TestGetByIDChecksPermissionBeforeExistence(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	existing := testsupport.SeedUser(t, f.repo)

	_, errExisting := f.svc.GetByID(context.Background(), userIdentity("other"), existing.ID)
	_, errMissing := f.svc.GetByID(context.Background(), userIdentity("other"), "653f1a2b3c4d5e6f70818283")

	if !errors.Is(errExisting, domain.ErrForbidden) || !errors.Is(errMissing, domain.ErrForbidden) {
		t.Errorf("both cases must be ErrForbidden alike: %v / %v", errExisting, errMissing)
	}
}

// --- List ---

func TestListByAdmin(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		testsupport.SeedUser(t, f.repo, testsupport.WithEmail(email))
	}

	out, err := f.svc.List(context.Background(), adminIdentity("admin-1"), app.ListInput{})
	if err != nil {
		t.Fatalf("an admin must be able to list: %v", err)
	}
	if len(out.Users) != 3 {
		t.Errorf("got %d users, want 3", len(out.Users))
	}
	if out.Total != 3 {
		t.Errorf("Total = %d", out.Total)
	}
	if out.NextCursor != "" {
		t.Errorf("with nothing left NextCursor must be empty, got %q", out.NextCursor)
	}
}

func TestListRejectsNonAdmin(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	testsupport.SeedUser(t, f.repo)

	_, err := f.svc.List(context.Background(), userIdentity("user-1"), app.ListInput{})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("an ordinary user must get ErrForbidden, got: %v", err)
	}
}

func TestListPagination(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	const total = 7
	for i := 0; i < total; i++ {
		testsupport.SeedUser(t, f.repo, testsupport.WithEmail(string(rune('a'+i))+"@example.com"))
	}

	admin := adminIdentity("admin-1")
	seen := make(map[string]bool)
	cursor := ""
	pages := 0

	for {
		out, err := f.svc.List(context.Background(), admin, app.ListInput{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++

		for _, u := range out.Users {
			if seen[u.ID] {
				t.Fatalf("user %s was returned twice across pages", u.ID)
			}
			seen[u.ID] = true
		}
		if out.Total != total {
			t.Errorf("Total = %d, want %d", out.Total, total)
		}
		if len(out.Users) > 3 {
			t.Errorf("a page must not exceed the limit, got %d", len(out.Users))
		}

		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
		if pages > 10 {
			t.Fatal("the loop is not ending; NextCursor may not be advancing")
		}
	}

	if len(seen) != total {
		t.Errorf("paging through found %d users, want %d", len(seen), total)
	}
	if pages != 3 {
		t.Errorf("page count = %d, want 3 (7 users in pages of 3)", pages)
	}
}

func TestListLimitIsCapped(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	for i := 0; i < 5; i++ {
		testsupport.SeedUser(t, f.repo, testsupport.WithEmail(string(rune('a'+i))+"@example.com"))
	}

	// Asking for more than the ceiling must not fail; it is clamped to MaxListLimit.
	out, err := f.svc.List(context.Background(), adminIdentity("admin-1"), app.ListInput{Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Users) != 5 {
		t.Errorf("got %d users", len(out.Users))
	}
}

// --- Update ---

func TestUpdateName(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo, testsupport.WithName("Old Name"))
	f.clock.Advance(90 * time.Minute)

	newName := "Brand New Name"
	out, err := f.svc.Update(context.Background(), userIdentity(target.ID), target.ID,
		app.UpdateUserInput{Name: &newName})
	if err != nil {
		t.Fatalf("the user itself must be able to rename: %v", err)
	}
	if out.Name != newName {
		t.Errorf("Name = %q", out.Name)
	}
	if out.Email != target.Email {
		t.Errorf("the email was not being changed and must stay, got %q", out.Email)
	}
	if !out.UpdatedAt.Equal(f.clock.Now()) {
		t.Errorf("UpdatedAt = %v, want %v", out.UpdatedAt, f.clock.Now())
	}
	if !out.CreatedAt.Equal(target.CreatedAt) {
		t.Error("CreatedAt must not change")
	}

	// It must really be persisted in the repository.
	if stored := f.repo.All()[0]; stored.Name != newName {
		t.Errorf("stored value = %q", stored.Name)
	}
}

func TestUpdateEmail(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo, testsupport.WithEmail("old@example.com"))

	newEmail := "NEW@Example.com"
	out, err := f.svc.Update(context.Background(), adminIdentity("admin-1"), target.ID,
		app.UpdateUserInput{Email: &newEmail})
	if err != nil {
		t.Fatalf("an admin must be able to edit: %v", err)
	}
	if out.Email != "new@example.com" {
		t.Errorf("Email = %q, it must be lower-cased", out.Email)
	}
}

func TestUpdateBothFields(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo)

	name, email := "Both Changed", "both@example.com"
	out, err := f.svc.Update(context.Background(), adminIdentity("a"), target.ID,
		app.UpdateUserInput{Name: &name, Email: &email})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != name || out.Email != email {
		t.Errorf("got %q / %q", out.Name, out.Email)
	}
}

func TestUpdateNoChanges(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo)

	_, err := f.svc.Update(context.Background(), adminIdentity("a"), target.ID, app.UpdateUserInput{})
	if !errors.Is(err, domain.ErrNoChanges) {
		t.Errorf("want ErrNoChanges, got: %v", err)
	}
}

func TestUpdateForbiddenForOtherUser(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo, testsupport.WithName("Victim"))

	name := "Hacked"
	_, err := f.svc.Update(context.Background(), userIdentity("attacker"), target.ID,
		app.UpdateUserInput{Name: &name})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("want ErrForbidden, got: %v", err)
	}
	if stored := f.repo.All()[0]; stored.Name != "Victim" {
		t.Errorf("the record must be untouched, but it became %q", stored.Name)
	}
}

func TestUpdateValidationErrors(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo)

	bad := ""
	badEmail := "not-an-email"

	tests := []struct {
		name  string
		in    app.UpdateUserInput
		field string
	}{
		{name: "blank name", in: app.UpdateUserInput{Name: &bad}, field: "name"},
		{name: "malformed email", in: app.UpdateUserInput{Email: &badEmail}, field: "email"},
		{
			name:  "both fields invalid are both reported",
			in:    app.UpdateUserInput{Name: &bad, Email: &badEmail},
			field: "email",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := f.svc.Update(context.Background(), adminIdentity("a"), target.ID, tc.in)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("want ErrValidation, got: %v", err)
			}
			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatal("it must be a *ValidationError")
			}
			found := false
			for _, fe := range ve.Fields {
				if fe.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("field %q must appear in %+v", tc.field, ve.Fields)
			}
		})
	}
}

func TestUpdateReportsBothInvalidFields(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo)

	badName, badEmail := "", "nope"
	_, err := f.svc.Update(context.Background(), adminIdentity("a"), target.ID,
		app.UpdateUserInput{Name: &badName, Email: &badEmail})

	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("it must be a *ValidationError, got %v", err)
	}
	if len(ve.Fields) != 2 {
		t.Errorf("both fields must be reported at once, got %+v", ve.Fields)
	}
}

func TestUpdateEmailAlreadyTaken(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	testsupport.SeedUser(t, f.repo, testsupport.WithEmail("taken@example.com"))
	target := testsupport.SeedUser(t, f.repo, testsupport.WithEmail("mine@example.com"))

	taken := "taken@example.com"
	_, err := f.svc.Update(context.Background(), adminIdentity("a"), target.ID,
		app.UpdateUserInput{Email: &taken})
	if !errors.Is(err, domain.ErrEmailTaken) {
		t.Errorf("want ErrEmailTaken, got: %v", err)
	}
}

func TestUpdateToSameEmailIsAllowed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo, testsupport.WithEmail("mine@example.com"))

	same := "mine@example.com"
	if _, err := f.svc.Update(context.Background(), userIdentity(target.ID), target.ID,
		app.UpdateUserInput{Email: &same}); err != nil {
		t.Errorf("resubmitting one's own address must not clash with oneself: %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	name := "Ghost"
	_, err := f.svc.Update(context.Background(), adminIdentity("a"), "653f1a2b3c4d5e6f70818283",
		app.UpdateUserInput{Name: &name})
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("want ErrUserNotFound, got: %v", err)
	}
}

// --- Delete ---

func TestDeleteByAdmin(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo)

	if err := f.svc.Delete(context.Background(), adminIdentity("admin-1"), target.ID); err != nil {
		t.Fatalf("an admin must be able to delete: %v", err)
	}
	if f.repo.Len() != 0 {
		t.Error("the record must really be gone")
	}
}

func TestDeleteRejectsNonAdmin(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	target := testsupport.SeedUser(t, f.repo)

	tests := []struct {
		name  string
		actor domain.Identity
	}{
		{name: "a user deleting someone else", actor: userIdentity("other")},
		{name: "a user deleting itself", actor: userIdentity(target.ID)},
		{name: "an empty identity", actor: domain.Identity{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := f.svc.Delete(context.Background(), tc.actor, target.ID)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("want ErrForbidden, got: %v", err)
			}
		})
	}
	if f.repo.Len() != 1 {
		t.Error("the record must still be there")
	}
}

// TestDeleteAdminCannotDeleteSelf guards the case where the last admin
// deletes itself and nobody can administer the system any more.
func TestDeleteAdminCannotDeleteSelf(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	admin := testsupport.SeedUser(t, f.repo, testsupport.WithRole(domain.RoleAdmin))

	err := f.svc.Delete(context.Background(), adminIdentity(admin.ID), admin.ID)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("want ErrForbidden, got: %v", err)
	}
	if f.repo.Len() != 1 {
		t.Error("the admin account must still be there")
	}
}

func TestDeleteNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	err := f.svc.Delete(context.Background(), adminIdentity("admin-1"), "653f1a2b3c4d5e6f70818283")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("want ErrUserNotFound, got: %v", err)
	}
}

// --- Count ---

func TestCount(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	n, err := f.svc.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an empty database must give 0, got %d", n)
	}

	testsupport.SeedUser(t, f.repo, testsupport.WithEmail("a@example.com"))
	testsupport.SeedUser(t, f.repo, testsupport.WithEmail("b@example.com"))

	if n, err = f.svc.Count(context.Background()); err != nil || n != 2 {
		t.Errorf("Count = %d, err = %v (want 2)", n, err)
	}
}

func TestCountRepositoryFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dbErr := errors.New("topology closed")
	f.repo.FailOn(testsupport.MethodCount, dbErr)

	if _, err := f.svc.Count(context.Background()); !errors.Is(err, dbErr) {
		t.Errorf("the error must propagate, got: %v", err)
	}
}

// --- EnsureBootstrapAdmin ---

func TestEnsureBootstrapAdminCreates(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	created, err := f.svc.EnsureBootstrapAdmin(context.Background(), "Root", "root@example.com", "secret-password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("an empty database must get a fresh admin")
	}

	stored := f.repo.All()[0]
	if stored.Role != domain.RoleAdmin {
		t.Errorf("Role = %q, want admin", stored.Role)
	}
	if stored.Email != "root@example.com" {
		t.Errorf("Email = %q", stored.Email)
	}
	if stored.PasswordHash == "secret-password" {
		t.Error("a hash must be stored, not the raw password")
	}
}

// TestEnsureBootstrapAdminIsIdempotent confirms repeated restarts never
// overwrite real data or reset a password the user may have changed.
func TestEnsureBootstrapAdminIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.EnsureBootstrapAdmin(ctx, "Root", "root@example.com", "secret-password"); err != nil {
		t.Fatal(err)
	}
	before := f.repo.All()[0]

	created, err := f.svc.EnsureBootstrapAdmin(ctx, "Root Renamed", "root@example.com", "a-different-password")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("the second call must not create anything")
	}
	if f.repo.Len() != 1 {
		t.Errorf("there must be exactly one user, got %d", f.repo.Len())
	}

	after := f.repo.All()[0]
	if after.Name != before.Name || after.PasswordHash != before.PasswordHash {
		t.Error("the existing record must not be modified at all")
	}
}

func TestEnsureBootstrapAdminRejectsWeakPassword(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	_, err := f.svc.EnsureBootstrapAdmin(context.Background(), "Root", "root@example.com", "123")
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a weak password must be rejected even for the bootstrap admin: %v", err)
	}
}

func TestEnsureBootstrapAdminRepositoryFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dbErr := errors.New("network unreachable")
	f.repo.FailOn(testsupport.MethodGetByEmail, dbErr)

	if _, err := f.svc.EnsureBootstrapAdmin(context.Background(), "Root", "root@example.com", "secret-password"); !errors.Is(err, dbErr) {
		t.Errorf("the error must propagate, got: %v", err)
	}
}
