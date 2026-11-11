package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"user-management-api/internal/adapter/httpapi"
	"user-management-api/internal/domain"
	"user-management-api/internal/platform/logger"
	"user-management-api/internal/testsupport"
)

// logEntry is one log line decoded from JSON.
type logEntry map[string]any

// captureLogs runs the given engine and returns every log line it wrote.
func captureLogs(t *testing.T, build func(l *bytes.Buffer) *gin.Engine, req *http.Request) ([]logEntry, *httptest.ResponseRecorder) {
	t.Helper()

	var buf bytes.Buffer
	engine := build(&buf)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	var entries []logEntry
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("the log must be parsable JSON: %v (got %q)", err, line)
		}
		entries = append(entries, e)
	}
	return entries, rec
}

func findEntry(entries []logEntry, msg string) logEntry {
	for _, e := range entries {
		if e["msg"] == msg {
			return e
		}
	}
	return nil
}

// TestLoggingMiddlewareCapturesRequiredFields checks the requirement
// directly: the middleware must record the HTTP method, path and duration.
func TestLoggingMiddlewareCapturesRequiredFields(t *testing.T) {
	t.Parallel()

	entries, rec := captureLogs(t, func(buf *bytes.Buffer) *gin.Engine {
		l := logger.New(buf, logger.Options{Level: "info", Format: "json"})
		r := gin.New()
		r.Use(httpapi.RequestID(l), httpapi.Logging(l))
		r.GET("/things/:id", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
		return r
	}, httptest.NewRequest(http.MethodGet, "/things/42?q=x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	entry := findEntry(entries, "http_request")
	if entry == nil {
		t.Fatalf("an http_request log line must exist, got %+v", entries)
	}

	if entry[logger.KeyMethod] != http.MethodGet {
		t.Errorf("%s = %v, want GET", logger.KeyMethod, entry[logger.KeyMethod])
	}
	// The route pattern is logged, not the concrete path, so lines group.
	if entry[logger.KeyPath] != "/things/:id" {
		t.Errorf("%s = %v, want /things/:id", logger.KeyPath, entry[logger.KeyPath])
	}
	if got, ok := entry[logger.KeyStatus].(float64); !ok || int(got) != http.StatusOK {
		t.Errorf("%s = %v", logger.KeyStatus, entry[logger.KeyStatus])
	}
	dur, ok := entry[logger.KeyDuration].(float64)
	if !ok {
		t.Fatalf("%s must be a number, got %T", logger.KeyDuration, entry[logger.KeyDuration])
	}
	if dur < 0 {
		t.Errorf("%s = %v, it must not be negative", logger.KeyDuration, dur)
	}
	if entry[logger.KeyRequestID] == "" || entry[logger.KeyRequestID] == nil {
		t.Error("a request_id is needed to tie the log to the response")
	}
	if entry["query"] != "q=x" {
		t.Errorf("query = %v", entry["query"])
	}
	if _, found := entry["client_ip"]; !found {
		t.Error("client_ip must be recorded")
	}
}

// TestLoggingIncludesIdentityWhenAuthenticated confirms an authenticated
// request's log carries user_id, so usage can be traced.
func TestLoggingIncludesIdentityWhenAuthenticated(t *testing.T) {
	t.Parallel()

	tokens := testsupport.NewFakeTokenService()
	token := testsupport.TokenFor("user-77", domain.RoleAdmin)

	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	entries, rec := captureLogs(t, func(buf *bytes.Buffer) *gin.Engine {
		l := logger.New(buf, logger.Options{Level: "info", Format: "json"})
		r := gin.New()
		r.Use(httpapi.RequestID(l), httpapi.Logging(l))
		r.GET("/private", httpapi.Authenticate(tokens), func(c *gin.Context) { c.Status(http.StatusOK) })
		return r
	}, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}

	entry := findEntry(entries, "http_request")
	if entry == nil {
		t.Fatal("no http_request log line found")
	}
	if entry[logger.KeyUserID] != "user-77" {
		t.Errorf("user_id = %v", entry[logger.KeyUserID])
	}
	if entry[logger.KeyRole] != "admin" {
		t.Errorf("role = %v", entry[logger.KeyRole])
	}
}

// TestLoggingNeverLogsCredentials keeps passwords and tokens out of the logs.
func TestLoggingNeverLogsCredentials(t *testing.T) {
	t.Parallel()

	const (
		secretToken    = "super-secret-token-value"
		secretPassword = "my-very-secret-password"
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"email":"a@example.com","password":"`+secretPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secretToken)

	var buf bytes.Buffer
	l := logger.New(&buf, logger.Options{Level: "debug", Format: "json"})

	r := gin.New()
	r.Use(httpapi.Recovery(l), httpapi.RequestID(l), httpapi.Logging(l))
	r.POST("/api/v1/auth/login", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	out := buf.String()
	for _, secret := range []string{secretToken, secretPassword} {
		if strings.Contains(out, secret) {
			t.Errorf("the log must not contain %q: %s", secret, out)
		}
	}
}

func TestLoggingLevelFollowsStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantLevel string
	}{
		{name: "success", status: http.StatusOK, wantLevel: "INFO"},
		{name: "caller's mistake", status: http.StatusBadRequest, wantLevel: "WARN"},
		{name: "not permitted", status: http.StatusForbidden, wantLevel: "WARN"},
		{name: "our fault", status: http.StatusInternalServerError, wantLevel: "ERROR"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entries, _ := captureLogs(t, func(buf *bytes.Buffer) *gin.Engine {
				l := logger.New(buf, logger.Options{Level: "info", Format: "json"})
				r := gin.New()
				r.Use(httpapi.RequestID(l), httpapi.Logging(l))
				r.GET("/x", func(c *gin.Context) { c.Status(tc.status) })
				return r
			}, httptest.NewRequest(http.MethodGet, "/x", nil))

			entry := findEntry(entries, "http_request")
			if entry == nil {
				t.Fatal("no log line found")
			}
			if entry["level"] != tc.wantLevel {
				t.Errorf("level = %v, want %s", entry["level"], tc.wantLevel)
			}
		})
	}
}

func TestLoggingUnmatchedRoute(t *testing.T) {
	t.Parallel()

	entries, _ := captureLogs(t, func(buf *bytes.Buffer) *gin.Engine {
		l := logger.New(buf, logger.Options{Level: "info", Format: "json"})
		r := gin.New()
		r.Use(httpapi.RequestID(l), httpapi.Logging(l))
		return r
	}, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	entry := findEntry(entries, "http_request")
	if entry == nil {
		t.Fatal("a request matching no route must still be logged")
	}
	if entry[logger.KeyPath] != "unmatched" {
		t.Errorf("path = %v, want unmatched", entry[logger.KeyPath])
	}
}

// --- RequestID ---

func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.Use(httpapi.RequestID(logger.Discard()))
	r.GET("/x", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": httpapi.RequestIDFrom(c)})
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	header := rec.Header().Get(httpapi.HeaderRequestID)
	if header == "" {
		t.Fatal("X-Request-ID must always come back in the response")
	}

	var body struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != header {
		t.Errorf("the id in the context (%q) must match the header (%q)", body.ID, header)
	}
}

func TestRequestIDReusesClientValue(t *testing.T) {
	t.Parallel()

	const clientID = "trace-from-upstream-proxy-123"

	r := gin.New()
	r.Use(httpapi.RequestID(logger.Discard()))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httpapi.HeaderRequestID, clientID)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get(httpapi.HeaderRequestID); got != clientID {
		t.Errorf("the client's value must be reused so traces span services, got %q", got)
	}
}

// TestRequestIDRejectsUnsafeClientValue blocks log and header injection
// through this value, which lands in both the log and a response header.
func TestRequestIDRejectsUnsafeClientValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "contains a newline", value: "abc\nInjected: true"},
		{name: "contains a carriage return", value: "abc\r\nX-Evil: 1"},
		{name: "too long", value: strings.Repeat("a", 500)},
		{name: "contains a control character", value: "abc\x00def"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := gin.New()
			r.Use(httpapi.RequestID(logger.Discard()))
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header[httpapi.HeaderRequestID] = []string{tc.value}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			got := rec.Header().Get(httpapi.HeaderRequestID)
			if got == tc.value {
				t.Errorf("an unsafe value must not be reused, got %q", got)
			}
			if got == "" {
				t.Error("a fresh id must be generated instead")
			}
		})
	}
}

// --- Recovery ---

// TestRecoveryTurnsPanicIntoErrorEnvelope confirms a panic does not kill the
// process and that the caller gets a 500 in the usual error shape.
func TestRecoveryTurnsPanicIntoErrorEnvelope(t *testing.T) {
	t.Parallel()

	entries, rec := captureLogs(t, func(buf *bytes.Buffer) *gin.Engine {
		l := logger.New(buf, logger.Options{Level: "info", Format: "json"})
		r := gin.New()
		r.Use(httpapi.Recovery(l), httpapi.RequestID(l))
		r.GET("/boom", func(_ *gin.Context) { panic("something went very wrong") })
		return r
	}, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the response must be JSON in the same shape: %v (got %q)", err, rec.Body)
	}
	if body.Error.Code != httpapi.CodeInternal {
		t.Errorf("code = %q", body.Error.Code)
	}

	// The panic value and stack trace belong in the server log only.
	if strings.Contains(rec.Body.String(), "something went very wrong") {
		t.Error("the panic message must not escape with the response")
	}
	if strings.Contains(rec.Body.String(), "middleware_test.go") {
		t.Error("the stack trace must not escape with the response")
	}

	entry := findEntry(entries, "panic_recovered")
	if entry == nil {
		t.Fatalf("a panic_recovered log line must exist, got %+v", entries)
	}
	if !strings.Contains(entry["error"].(string), "something went very wrong") {
		t.Errorf("the log must keep the panic detail: %v", entry["error"])
	}
	if stack, ok := entry["stack"].(string); !ok || !strings.Contains(stack, "middleware_test.go") {
		t.Error("the log must carry a stack trace to find the cause")
	}
}

func TestRecoveryKeepsServerAliveForNextRequest(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.Use(httpapi.Recovery(logger.Discard()), httpapi.RequestID(logger.Discard()))
	r.GET("/boom", func(_ *gin.Context) { panic("boom") })
	r.GET("/fine", func(c *gin.Context) { c.Status(http.StatusOK) })

	first := httptest.NewRecorder()
	r.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", first.Code)
	}

	second := httptest.NewRecorder()
	r.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/fine", nil))
	if second.Code != http.StatusOK {
		t.Errorf("the next request must still work, got status %d", second.Code)
	}
}

// --- RequireAdmin when Authenticate was forgotten ---

// TestRequireAdminWithoutAuthenticate confirms that misordered middleware
// makes the system refuse the request rather than let an empty identity
// through (fail closed).
func TestRequireAdminWithoutAuthenticate(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.Use(httpapi.RequestID(logger.Discard()))
	r.GET("/admin", httpapi.RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — nothing may pass without an identity", rec.Code)
	}
}

func TestIdentityFromEmptyContext(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if _, found := httpapi.IdentityFrom(c); found {
		t.Error("a context without an identity must report found=false")
	}
	if got := httpapi.RequestIDFrom(c); got != "" {
		t.Errorf("RequestIDFrom = %q, want an empty string", got)
	}
}
