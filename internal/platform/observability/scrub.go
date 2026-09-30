package observability

import (
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
)

// forbiddenPropertyNames are property names no event may carry, whatever an
// allowlist entry says. They are the shapes of the data the plan forbids in
// telemetry: identity, credentials, message content and provider payloads.
// The allowlist test iterates every admitted property against this set, so a
// future event cannot quietly reintroduce what the allowlist exists to
// prevent; the check in Scrubbed is the runtime half of the same rule.
var forbiddenPropertyNames = map[string]bool{
	"email": true, "email_address": true, "mail": true,
	"password": true, "passwd": true, "token": true, "code": true,
	"session": true, "session_id": true, "cookie": true,
	"content": true, "body": true, "message": true, "argument": true, "excerpt": true, "subject": true,
	"amount": true, "currency": true, "price_id": true, "customer": true,
	"payment_intent": true, "checkout_session": true, "provider_payload": true,
	"authorization": true, "secret": true, "api_key": true, "credential": true,
	"ip": true, "ip_address": true, "user_agent": true, "referer": true,
}

// Scrubbed validates the event against the allowlist and returns the
// property document the provider receives.
//
// Two kinds of mistake are told apart:
//
//   - an event name outside the allowlist, an unadmitted property or a
//     missing account attribution is a programming error: the whole event is
//     refused with an error that names it;
//   - an admitted property whose value does not fit its domain (a locale tag
//     outside the catalog, a number out of bounds, a value of the wrong
//     type) is dropped from the document and the event still travels: the
//     value came from request-adjacent data, and telemetry is best-effort.
func Scrubbed(event Event) (map[string]any, error) {
	spec, admitted := allowlist[event.Name]
	if !admitted {
		return nil, fmt.Errorf("observability: event %q is not allowlisted (see internal/platform/observability/events.go)", event.Name)
	}
	if event.AccountID == "" {
		return nil, fmt.Errorf("observability: event %q has no account attribution", event.Name)
	}

	properties := make(map[string]any, len(event.Properties))
	for name, value := range event.Properties {
		property, known := spec[name]
		if !known {
			return nil, fmt.Errorf("observability: event %q does not admit property %q", event.Name, name)
		}
		if forbiddenPropertyNames[name] {
			return nil, fmt.Errorf("observability: property %q is forbidden in telemetry", name)
		}

		switch property.kind {
		case kindLocale:
			text, isText := value.(string)
			if !isText || !locale.IsSupported(locale.Tag(text)) {
				continue
			}
			properties[name] = text
		case kindInteger:
			number, isInteger := integerValue(value)
			if !isInteger || number < property.min || number > property.max {
				continue
			}
			properties[name] = number
		}
	}
	return properties, nil
}

// integerValue admits the two integer shapes a call site passes without a
// conversion, and nothing else: a float that arrived from request parsing is
// not silently truncated into analytics.
func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

// maxTagLength bounds one error-report tag. Request identifiers are already
// bounded by the platform middleware; this is the second half of the same
// contract, applied where the value becomes a Sentry tag.
const maxTagLength = 64

// SanitizedTags renders the stable fields of an error report as provider
// tags, dropping empties and anything that is not a short printable token. A
// tag is where an unexpected error would echo its input, so the rule is
// allowlist-by-shape: what does not look like an operation name is dropped.
func SanitizedTags(report ErrorReport) map[string]any {
	tags := make(map[string]any, 3)
	for name, value := range map[string]string{
		"kind":       report.Kind,
		"operation":  report.Operation,
		"request_id": report.RequestID,
	} {
		if value == "" || len(value) > maxTagLength || !isPrintableToken(value) {
			continue
		}
		tags[name] = value
	}
	return tags
}

// isPrintableToken reports whether every byte is printable ASCII without a
// control character. A newline in a tag is a forged log line at the provider.
func isPrintableToken(value string) bool {
	for index := 0; index < len(value); index++ {
		if character := value[index]; character < ' ' || character > '~' {
			return false
		}
	}
	return true
}
