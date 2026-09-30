// Package logging configures the structured JSON logger of Regnovum and
// hosts the central redaction applied to log values (P02-T04).
//
// Sentry is deliberately not integrated at this stage, per the plan.
//
// Redaction design: sensitive values must never reach a log record. The
// RedactValue helper replaces dangerous strings before they are handed to
// the logger, and tests prove the absence of cookies, authorization
// headers, emails and raw request bodies in captured output.
package logging

import (
	"log/slog"
	"os"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

// redactedPlaceholder replaces any recognized sensitive value.
const redactedPlaceholder = "[REDACTED]"

// sensitiveMarkers are substrings that, when found in a value, identify it
// as sensitive: session cookies, authorization headers and bearer tokens.
var sensitiveMarkers = []string{
	"arena_session=",
	"arena_session",
	"authorization:",
	"bearer ",
	"basic ",
	"postgres://",
	"postgresql://",
	"password=",
	"$argon2id$",
}

// RedactValue returns the value safe for logging: recognized sensitive
// payloads collapse to [REDACTED]; everything else flows through unchanged.
// Raw request or response bodies (JSON-shaped payloads) are never logged —
// extract the safe fields explicitly instead. Emails are handled by
// RedactEmail to keep the rule explicit at call sites.
func RedactValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return redactedPlaceholder
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range sensitiveMarkers {
		if strings.Contains(lower, marker) {
			return redactedPlaceholder
		}
	}
	return value
}

// RedactEmail masks the local part of an email address, keeping the domain
// for operational correlation without exposing personal data:
// operator@example.com -> o***@example.com.
func RedactEmail(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return redactedPlaceholder
	}
	return address[:1] + "***" + address[at:]
}

// New builds the production JSON logger writing to writer with the level
// from the typed configuration. slog handles key-value records; callers are
// still responsible for passing values through RedactValue/RedactEmail when
// they may contain personal data.
func New(writer *os.File, level config.LogLevel) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: slogLevel(level),
	}))
}

// slogLevel maps the configuration vocabulary to slog levels.
func slogLevel(level config.LogLevel) slog.Level {
	switch level {
	case config.LogLevelDebug:
		return slog.LevelDebug
	case config.LogLevelWarn:
		return slog.LevelWarn
	case config.LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
