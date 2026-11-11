package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"user-management-api/internal/adapter/httpapi"
	"user-management-api/internal/app"
	"user-management-api/internal/domain"
	"user-management-api/internal/platform/logger"
	"user-management-api/internal/testsupport"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// harness builds the real router on top of the real service, backed by a
// fake repository.
//
// That lets one test check the behaviour from HTTP all the way down to the
// business rules without MongoDB. Stubbing at the service level instead
// would let these tests pass even with the permission rules broken.
type harness struct {
	router *gin.Engine
	repo   *testsupport.FakeUserRepository
	tokens *testsupport.FakeTokenService
	clock  *testsupport.FakeClock
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	repo := testsupport.NewFakeUserRepository()
	hasher := testsupport.NewFakeHasher()
	tokens := testsupport.NewFakeTokenService()
	clock := testsupport.NewFakeClock(testsupport.FixedTime())

	svc, err := app.NewUserService(app.Deps{Repo: repo, Hasher: hasher, Tokens: tokens, Clock: clock})
	if err != nil {
		t.Fatalf("could not build the service: %v", err)
	}

	router := httpapi.NewRouter(httpapi.RouterConfig{
		Users:        svc,
		Tokens:       tokens,
		Logger:       logger.Discard(),
		MaxBodyBytes: 1 << 20,
		Version:      "test",
	})

	return &harness{router: router, repo: repo, tokens: tokens, clock: clock}
}

// do sends a request through the router and returns the recorder.
func (h *harness) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			reader = strings.NewReader(v)
		default:
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("could not encode the body: %v", err)
			}
			reader = bytes.NewReader(raw)
		}
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response must be parsable JSON: %v (got %q)", err, rec.Body.String())
	}
	return out
}

type userBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Field string `json:"field"`
			Issue string `json:"issue"`
		} `json:"details"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) errorBody {
	t.Helper()
	body := decodeJSON[errorBody](t, rec)
	if body.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q (body: %s)", body.Error.Code, wantCode, rec.Body.String())
	}
	if body.Error.Message == "" {
		t.Error("error.message must not be empty")
	}
	if body.RequestID == "" {
		t.Error("every error must carry a request_id so the log can be found")
	}
	return body
}

// assertNoSecretLeak is the check that no password or hash rides along with
// a response.
func assertNoSecretLeak(t *testing.T, rec *httptest.ResponseRecorder, password string) {
	t.Helper()
	raw := rec.Body.String()
	for _, needle := range []string{password, "password", "hash", testsupport.FakeHashPrefix} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(needle)) {
			t.Errorf("the response must not contain %q: %s", needle, raw)
		}
	}
}

// --- POST /auth/register ---

func TestRegisterEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"name":     "Somchai Jaidee",
		"email":    "Somchai@Example.com",
		"password": "secret-password",
	})

	assertStatus(t, rec, http.StatusCreated)
	body := decodeJSON[userBody](t, rec)

	if body.ID == "" {
		t.Error("an id must come back")
	}
	if body.Email != "somchai@example.com" {
		t.Errorf("email = %q, it must be lower-cased", body.Email)
	}
	if body.Role != "user" {
		t.Errorf("role = %q, want user", body.Role)
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/users/"+body.ID {
		t.Errorf("Location = %q", got)
	}
	assertNoSecretLeak(t, rec, "secret-password")
}

// TestRegisterIgnoresRoleInBody confirms sending a role has no effect.
func TestRegisterIgnoresRoleInBody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"name":     "Attacker",
		"email":    "attacker@example.com",
		"password": "secret-password",
		"role":     "admin",
	})

	assertStatus(t, rec, http.StatusCreated)
	if body := decodeJSON[userBody](t, rec); body.Role != "user" {
		t.Errorf("role = %q — the body must not be able to grant admin", body.Role)
	}
}

func TestRegisterValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      any
		wantCode  string
		wantField string
	}{
		{
			name:      "no email",
			body:      map[string]string{"name": "Alice", "password": "secret-password"},
			wantCode:  httpapi.CodeValidation,
			wantField: "email",
		},
		{
			name:      "malformed email",
			body:      map[string]string{"name": "Alice", "email": "nope", "password": "secret-password"},
			wantCode:  httpapi.CodeValidation,
			wantField: "email",
		},
		{
			name:      "password too short",
			body:      map[string]string{"name": "Alice", "email": "a@example.com", "password": "short"},
			wantCode:  httpapi.CodeValidation,
			wantField: "password",
		},
		{
			name:      "name too short",
			body:      map[string]string{"name": "A", "email": "a@example.com", "password": "secret-password"},
			wantCode:  httpapi.CodeValidation,
			wantField: "name",
		},
		{
			name:      "wrong field type",
			body:      `{"name": 123, "email": "a@example.com", "password": "secret-password"}`,
			wantCode:  httpapi.CodeValidation,
			wantField: "name",
		},
		{
			name:     "broken JSON",
			body:     `{"name": "Alice",`,
			wantCode: httpapi.CodeBadRequest,
		},
		{
			name:     "empty body",
			body:     `{}`,
			wantCode: httpapi.CodeValidation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			rec := h.do(t, http.MethodPost, "/api/v1/auth/register", "", tc.body)

			assertStatus(t, rec, http.StatusBadRequest)
			body := assertErrorCode(t, rec, tc.wantCode)

			if tc.wantField != "" {
				found := false
				for _, d := range body.Error.Details {
					if d.Field == tc.wantField {
						found = true
						if d.Issue == "" {
							t.Error("details.issue must explain what is wrong")
						}
					}
				}
				if !found {
					t.Errorf("details must point at field %q, got %+v", tc.wantField, body.Error.Details)
				}
			}
			if h.repo.Len() != 0 {
				t.Error("a request that fails validation must not create a user")
			}
		})
	}
}

func TestRegisterDuplicateEmailEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	body := map[string]string{"name": "Alice", "email": "alice@example.com", "password": "secret-password"}

	assertStatus(t, h.do(t, http.MethodPost, "/api/v1/auth/register", "", body), http.StatusCreated)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/register", "", body)
	assertStatus(t, rec, http.StatusConflict)
	assertErrorCode(t, rec, httpapi.CodeEmailTaken)
}

// --- POST /auth/login ---

func TestLoginEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seeded := testsupport.SeedUser(t, h.repo,
		testsupport.WithEmail("alice@example.com"),
		testsupport.WithPassword("secret-password"),
	)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email":    "Alice@Example.com",
		"password": "secret-password",
	})
	assertStatus(t, rec, http.StatusOK)

	var body struct {
		AccessToken string   `json:"access_token"`
		TokenType   string   `json:"token_type"`
		ExpiresIn   int64    `json:"expires_in"`
		User        userBody `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if body.AccessToken == "" {
		t.Error("an access_token must come back")
	}
	if body.TokenType != "Bearer" {
		t.Errorf("token_type = %q", body.TokenType)
	}
	if body.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d", body.ExpiresIn)
	}
	if body.User.ID != seeded.ID {
		t.Errorf("user.id = %q, want %q", body.User.ID, seeded.ID)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q — a response carrying a token must not be cached", got)
	}
	assertNoSecretLeak(t, rec, "secret-password")
}

// TestLoginFailuresAreIndistinguishable is a security test: whether the
// address does not exist or the password is wrong, the answers must be alike.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	testsupport.SeedUser(t, h.repo,
		testsupport.WithEmail("known@example.com"),
		testsupport.WithPassword("the-right-password"),
	)

	unknown := h.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email":    "nobody@example.com",
		"password": "some-password",
	})
	wrongPass := h.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email":    "known@example.com",
		"password": "the-wrong-password",
	})

	assertStatus(t, unknown, http.StatusUnauthorized)
	assertStatus(t, wrongPass, http.StatusUnauthorized)

	normalize := func(rec *httptest.ResponseRecorder) string {
		b := decodeJSON[errorBody](t, rec)
		return b.Error.Code + "|" + b.Error.Message
	}
	if normalize(unknown) != normalize(wrongPass) {
		t.Errorf("the answers must be identical in both cases:\n  %s\n  %s", unknown.Body, wrongPass.Body)
	}
}

func TestLoginValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "not-an-email"})
	assertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, httpapi.CodeValidation)
}

// --- authentication middleware ---

func TestProtectedEndpointsRequireToken(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo)

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodGet, "/api/v1/users/" + target.ID},
		{http.MethodPatch, "/api/v1/users/" + target.ID},
		{http.MethodDelete, "/api/v1/users/" + target.ID},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			t.Parallel()

			rec := h.do(t, ep.method, ep.path, "", nil)
			assertStatus(t, rec, http.StatusUnauthorized)
			assertErrorCode(t, rec, httpapi.CodeUnauthenticated)

			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, it must say Bearer", got)
			}
		})
	}
}

func TestAuthorizationHeaderVariants(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo)
	valid := testsupport.TokenFor(target.ID, domain.RoleUser)

	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "valid", header: "Bearer " + valid, wantStatus: http.StatusOK},
		{name: "lower-case bearer is fine per the RFC", header: "bearer " + valid, wantStatus: http.StatusOK},
		{name: "no header", header: "", wantStatus: http.StatusUnauthorized},
		{name: "no Bearer keyword", header: valid, wantStatus: http.StatusUnauthorized},
		{name: "Bearer with no token", header: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "another scheme", header: "Basic dXNlcjpwYXNz", wantStatus: http.StatusUnauthorized},
		{name: "bogus token", header: "Bearer not-a-real-token", wantStatus: http.StatusUnauthorized},
		{name: "the word Bearer alone", header: "Bearer", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body)
			}
		})
	}
}

// --- RBAC ---

func TestAdminOnlyEndpoints(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo, testsupport.WithEmail("target@example.com"))
	userToken := testsupport.TokenFor("some-user-id", domain.RoleUser)
	adminToken := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{name: "list users", method: http.MethodGet, path: "/api/v1/users"},
		{
			name:   "create user",
			method: http.MethodPost,
			path:   "/api/v1/users",
			body:   map[string]string{"name": "New", "email": "new@example.com", "password": "secret-password"},
		},
		{name: "delete user", method: http.MethodDelete, path: "/api/v1/users/" + target.ID},
	}

	for _, tc := range tests {
		t.Run(tc.name+" with role user must give 403", func(t *testing.T) {
			rec := h.do(t, tc.method, tc.path, userToken, tc.body)
			assertStatus(t, rec, http.StatusForbidden)
			assertErrorCode(t, rec, httpapi.CodeForbidden)
		})
	}

	// An admin must be able to do all of it.
	assertStatus(t, h.do(t, http.MethodGet, "/api/v1/users", adminToken, nil), http.StatusOK)
	assertStatus(t, h.do(t, http.MethodPost, "/api/v1/users", adminToken,
		map[string]string{"name": "New", "email": "new@example.com", "password": "secret-password"}),
		http.StatusCreated)
	assertStatus(t, h.do(t, http.MethodDelete, "/api/v1/users/"+target.ID, adminToken, nil),
		http.StatusNoContent)
}

// --- GET /users/me ---

func TestGetMe(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	me := testsupport.SeedUser(t, h.repo, testsupport.WithEmail("me@example.com"))

	rec := h.do(t, http.MethodGet, "/api/v1/users/me", testsupport.TokenFor(me.ID, domain.RoleUser), nil)
	assertStatus(t, rec, http.StatusOK)

	body := decodeJSON[userBody](t, rec)
	if body.ID != me.ID || body.Email != "me@example.com" {
		t.Errorf("got %+v", body)
	}
}

// TestMeIsNotTreatedAsID confirms /users/me is not matched as /users/:id.
func TestMeIsNotTreatedAsID(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	me := testsupport.SeedUser(t, h.repo)

	rec := h.do(t, http.MethodGet, "/api/v1/users/me", testsupport.TokenFor(me.ID, domain.RoleUser), nil)
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusForbidden {
		t.Errorf(`"me" was matched as an id (status %d)`, rec.Code)
	}
}

// --- GET /users/:id ---

func TestGetUserByID(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo, testsupport.WithEmail("target@example.com"))

	tests := []struct {
		name   string
		token  string
		status int
	}{
		{name: "the user itself", token: testsupport.TokenFor(target.ID, domain.RoleUser), status: http.StatusOK},
		{name: "admin", token: testsupport.TokenFor("admin-id", domain.RoleAdmin), status: http.StatusOK},
		{name: "someone else", token: testsupport.TokenFor("other-id", domain.RoleUser), status: http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := h.do(t, http.MethodGet, "/api/v1/users/"+target.ID, tc.token, nil)
			assertStatus(t, rec, tc.status)
			if tc.status == http.StatusOK {
				assertNoSecretLeak(t, rec, "password123")
			}
		})
	}
}

func TestGetUserNotFound(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	for _, id := range []string{"653f1a2b3c4d5e6f70818283", "not-an-id", "000000000000000000000000"} {
		t.Run(id, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/api/v1/users/"+id, admin, nil)
			assertStatus(t, rec, http.StatusNotFound)
			assertErrorCode(t, rec, httpapi.CodeNotFound)
		})
	}
}

// --- GET /users ---

func TestListUsers(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for i := 0; i < 5; i++ {
		testsupport.SeedUser(t, h.repo, testsupport.WithEmail(fmt.Sprintf("u%d@example.com", i)))
	}
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	rec := h.do(t, http.MethodGet, "/api/v1/users", admin, nil)
	assertStatus(t, rec, http.StatusOK)

	var body struct {
		Data []userBody `json:"data"`
		Meta struct {
			Count      int     `json:"count"`
			Limit      int     `json:"limit"`
			NextCursor *string `json:"next_cursor"`
			Total      int64   `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if len(body.Data) != 5 {
		t.Errorf("data has %d entries, want 5", len(body.Data))
	}
	if body.Meta.Total != 5 || body.Meta.Count != 5 {
		t.Errorf("meta = %+v", body.Meta)
	}
	if body.Meta.NextCursor != nil {
		t.Errorf("with nothing left next_cursor must be null, got %v", *body.Meta.NextCursor)
	}
	if body.Meta.Limit != domain.DefaultListLimit {
		t.Errorf("limit = %d, want the default of %d", body.Meta.Limit, domain.DefaultListLimit)
	}
	assertNoSecretLeak(t, rec, "password123")
}

func TestListUsersPagination(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for i := 0; i < 5; i++ {
		testsupport.SeedUser(t, h.repo, testsupport.WithEmail(fmt.Sprintf("u%d@example.com", i)))
	}
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	rec := h.do(t, http.MethodGet, "/api/v1/users?limit=2", admin, nil)
	assertStatus(t, rec, http.StatusOK)

	var page struct {
		Data []userBody `json:"data"`
		Meta struct {
			NextCursor *string `json:"next_cursor"`
			Limit      int     `json:"limit"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("the first page must hold 2 entries, got %d", len(page.Data))
	}
	if page.Meta.NextCursor == nil {
		t.Fatal("with more data to come next_cursor must not be null")
	}
	if page.Meta.Limit != 2 {
		t.Errorf("limit = %d", page.Meta.Limit)
	}

	// Use the cursor for the next page; nothing may repeat.
	next := h.do(t, http.MethodGet, "/api/v1/users?limit=2&cursor="+*page.Meta.NextCursor, admin, nil)
	assertStatus(t, next, http.StatusOK)

	var page2 struct {
		Data []userBody `json:"data"`
	}
	if err := json.Unmarshal(next.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	for _, a := range page.Data {
		for _, b := range page2.Data {
			if a.ID == b.ID {
				t.Errorf("user %s appears on two pages", a.ID)
			}
		}
	}
}

func TestListUsersBadQuery(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	tests := []string{
		"/api/v1/users?limit=0",
		"/api/v1/users?limit=-1",
		"/api/v1/users?limit=101",
		"/api/v1/users?limit=abc",
		"/api/v1/users?cursor=too-short",
		"/api/v1/users?cursor=zzzzzzzzzzzzzzzzzzzzzzzz",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := h.do(t, http.MethodGet, path, admin, nil)
			assertStatus(t, rec, http.StatusBadRequest)
		})
	}
}

// --- POST /users ---

func TestCreateUserByAdmin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	rec := h.do(t, http.MethodPost, "/api/v1/users", admin, map[string]string{
		"name":     "New Admin",
		"email":    "new.admin@example.com",
		"password": "secret-password",
		"role":     "admin",
	})
	assertStatus(t, rec, http.StatusCreated)

	if body := decodeJSON[userBody](t, rec); body.Role != "admin" {
		t.Errorf("role = %q, want admin", body.Role)
	}
	assertNoSecretLeak(t, rec, "secret-password")
}

func TestCreateUserInvalidRole(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	rec := h.do(t, http.MethodPost, "/api/v1/users", admin, map[string]string{
		"name":     "X",
		"email":    "x@example.com",
		"password": "secret-password",
		"role":     "superuser",
	})
	assertStatus(t, rec, http.StatusBadRequest)
	body := assertErrorCode(t, rec, httpapi.CodeValidation)

	found := false
	for _, d := range body.Error.Details {
		if d.Field == "role" {
			found = true
			if !strings.Contains(d.Issue, "admin") {
				t.Errorf("it should list the accepted values: %q", d.Issue)
			}
		}
	}
	if !found {
		t.Errorf("it must point at the role field: %+v", body.Error.Details)
	}
}

// --- PATCH /users/:id ---

func TestUpdateUserEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo,
		testsupport.WithName("Old Name"),
		testsupport.WithEmail("old@example.com"),
	)
	ownToken := testsupport.TokenFor(target.ID, domain.RoleUser)

	t.Run("name only", func(t *testing.T) {
		rec := h.do(t, http.MethodPatch, "/api/v1/users/"+target.ID, ownToken,
			map[string]string{"name": "New Name"})
		assertStatus(t, rec, http.StatusOK)

		body := decodeJSON[userBody](t, rec)
		if body.Name != "New Name" {
			t.Errorf("name = %q", body.Name)
		}
		if body.Email != "old@example.com" {
			t.Errorf("the email was not being changed and must stay, got %q", body.Email)
		}
	})

	t.Run("email only", func(t *testing.T) {
		rec := h.do(t, http.MethodPatch, "/api/v1/users/"+target.ID, ownToken,
			map[string]string{"email": "NEW@Example.com"})
		assertStatus(t, rec, http.StatusOK)

		if body := decodeJSON[userBody](t, rec); body.Email != "new@example.com" {
			t.Errorf("email = %q", body.Email)
		}
	})
}

func TestUpdateUserErrors(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo, testsupport.WithEmail("target@example.com"))
	testsupport.SeedUser(t, h.repo, testsupport.WithEmail("taken@example.com"))
	own := testsupport.TokenFor(target.ID, domain.RoleUser)

	tests := []struct {
		name     string
		token    string
		body     any
		status   int
		wantCode string
	}{
		{
			name:     "no fields at all",
			token:    own,
			body:     map[string]string{},
			status:   http.StatusBadRequest,
			wantCode: httpapi.CodeNoChanges,
		},
		{
			name:     "malformed email",
			token:    own,
			body:     map[string]string{"email": "nope"},
			status:   http.StatusBadRequest,
			wantCode: httpapi.CodeValidation,
		},
		{
			name:     "name too short",
			token:    own,
			body:     map[string]string{"name": "A"},
			status:   http.StatusBadRequest,
			wantCode: httpapi.CodeValidation,
		},
		{
			name:     "email already used by someone else",
			token:    own,
			body:     map[string]string{"email": "taken@example.com"},
			status:   http.StatusConflict,
			wantCode: httpapi.CodeEmailTaken,
		},
		{
			name:     "editing someone else",
			token:    testsupport.TokenFor("attacker", domain.RoleUser),
			body:     map[string]string{"name": "Hacked"},
			status:   http.StatusForbidden,
			wantCode: httpapi.CodeForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodPatch, "/api/v1/users/"+target.ID, tc.token, tc.body)
			assertStatus(t, rec, tc.status)
			assertErrorCode(t, rec, tc.wantCode)
		})
	}
}

// --- DELETE /users/:id ---

func TestDeleteUserEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo)
	admin := testsupport.TokenFor("admin-id", domain.RoleAdmin)

	rec := h.do(t, http.MethodDelete, "/api/v1/users/"+target.ID, admin, nil)
	assertStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("a 204 must carry no body, got %q", rec.Body.String())
	}

	// Once deleted it must not be found.
	assertStatus(t, h.do(t, http.MethodGet, "/api/v1/users/"+target.ID, admin, nil), http.StatusNotFound)
	// Deleting again must give 404.
	assertStatus(t, h.do(t, http.MethodDelete, "/api/v1/users/"+target.ID, admin, nil), http.StatusNotFound)
}

func TestDeleteSelfByAdminIsBlocked(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	admin := testsupport.SeedUser(t, h.repo, testsupport.WithRole(domain.RoleAdmin))

	rec := h.do(t, http.MethodDelete, "/api/v1/users/"+admin.ID,
		testsupport.TokenFor(admin.ID, domain.RoleAdmin), nil)
	assertStatus(t, rec, http.StatusForbidden)
	if h.repo.Len() != 1 {
		t.Error("the account must still be there")
	}
}

// --- health ---

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := h.do(t, http.MethodGet, path, "", nil)
			assertStatus(t, rec, http.StatusOK)

			var body struct {
				Status  string `json:"status"`
				Version string `json:"version"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != "ok" {
				t.Errorf("status = %q", body.Status)
			}
			if body.Version != "test" {
				t.Errorf("version = %q", body.Version)
			}
		})
	}
}

func TestReadyzReportsDatabaseDown(t *testing.T) {
	t.Parallel()

	repo := testsupport.NewFakeUserRepository()
	svc, err := app.NewUserService(app.Deps{
		Repo:   repo,
		Hasher: testsupport.NewFakeHasher(),
		Tokens: testsupport.NewFakeTokenService(),
	})
	if err != nil {
		t.Fatal(err)
	}

	router := httpapi.NewRouter(httpapi.RouterConfig{
		Users:  svc,
		Tokens: testsupport.NewFakeTokenService(),
		Pinger: failingPinger{},
		Logger: logger.Discard(),
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the database is unreachable", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"mongo":"down"`) {
		t.Errorf("body = %s", rec.Body)
	}
}

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("no connection") }

// --- routing odds and ends ---

func TestUnknownRouteReturnsJSONEnvelope(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/does-not-exist", "", nil)

	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, httpapi.CodeNotFound)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, it must be JSON rather than gin's HTML", ct)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	rec := h.do(t, http.MethodPut, "/api/v1/auth/login", "", nil)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
	assertErrorCode(t, rec, httpapi.CodeMethodNotAllowed)
}

// TestBodyTooLarge confirms an enormous body is refused before it eats memory.
func TestBodyTooLarge(t *testing.T) {
	t.Parallel()

	repo := testsupport.NewFakeUserRepository()
	svc, err := app.NewUserService(app.Deps{
		Repo:   repo,
		Hasher: testsupport.NewFakeHasher(),
		Tokens: testsupport.NewFakeTokenService(),
	})
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter(httpapi.RouterConfig{
		Users:        svc,
		Tokens:       testsupport.NewFakeTokenService(),
		Logger:       logger.Discard(),
		MaxBodyBytes: 256, // low, to keep the test quick
	})

	huge := `{"name":"` + strings.Repeat("a", 2000) + `","email":"a@example.com","password":"secret-password"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 (body: %s)", rec.Code, rec.Body)
	}
}

// TestInternalErrorIsOpaque confirms that when the database breaks, the
// caller gets a 500 with no internal detail leaking out.
func TestInternalErrorIsOpaque(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.repo.FailOn(testsupport.MethodCreate,
		errors.New("mongo: topology closed, cluster at 10.0.3.14:27017 unreachable"))

	rec := h.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"name":     "Alice",
		"email":    "alice@example.com",
		"password": "secret-password",
	})

	assertStatus(t, rec, http.StatusInternalServerError)
	assertErrorCode(t, rec, httpapi.CodeInternal)

	for _, leak := range []string{"topology", "10.0.3.14", "27017", "mongo"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(leak)) {
			t.Errorf("the response exposes internal detail (%q): %s", leak, rec.Body)
		}
	}
}
