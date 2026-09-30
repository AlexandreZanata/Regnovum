// Package domain holds the transactional email vocabulary of Regnovum
// (P15-T03): the closed sets of templates and locales, the message that may
// be handed to a provider, and the structural rules that keep a request
// safe before any adapter sees it.
//
// The package is standard library only: transport, serialization and the
// localized catalog belong to adapters, which is why validation here is
// structural (shape, bounds, character classes) and never depends on a
// parser or a template engine.
package domain

import "errors"

var (
	// ErrInvalidRecipient reports an address that is not a well-formed,
	// bounded mailbox: exactly one "@", a non-empty local part within the
	// RFC 5321 bound, a non-empty domain, and no control character.
	ErrInvalidRecipient = errors.New("notifications: invalid recipient")

	// ErrUnsupportedTemplate reports a template outside the closed set.
	ErrUnsupportedTemplate = errors.New("notifications: unsupported template")

	// ErrUnsupportedLocale reports a locale outside the catalog allowlist.
	ErrUnsupportedLocale = errors.New("notifications: unsupported locale")

	// ErrInvalidBody reports an empty, oversized or header-unsafe rendered
	// body: a subject with a CR/LF would let content inject email headers.
	ErrInvalidBody = errors.New("notifications: invalid rendered body")

	// ErrInvalidIdempotencyKey reports a missing or malformed key. Every
	// delivery carries one: retrying a durable job must reach the provider
	// as the same logical send.
	ErrInvalidIdempotencyKey = errors.New("notifications: invalid idempotency key")

	// ErrInvalidTemplateValue reports a value that may not travel inside a
	// message: control characters (header injection, log forgery) are
	// rejected outright rather than escaped away.
	ErrInvalidTemplateValue = errors.New("notifications: invalid template value")

	// ErrMissingDependency reports an incomplete wiring (nil port), so a
	// misconfigured composition root fails at construction, not on the
	// first user request.
	ErrMissingDependency = errors.New("notifications: missing dependency")
)
