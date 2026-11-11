// Package httpapi is the REST driving adapter: it turns HTTP requests into
// application-layer commands and the results back into JSON.
//
// This is the only layer that knows about gin, status codes and the JSON
// shape; all the business rules live in internal/app and internal/domain.
package httpapi

import (
	"time"

	"github.com/pitchter/7-solution/internal/app"
	"github.com/pitchter/7-solution/internal/domain"
)

// --- request ---

// registerRequest is the body of POST /auth/register. It has no role field;
// app.RegisterInput says why.
type registerRequest struct {
	Name     string `json:"name" binding:"required,min=2,max=100"`
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

// loginRequest is the body of POST /auth/login.
//
// There is deliberately no password length rule here: answering 400 because
// the password is "too short" would leak the policy and separate that case
// from the 401s, making it easier to probe.
type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// createUserRequest is the body of POST /users (admin only).
type createUserRequest struct {
	Name     string `json:"name" binding:"required,min=2,max=100"`
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,min=8,max=72"`
	// Role may be omitted, which means user.
	Role string `json:"role" binding:"omitempty,oneof=admin user"`
}

// updateUserRequest is the body of PATCH /users/:id.
//
// Pointers distinguish "field not sent" (nil, leave it alone) from "empty
// value sent" (clear it, which validation rejects), per PATCH semantics.
type updateUserRequest struct {
	Name  *string `json:"name" binding:"omitempty,min=2,max=100"`
	Email *string `json:"email" binding:"omitempty,email,max=254"`
}

// toInput converts the request into the application-layer DTO.
func (r updateUserRequest) toInput() app.UpdateUserInput {
	return app.UpdateUserInput{Name: r.Name, Email: r.Email}
}

// listQuery is the query string of GET /users.
//
// Limit is a pointer so that "no limit given" (nil, use the default) can be
// told apart from "limit=0 given" (asking for zero records, which makes no
// sense and earns a 400). A plain int would make both cases look like 0.
type listQuery struct {
	Limit  *int   `form:"limit" binding:"omitempty,min=1,max=100"`
	Cursor string `form:"cursor" binding:"omitempty,len=24,hexadecimal"`
}

// limitOrDefault returns the requested limit, or the default when unset.
func (q listQuery) limitOrDefault() int {
	if q.Limit == nil {
		return domain.DefaultListLimit
	}
	return *q.Limit
}

// --- response ---

// userResponse is the user data sent back.
//
// It is built from app.UserOutput, which has no password field at all, so
// nothing sensitive can slip through.
type userResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newUserResponse(u app.UserOutput) userResponse {
	return userResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		Role:      u.Role,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

// listResponse is the result of GET /users.
type listResponse struct {
	Data []userResponse `json:"data"`
	Meta listMeta       `json:"meta"`
}

// listMeta carries the pagination details.
type listMeta struct {
	Count int `json:"count"`
	Limit int `json:"limit"`
	// NextCursor is passed back as ?cursor= for the next page (null = no more).
	NextCursor *string `json:"next_cursor"`
	Total      int64   `json:"total"`
}

func newListResponse(out app.ListOutput, limit int) listResponse {
	data := make([]userResponse, 0, len(out.Users))
	for _, u := range out.Users {
		data = append(data, newUserResponse(u))
	}

	var next *string
	if out.NextCursor != "" {
		cursor := out.NextCursor
		next = &cursor
	}
	return listResponse{
		Data: data,
		Meta: listMeta{
			Count:      len(data),
			Limit:      limit,
			NextCursor: next,
			Total:      out.Total,
		},
	}
}

// tokenResponse is the result of POST /auth/login.
//
// The field names follow OAuth 2.0 (RFC 6749) so standard client libraries
// can read it as-is.
type tokenResponse struct {
	AccessToken string       `json:"access_token"`
	TokenType   string       `json:"token_type"`
	ExpiresIn   int64        `json:"expires_in"`
	ExpiresAt   time.Time    `json:"expires_at"`
	User        userResponse `json:"user"`
}

func newTokenResponse(out app.AuthOutput) tokenResponse {
	return tokenResponse{
		AccessToken: out.AccessToken,
		TokenType:   out.TokenType,
		ExpiresIn:   out.ExpiresIn,
		ExpiresAt:   out.ExpiresAt,
		User:        newUserResponse(out.User),
	}
}

// healthResponse is the result of /healthz and /readyz.
type healthResponse struct {
	Status  string `json:"status"`
	Mongo   string `json:"mongo,omitempty"`
	Version string `json:"version,omitempty"`
}
