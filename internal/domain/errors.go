package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors of the domain. Outer layers (HTTP and gRPC adapters) map
// these to status codes in one place using errors.Is, which is what keeps the
// business rules from ever needing to know about transport protocols.
var (
	ErrUserNotFound = errors.New("user not found")
	ErrEmailTaken   = errors.New("email already registered")
	// ErrInvalidCredentials means the email or the password is wrong.
	//
	// Callers always get this exact error for both cases, so an attacker
	// cannot tell whether an address exists in the system (user enumeration).
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrForbidden means the caller is authenticated but not allowed to do this.
	ErrForbidden = errors.New("operation not permitted")
	// ErrUnauthenticated means no token was supplied, or it cannot be used.
	ErrUnauthenticated = errors.New("authentication required")
	// ErrValidation means the input violates a domain rule. It is reported
	// together with ValidationError, which carries the offending fields.
	ErrValidation = errors.New("validation failed")
	ErrNoChanges  = errors.New("no fields to update")
)

// FieldError names one invalid field and why it is invalid, so the client can
// fix exactly what is wrong.
//
// It carries no json tags: like every other type in the domain and the
// application layer, it does not know how it will be serialised. The REST
// adapter maps it onto its own wire type.
type FieldError struct {
	Field string
	Issue string
}

// ValidationError collects several FieldErrors into a single error.
//
// errors.Is(err, ErrValidation) is always true for it, so an adapter can map
// it to 400 without type assertions.
type ValidationError struct {
	Fields []FieldError
}

// NewValidationError builds a ValidationError from its first field.
func NewValidationError(field, issue string) *ValidationError {
	return &ValidationError{Fields: []FieldError{{Field: field, Issue: issue}}}
}

// Add appends one more invalid field. It returns nothing, because a
// *ValidationError returned for chaining would read as an ignored error.
func (e *ValidationError) Add(field, issue string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Issue: issue})
}

// Absorb merges the invalid fields of err into e and reports whether err was
// a validation failure at all.
//
// Returning false, rather than inventing a placeholder field, forces a caller
// that hands it an unrelated error (a database fault, say) to pass that error
// on instead of silently turning it into a field complaint.
func (e *ValidationError) Absorb(err error) bool {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	e.Fields = append(e.Fields, ve.Fields...)
	return true
}

// HasErrors reports whether any invalid field was recorded. Useful when
// collecting several fields before deciding to fail.
func (e *ValidationError) HasErrors() bool {
	return e != nil && len(e.Fields) > 0
}

func (e *ValidationError) Error() string {
	if !e.HasErrors() {
		return ErrValidation.Error()
	}
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f.Field, f.Issue))
	}
	return "validation failed (" + strings.Join(parts, "; ") + ")"
}

// Is makes errors.Is(err, ErrValidation) report true.
func (e *ValidationError) Is(target error) bool {
	return errors.Is(target, ErrValidation)
}
