package testsupport

import (
	"errors"
	"strings"
	"time"

	"github.com/pitchter/7-solution/internal/domain"
)

// FakeTokenService stands in for real JWTs in use-case and handler tests.
//
// Its token format is "fake-token:<userID>:<role>", which is readable at a
// glance while debugging a test and lets assertions confirm that a handler
// issued a token bound to the right user. Real JWT behaviour (signatures,
// expiry, algorithm confusion) is covered in internal/adapter/jwtauth.
type FakeTokenService struct {
	// IssueErr, when set, makes Issue fail.
	IssueErr error
	// TTL is the lifetime reported back to the caller.
	TTL time.Duration
	// Issued records every token handed out, in order.
	Issued []domain.Token
}

var _ domain.TokenService = (*FakeTokenService)(nil)

// FakeTokenPrefix is the prefix of a fake token.
const FakeTokenPrefix = "fake-token:"

// NewFakeTokenService builds the fake token service.
func NewFakeTokenService() *FakeTokenService {
	return &FakeTokenService{TTL: 15 * time.Minute}
}

// Issue mints a fake token that can be decoded back into an identity.
func (s *FakeTokenService) Issue(u *domain.User) (domain.Token, error) {
	if s.IssueErr != nil {
		return domain.Token{}, s.IssueErr
	}
	if u == nil || u.ID == "" {
		return domain.Token{}, errors.New("testsupport: cannot issue a token for a user without an id")
	}

	tok := domain.Token{
		Value:     FakeTokenPrefix + u.ID + ":" + u.Role.String(),
		Type:      "Bearer",
		ExpiresIn: int64(s.TTL.Seconds()),
		ExpiresAt: FixedTime().Add(s.TTL),
	}
	s.Issued = append(s.Issued, tok)
	return tok, nil
}

// Verify decodes the identity out of a fake token.
func (s *FakeTokenService) Verify(raw string) (domain.Identity, error) {
	if !strings.HasPrefix(raw, FakeTokenPrefix) {
		return domain.Identity{}, domain.ErrUnauthenticated
	}

	parts := strings.Split(strings.TrimPrefix(raw, FakeTokenPrefix), ":")
	if len(parts) != 2 || parts[0] == "" {
		return domain.Identity{}, domain.ErrUnauthenticated
	}

	role, err := domain.ParseRole(parts[1])
	if err != nil {
		return domain.Identity{}, domain.ErrUnauthenticated
	}
	return domain.Identity{UserID: parts[0], Role: role}, nil
}

// TokenFor builds a fake token for an identity, for setting test headers.
func TokenFor(userID string, role domain.Role) string {
	return FakeTokenPrefix + userID + ":" + role.String()
}
