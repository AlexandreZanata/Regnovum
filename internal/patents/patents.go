// Package patents owns the social contract of honorific patents:
// a non-transferable cosmetic status bound to one season, without
// authority, vote, truth or reputation. Price, seats and duration
// arrive only in ratified terms: without them no sale exists, and
// the honor of a past season is a historic fact that grants no
// discount or power in a new cycle. Production stays disabled
// until P44; this module holds decision-free mechanics only, with
// no wiring, no routes and no movement of value.
//
// The domain layer (internal/patents/domain) holds value objects
// and errors using only the Go standard library.
package patents

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "patents"
