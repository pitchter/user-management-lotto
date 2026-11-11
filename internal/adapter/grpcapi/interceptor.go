package grpcapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"user-management-api/internal/domain"
	"user-management-api/internal/platform/idgen"
	"user-management-api/internal/platform/logger"
)

// MetadataAuthorization is the metadata key carrying the token.
//
// gRPC requires metadata keys to be entirely lower-case.
const MetadataAuthorization = "authorization"

// MetadataRequestID is the metadata key for the request correlation id.
const MetadataRequestID = "x-request-id"

const bearerPrefix = "bearer "

type identityKey struct{}

// UnaryAuthInterceptor verifies the token from the metadata and stores the
// identity in the context.
//
// Every RPC on this service needs a token; there is no exemption list, so a
// new method is protected the moment it is registered rather than when
// somebody remembers to add it.
//
// It is the counterpart of the HTTP Authenticate middleware and uses the very
// same domain.TokenService, so one token works for both REST and gRPC.
func UnaryAuthInterceptor(tokens domain.TokenService) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		raw, err := bearerFromMetadata(ctx)
		if err != nil {
			return nil, err
		}

		identity, err := tokens.Verify(raw)
		if err != nil {
			// Same split as over HTTP: the reason is logged, the caller is
			// only told the token cannot be used.
			logger.FromContext(ctx).WarnContext(ctx, "token_rejected",
				slog.String(logger.KeyError, err.Error()),
				slog.String(logger.KeyMethod, info.FullMethod),
			)
			return nil, status.Error(codes.Unauthenticated, "the access token is invalid or has expired")
		}

		return handler(context.WithValue(ctx, identityKey{}, identity), req)
	}
}

// UnaryLoggingInterceptor writes one line per RPC, including the time taken.
//
// It records the same information as the HTTP Logging middleware (method,
// duration, outcome), so both protocols can be diagnosed the same way.
func UnaryLoggingInterceptor(l *slog.Logger) grpc.UnaryServerInterceptor {
	if l == nil {
		l = logger.Discard()
	}

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()

		requestID := requestIDFromMetadata(ctx)
		reqLogger := l.With(
			slog.String(logger.KeyRequestID, requestID),
			slog.String(logger.KeyComponent, "grpc"),
		)
		ctx = logger.IntoContext(ctx, reqLogger)

		resp, err := handler(ctx, req)

		attrs := []any{
			slog.String(logger.KeyMethod, info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Float64(logger.KeyDuration, float64(time.Since(start).Microseconds())/1000.0),
		}
		if identity, idErr := IdentityFrom(ctx); idErr == nil {
			attrs = append(attrs,
				slog.String(logger.KeyUserID, identity.UserID),
				slog.String(logger.KeyRole, identity.Role.String()),
			)
		}

		switch {
		case status.Code(err) == codes.Internal || status.Code(err) == codes.Unknown:
			// The real detail stays in the server-side log only.
			attrs = append(attrs, slog.String(logger.KeyError, errText(err)))
			reqLogger.ErrorContext(ctx, "grpc_request", attrs...)
		case err != nil:
			reqLogger.WarnContext(ctx, "grpc_request", attrs...)
		default:
			reqLogger.InfoContext(ctx, "grpc_request", attrs...)
		}

		return resp, err
	}
}

// UnaryRecoveryInterceptor stops a panic in a handler from killing the process.
func UnaryRecoveryInterceptor(l *slog.Logger) grpc.UnaryServerInterceptor {
	if l == nil {
		l = logger.Discard()
	}

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				l.ErrorContext(ctx, "grpc_panic_recovered",
					slog.String(logger.KeyMethod, info.FullMethod),
					slog.Any("panic", rec),
				)
				err = status.Error(codes.Internal, "an internal error occurred")
			}
		}()

		return handler(ctx, req)
	}
}

// IdentityFrom returns the caller identity the interceptor stored.
//
// If it is missing, UnaryAuthInterceptor was not installed, so this returns
// Unauthenticated instead of letting an empty identity through (fail closed).
func IdentityFrom(ctx context.Context) (domain.Identity, error) {
	identity, ok := ctx.Value(identityKey{}).(domain.Identity)
	if !ok {
		return domain.Identity{}, status.Error(codes.Unauthenticated, "authentication is required")
	}
	return identity, nil
}

// bearerFromMetadata extracts the token from the request metadata.
func bearerFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "the request carries no metadata")
	}

	values := md.Get(MetadataAuthorization)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", status.Error(codes.Unauthenticated,
			`metadata "authorization: Bearer <token>" is required`)
	}

	header := values[0]
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", status.Error(codes.Unauthenticated,
			`the authorization metadata must look like "Bearer <token>"`)
	}

	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", status.Error(codes.Unauthenticated, "no token found in the metadata")
	}
	return token, nil
}

// requestIDFromMetadata reuses the client's request id, or generates one.
func requestIDFromMetadata(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(MetadataRequestID); len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return idgen.New()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
