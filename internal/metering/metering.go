// Package metering owns the INK metering price catalog: versioned prices
// by service and time, without any ratified value inside the code.
//
// Every price arrives per entry with its service, version, half-open
// validity window, integer milliINK amount, canonical Unicode unit and
// approving authority. Production stays disabled until P44; this module
// holds decision-free mechanics only, with no wiring, no routes and no
// money movement. Handlers must read the catalog instead of hardcoding
// any price.
//
// The domain layer (internal/metering/domain) holds value objects and
// errors using only the Go standard library.
package metering

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "metering"
