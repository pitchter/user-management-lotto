package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"user-management-api/internal/domain"
	"user-management-api/internal/platform/logger"
)

// Error codes let a client tell cases apart without parsing prose: the
// message may be reworded or translated, but these codes are part of the
// API contract.
const (
	CodeValidation       = "VALIDATION_ERROR"
	CodeBadRequest       = "BAD_REQUEST"
	CodeUnauthenticated  = "UNAUTHENTICATED"
	CodeForbidden        = "FORBIDDEN"
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodeEmailTaken       = "EMAIL_TAKEN"
	CodeNoChanges        = "NO_CHANGES"
	CodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	CodeInternal         = "INTERNAL_ERROR"
)

// fieldDetail names one invalid field and why it is invalid.
type fieldDetail struct {
	Field string `json:"field"`
	Issue string `json:"issue"`
}

type errorBody struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []fieldDetail `json:"details,omitempty"`
}

// errorResponse is the single error shape every endpoint uses.
//
// One shape across the whole API means a client needs only one error handler,
// and the request_id travelling with it points straight at the server logs
// for that request.
type errorResponse struct {
	Error     errorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

// respondError turns an error into an HTTP response.
//
// This is the one place where domain errors become status codes, which is
// what frees the domain and application layers from knowing about HTTP; a new
// error only has to be handled here.
//
// An unrecognised error becomes a 500 with a generic message, while the real
// detail goes to the server log and never to the caller. That keeps internal
// structure (collection names, broken queries) from leaking.
func respondError(c *gin.Context, err error) {
	status, body := translateError(err)

	if status >= http.StatusInternalServerError {
		logger.FromContext(c.Request.Context()).ErrorContext(c.Request.Context(),
			"request_failed",
			slog.String(logger.KeyError, err.Error()),
			slog.String(logger.KeyPath, c.FullPath()),
		)
	}

	c.AbortWithStatusJSON(status, errorResponse{
		Error:     body,
		RequestID: RequestIDFrom(c),
	})
}

// translateError picks the status code and body that fit the given error.
func translateError(err error) (int, errorBody) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return http.StatusBadRequest, errorBody{
			Code:    CodeValidation,
			Message: "the submitted data is invalid",
			Details: toDetails(ve.Fields),
		}
	}

	switch {
	case errors.Is(err, domain.ErrValidation):
		return http.StatusBadRequest, errorBody{Code: CodeValidation, Message: err.Error()}

	case errors.Is(err, domain.ErrNoChanges):
		return http.StatusBadRequest, errorBody{
			Code:    CodeNoChanges,
			Message: "send at least one field to change (name or email)",
		}

	case errors.Is(err, domain.ErrUnauthenticated):
		return http.StatusUnauthorized, errorBody{
			Code:    CodeUnauthenticated,
			Message: "a valid access token is required",
		}

	case errors.Is(err, domain.ErrInvalidCredentials):
		// The same message whether the address is unknown or the password is wrong.
		return http.StatusUnauthorized, errorBody{
			Code:    CodeUnauthenticated,
			Message: domain.ErrInvalidCredentials.Error(),
		}

	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, errorBody{
			Code:    CodeForbidden,
			Message: "you are not allowed to perform this action",
		}

	case errors.Is(err, domain.ErrUserNotFound):
		return http.StatusNotFound, errorBody{
			Code:    CodeNotFound,
			Message: "the requested user does not exist",
		}

	case errors.Is(err, domain.ErrEmailTaken):
		return http.StatusConflict, errorBody{
			Code:    CodeEmailTaken,
			Message: "that email is already registered",
		}

	default:
		return http.StatusInternalServerError, errorBody{
			Code:    CodeInternal,
			Message: "an internal error occurred",
		}
	}
}

// respondBindError turns a body binding or validation failure into a response.
//
// It is separate from respondError because validator errors need translating
// into something a client can act on, and because the causes differ: broken
// JSON, a wrong field type, or a body over the size limit.
func respondBindError(c *gin.Context, err error) {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		c.AbortWithStatusJSON(http.StatusBadRequest, errorResponse{
			Error: errorBody{
				Code:    CodeValidation,
				Message: "the submitted data is invalid",
				Details: translateValidatorErrors(verrs),
			},
			RequestID: RequestIDFrom(c),
		})
		return
	}

	// The body exceeded the configured ceiling (http.MaxBytesReader's error).
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, errorResponse{
			Error: errorBody{
				Code:    CodePayloadTooLarge,
				Message: fmt.Sprintf("the request body exceeds %d bytes", maxBytesErr.Limit),
			},
			RequestID: RequestIDFrom(c),
		})
		return
	}

	// A field's type does not match its declaration, e.g. "name": 123.
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		c.AbortWithStatusJSON(http.StatusBadRequest, errorResponse{
			Error: errorBody{
				Code:    CodeValidation,
				Message: "a field has the wrong type",
				Details: []fieldDetail{{
					Field: typeErr.Field,
					Issue: fmt.Sprintf("must be of type %s", typeErr.Type.String()),
				}},
			},
			RequestID: RequestIDFrom(c),
		})
		return
	}

	c.AbortWithStatusJSON(http.StatusBadRequest, errorResponse{
		Error: errorBody{
			Code:    CodeBadRequest,
			Message: "the request could not be read; it must be valid JSON in the documented shape",
		},
		RequestID: RequestIDFrom(c),
	})
}

// translateValidatorErrors rewrites go-playground/validator errors into
// messages that say what to fix, instead of its hard-to-read raw text.
func translateValidatorErrors(verrs validator.ValidationErrors) []fieldDetail {
	out := make([]fieldDetail, 0, len(verrs))
	for _, fe := range verrs {
		out = append(out, fieldDetail{
			Field: fe.Field(),
			Issue: describeValidationTag(fe),
		})
	}
	return out
}

func describeValidationTag(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		if fe.Kind() == reflect.String {
			return fmt.Sprintf("must be at least %s characters long", fe.Param())
		}
		return fmt.Sprintf("must be at least %s", fe.Param())
	case "max":
		if fe.Kind() == reflect.String {
			return fmt.Sprintf("must not exceed %s characters", fe.Param())
		}
		return fmt.Sprintf("must not exceed %s", fe.Param())
	case "oneof":
		return "must be one of: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "len":
		return fmt.Sprintf("must be exactly %s characters long", fe.Param())
	case "hexadecimal":
		return "must be a hexadecimal string"
	default:
		return "is invalid (" + fe.Tag() + ")"
	}
}

func toDetails(fields []domain.FieldError) []fieldDetail {
	out := make([]fieldDetail, 0, len(fields))
	for _, f := range fields {
		out = append(out, fieldDetail{Field: f.Field, Issue: f.Issue})
	}
	return out
}

// Internal errors from reading the Authorization header.
var (
	errMissingToken    = authError("an Authorization: Bearer <token> header is required")
	errMalformedHeader = authError(`the Authorization header must look like "Bearer <token>"`)
)

// authError is a tiny error type whose message is safe to show a client.
type authError string

func (e authError) Error() string { return string(e) }
