package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"user-management-api/internal/domain"
	"user-management-api/internal/platform/logger"
)

// UserService holds every use case about users.
//
// It depends only on ports (the interfaces declared in the domain), never on
// a concrete adapter, so every path can be tested with in-memory fakes.
type UserService struct {
	repo   domain.UserRepository
	hasher domain.PasswordHasher
	tokens domain.TokenService
	clock  domain.Clock
	log    *slog.Logger
}

// Deps is what a UserService needs.
//
// It is a struct rather than a list of positional parameters so the wiring
// point (main.go) reads clearly and a new dependency does not force every
// call site to change.
type Deps struct {
	Repo   domain.UserRepository
	Hasher domain.PasswordHasher
	Tokens domain.TokenService
	// Clock is optional; the real system clock is used when omitted.
	Clock domain.Clock
	// Logger is optional; everything is discarded when omitted.
	Logger *slog.Logger
}

// NewUserService builds the service and checks that the required
// dependencies are present.
//
// Validating at wiring time beats letting a nil pointer panic halfway
// through a request once traffic is flowing.
func NewUserService(d Deps) (*UserService, error) {
	switch {
	case d.Repo == nil:
		return nil, errors.New("app: a UserRepository is required")
	case d.Hasher == nil:
		return nil, errors.New("app: a PasswordHasher is required")
	case d.Tokens == nil:
		return nil, errors.New("app: a TokenService is required")
	}

	if d.Clock == nil {
		d.Clock = domain.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = logger.Discard()
	}

	return &UserService{
		repo:   d.Repo,
		hasher: d.Hasher,
		tokens: d.Tokens,
		clock:  d.Clock,
		log:    d.Logger,
	}, nil
}

// Register is self-service sign-up; the result always has the user role.
func (s *UserService) Register(ctx context.Context, in RegisterInput) (UserOutput, error) {
	u, err := s.createUser(ctx, in.Name, in.Email, in.Password, domain.RoleUser)
	if err != nil {
		return UserOutput{}, err
	}

	s.log.InfoContext(ctx, "user_registered",
		slog.String(logger.KeyUserID, u.ID),
		slog.String(logger.KeyRole, u.Role.String()),
	)
	return newUserOutput(*u), nil
}

// Create lets an admin create a user and choose the role.
func (s *UserService) Create(ctx context.Context, actor domain.Identity, in CreateUserInput) (UserOutput, error) {
	if !actor.IsAdmin() {
		return UserOutput{}, fmt.Errorf("%w: only an admin may create users", domain.ErrForbidden)
	}

	role, err := domain.ParseRole(in.Role)
	if err != nil {
		return UserOutput{}, err
	}

	u, err := s.createUser(ctx, in.Name, in.Email, in.Password, role)
	if err != nil {
		return UserOutput{}, err
	}

	s.log.InfoContext(ctx, "user_created_by_admin",
		slog.String(logger.KeyUserID, u.ID),
		slog.String("created_by", actor.UserID),
		slog.String(logger.KeyRole, u.Role.String()),
	)
	return newUserOutput(*u), nil
}

// Authenticate checks an email and password and issues an access token.
//
// Whether the address does not exist or the password is wrong, the caller
// gets the same ErrInvalidCredentials and the work takes about equally long.
func (s *UserService) Authenticate(ctx context.Context, in LoginInput) (AuthOutput, error) {
	email := domain.NormalizeEmail(in.Email)

	u, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			// Spend the same time as the found-user path, so response timing
			// cannot reveal whether this address is registered.
			_ = s.hasher.Compare(s.hasher.DummyHash(), in.Password)
			s.log.InfoContext(ctx, "login_failed", slog.String("reason", "email_not_found"))
			return AuthOutput{}, domain.ErrInvalidCredentials
		}
		return AuthOutput{}, err
	}

	if compareErr := s.hasher.Compare(u.PasswordHash, in.Password); compareErr != nil {
		if errors.Is(compareErr, domain.ErrInvalidCredentials) {
			s.log.InfoContext(ctx, "login_failed",
				slog.String("reason", "wrong_password"),
				slog.String(logger.KeyUserID, u.ID),
			)
			return AuthOutput{}, domain.ErrInvalidCredentials
		}
		// A corrupt stored hash is a system fault, not the user's problem.
		return AuthOutput{}, fmt.Errorf("could not check the password: %w", compareErr)
	}

	tok, err := s.tokens.Issue(u)
	if err != nil {
		return AuthOutput{}, fmt.Errorf("could not issue the token: %w", err)
	}

	s.log.InfoContext(ctx, "login_succeeded",
		slog.String(logger.KeyUserID, u.ID),
		slog.String(logger.KeyRole, u.Role.String()),
	)

	return AuthOutput{
		AccessToken: tok.Value,
		TokenType:   tok.Type,
		ExpiresIn:   tok.ExpiresIn,
		ExpiresAt:   tok.ExpiresAt,
		User:        newUserOutput(*u),
	}, nil
}

// GetByID reads a user: an admin may read anyone, a user only itself.
func (s *UserService) GetByID(ctx context.Context, actor domain.Identity, id string) (UserOutput, error) {
	if err := actor.Authorize(id); err != nil {
		return UserOutput{}, err
	}

	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return UserOutput{}, err
	}
	return newUserOutput(*u), nil
}

// List returns every user and is restricted to admins.
//
// It is admin-only because the listing exposes every user's email address,
// which is personal data that ordinary users should not be able to bulk-export.
func (s *UserService) List(ctx context.Context, actor domain.Identity, in ListInput) (ListOutput, error) {
	if !actor.IsAdmin() {
		return ListOutput{}, fmt.Errorf("%w: only an admin may list all users", domain.ErrForbidden)
	}

	res, err := s.repo.List(ctx, domain.ListParams{Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return ListOutput{}, err
	}

	out := make([]UserOutput, 0, len(res.Users))
	for _, u := range res.Users {
		out = append(out, newUserOutput(u))
	}
	return ListOutput{Users: out, NextCursor: res.NextCursor, Total: res.Total}, nil
}

// Update changes a user's name or email: an admin may edit anyone, a user
// only itself.
func (s *UserService) Update(ctx context.Context, actor domain.Identity, id string, in UpdateUserInput) (UserOutput, error) {
	if err := actor.Authorize(id); err != nil {
		return UserOutput{}, err
	}
	if !in.HasChanges() {
		return UserOutput{}, fmt.Errorf("%w: send at least one of name or email", domain.ErrNoChanges)
	}

	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return UserOutput{}, err
	}

	now := s.clock.Now()
	v := &domain.ValidationError{}

	// Both fields are attempted before failing, so a request with two bad
	// values is told about both at once.
	if in.Name != nil {
		if err := u.Rename(*in.Name, now); err != nil && !v.Absorb(err) {
			return UserOutput{}, err
		}
	}
	if in.Email != nil {
		if err := u.ChangeEmail(*in.Email, now); err != nil && !v.Absorb(err) {
			return UserOutput{}, err
		}
	}
	if v.HasErrors() {
		return UserOutput{}, v
	}

	if err := s.repo.Update(ctx, u); err != nil {
		return UserOutput{}, err
	}

	s.log.InfoContext(ctx, "user_updated",
		slog.String(logger.KeyUserID, id),
		slog.String("updated_by", actor.UserID),
		slog.Bool("name_changed", in.Name != nil),
		slog.Bool("email_changed", in.Email != nil),
	)
	return newUserOutput(*u), nil
}

// Delete removes a user and is restricted to admins.
//
// An admin cannot delete its own account: if it were the only admin left,
// nobody could create or edit users again — recovery would mean setting
// BOOTSTRAP_ADMIN_* and restarting. Better to refuse up front.
func (s *UserService) Delete(ctx context.Context, actor domain.Identity, id string) error {
	if !actor.IsAdmin() {
		return fmt.Errorf("%w: only an admin may delete users", domain.ErrForbidden)
	}
	if actor.UserID == id {
		return fmt.Errorf("%w: an admin cannot delete their own account", domain.ErrForbidden)
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	s.log.InfoContext(ctx, "user_deleted",
		slog.String(logger.KeyUserID, id),
		slog.String("deleted_by", actor.UserID),
	)
	return nil
}

// Count returns the total number of users (used by the background worker).
func (s *UserService) Count(ctx context.Context) (int64, error) {
	return s.repo.Count(ctx)
}

// EnsureBootstrapAdmin creates the first admin unless that email already exists.
//
// It is idempotent: if a user with this address is already there, nothing is
// touched (the password is not reset either), so restarting repeatedly never
// overwrites real data. It reports true when a user was just created.
func (s *UserService) EnsureBootstrapAdmin(ctx context.Context, name, email, password string) (bool, error) {
	normalized := domain.NormalizeEmail(email)

	_, err := s.repo.GetByEmail(ctx, normalized)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, domain.ErrUserNotFound):
		return false, err
	}

	u, err := s.createUser(ctx, name, normalized, password, domain.RoleAdmin)
	if err != nil {
		// Another replica may have created it at the same moment; not a problem.
		if errors.Is(err, domain.ErrEmailTaken) {
			return false, nil
		}
		return false, fmt.Errorf("could not create the bootstrap admin: %w", err)
	}

	s.log.InfoContext(ctx, "bootstrap_admin_created", slog.String(logger.KeyUserID, u.ID))
	return true, nil
}

// createUser is the shared path for every way a user is created (sign-up,
// admin create, bootstrap): check the password, hash it, build, store.
func (s *UserService) createUser(ctx context.Context, name, email, password string, role domain.Role) (*domain.User, error) {
	if err := domain.ValidatePassword(password); err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(password)
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			return nil, err
		}
		return nil, fmt.Errorf("could not prepare the password: %w", err)
	}

	u, err := domain.NewUser(name, email, hash, role, s.clock.Now())
	if err != nil {
		return nil, err
	}

	// There is no "check whether the email is free, then write" step, because
	// another request can slip in between the two. The database's unique index
	// decides, and its error is translated back into ErrEmailTaken.
	if err := s.repo.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}
