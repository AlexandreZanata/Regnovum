// Package crumbs owns the weekly crumb mechanics: deterministic ISO
// epochs in UTC, classified treasury reflux, the R4 budget, newcomer
// eligibility and fixed-stock distribution. Production stays disabled
// until P44; this module holds decision-free mechanics only, with no
// wiring, no routes and no money movement.
//
// The domain layer (internal/crumbs/domain) holds value objects
// and errors using only the Go standard library. Rates, thresholds
// and budgets arrive per call in later tasks, never as ratified
// values here.
package crumbs

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "crumbs"
