package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/pitchter/7-solution/internal/domain"
	"github.com/pitchter/7-solution/internal/platform/idgen"
	"github.com/pitchter/7-solution/internal/platform/logger"
)

// Keys the middleware uses to stash values in the gin.Context.
const (
	ctxKeyRequestID = "request_id"
	ctxKeyIdentity  = "identity"
)

// HeaderRequestID is the header used to receive and return a request id.
const HeaderRequestID = "X-Request-ID"

// RequestID gives every request exactly one correlation id.
//
// If the client (or a reverse proxy) already sent X-Request-ID, that value is
// kept so a trace can be followed across services; otherwise a new one is
// generated. It always goes back in the response header, so a user reporting
// a problem can quote it and we can find the log line immediately.
func RequestID(l *slog.Logger) gin.HandlerFunc {
	const maxLen = 64

	if l == nil {
		l = logger.Discard()
	}

	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if id == "" || len(id) > maxLen || !isPrintableASCII(id) {
			// This value ends up in the logs and in a response header, so
			// control characters have to go (log and header injection).
			id = idgen.New()
		}

		c.Set(ctxKeyRequestID, id)
		c.Header(HeaderRequestID, id)

		// Put a logger carrying request_id into the context for lower layers.
		// It is built from the logger the application configured, not
		// slog.Default(), so the destination stays under our control in
		// both the app and the tests.
		reqLogger := l.With(slog.String(logger.KeyRequestID, id))
		c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), reqLogger))

		c.Next()
	}
}

// Logging writes one line per request once it has been served.
//
// The challenge requires the method, the path and the time taken; status,
// response size, request_id, user_id and the client IP are added because
// that is what actually makes a problem diagnosable.
//
// It logs after c.Next(), so it sees both the outcome and the total time of
// the whole handler chain.
func Logging(l *slog.Logger) gin.HandlerFunc {
	if l == nil {
		l = logger.Discard()
	}

	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// Use the registered route (/users/:id) rather than the concrete path,
		// so lines group into useful statistics and no user id ends up in the
		// logs unnecessarily.
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}

		attrs := []any{
			slog.String(logger.KeyMethod, c.Request.Method),
			slog.String(logger.KeyPath, route),
			slog.Int(logger.KeyStatus, c.Writer.Status()),
			// Fractional milliseconds: easy to read and to compare.
			slog.Float64(logger.KeyDuration, float64(time.Since(start).Microseconds())/1000.0),
			slog.Int("bytes", c.Writer.Size()),
			slog.String("client_ip", c.ClientIP()),
			slog.String(logger.KeyRequestID, RequestIDFrom(c)),
		}
		if id, ok := IdentityFrom(c); ok {
			attrs = append(attrs,
				slog.String(logger.KeyUserID, id.UserID),
				slog.String(logger.KeyRole, id.Role.String()),
			)
		}
		if raw := c.Request.URL.RawQuery; raw != "" {
			attrs = append(attrs, slog.String("query", raw))
		}

		msg := "http_request"
		switch {
		case c.Writer.Status() >= http.StatusInternalServerError:
			l.ErrorContext(c.Request.Context(), msg, attrs...)
		case c.Writer.Status() >= http.StatusBadRequest:
			l.WarnContext(c.Request.Context(), msg, attrs...)
		default:
			l.InfoContext(c.Request.Context(), msg, attrs...)
		}
	}
}

// Recovery stops a panic from taking the whole process down and answers 500
// in the same shape as every other API error.
//
// Neither the panic value nor the stack trace is returned to the caller (it
// would expose internals and file paths); both are logged in full server-side.
func Recovery(l *slog.Logger) gin.HandlerFunc {
	if l == nil {
		l = logger.Discard()
	}

	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				l.ErrorContext(c.Request.Context(), "panic_recovered",
					slog.String(logger.KeyError, fmt.Sprint(rec)),
					slog.String(logger.KeyMethod, c.Request.Method),
					slog.String(logger.KeyPath, c.FullPath()),
					slog.String(logger.KeyRequestID, RequestIDFrom(c)),
					slog.String("stack", stackTrace()),
				)

				c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{
					Error: errorBody{
						Code:    CodeInternal,
						Message: "an internal error occurred",
					},
					RequestID: RequestIDFrom(c),
				})
			}
		}()

		c.Next()
	}
}

// MaxBodySize caps the size of a request body.
//
// It stops a request from sending an enormous body to exhaust the server's
// memory. The cap has to act at the io.Reader level: by the time a JSON
// decode finishes, the memory is already gone.
func MaxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// RequestIDFrom returns this request's correlation id.
func RequestIDFrom(c *gin.Context) string {
	if v, found := c.Get(ctxKeyRequestID); found {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// IdentityFrom returns the caller identity the Authenticate middleware stored.
func IdentityFrom(c *gin.Context) (domain.Identity, bool) {
	v, found := c.Get(ctxKeyIdentity)
	if !found {
		return domain.Identity{}, false
	}
	id, ok := v.(domain.Identity)
	return id, ok
}

func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
