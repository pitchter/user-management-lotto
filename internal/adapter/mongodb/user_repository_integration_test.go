//go:build integration

// The tests in this file run a real MongoDB in a container, so they sit
// behind the `integration` build tag: the ordinary suite (make test) never
// touches this file and needs no Docker.
//
// Run them with: make test-integration
//
// Why this layer exists: the use-case tests use an in-memory fake, which
// proves the business rules but cannot prove that the queries, the bson type
// conversions and the unique index actually behave. Only a real database can.
// (mtest moved under internal/ in driver v2 and cannot be used from outside.)
package mongodb_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"

	"user-management-api/internal/adapter/mongodb"
	"user-management-api/internal/domain"
)

var fixedTime = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// startMongo starts one MongoDB container for the whole file.
//
// Every test shares the container and gets its own database name, because
// starting a container takes seconds while creating a database is free.
func startMongo(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	container, err := tcmongo.Run(ctx, "mongo:8")
	if err != nil {
		t.Fatalf("could not start the MongoDB container (Docker must be running): %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("could not terminate the container: %v", err)
		}
	})

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("could not read the connection string: %v", err)
	}
	return uri
}

// newRepo builds a repository on a fresh database, isolated from other tests.
func newRepo(t *testing.T, uri string) *mongodb.UserRepository {
	t.Helper()

	ctx := context.Background()
	// MongoDB caps database names at 63 characters and sub-test names get long
	// quickly, so the name is truncated and given a unique suffix.
	dbName := fmt.Sprintf("test_%s_%d", truncate(sanitize(t.Name()), 24), nextDBSeq())

	client, err := mongodb.Connect(ctx, mongodb.Config{
		URI:      uri,
		Database: dbName,
		Timeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatalf("could not connect to MongoDB: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Close(closeCtx)
	})

	// The indexes must exist first, or the unique-email rule has no effect.
	if err := client.EnsureIndexes(ctx); err != nil {
		t.Fatalf("could not create the indexes: %v", err)
	}

	return mongodb.NewUserRepository(client.Database())
}

// dbSeq keeps each test's database name unique, even when they run in parallel.
var dbSeq atomic.Int64

func nextDBSeq() int64 { return dbSeq.Add(1) }

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func newUser(t *testing.T, name, email string, role domain.Role) *domain.User {
	t.Helper()
	u, err := domain.NewUser(name, email, "$2a$12$abcdefghijklmnopqrstuv", role, fixedTime)
	if err != nil {
		t.Fatalf("could not build the entity: %v", err)
	}
	return u
}

// TestIntegrationUserRepositoryCRUD walks one full lifecycle of a record.
func TestIntegrationUserRepositoryCRUD(t *testing.T) {
	uri := startMongo(t)
	repo := newRepo(t, uri)
	ctx := context.Background()

	u := newUser(t, "Somchai Jaidee", "Somchai@Example.com", domain.RoleUser)

	// --- Create ---
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == "" {
		t.Fatal("Create must copy the MongoDB-generated ID back into the entity")
	}
	if len(u.ID) != 24 {
		t.Errorf("ID = %q, it must be a 24-character ObjectID hex string", u.ID)
	}

	// --- GetByID ---
	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Somchai Jaidee" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Email != "somchai@example.com" {
		t.Errorf("Email = %q — it must be stored lower-cased", got.Email)
	}
	if got.Role != domain.RoleUser {
		t.Errorf("Role = %q", got.Role)
	}
	if !got.CreatedAt.Equal(fixedTime) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, fixedTime)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Errorf("times read back must be UTC, got %v", got.CreatedAt.Location())
	}
	if got.PasswordHash != u.PasswordHash {
		t.Error("PasswordHash must be stored and read back intact")
	}

	// --- GetByEmail (must match regardless of the case sent) ---
	for _, email := range []string{"somchai@example.com", "SOMCHAI@EXAMPLE.COM", " Somchai@Example.com "} {
		found, err := repo.GetByEmail(ctx, email)
		if err != nil {
			t.Errorf("GetByEmail(%q): %v", email, err)
			continue
		}
		if found.ID != u.ID {
			t.Errorf("GetByEmail(%q) gave id %q, want %q", email, found.ID, u.ID)
		}
	}

	// --- Update ---
	later := fixedTime.Add(time.Hour)
	if err := got.Rename("Somchai Updated", later); err != nil {
		t.Fatal(err)
	}
	if err := got.ChangeEmail("NEW.Email@Example.com", later); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "Somchai Updated" {
		t.Errorf("Name = %q", after.Name)
	}
	if after.Email != "new.email@example.com" {
		t.Errorf("Email = %q", after.Email)
	}
	if !after.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", after.UpdatedAt, later)
	}
	if !after.CreatedAt.Equal(fixedTime) {
		t.Errorf("Update must not touch CreatedAt, got %v", after.CreatedAt)
	}

	// The old address must no longer resolve.
	if _, err := repo.GetByEmail(ctx, "somchai@example.com"); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("the old email must not be found after the change, got %v", err)
	}

	// --- Delete ---
	if err := repo.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, u.ID); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("after deletion want ErrUserNotFound, got %v", err)
	}
	// Deleting again must report that nothing was found.
	if err := repo.Delete(ctx, u.ID); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("deleting again must give ErrUserNotFound, got %v", err)
	}
}

// TestIntegrationUniqueEmailIndex proves the unique-email rule is enforced
// by the real database, not just by the code; the in-memory fake cannot
// stand in for this.
func TestIntegrationUniqueEmailIndex(t *testing.T) {
	uri := startMongo(t)
	repo := newRepo(t, uri)
	ctx := context.Background()

	first := newUser(t, "First User", "dup@example.com", domain.RoleUser)
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create of the first user: %v", err)
	}

	t.Run("creating again with the same email", func(t *testing.T) {
		second := newUser(t, "Second User", "dup@example.com", domain.RoleUser)
		err := repo.Create(ctx, second)
		if !errors.Is(err, domain.ErrEmailTaken) {
			t.Fatalf("want ErrEmailTaken, got %v", err)
		}
	})

	t.Run("creating again with a different case", func(t *testing.T) {
		second := newUser(t, "Third User", "DUP@Example.COM", domain.RoleUser)
		err := repo.Create(ctx, second)
		if !errors.Is(err, domain.ErrEmailTaken) {
			t.Fatalf("a different case must still count as a duplicate, got %v", err)
		}
	})

	t.Run("changing an email onto someone else's", func(t *testing.T) {
		other := newUser(t, "Other User", "other@example.com", domain.RoleUser)
		if err := repo.Create(ctx, other); err != nil {
			t.Fatal(err)
		}
		if err := other.ChangeEmail("dup@example.com", fixedTime); err != nil {
			t.Fatal(err)
		}
		if err := repo.Update(ctx, other); !errors.Is(err, domain.ErrEmailTaken) {
			t.Fatalf("want ErrEmailTaken, got %v", err)
		}
	})

	if n, err := repo.Count(ctx); err != nil || n != 2 {
		t.Errorf("Count = %d, err = %v (want 2)", n, err)
	}
}

// TestIntegrationConcurrentCreateSameEmail simulates two requests signing up
// with the same address at once; exactly one may succeed.
//
// This is why the code does not "check it is free, then write": another
// request can slip in between, so the unique index decides.
func TestIntegrationConcurrentCreateSameEmail(t *testing.T) {
	uri := startMongo(t)
	repo := newRepo(t, uri)
	ctx := context.Background()

	const attempts = 8
	errs := make(chan error, attempts)

	for i := 0; i < attempts; i++ {
		go func(i int) {
			u := newUser(t, fmt.Sprintf("Racer %d", i), "race@example.com", domain.RoleUser)
			errs <- repo.Create(ctx, u)
		}(i)
	}

	var ok, taken int
	for i := 0; i < attempts; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrEmailTaken):
			taken++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}

	if ok != 1 {
		t.Errorf("exactly 1 must succeed, but %d did", ok)
	}
	if taken != attempts-1 {
		t.Errorf("the rest must all give ErrEmailTaken (%d), got %d", attempts-1, taken)
	}
	if n, _ := repo.Count(ctx); n != 1 {
		t.Errorf("the database must hold 1 user, it holds %d", n)
	}
}

// TestIntegrationKeysetPagination confirms cursor pagination walks every
// record exactly once against the real database.
func TestIntegrationKeysetPagination(t *testing.T) {
	uri := startMongo(t)
	repo := newRepo(t, uri)
	ctx := context.Background()

	const total = 25
	for i := 0; i < total; i++ {
		u := newUser(t, fmt.Sprintf("User %02d", i), fmt.Sprintf("user%02d@example.com", i), domain.RoleUser)
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("Create of user %d: %v", i, err)
		}
	}

	seen := make(map[string]bool, total)
	cursor := ""
	pages := 0

	for {
		res, err := repo.List(ctx, domain.ListParams{Limit: 7, Cursor: cursor})
		if err != nil {
			t.Fatalf("List page %d: %v", pages+1, err)
		}
		pages++

		if res.Total != total {
			t.Errorf("Total = %d, want %d", res.Total, total)
		}
		if len(res.Users) > 7 {
			t.Fatalf("a page must not exceed the limit, got %d", len(res.Users))
		}

		// Within a page the ids must ascend.
		for i := 1; i < len(res.Users); i++ {
			if res.Users[i-1].ID >= res.Users[i].ID {
				t.Errorf("results must ascend by id: %q came before %q",
					res.Users[i-1].ID, res.Users[i].ID)
			}
		}

		for _, u := range res.Users {
			if seen[u.ID] {
				t.Fatalf("user %s was returned twice across pages", u.ID)
			}
			seen[u.ID] = true
		}

		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
		if pages > 10 {
			t.Fatal("the loop is not ending; the cursor may not be advancing")
		}
	}

	if len(seen) != total {
		t.Errorf("paging found %d users, want %d", len(seen), total)
	}
	if pages != 4 {
		t.Errorf("page count = %d, want 4 (25 users in pages of 7)", pages)
	}
}

func TestIntegrationListDefaultsAndLimits(t *testing.T) {
	uri := startMongo(t)
	repo := newRepo(t, uri)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		u := newUser(t, fmt.Sprintf("U%d", i), fmt.Sprintf("u%d@example.com", i), domain.RoleUser)
		if err := repo.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no limit given uses the default", func(t *testing.T) {
		res, err := repo.List(ctx, domain.ListParams{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Users) != 3 || res.NextCursor != "" {
			t.Errorf("got %d users, cursor = %q", len(res.Users), res.NextCursor)
		}
	})

	t.Run("a cursor that is not an ObjectID", func(t *testing.T) {
		_, err := repo.List(ctx, domain.ListParams{Cursor: "not-a-valid-cursor"})
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("want ErrValidation, got %v", err)
		}
	})

	t.Run("an empty database", func(t *testing.T) {
		empty := newRepo(t, uri)
		res, err := empty.List(ctx, domain.ListParams{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Users) != 0 || res.Total != 0 || res.NextCursor != "" {
			t.Errorf("got %+v", res)
		}
		// It must be an empty slice, not nil, so the JSON is [] rather than null.
		if res.Users == nil {
			t.Error("Users must be an empty slice, not nil")
		}
	})
}

func TestIntegrationNotFoundCases(t *testing.T) {
	uri := startMongo(t)
	repo := newRepo(t, uri)
	ctx := context.Background()

	missing := "653f1a2b3c4d5e6f70818283" // a valid ObjectID that is not stored
	invalid := "not-an-object-id"

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "GetByID, not stored", run: func() error { _, err := repo.GetByID(ctx, missing); return err }},
		{name: "GetByID, malformed", run: func() error { _, err := repo.GetByID(ctx, invalid); return err }},
		{name: "GetByID, empty", run: func() error { _, err := repo.GetByID(ctx, ""); return err }},
		{name: "GetByEmail, not stored", run: func() error { _, err := repo.GetByEmail(ctx, "nobody@example.com"); return err }},
		{name: "Delete, not stored", run: func() error { return repo.Delete(ctx, missing) }},
		{name: "Delete, malformed", run: func() error { return repo.Delete(ctx, invalid) }},
		{
			name: "Update, not stored",
			run: func() error {
				u := newUser(t, "Ghost", "ghost@example.com", domain.RoleUser)
				u.ID = missing
				return repo.Update(ctx, u)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, domain.ErrUserNotFound) {
				t.Errorf("want ErrUserNotFound, got %v", err)
			}
		})
	}
}

// TestIntegrationEnsureIndexesIsIdempotent confirms repeated calls on restart
// are safe.
func TestIntegrationEnsureIndexesIsIdempotent(t *testing.T) {
	uri := startMongo(t)
	ctx := context.Background()

	client, err := mongodb.Connect(ctx, mongodb.Config{
		URI:      uri,
		Database: "idempotent_index_test",
		Timeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	for i := 0; i < 3; i++ {
		if err := client.EnsureIndexes(ctx); err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
	}
}

func TestIntegrationPingAndClose(t *testing.T) {
	uri := startMongo(t)
	ctx := context.Background()

	client, err := mongodb.Connect(ctx, mongodb.Config{
		URI:      uri,
		Database: "ping_test",
		Timeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping must succeed: %v", err)
	}
	if err := client.Close(ctx); err != nil {
		t.Errorf("Close must succeed: %v", err)
	}
	// Once closed, Ping must fail.
	if err := client.Ping(ctx); err == nil {
		t.Error("after closing the connection Ping must fail")
	}
}

func TestIntegrationConnectFailsFast(t *testing.T) {
	ctx := context.Background()

	// A port with nothing listening: it must fail within the timeout, not hang.
	start := time.Now()
	_, err := mongodb.Connect(ctx, mongodb.Config{
		URI:      "mongodb://127.0.0.1:1",
		Database: "nope",
		Timeout:  3 * time.Second,
	})
	if err == nil {
		t.Fatal("an unreachable server must produce an error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("it took %v, which is too long; it should stop at the configured timeout", elapsed)
	}
}
