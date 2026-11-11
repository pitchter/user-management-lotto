package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pitchter/7-solution/internal/platform/logger"
)

func TestNewJSONFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "info", Format: "json"})
	l.Info("hello", slog.String("key", "value"))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("the output must be parsable JSON: %v (got %q)", err, buf.String())
	}
	if entry["msg"] != "hello" {
		t.Errorf("msg = %v", entry["msg"])
	}
	if entry["key"] != "value" {
		t.Errorf("key = %v", entry["key"])
	}
	if entry["level"] != "INFO" {
		t.Errorf("level = %v", entry["level"])
	}
}

func TestNewTextFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "info", Format: "text"})
	l.Info("hello")

	out := buf.String()
	if !strings.Contains(out, "msg=hello") {
		t.Errorf("the text handler must write msg=hello, got %q", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("format=text must not produce JSON: %q", out)
	}
}

func TestLevelFiltering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level     string
		wantDebug bool
		wantInfo  bool
		wantError bool
	}{
		{level: "debug", wantDebug: true, wantInfo: true, wantError: true},
		{level: "info", wantDebug: false, wantInfo: true, wantError: true},
		{level: "warn", wantDebug: false, wantInfo: false, wantError: true},
		{level: "error", wantDebug: false, wantInfo: false, wantError: true},
		{level: "not-a-level", wantDebug: false, wantInfo: true, wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			l := logger.New(&buf, logger.Options{Level: tc.level, Format: "json"})
			l.Debug("d")
			l.Info("i")
			l.Error("e")

			out := buf.String()
			if got := strings.Contains(out, `"msg":"d"`); got != tc.wantDebug {
				t.Errorf("debug emitted = %v, want %v", got, tc.wantDebug)
			}
			if got := strings.Contains(out, `"msg":"i"`); got != tc.wantInfo {
				t.Errorf("info emitted = %v, want %v", got, tc.wantInfo)
			}
			if got := strings.Contains(out, `"msg":"e"`); got != tc.wantError {
				t.Errorf("error emitted = %v, want %v", got, tc.wantError)
			}
		})
	}
}

// TestRedactSensitiveKeys is the last line of defence against secrets in logs.
func TestRedactSensitiveKeys(t *testing.T) {
	t.Parallel()

	secrets := map[string]string{
		"password":      "hunter2",
		"password_hash": "$2a$12$abcdefg",
		"authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
		"token":         "eyJhbGciOiJIUzI1NiJ9",
		"access_token":  "eyJhbGciOiJIUzI1NiJ9",
		"jwt_secret":    "super-secret",
		"api_key":       "sk-live-123",
		"cookie":        "session=abc",
	}

	for key, value := range secrets {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			l := logger.New(&buf, logger.Options{Level: "info", Format: "json"})
			l.Info("sensitive", slog.String(key, value))

			out := buf.String()
			if strings.Contains(out, value) {
				t.Errorf("the value of %q leaked into the log: %s", key, out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Errorf("it must be replaced with [REDACTED], got: %s", out)
			}
		})
	}
}

func TestRedactIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "info", Format: "json"})
	l.Info("x", slog.String("Authorization", "Bearer abc123"), slog.String("PASSWORD", "hunter2"))

	out := buf.String()
	if strings.Contains(out, "abc123") || strings.Contains(out, "hunter2") {
		t.Errorf("redaction must be case-insensitive: %s", out)
	}
}

func TestNonSensitiveKeysSurvive(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "info", Format: "json"})
	l.Info("ok",
		slog.String(logger.KeyUserID, "653f1a2b3c"),
		slog.String(logger.KeyMethod, "POST"),
		slog.String(logger.KeyPath, "/api/v1/users"),
	)

	out := buf.String()
	for _, want := range []string{"653f1a2b3c", "POST", "/api/v1/users"} {
		if !strings.Contains(out, want) {
			t.Errorf("ordinary fields must survive redaction: %q not found in %s", want, out)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "info", Format: "json"}).
		With(slog.String(logger.KeyRequestID, "req-123"))

	ctx := logger.IntoContext(context.Background(), l)
	logger.FromContext(ctx).Info("in request")

	if !strings.Contains(buf.String(), "req-123") {
		t.Errorf("a logger from the context must carry request_id: %s", buf.String())
	}
}

func TestFromContextFallsBackToDefault(t *testing.T) {
	t.Parallel()

	if got := logger.FromContext(context.Background()); got == nil {
		t.Fatal("a context without a logger must give slog.Default(), not nil")
	}
	// It must not panic even on a completely empty context.
	logger.FromContext(context.Background()).Info("must not panic")
}

func TestDiscard(t *testing.T) {
	t.Parallel()

	l := logger.Discard()
	if l == nil {
		t.Fatal("Discard must not return nil")
	}
	l.Error("this line must simply vanish without panicking")
}

func TestAddSource(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "info", Format: "json", AddSource: true})
	l.Info("with source")

	if !strings.Contains(buf.String(), "logger_test.go") {
		t.Errorf("AddSource=true must put the file name in the log: %s", buf.String())
	}
}
