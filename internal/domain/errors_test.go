package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"user-management-api/internal/domain"
)

func TestValidationErrorIsErrValidation(t *testing.T) {
	t.Parallel()

	err := domain.NewValidationError("email", "must be a valid email address")

	if !errors.Is(err, domain.ErrValidation) {
		t.Error("ValidationError must match ErrValidation through errors.Is")
	}
	// It must keep working through several layers of wrapping.
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", err))
	if !errors.Is(wrapped, domain.ErrValidation) {
		t.Error("a wrapped ValidationError must still match ErrValidation")
	}

	var ve *domain.ValidationError
	if !errors.As(wrapped, &ve) {
		t.Fatal("errors.As must recover the *ValidationError")
	}
	if len(ve.Fields) != 1 || ve.Fields[0].Field != "email" {
		t.Errorf("Fields = %+v", ve.Fields)
	}
}

func TestValidationErrorAddAndMessage(t *testing.T) {
	t.Parallel()

	err := domain.NewValidationError("name", "is required")
	err.Add("password", "too short")

	if len(err.Fields) != 2 {
		t.Fatalf("want 2 fields, got %d", len(err.Fields))
	}
	msg := err.Error()
	for _, want := range []string{"name", "is required", "password", "too short"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q must mention %q", msg, want)
		}
	}
}

func TestValidationErrorEmpty(t *testing.T) {
	t.Parallel()

	empty := &domain.ValidationError{}
	if empty.HasErrors() {
		t.Error("an empty ValidationError must not report HasErrors")
	}
	if got := empty.Error(); got != domain.ErrValidation.Error() {
		t.Errorf("Error() of an empty value = %q", got)
	}
}

func TestValidationErrorAbsorb(t *testing.T) {
	t.Parallel()

	v := &domain.ValidationError{}

	if !v.Absorb(domain.NewValidationError("name", "is required")) {
		t.Fatal("Absorb must accept a ValidationError")
	}
	if !v.Absorb(fmt.Errorf("wrapped: %w", domain.NewValidationError("email", "is invalid"))) {
		t.Fatal("Absorb must see through wrapping")
	}
	if len(v.Fields) != 2 {
		t.Fatalf("want 2 collected fields, got %+v", v.Fields)
	}

	// An error that is not a validation failure must be refused rather than
	// turned into a field complaint, so the caller is forced to pass it on.
	if v.Absorb(domain.ErrUserNotFound) {
		t.Error("Absorb must refuse an unrelated error")
	}
	if len(v.Fields) != 2 {
		t.Errorf("a refused error must leave Fields alone, got %+v", v.Fields)
	}
}

// TestInvalidCredentialsRevealsNothing guards the wording of the one error
// whose text is handed to the client verbatim: it must not say whether the
// address exists.
func TestInvalidCredentialsRevealsNothing(t *testing.T) {
	t.Parallel()

	msg := domain.ErrInvalidCredentials.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "exist") {
		t.Errorf("message %q risks revealing that an address is registered", msg)
	}
}
