// Package commerce owns the voluntary transfer taxonomy: personal
// gifts without consideration, formal commercial payments,
// refunds and treasury movements. Production stays disabled until
// P44; this module holds decision-free mechanics only, with no
// wiring, no routes and no money movement.
//
// The domain layer (internal/commerce/domain) holds value objects
// and errors using only the Go standard library. Rates, thresholds
// and tithe shares arrive per call in later tasks, never as
// ratified values here.
package commerce

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "commerce"
