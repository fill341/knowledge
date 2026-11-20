package domain

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is a domain error that maps 1:1 to an RFC 9457 problem response.
type Error struct {
	Status int
	Code   string
	Detail string
	// Extra fields are merged into the problem body (e.g. existing_id, current_version).
	Extra map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func newErr(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Detail: fmt.Sprintf(format, args...)}
}

func (e *Error) With(key string, v any) *Error {
	if e.Extra == nil {
		e.Extra = map[string]any{}
	}
	e.Extra[key] = v
	return e
}

func BadRequest(format string, args ...any) *Error {
	return newErr(http.StatusBadRequest, "bad_request", format, args...)
}
func Unauthenticated(format string, args ...any) *Error {
	return newErr(http.StatusUnauthorized, "unauthenticated", format, args...)
}
func Forbidden(format string, args ...any) *Error {
	return newErr(http.StatusForbidden, "forbidden", format, args...)
}
func ForbiddenSpace(space string) *Error {
	return newErr(http.StatusForbidden, "forbidden_space", "token has no access to space %q", space)
}
func NotFound(format string, args ...any) *Error {
	return newErr(http.StatusNotFound, "not_found", format, args...)
}
func Duplicate(existingID string) *Error {
	return newErr(http.StatusConflict, "duplicate", "an identical entry already exists").With("existing_id", existingID)
}

// DuplicateContent: a live entry of the space already has the same text (on_duplicate_content=reject).
func DuplicateContent(existingID string) *Error {
	return newErr(http.StatusConflict, "duplicate_content", "an entry with the same text already exists in this space").
		With("existing_id", existingID)
}

func Conflict(format string, args ...any) *Error {
	return newErr(http.StatusConflict, "conflict", format, args...)
}
func InvalidTransition(from, to Status) *Error {
	return newErr(http.StatusConflict, "invalid_transition", "cannot change status from %s to %s", from, to)
}
func VersionConflict(expected, current int) *Error {
	return newErr(http.StatusPreconditionFailed, "version_conflict",
		"entry was modified: expected version %d, current %d", expected, current).With("current_version", current)
}
func PreconditionRequired(format string, args ...any) *Error {
	return newErr(http.StatusPreconditionRequired, "precondition_required", format, args...)
}
func Validation(format string, args ...any) *Error {
	return newErr(http.StatusUnprocessableEntity, "validation_failed", format, args...)
}
func Unprocessable(code, format string, args ...any) *Error {
	return newErr(http.StatusUnprocessableEntity, code, format, args...)
}
func RateLimited(format string, args ...any) *Error {
	return newErr(http.StatusTooManyRequests, "rate_limited", format, args...)
}

// AsError extracts a *Error from err, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
