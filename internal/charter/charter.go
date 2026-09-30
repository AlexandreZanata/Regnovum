// Package charter owns the versioned constitutional rules: immutable
// Charter releases with hash, locale, effective time and a link to
// the previous release, plus the effective-time mechanics that bind
// facts to their contemporary rule. Production stays disabled until
// P44; this module holds decision-free mechanics only, with no
// wiring, no routes and no movement of value.
//
// The domain layer (internal/charter/domain) holds value objects
// and errors using only the Go standard library. Versions, locales
// and effective instants arrive per call in later tasks, never as
// ratified values here.
package charter

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "charter"
