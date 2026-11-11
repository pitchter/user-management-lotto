// Package grpcapi is the gRPC driving adapter.
//
// It calls the same use cases as REST (internal/app.UserService), which is
// the proof that the business rules are not tied to any transport: a new
// entry point needs no copy of the logic or of the permission rules.
package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	userv1 "github.com/pitchter/7-solution/internal/adapter/grpcapi/pb/user/v1"
	"github.com/pitchter/7-solution/internal/app"
	"github.com/pitchter/7-solution/internal/domain"
)

// UserService is what this adapter needs from the application layer.
type UserService interface {
	Create(ctx context.Context, actor domain.Identity, in app.CreateUserInput) (app.UserOutput, error)
	GetByID(ctx context.Context, actor domain.Identity, id string) (app.UserOutput, error)
}

// Server implements the service declared in user.proto.
type Server struct {
	userv1.UnimplementedUserServiceServer
	users UserService
}

// NewUserServiceHandler builds the RPC handler for UserService.
//
// (The ready-to-serve grpc.Server is built by NewServer in runtime.go.)
func NewUserServiceHandler(users UserService) *Server {
	return &Server{users: users}
}

// CreateUser creates a user (admin only).
func (s *Server) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.UserResponse, error) {
	identity, err := IdentityFrom(ctx)
	if err != nil {
		return nil, err
	}

	out, err := s.users.Create(ctx, identity, app.CreateUserInput{
		Name:     req.GetName(),
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
		Role:     req.GetRole(),
	})
	if err != nil {
		return nil, toStatusError(err)
	}
	return &userv1.UserResponse{User: toProto(out)}, nil
}

// GetUser reads a user (the user itself, or an admin).
func (s *Server) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.UserResponse, error) {
	identity, err := IdentityFrom(ctx)
	if err != nil {
		return nil, err
	}

	out, err := s.users.GetByID(ctx, identity, req.GetId())
	if err != nil {
		return nil, toStatusError(err)
	}
	return &userv1.UserResponse{User: toProto(out)}, nil
}

// toProto converts an application DTO into a protobuf message.
func toProto(u app.UserOutput) *userv1.User {
	return &userv1.User{
		Id:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		Role:      u.Role,
		CreatedAt: timestamppb.New(u.CreatedAt),
		UpdatedAt: timestamppb.New(u.UpdatedAt),
	}
}

// toStatusError maps domain errors onto gRPC status codes.
//
// It mirrors the HTTP mapping in httpapi/errors.go, so the same error means
// the same thing over both protocols while the domain knows about neither.
//
// An unrecognised error becomes codes.Internal with a generic message; no
// internal detail reaches the caller.
func toStatusError(err error) error {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		st := status.New(codes.InvalidArgument, ve.Error())
		return st.Err()
	}

	switch {
	case errors.Is(err, domain.ErrValidation), errors.Is(err, domain.ErrNoChanges):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrUnauthenticated), errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "a valid access token is required")
	case errors.Is(err, domain.ErrForbidden):
		return status.Error(codes.PermissionDenied, "you are not allowed to perform this action")
	case errors.Is(err, domain.ErrUserNotFound):
		return status.Error(codes.NotFound, "the requested user does not exist")
	case errors.Is(err, domain.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "that email is already registered")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the request was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the request took too long")
	default:
		return status.Error(codes.Internal, "an internal error occurred")
	}
}
