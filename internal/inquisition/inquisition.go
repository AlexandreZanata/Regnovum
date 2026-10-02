// Package inquisition owns the severe-case safety jurisdiction:
// the charter violations that authorize institutional action
// without a specific acceptance, kept apart from private
// debt and private challenge. Production stays disabled until
// P44; this module holds decision-free mechanics only, with no
// wiring, no routes and no movement of value.
//
// The domain layer (internal/inquisition/domain) holds value
// objects and errors using only the Go standard library. Safety
// is persistent across seasons: reports carry no season book,
// and "death" here means the digital service state, never harm
// to a person or deletion of data.
package inquisition

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "inquisition"
