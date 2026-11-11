// Package jwtauth is the driven adapter for the domain.TokenService port.
// It issues and verifies HMAC-SHA256 (HS256) JSON Web Tokens, as the
// challenge requires.
package jwtauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/pitchter/7-solution/internal/domain"
	"github.com/pitchter/7-solution/internal/platform/idgen"
)

// SigningMethod is the only algorithm this system accepts.
//
// It has to be pinned for both signing and verification. If verification
// trusted the "alg" header that came with the token, an attacker could send
// one declaring alg=none (no signature at all), or switch to an asymmetric
// algorithm and use the public key as the HMAC key. These attacks are known
// collectively as algorithm confusion.
var SigningMethod = jwt.SigningMethodHS256

// RoleClaim is the name of the custom claim holding the permission level.
const RoleClaim = "role"

// TokenType follows RFC 6750.
const TokenType = "Bearer"

// Config holds the settings used when issuing tokens.
type Config struct {
	// Secret is the HMAC key; its length is validated by the config layer.
	Secret   string
	Issuer   string
	Audience string
	TTL      time.Duration
	// Leeway absorbs small clock differences between machines.
	Leeway time.Duration
}

// Claims is what a token carries.
//
// jwt.RegisteredClaims is embedded to get the standard claims (sub, iss,
// aud, exp, iat, jti); only role is added, since it is needed to decide
// permissions on every request.
//
// Name and email are deliberately left out: a JWT payload is plain base64
// that anyone can read (it is not encrypted), and embedding them would make
// the token's copy stale as soon as the user edits their profile.
type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// TokenService issues and verifies tokens.
type TokenService struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	leeway   time.Duration
	clock    domain.Clock
}

var _ domain.TokenService = (*TokenService)(nil)

// Option tweaks a TokenService.
type Option func(*TokenService)

// WithClock supplies a custom Clock, which tests use to pin down time.
func WithClock(c domain.Clock) Option {
	return func(s *TokenService) {
		if c != nil {
			s.clock = c
		}
	}
}

// NewTokenService builds a service from its configuration.
func NewTokenService(cfg Config, opts ...Option) (*TokenService, error) {
	if cfg.Secret == "" {
		return nil, errors.New("jwtauth: a secret is required")
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("jwtauth: TTL must be greater than 0")
	}

	s := &TokenService{
		secret:   []byte(cfg.Secret),
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		ttl:      cfg.TTL,
		leeway:   cfg.Leeway,
		clock:    domain.SystemClock{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Issue mints an access token for a user.
func (s *TokenService) Issue(u *domain.User) (domain.Token, error) {
	if u == nil || u.ID == "" {
		return domain.Token{}, errors.New("jwtauth: cannot issue a token for a user without an id")
	}

	now := s.clock.Now()
	expiresAt := now.Add(s.ttl)

	claims := Claims{
		Role: u.Role.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			Issuer:    s.issuer,
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			// A unique jti per token makes them traceable in the logs and
			// leaves room for a revocation list later without a format change.
			ID: idgen.New(),
		},
	}

	signed, err := jwt.NewWithClaims(SigningMethod, claims).SignedString(s.secret)
	if err != nil {
		return domain.Token{}, fmt.Errorf("could not sign the token: %w", err)
	}

	return domain.Token{
		Value:     signed,
		Type:      TokenType,
		ExpiresIn: int64(s.ttl.Seconds()),
		ExpiresAt: expiresAt,
	}, nil
}

// Verify validates a token and returns the identity inside it.
//
// Every layer is checked: the algorithm must be HS256, the signature must be
// valid, the token must not have expired, it must come from our issuer, name
// our audience and carry a usable sub. Any one of those failing yields
// ErrUnauthenticated.
func (s *TokenService) Verify(raw string) (domain.Identity, error) {
	if raw == "" {
		return domain.Identity{}, fmt.Errorf("%w: no token supplied", domain.ErrUnauthenticated)
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{SigningMethod.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithLeeway(s.leeway),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.clock.Now),
	)

	claims := &Claims{}
	token, err := parser.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil {
		return domain.Identity{}, fmt.Errorf("%w: %s", domain.ErrUnauthenticated, reason(err))
	}
	if !token.Valid {
		return domain.Identity{}, fmt.Errorf("%w: the token is not usable", domain.ErrUnauthenticated)
	}

	if claims.Subject == "" {
		return domain.Identity{}, fmt.Errorf("%w: the token has no subject", domain.ErrUnauthenticated)
	}

	role, err := domain.ParseRole(claims.Role)
	if err != nil {
		// An unknown role may come from an old or tampered token; never guess.
		return domain.Identity{}, fmt.Errorf("%w: the role in the token is not usable", domain.ErrUnauthenticated)
	}

	return domain.Identity{UserID: claims.Subject, Role: role}, nil
}

// reason turns a jwt error into a short phrase that tells a developer what
// went wrong without over-sharing internals with the caller.
func reason(err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return "the token has expired"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return "the token is not valid yet"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "the signature is invalid"
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return "the issuer does not match"
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return "the audience does not match"
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return "the signature could not be checked"
	case errors.Is(err, jwt.ErrTokenMalformed):
		return "the token is malformed"
	default:
		return "the token is not usable"
	}
}
