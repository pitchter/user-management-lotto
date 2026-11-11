package jwtauth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"user-management-api/internal/adapter/jwtauth"
	"user-management-api/internal/domain"
	"user-management-api/internal/testsupport"
)

const (
	testSecret   = "test-secret-that-is-long-enough-32"
	testIssuer   = "user-management-api"
	testAudience = "user-management-client"
	testTTL      = 15 * time.Minute
)

func newService(t *testing.T, clock domain.Clock) *jwtauth.TokenService {
	t.Helper()

	svc, err := jwtauth.NewTokenService(jwtauth.Config{
		Secret:   testSecret,
		Issuer:   testIssuer,
		Audience: testAudience,
		TTL:      testTTL,
		Leeway:   30 * time.Second,
	}, jwtauth.WithClock(clock))
	if err != nil {
		t.Fatalf("could not build the TokenService: %v", err)
	}
	return svc
}

func testUser() *domain.User {
	return &domain.User{
		ID:    "653f1a2b3c4d5e6f70818283",
		Name:  "Alice",
		Email: "alice@example.com",
		Role:  domain.RoleAdmin,
	}
}

func TestIssueThenVerify(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())
	svc := newService(t, clock)
	u := testUser()

	tok, err := svc.Issue(u)
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if tok.Type != "Bearer" {
		t.Errorf("Type = %q, want Bearer", tok.Type)
	}
	if tok.ExpiresIn != int64(testTTL.Seconds()) {
		t.Errorf("ExpiresIn = %d, want %d", tok.ExpiresIn, int64(testTTL.Seconds()))
	}
	if !tok.ExpiresAt.Equal(clock.Now().Add(testTTL)) {
		t.Errorf("ExpiresAt = %v", tok.ExpiresAt)
	}
	if n := len(strings.Split(tok.Value, ".")); n != 3 {
		t.Errorf("a JWT must have 3 parts, got %d", n)
	}

	id, err := svc.Verify(tok.Value)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if id.UserID != u.ID {
		t.Errorf("UserID = %q, want %q", id.UserID, u.ID)
	}
	if id.Role != domain.RoleAdmin {
		t.Errorf("Role = %q, want admin", id.Role)
	}
}

// TestTokenHeaderUsesHS256 confirms tokens are signed with HS256, as required.
func TestTokenHeaderUsesHS256(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))
	tok, err := svc.Issue(testUser())
	if err != nil {
		t.Fatal(err)
	}

	header := decodeSegment(t, strings.Split(tok.Value, ".")[0])
	var h map[string]any
	if err := json.Unmarshal(header, &h); err != nil {
		t.Fatal(err)
	}
	if h["alg"] != "HS256" {
		t.Errorf("alg = %v, want HS256", h["alg"])
	}
	if h["typ"] != "JWT" {
		t.Errorf("typ = %v, want JWT", h["typ"])
	}
}

// TestClaimsContent checks the payload has every standard claim and nothing
// that does not belong there.
func TestClaimsContent(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())
	svc := newService(t, clock)
	u := testUser()

	tok, err := svc.Issue(u)
	if err != nil {
		t.Fatal(err)
	}

	payload := decodeSegment(t, strings.Split(tok.Value, ".")[1])
	var c map[string]any
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}

	if c["sub"] != u.ID {
		t.Errorf("sub = %v", c["sub"])
	}
	if c["iss"] != testIssuer {
		t.Errorf("iss = %v", c["iss"])
	}
	if c["role"] != "admin" {
		t.Errorf("role = %v", c["role"])
	}
	for _, key := range []string{"exp", "iat", "nbf", "jti", "aud"} {
		if _, found := c[key]; !found {
			t.Errorf("claim %q must be present", key)
		}
	}
	// A JWT payload is base64 that anyone can decode, so it must hold no
	// personal data and no secrets.
	for _, mustNot := range []string{"password", "password_hash", "email", "name"} {
		if _, found := c[mustNot]; found {
			t.Errorf("claim %q must not be in the token", mustNot)
		}
	}
	if strings.Contains(string(payload), u.Email) {
		t.Error("the email must not appear in the payload")
	}
}

func TestJTIIsUniquePerToken(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))
	u := testUser()

	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		tok, err := svc.Issue(u)
		if err != nil {
			t.Fatal(err)
		}
		var c jwtauth.Claims
		if _, _, err := jwt.NewParser().ParseUnverified(tok.Value, &c); err != nil {
			t.Fatal(err)
		}
		if c.ID == "" {
			t.Fatal("jti must not be empty")
		}
		if seen[c.ID] {
			t.Fatalf("duplicate jti: %s", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())
	svc := newService(t, clock)

	tok, err := svc.Issue(testUser())
	if err != nil {
		t.Fatal(err)
	}

	// Still inside its lifetime (plus leeway), so it must verify.
	clock.Advance(testTTL - time.Minute)
	if _, err := svc.Verify(tok.Value); err != nil {
		t.Fatalf("a token that has not expired must verify, got: %v", err)
	}

	// Well past the expiry and the leeway, so it must not verify.
	clock.Advance(2 * time.Minute)
	_, err = svc.Verify(tok.Value)
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("an expired token must give ErrUnauthenticated, got: %v", err)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the message should say it expired, got: %v", err)
	}
}

func TestVerifyWithinLeeway(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())
	svc := newService(t, clock)
	tok, err := svc.Issue(testUser())
	if err != nil {
		t.Fatal(err)
	}

	// Just expired but still within the 30-second leeway, for clock drift.
	clock.Advance(testTTL + 10*time.Second)
	if _, err := svc.Verify(tok.Value); err != nil {
		t.Errorf("inside the leeway it must still verify, got: %v", err)
	}
}

func TestVerifyWrongSecret(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())
	issuer := newService(t, clock)

	tok, err := issuer.Issue(testUser())
	if err != nil {
		t.Fatal(err)
	}

	other, err := jwtauth.NewTokenService(jwtauth.Config{
		Secret:   "a-completely-different-secret-key!!",
		Issuer:   testIssuer,
		Audience: testAudience,
		TTL:      testTTL,
	}, jwtauth.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := other.Verify(tok.Value); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("a different secret must not verify, got: %v", err)
	}
}

// TestVerifyRejectsAlgNone is the most important security case here.
//
// An attacker builds a token declaring alg=none and carrying no signature.
// If the parser trusted the header that came with the token, it would accept
// a payload anyone can edit (changing role to admin, say). Passing
// jwt.WithValidMethods(["HS256"]) closes that hole.
func TestVerifyRejectsAlgNone(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))

	header := encodeSegment(t, map[string]any{"alg": "none", "typ": "JWT"})
	payload := encodeSegment(t, map[string]any{
		"sub":  "653f1a2b3c4d5e6f70818283",
		"role": "admin",
		"iss":  testIssuer,
		"aud":  testAudience,
		"exp":  testsupport.FixedTime().Add(time.Hour).Unix(),
		"iat":  testsupport.FixedTime().Unix(),
	})

	for _, variant := range []string{
		header + "." + payload + ".",   // no signature
		header + "." + payload + ".xx", // garbage signature
	} {
		if _, err := svc.Verify(variant); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Errorf("an alg=none token must be rejected, got: %v", err)
		}
	}
}

// TestVerifyRejectsAlgSwap covers the other form of algorithm confusion:
// declare a different alg (HS512, RS256) but sign with the same secret.
func TestVerifyRejectsAlgSwap(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))

	for _, alg := range []string{"HS512", "HS384", "RS256", "ES256", "NONE", "nOnE"} {
		t.Run(alg, func(t *testing.T) {
			t.Parallel()

			raw := forgeHS256SignedToken(t, alg, map[string]any{
				"sub":  "653f1a2b3c4d5e6f70818283",
				"role": "admin",
				"iss":  testIssuer,
				"aud":  testAudience,
				"exp":  testsupport.FixedTime().Add(time.Hour).Unix(),
				"iat":  testsupport.FixedTime().Unix(),
			})
			if _, err := svc.Verify(raw); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Errorf("alg=%s must be rejected, got: %v", alg, err)
			}
		})
	}
}

// TestVerifyRejectsTamperedPayload confirms editing role breaks the signature.
func TestVerifyRejectsTamperedPayload(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())
	svc := newService(t, clock)

	u := testUser()
	u.Role = domain.RoleUser
	tok, err := svc.Issue(u)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(tok.Value, ".")
	var claims map[string]any
	if err := json.Unmarshal(decodeSegment(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	claims["role"] = "admin" // escalate our own privileges
	parts[1] = encodeSegment(t, claims)

	if _, err := svc.Verify(strings.Join(parts, ".")); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("a tampered payload must be rejected, got: %v", err)
	}
}

func TestVerifyWrongIssuerOrAudience(t *testing.T) {
	t.Parallel()

	clock := testsupport.NewFakeClock(testsupport.FixedTime())

	tests := []struct {
		name     string
		issuer   string
		audience string
	}{
		{name: "issuer does not match", issuer: "someone-else", audience: testAudience},
		{name: "audience does not match", issuer: testIssuer, audience: "another-client"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			foreign, err := jwtauth.NewTokenService(jwtauth.Config{
				Secret:   testSecret, // same secret, but the claims do not match
				Issuer:   tc.issuer,
				Audience: tc.audience,
				TTL:      testTTL,
			}, jwtauth.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}

			tok, err := foreign.Issue(testUser())
			if err != nil {
				t.Fatal(err)
			}

			ours := newService(t, clock)
			if _, err := ours.Verify(tok.Value); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Errorf("it must be rejected, got: %v", err)
			}
		})
	}
}

func TestVerifyMalformedInput(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "plain text", input: "not-a-token"},
		{name: "only two parts", input: "aaa.bbb"},
		{name: "broken base64", input: "!!!.???.###"},
		{name: "nothing but dots", input: "..."},
		{name: "Bearer prefix left in", input: "Bearer abc.def.ghi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := svc.Verify(tc.input); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Errorf("want ErrUnauthenticated, got: %v", err)
			}
		})
	}
}

// TestVerifyRejectsTokenWithoutExpiration blocks a token with no exp, which
// would otherwise be valid forever.
func TestVerifyRejectsTokenWithoutExpiration(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))

	raw := forgeHS256SignedToken(t, "HS256", map[string]any{
		"sub":  "653f1a2b3c4d5e6f70818283",
		"role": "user",
		"iss":  testIssuer,
		"aud":  testAudience,
		"iat":  testsupport.FixedTime().Unix(),
	})
	if _, err := svc.Verify(raw); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("a token without exp must be rejected, got: %v", err)
	}
}

func TestVerifyRejectsBadClaims(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))
	base := func() map[string]any {
		return map[string]any{
			"sub":  "653f1a2b3c4d5e6f70818283",
			"role": "user",
			"iss":  testIssuer,
			"aud":  testAudience,
			"exp":  testsupport.FixedTime().Add(time.Hour).Unix(),
			"iat":  testsupport.FixedTime().Unix(),
		}
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "no sub", mutate: func(c map[string]any) { delete(c, "sub") }},
		{name: "empty sub", mutate: func(c map[string]any) { c["sub"] = "" }},
		{name: "unknown role", mutate: func(c map[string]any) { c["role"] = "superadmin" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			claims := base()
			tc.mutate(claims)
			raw := forgeHS256SignedToken(t, "HS256", claims)

			if _, err := svc.Verify(raw); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Errorf("it must be rejected, got: %v", err)
			}
		})
	}
}

// TestRoleMissingDefaultsToUser: a correctly signed token with no role claim
// must get the lowest privilege, not admin (fail closed).
func TestRoleMissingDefaultsToUser(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))
	raw := forgeHS256SignedToken(t, "HS256", map[string]any{
		"sub": "653f1a2b3c4d5e6f70818283",
		"iss": testIssuer,
		"aud": testAudience,
		"exp": testsupport.FixedTime().Add(time.Hour).Unix(),
		"iat": testsupport.FixedTime().Unix(),
	})

	id, err := svc.Verify(raw)
	if err != nil {
		t.Fatalf("a correctly signed token should verify: %v", err)
	}
	if id.Role != domain.RoleUser {
		t.Errorf("Role = %q, want user (the lowest privilege)", id.Role)
	}
	if id.IsAdmin() {
		t.Error("a token with no role must not get admin rights")
	}
}

func TestIssueRejectsUserWithoutID(t *testing.T) {
	t.Parallel()

	svc := newService(t, testsupport.NewFakeClock(testsupport.FixedTime()))

	if _, err := svc.Issue(nil); err == nil {
		t.Error("passing nil must fail")
	}
	if _, err := svc.Issue(&domain.User{Role: domain.RoleUser}); err == nil {
		t.Error("a user without an id must fail")
	}
}

func TestNewTokenServiceValidation(t *testing.T) {
	t.Parallel()

	if _, err := jwtauth.NewTokenService(jwtauth.Config{TTL: testTTL}); err == nil {
		t.Error("a missing secret must fail")
	}
	if _, err := jwtauth.NewTokenService(jwtauth.Config{Secret: testSecret}); err == nil {
		t.Error("a TTL of 0 must fail")
	}
	svc, err := jwtauth.NewTokenService(jwtauth.Config{Secret: testSecret, TTL: testTTL})
	if err != nil {
		t.Fatalf("a valid configuration must pass: %v", err)
	}
	var _ domain.TokenService = svc
}

// --- helper ---

func decodeSegment(t *testing.T, seg string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("could not decode the base64: %v", err)
	}
	return b
}

func encodeSegment(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("could not encode the JSON: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// forgeHS256SignedToken assembles a token declaring whatever alg is asked
// for while signing it with HMAC-SHA256 and the system's secret. It plays
// the attacker trying to trick the parser into another algorithm, and it
// also builds tokens with odd claims that Issue would never produce.
func forgeHS256SignedToken(t *testing.T, alg string, claims map[string]any) string {
	t.Helper()

	header := encodeSegment(t, map[string]any{"alg": alg, "typ": "JWT"})
	payload := encodeSegment(t, claims)
	signingInput := header + "." + payload

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + sig
}
