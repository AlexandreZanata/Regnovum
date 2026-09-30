// Package apperr defines the application and domain error vocabulary of
// Regnovum (P02-T03).
//
// Errors are HTTP-free by design: this package does not import net/http, so
// use cases can be expressed, tested and reused without transport concerns.
// The HTTP adapter (internal/platform/httperror) maps these errors to
// RFC 9457 Problem Details responses.
//
// Every error carries a stable machine-readable Code, an error Kind that
// selects the public status mapping, an optional public-safe detail and the
// wrapped internal cause. The cause never serializes; it exists for logging
// and errors.Is/As unwrapping only.
package apperr

import (
	"errors"
	"fmt"
)

// Kind classifies an application error and fixes its public HTTP status via
// httperror. Kinds are stable vocabulary: adding one requires a matching
// mapper entry and table tests on both sides.
type Kind string

const (
	// KindValidation marks input that failed structural validation.
	KindValidation Kind = "validation"
	// KindUnauthorized marks missing or invalid authentication.
	KindUnauthorized Kind = "unauthorized"
	// KindForbidden marks an authenticated caller without permission.
	KindForbidden Kind = "forbidden"
	// KindNotFound marks a missing resource.
	KindNotFound Kind = "not_found"
	// KindConflict marks a state or uniqueness conflict.
	KindConflict Kind = "conflict"
	// KindRateLimited marks quota or throttling rejection.
	KindRateLimited Kind = "rate_limited"
	// KindInternal marks unexpected failures; details must stay generic.
	KindInternal Kind = "internal"
)

// Error is the vocabulary error of the application and domain layers.
type Error struct {
	kind      Kind
	code      string
	detail    string
	cause     error
	retryable bool
}

// New builds an application error of the given kind.
func New(kind Kind, code, detail string) *Error {
	return &Error{kind: kind, code: code, detail: detail}
}

// Newf builds an application error with a formatted public detail.
func Newf(kind Kind, code, format string, arguments ...any) *Error {
	return &Error{kind: kind, code: code, detail: fmt.Sprintf(format, arguments...)}
}

// WithCause wraps the internal cause (for logs and unwrapping) and returns
// the same error for chaining. The cause never reaches the public response.
func (appError *Error) WithCause(cause error) *Error {
	appError.cause = cause
	return appError
}

// MarkRetryable flags transient failures that clients may retry.
func (appError *Error) MarkRetryable() *Error {
	appError.retryable = true
	return appError
}

// Kind returns the stable error kind.
func (appError *Error) Kind() Kind { return appError.kind }

// Code returns the stable machine-readable error code.
func (appError *Error) Code() string { return appError.code }

// Detail returns the public-safe detail. It never contains internal causes.
func (appError *Error) Detail() string { return appError.detail }

// IsRetryable reports whether the client may retry the operation.
func (appError *Error) IsRetryable() bool { return appError.retryable }

// Unwrap exposes the internal cause for errors.Is/As and logging.
func (appError *Error) Unwrap() error { return appError.cause }

// Error renders the stable code and public detail; it deliberately excludes
// the wrapped internal cause so accidental logging of the error value stays
// safe.
func (appError *Error) Error() string {
	if appError.detail == "" {
		return appError.code
	}
	return fmt.Sprintf("%s: %s", appError.code, appError.detail)
}

// KindOf extracts the Kind of the first apperr.Error in the chain, with a
// default of KindInternal for foreign errors.
func KindOf(err error) Kind {
	var appError *Error
	if errors.As(err, &appError) {
		return appError.kind
	}
	return KindInternal
}
