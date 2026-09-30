// Package requestid provides request correlation for Regnovum (P02-T04):
// inbound X-Request-Id headers are validated and reused, absent ones are
// generated through the injected IDGenerator port, the resolved value lives
// in the request context and is echoed back on every response.
package requestid

import (
	"context"
	"net/http"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Header is the correlation header of the platform.
const Header = "X-Request-Id"

// contextKey is the unexported context key for the resolved request ID.
type contextKey struct{}

// maxLen bounds accepted inbound identifiers so oversized headers cannot
// flood logs. Hexadecimal and hyphens keep the format auditable.
const maxLen = 64

// Validate accepts a client-supplied request ID when it is a short opaque
// token of letters, digits, hyphens and underscores; anything else (empty,
// oversized, whitespace or meta characters) is rejected.
func Validate(value string) bool {
	if value == "" || len(value) > maxLen {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-':
		case character == '_':
		default:
			return false
		}
	}
	return true
}

// Middleware resolves the request ID for every request: it validates the
// inbound header, falls back to the injected generator, stores the value in
// the request context and echoes it back in the response header.
func Middleware(generator ports.IDGenerator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		inbound := strings.TrimSpace(request.Header.Get(Header))
		id := inbound
		if !Validate(inbound) {
			id = generator.NewID()
		}

		writer.Header().Set(Header, id)
		next.ServeHTTP(writer, request.WithContext(WithID(request.Context(), id)))
	})
}

// WithID stores the request ID in the context.
func WithID(parent context.Context, id string) context.Context {
	return context.WithValue(parent, contextKey{}, id)
}

// FromContext extracts the request ID from the context; it returns an empty
// string outside the middleware.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(contextKey{}).(string); ok {
		return id
	}
	return ""
}

// FromRequest extracts the request ID from a request's context.
func FromRequest(request *http.Request) string {
	return FromContext(request.Context())
}
