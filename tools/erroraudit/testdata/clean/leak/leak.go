// Package leak is the clean half of the public message rule: the detail is a
// literal sentence, and the error is handed to `WriteProblem`, whose contract is
// to collapse a foreign error into the generic internal problem — the error is
// allowed there because the text it produces is not.
package leak

import "errors"

var errProvider = errors.New("the provider refused the request")

// Literal is a public message that says what happened without saying why.
func Literal() error {
	return apperr.New(apperr.KindValidation, "invalid_date", "the close date cannot be before the open date")
}

// Formatted builds the same sentence from a value that is not an error.
func Formatted(requestID string) error {
	return apperr.Newf(apperr.KindInternal, "server_error", "the request %s could not be served", requestID)
}

// Collected hands the error to the writer that owns the public text: the writer
// collapses the foreign error and only the code and the generic detail reach the
// response.
func Collected(writer writer, err error) {
	_ = writer.WriteProblem(err)
}

// Curated publishes the two parts of a domain error that are public by contract:
// the code the client acts on and the message written for it. Everything else an
// error carries — the error itself, its `Error()` text, its cause — is not.
func Curated(domainErr *domainError) error {
	return apperr.New(apperr.KindConflict, domainErr.Code, domainErr.Message)
}

type domainError struct {
	Code    string
	Message string
	cause   error
}

type writer interface {
	WriteProblem(err error) int
}
