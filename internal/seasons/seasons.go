// Package seasons owns the ninety-day season lifecycle: immutable
// identity, manifesto with hash, UTC windows and the staged state
// machine. Production stays disabled until P44; this module holds
// decision-free mechanics only, with no wiring, no routes and no
// money movement.
//
// The domain layer (internal/seasons/domain) holds value objects
// and errors using only the Go standard library. Clocks and policy
// arrive per call as values, never as ratified numbers here.
package seasons

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "seasons"
