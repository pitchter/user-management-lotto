package testsupport

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"user-management-api/internal/domain"
)

// FakeUserRepository is an in-memory UserRepository that reproduces the
// behaviour that matters: unique emails, sortable ids, keyset pagination.
//
// The ids it generates are 24-character hex strings like an ObjectID, and
// they sort in creation order, so pagination tests behave as they would
// against MongoDB.
type FakeUserRepository struct {
	mu     sync.RWMutex
	users  map[string]domain.User
	nextID int

	// errs injects a failure per method, to check that the layers above handle
	// database errors correctly.
	errs map[string]error
}

var _ domain.UserRepository = (*FakeUserRepository)(nil)

// Method names used as keys when injecting errors.
const (
	MethodCreate     = "Create"
	MethodGetByID    = "GetByID"
	MethodGetByEmail = "GetByEmail"
	MethodList       = "List"
	MethodUpdate     = "Update"
	MethodDelete     = "Delete"
	MethodCount      = "Count"
)

// NewFakeUserRepository returns an empty repository.
func NewFakeUserRepository() *FakeUserRepository {
	return &FakeUserRepository{
		users: make(map[string]domain.User),
		errs:  make(map[string]error),
	}
}

// FailOn makes the named method return the given error (nil clears it).
func (r *FakeUserRepository) FailOn(method string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		delete(r.errs, method)
		return
	}
	r.errs[method] = err
}

// Seed inserts users directly, bypassing every rule, to set up a test's
// starting state. It returns the users with their ids filled in.
func (r *FakeUserRepository) Seed(users ...domain.User) []domain.User {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]domain.User, 0, len(users))
	for _, u := range users {
		if u.ID == "" {
			u.ID = r.newIDLocked()
		}
		u.Email = domain.NormalizeEmail(u.Email)
		r.users[u.ID] = u
		out = append(out, u)
	}
	return out
}

// All returns every user ordered by id, for asserting on the end state.
func (r *FakeUserRepository) All() []domain.User {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sortedLocked()
}

// Len returns how many users are stored.
func (r *FakeUserRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.users)
}

// Create stores a new user and enforces the unique-email rule the way a real
// unique index would.
func (r *FakeUserRepository) Create(ctx context.Context, u *domain.User) error {
	if err := r.gate(ctx, MethodCreate); err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("testsupport: Create was given nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	email := domain.NormalizeEmail(u.Email)
	for _, existing := range r.users {
		if existing.Email == email {
			return fmt.Errorf("%w: %s", domain.ErrEmailTaken, email)
		}
	}

	stored := *u
	stored.Email = email
	stored.ID = r.newIDLocked()
	r.users[stored.ID] = stored

	u.ID = stored.ID
	return nil
}

// GetByID fetches a user by id.
func (r *FakeUserRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	if err := r.gate(ctx, MethodGetByID); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	u, found := r.users[id]
	if !found {
		return nil, fmt.Errorf("%w (id %s)", domain.ErrUserNotFound, id)
	}
	// Always return a copy so that a caller mutating the result does not
	// change what is stored (the real one decodes a fresh value every time).
	return &u, nil
}

// GetByEmail fetches a user by email.
func (r *FakeUserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	if err := r.gate(ctx, MethodGetByEmail); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	normalized := domain.NormalizeEmail(email)
	for _, u := range r.users {
		if u.Email == normalized {
			found := u
			return &found, nil
		}
	}
	return nil, fmt.Errorf("%w (email %s)", domain.ErrUserNotFound, normalized)
}

// List returns a page using keyset pagination, like the real adapter.
func (r *FakeUserRepository) List(ctx context.Context, p domain.ListParams) (domain.ListResult, error) {
	if err := r.gate(ctx, MethodList); err != nil {
		return domain.ListResult{}, err
	}
	p = p.Normalize()

	r.mu.RLock()
	defer r.mu.RUnlock()

	all := r.sortedLocked()

	start := 0
	if p.Cursor != "" {
		start = len(all)
		for i, u := range all {
			if u.ID > p.Cursor {
				start = i
				break
			}
		}
	}

	end := min(start+p.Limit, len(all))
	page := append([]domain.User(nil), all[start:end]...)

	nextCursor := ""
	if end < len(all) && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}

	return domain.ListResult{
		Users:      page,
		NextCursor: nextCursor,
		Total:      int64(len(all)),
	}, nil
}

// Update persists changes and rejects an email already used by someone else.
func (r *FakeUserRepository) Update(ctx context.Context, u *domain.User) error {
	if err := r.gate(ctx, MethodUpdate); err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("testsupport: Update was given nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, found := r.users[u.ID]; !found {
		return fmt.Errorf("%w (id %s)", domain.ErrUserNotFound, u.ID)
	}

	email := domain.NormalizeEmail(u.Email)
	for id, existing := range r.users {
		if id != u.ID && existing.Email == email {
			return fmt.Errorf("%w: %s", domain.ErrEmailTaken, email)
		}
	}

	stored := *u
	stored.Email = email
	r.users[u.ID] = stored
	return nil
}

// Delete removes a user.
func (r *FakeUserRepository) Delete(ctx context.Context, id string) error {
	if err := r.gate(ctx, MethodDelete); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, found := r.users[id]; !found {
		return fmt.Errorf("%w (id %s)", domain.ErrUserNotFound, id)
	}
	delete(r.users, id)
	return nil
}

// Count returns the total number of users.
func (r *FakeUserRepository) Count(ctx context.Context) (int64, error) {
	if err := r.gate(ctx, MethodCount); err != nil {
		return 0, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	return int64(len(r.users)), nil
}

// gate returns any injected error and honours context cancellation, the way
// the real driver stops working once the context is cancelled.
func (r *FakeUserRepository) gate(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.errs[method]
}

// newIDLocked generates an id in the same shape as an ObjectID hex string
// (24 characters) that sorts by creation order. The lock must be held.
func (r *FakeUserRepository) newIDLocked() string {
	r.nextID++
	return fmt.Sprintf("%024x", r.nextID)
}

// sortedLocked returns every user ordered by id. The lock must be held.
func (r *FakeUserRepository) sortedLocked() []domain.User {
	out := make([]domain.User, 0, len(r.users))
	for _, u := range r.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
