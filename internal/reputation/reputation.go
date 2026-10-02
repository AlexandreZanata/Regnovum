// Package reputation owns professional standing from verifiable
// events: deadlines met, reversals suffered, conflicts declared
// and decisions upheld, per role and season. Standing is factual
// history, never an opaque single score, and historic merit
// grants no wealth or office. Production stays disabled until
// P44; this module holds decision-free mechanics only, with no
// wiring, no routes and no movement of value.
//
// The domain layer (internal/reputation/domain) holds value
// objects and errors using only the Go standard library.
package reputation

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "reputation"
