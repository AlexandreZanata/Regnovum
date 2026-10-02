// Package institutions owns the institutional book: office acts
// with author, competence, rule, date, effect and linked
// correction, a privacy-safe public summary and a full record
// under access control. Production stays disabled until P44;
// this module holds decision-free mechanics only, with no
// wiring, no routes and no movement of value.
//
// The domain layer (internal/institutions/domain) holds value
// objects and errors using only the Go standard library.
package institutions

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "institutions"
