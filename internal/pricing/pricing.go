// Package pricing owns the BTC/BRL reference rate plumbing: independent
// source observations with price, source, instant and provenance,
// guarded by timeouts and circuit breakers. Production stays disabled
// until P44; this module holds decision-free mechanics only, with no
// real-money flow and no approved live provider.
//
// The domain layer (internal/pricing/domain) holds value objects and
// errors using only the Go standard library. Prices travel as integer
// minor units (never float); freshness windows arrive as explicit
// parameters per call, never as ratified policy.
package pricing

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "pricing"
