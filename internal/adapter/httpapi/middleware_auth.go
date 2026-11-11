package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"user-management-api/internal/domain"
	"user-management-api/internal/platform/logger"
)

// HeaderAuthorization is the header carrying the access token.
const HeaderAuthorization = "Authorization"

// bearerPrefix is the shape RFC 6750 defines for a bearer token.
const bearerPrefix = "Bearer "

// Authenticate verifies the JWT on the request and stores the caller's
// identity in the context.
//
// It only establishes who the caller is (authentication); deciding what they
// may do (authorization) is the job of RequireAdmin and of the application
// layer. Keeping the two apart makes both easier to follow and to test.
func Authenticate(tokens domain.TokenService) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := bearerToken(c.GetHeader(HeaderAuthorization))
		if err != nil {
			unauthorized(c, err.Error())
			return
		}

		identity, err := tokens.Verify(raw)
		if err != nil {
			// The reason (expired, bad signature, wrong audience) goes to the
			// log, where it is what makes a failing client diagnosable. The
			// caller is told only that the token is unusable, so probing the
			// endpoint reveals nothing about why.
			ctx := c.Request.Context()
			logger.FromContext(ctx).WarnContext(ctx, "token_rejected",
				slog.String(logger.KeyError, err.Error()),
				slog.String(logger.KeyPath, c.FullPath()),
			)
			unauthorized(c, "the access token is invalid or has expired")
			return
		}

		c.Set(ctxKeyIdentity, identity)
		c.Next()
	}
}

// RequireAdmin lets only administrators through.
//
// The application layer checks permissions again anyway. This is the front
// gate: it makes the route table in router.go show at a glance which
// endpoints are admin-only, and it turns unauthorised requests away before
// they touch the database (defence in depth).
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, found := IdentityFrom(c)
		if !found {
			// Reaching here without an identity means Authenticate is missing.
			unauthorized(c, "authentication is required")
			return
		}
		if !identity.IsAdmin() {
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{
				Error: errorBody{
					Code:    CodeForbidden,
					Message: "this endpoint is restricted to administrators",
				},
				RequestID: RequestIDFrom(c),
			})
			return
		}
		c.Next()
	}
}

// bearerToken extracts the token from an Authorization header value.
func bearerToken(header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", errMissingToken
	}
	// Compared case-insensitively per RFC 6750, so "bearer" is valid too.
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", errMalformedHeader
	}

	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", errMissingToken
	}
	return token, nil
}

func unauthorized(c *gin.Context, message string) {
	c.Header("WWW-Authenticate", `Bearer realm="user-management-api"`)
	c.AbortWithStatusJSON(http.StatusUnauthorized, errorResponse{
		Error: errorBody{
			Code:    CodeUnauthenticated,
			Message: message,
		},
		RequestID: RequestIDFrom(c),
	})
}
