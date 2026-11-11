package grpcapi

import (
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	userv1 "github.com/pitchter/7-solution/internal/adapter/grpcapi/pb/user/v1"
	"github.com/pitchter/7-solution/internal/domain"
)

// Config holds the gRPC server settings.
type Config struct {
	Users  UserService
	Tokens domain.TokenService
	Logger *slog.Logger
	// EnableReflection lets tools such as grpcurl ask for the schema.
	//
	// Keep it to development only: reflection exposes every service and
	// message on the server to anyone who can reach it.
	EnableReflection bool
	// MaxRecvMsgBytes caps the accepted message size (0 = gRPC's own default).
	MaxRecvMsgBytes int
}

// NewServer assembles the grpc.Server with its interceptors and registers
// the service.
//
// The interceptors run outermost first: Recovery (catches panics from every
// layer), then Logging (times the whole call), then Auth (authenticates
// before the handler runs).
func NewServer(cfg Config) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			UnaryRecoveryInterceptor(cfg.Logger),
			UnaryLoggingInterceptor(cfg.Logger),
			UnaryAuthInterceptor(cfg.Tokens),
		),
		// Drop connections that went quiet, so they stop holding resources.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 5 * time.Minute,
			Time:              2 * time.Minute,
			Timeout:           20 * time.Second,
		}),
		// Refuse clients that ping too often, which is a cheap way to flood us.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             30 * time.Second,
			PermitWithoutStream: false,
		}),
	}
	if cfg.MaxRecvMsgBytes > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(cfg.MaxRecvMsgBytes))
	}

	srv := grpc.NewServer(opts...)
	userv1.RegisterUserServiceServer(srv, NewUserServiceHandler(cfg.Users))

	if cfg.EnableReflection {
		reflection.Register(srv)
	}
	return srv
}
