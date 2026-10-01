// Package crown owns the seasonal game office: sovereign identity,
// delegation and act authorization for the Crown. The King is a game
// office held by a participant for one season and reign, never the
// technical operator and never the owner of the Treasury. Production
// stays disabled until P44; this module holds decision-free
// mechanics only, with no wiring, no routes and no movement of value.
//
// The domain layer (internal/crown/domain) holds value objects and
// errors using only the Go standard library. Seasons, reigns,
// instants and session windows arrive per call, never as ratified
// values here. Wealth selection of the holder arrives in P47;
// tests and fakes never become a fixed holder in production.
package crown

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "crown"
