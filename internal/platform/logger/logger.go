// Package logger configures the application's structured logger on top of log/slog.
//
// slog is used instead of an external logger (zap, logrus) because it has been
// in the standard library since Go 1.21: one less dependency, and the output is
// still structured for log aggregation tools to query.
package logger

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// Field names reused across the application, so log queries stay consistent.
const (
	KeyRequestID = "request_id"
	KeyUserID    = "user_id"
	KeyRole      = "role"
	KeyMethod    = "method"
	KeyPath      = "path"
	KeyStatus    = "status"
	KeyDuration  = "duration_ms"
	KeyError     = "error"
	KeyComponent = "component"
)

// redactedKeys are the field names that must never reach the log output.
//
// This guards against accidentally logging a struct that carries one of them
// inside: the value is replaced with "[REDACTED]" in the handler, which is the
// last place before anything is written out.
var redactedKeys = map[string]struct{}{
	"password":       {},
	"password_hash":  {},
	"passwordhash":   {},
	"authorization":  {},
	"token":          {},
	"access_token":   {},
	"jwt":            {},
	"secret":         {},
	"jwt_secret":     {},
	"refresh_token":  {},
	"set-cookie":     {},
	"cookie":         {},
	"client_secret":  {},
	"api_key":        {},
	"admin_password": {},
}

// Options configures the logger.
type Options struct {
	// Level: debug | info | warn | error
	Level string
	// Format: json | text
	Format string
	// AddSource records the file and line that logged (costs time; for debugging).
	AddSource bool
}

// New builds a slog.Logger from the options, writing to w.
func New(w io.Writer, opts Options) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{
		Level:       parseLevel(opts.Level),
		AddSource:   opts.AddSource,
		ReplaceAttr: redact,
	}

	var h slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		h = slog.NewTextHandler(w, handlerOpts)
	} else {
		h = slog.NewJSONHandler(w, handlerOpts)
	}
	return slog.New(h)
}

// Discard returns a logger that throws everything away, for tests that do not care.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// parseLevel maps a level name to slog.Level; anything unknown means info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// redact replaces the value of a secret-bearing field with [REDACTED].
func redact(_ []string, a slog.Attr) slog.Attr {
	if _, found := redactedKeys[strings.ToLower(a.Key)]; found {
		return slog.String(a.Key, "[REDACTED]")
	}
	return a
}

type ctxKey struct{}

// IntoContext stores a logger in the context so that lower layers log with
// the fields of the current request already attached (request_id, user_id).
func IntoContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext pulls the logger out of a context, falling back to
// slog.Default(), which means callers never have to check for nil.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
