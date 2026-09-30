// Package leak is the fixture of the public message that carries what must not be
// published: the detail of an RFC 9457 problem reaches the caller, so an error
// inside it is an internal detail published and a credential inside it is the
// credential published.
package leak

import "errors"

var errProvider = errors.New("the provider refused the request")

// Internal publishes the internal error as the detail of the response.
func Internal() error {
	return apperr.New(apperr.KindInternal, "server_error", errProvider.Error())
}

// Formatted publishes it through the format of the constructor.
func Formatted(key string) error {
	return apperr.Newf(apperr.KindInternal, "server_error", "call %q failed: %v", key, errProvider)
}

// Credential publishes the value the operator handed the process.
func Credential(apiKey string) error {
	return apperr.Newf(apperr.KindInternal, "server_error", "the provider answered 401 for %s", apiKey)
}
