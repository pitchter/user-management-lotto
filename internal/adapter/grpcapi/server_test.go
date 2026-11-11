package grpcapi_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"user-management-api/internal/adapter/grpcapi"
	userv1 "user-management-api/internal/adapter/grpcapi/pb/user/v1"
	"user-management-api/internal/app"
	"user-management-api/internal/domain"
	"user-management-api/internal/platform/logger"
	"user-management-api/internal/testsupport"
)

// harness starts a real gRPC server on an in-memory listener (bufconn) and
// connects a client to it, which exercises the interceptors, the message
// conversion and the error mapping without opening a real port.
type harness struct {
	client userv1.UserServiceClient
	repo   *testsupport.FakeUserRepository
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	repo := testsupport.NewFakeUserRepository()
	tokens := testsupport.NewFakeTokenService()
	svc, err := app.NewUserService(app.Deps{
		Repo:   repo,
		Hasher: testsupport.NewFakeHasher(),
		Tokens: tokens,
		Clock:  testsupport.NewFakeClock(testsupport.FixedTime()),
	})
	if err != nil {
		t.Fatalf("could not build the service: %v", err)
	}

	server := grpcapi.NewServer(grpcapi.Config{
		Users:  svc,
		Tokens: tokens,
		Logger: logger.Discard(),
	})

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("could not connect the client: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
		_ = lis.Close()
	})

	return &harness{client: userv1.NewUserServiceClient(conn), repo: repo}
}

// ctxWithToken puts the token into the metadata, as a real client must.
func ctxWithToken(t *testing.T, token string) context.Context {
	t.Helper()
	if token == "" {
		return context.Background()
	}
	return metadata.AppendToOutgoingContext(context.Background(),
		grpcapi.MetadataAuthorization, "Bearer "+token)
}

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %s, want %s (err: %v)", got, want, err)
	}
}

// --- CreateUser ---

func TestCreateUserByAdmin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := ctxWithToken(t, testsupport.TokenFor("admin-1", domain.RoleAdmin))

	res, err := h.client.CreateUser(ctx, &userv1.CreateUserRequest{
		Name:     "Grpc User",
		Email:    "Grpc@Example.com",
		Password: "secret-password",
		Role:     "admin",
	})
	if err != nil {
		t.Fatalf("an admin must be able to create a user: %v", err)
	}

	u := res.GetUser()
	if u.GetId() == "" {
		t.Error("an id must come back")
	}
	if u.GetEmail() != "grpc@example.com" {
		t.Errorf("email = %q, it must be lower-cased", u.GetEmail())
	}
	if u.GetRole() != "admin" {
		t.Errorf("role = %q", u.GetRole())
	}
	if u.GetCreatedAt() == nil || !u.GetCreatedAt().AsTime().Equal(testsupport.FixedTime()) {
		t.Errorf("created_at = %v", u.GetCreatedAt())
	}

	// The protobuf message must give a password no way out.
	if strings.Contains(u.String(), "secret-password") {
		t.Errorf("the response carries the password: %s", u.String())
	}
}

func TestCreateUserRequiresAdmin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := ctxWithToken(t, testsupport.TokenFor("user-1", domain.RoleUser))

	_, err := h.client.CreateUser(ctx, &userv1.CreateUserRequest{
		Name:     "X",
		Email:    "x@example.com",
		Password: "secret-password",
	})
	assertCode(t, err, codes.PermissionDenied)

	if h.repo.Len() != 0 {
		t.Error("no user may have been created")
	}
}

func TestCreateUserValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := ctxWithToken(t, testsupport.TokenFor("admin-1", domain.RoleAdmin))

	tests := []struct {
		name string
		req  *userv1.CreateUserRequest
	}{
		{
			name: "no name",
			req:  &userv1.CreateUserRequest{Email: "a@example.com", Password: "secret-password"},
		},
		{
			name: "malformed email",
			req:  &userv1.CreateUserRequest{Name: "Alice", Email: "nope", Password: "secret-password"},
		},
		{
			name: "password too short",
			req:  &userv1.CreateUserRequest{Name: "Alice", Email: "a@example.com", Password: "x"},
		},
		{
			name: "unknown role",
			req: &userv1.CreateUserRequest{
				Name: "Alice", Email: "a@example.com", Password: "secret-password", Role: "root",
			},
		},
		{
			name: "completely empty request",
			req:  &userv1.CreateUserRequest{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := h.client.CreateUser(ctx, tc.req)
			assertCode(t, err, codes.InvalidArgument)
			if status.Convert(err).Message() == "" {
				t.Error("there should be a message saying what is wrong")
			}
		})
	}
}

func TestCreateUserDuplicateEmail(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := ctxWithToken(t, testsupport.TokenFor("admin-1", domain.RoleAdmin))
	req := &userv1.CreateUserRequest{
		Name: "Alice", Email: "alice@example.com", Password: "secret-password",
	}

	if _, err := h.client.CreateUser(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, err := h.client.CreateUser(ctx, req)
	assertCode(t, err, codes.AlreadyExists)
}

// --- GetUser ---

func TestGetUser(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo, testsupport.WithEmail("target@example.com"))

	tests := []struct {
		name  string
		token string
		want  codes.Code
	}{
		{name: "an admin may read it", token: testsupport.TokenFor("admin-1", domain.RoleAdmin), want: codes.OK},
		{name: "the user itself may read it", token: testsupport.TokenFor(target.ID, domain.RoleUser), want: codes.OK},
		{
			name:  "someone else may not",
			token: testsupport.TokenFor("other", domain.RoleUser),
			want:  codes.PermissionDenied,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := h.client.GetUser(ctxWithToken(t, tc.token), &userv1.GetUserRequest{Id: target.ID})
			if tc.want != codes.OK {
				assertCode(t, err, tc.want)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.GetUser().GetId() != target.ID {
				t.Errorf("id = %q", res.GetUser().GetId())
			}
			if res.GetUser().GetEmail() != "target@example.com" {
				t.Errorf("email = %q", res.GetUser().GetEmail())
			}
		})
	}
}

func TestGetUserNotFound(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := ctxWithToken(t, testsupport.TokenFor("admin-1", domain.RoleAdmin))

	for _, id := range []string{"653f1a2b3c4d5e6f70818283", "", "not-an-id"} {
		_, err := h.client.GetUser(ctx, &userv1.GetUserRequest{Id: id})
		assertCode(t, err, codes.NotFound)
	}
}

// --- auth interceptor ---

// TestAuthInterceptorRejectsBadMetadata confirms every RPC needs a usable token.
func TestAuthInterceptorRejectsBadMetadata(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	tests := []struct {
		name string
		md   []string
	}{
		{name: "no metadata at all", md: nil},
		{name: "empty value", md: []string{grpcapi.MetadataAuthorization, ""}},
		{name: "no Bearer keyword", md: []string{grpcapi.MetadataAuthorization, "abc.def.ghi"}},
		{name: "Bearer with no token", md: []string{grpcapi.MetadataAuthorization, "Bearer "}},
		{name: "bogus token", md: []string{grpcapi.MetadataAuthorization, "Bearer not-a-token"}},
		{name: "another scheme", md: []string{grpcapi.MetadataAuthorization, "Basic dXNlcjpwYXNz"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tc.md != nil {
				ctx = metadata.AppendToOutgoingContext(ctx, tc.md...)
			}

			_, err := h.client.GetUser(ctx, &userv1.GetUserRequest{Id: "653f1a2b3c4d5e6f70818283"})
			assertCode(t, err, codes.Unauthenticated)

			_, err = h.client.CreateUser(ctx, &userv1.CreateUserRequest{
				Name: "X", Email: "x@example.com", Password: "secret-password",
			})
			assertCode(t, err, codes.Unauthenticated)
		})
	}
}

// TestAuthInterceptorAcceptsLowercaseBearer: per RFC 6750 the word bearer is
// case-insensitive.
func TestAuthInterceptorAcceptsLowercaseBearer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	target := testsupport.SeedUser(t, h.repo)

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		grpcapi.MetadataAuthorization, "bearer "+testsupport.TokenFor(target.ID, domain.RoleUser))

	if _, err := h.client.GetUser(ctx, &userv1.GetUserRequest{Id: target.ID}); err != nil {
		t.Errorf("a lower-case bearer must be accepted: %v", err)
	}
}

// TestInternalErrorIsOpaque confirms database errors do not leak over gRPC.
func TestInternalErrorIsOpaque(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.repo.FailOn(testsupport.MethodGetByID,
		errors.New("mongo: topology closed, host 10.0.3.14:27017 unreachable"))

	ctx := ctxWithToken(t, testsupport.TokenFor("admin-1", domain.RoleAdmin))
	_, err := h.client.GetUser(ctx, &userv1.GetUserRequest{Id: "653f1a2b3c4d5e6f70818283"})

	assertCode(t, err, codes.Internal)
	msg := status.Convert(err).Message()
	for _, leak := range []string{"topology", "10.0.3.14", "27017", "mongo"} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(leak)) {
			t.Errorf("the message exposes internal detail (%q): %s", leak, msg)
		}
	}
}

// TestIdentityFromWithoutInterceptor confirms the handler refuses on its own
// when the context has no identity (fail closed on misassembled interceptors).
func TestIdentityFromWithoutInterceptor(t *testing.T) {
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

	handler := grpcapi.NewUserServiceHandler(svc)

	if _, err := handler.GetUser(context.Background(), &userv1.GetUserRequest{Id: "x"}); err == nil {
		t.Error("it must refuse when there is no identity")
	} else {
		assertCode(t, err, codes.Unauthenticated)
	}

	if _, err := handler.CreateUser(context.Background(), &userv1.CreateUserRequest{}); err == nil {
		t.Error("it must refuse when there is no identity")
	} else {
		assertCode(t, err, codes.Unauthenticated)
	}
}

// TestRecoveryInterceptor confirms a panic in a handler becomes Internal
// rather than taking the process down.
func TestRecoveryInterceptor(t *testing.T) {
	t.Parallel()

	server := grpcapi.NewServer(grpcapi.Config{
		Users:  panickingService{},
		Tokens: testsupport.NewFakeTokenService(),
		Logger: logger.Discard(),
	})

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() {
		server.Stop()
		_ = lis.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	client := userv1.NewUserServiceClient(conn)
	ctx := ctxWithToken(t, testsupport.TokenFor("admin-1", domain.RoleAdmin))

	_, err = client.GetUser(ctx, &userv1.GetUserRequest{Id: "any"})
	assertCode(t, err, codes.Internal)

	// The server must still accept the next request.
	_, err = client.GetUser(ctx, &userv1.GetUserRequest{Id: "any"})
	assertCode(t, err, codes.Internal)
}

// panickingService stands in for a bug that makes a handler panic.
type panickingService struct{}

func (panickingService) Create(context.Context, domain.Identity, app.CreateUserInput) (app.UserOutput, error) {
	panic("boom in create")
}

func (panickingService) GetByID(context.Context, domain.Identity, string) (app.UserOutput, error) {
	panic("boom in get")
}
