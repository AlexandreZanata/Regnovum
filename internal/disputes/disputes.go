// Package disputes owns voluntary private dispute terms: versioned
// bilateral proposals with sealed terms and acceptance by both named
// parties before any obligation exists. Production stays disabled
// until P44; this module holds decision-free mechanics only, with no
// wiring, no routes and no movement of value.
//
// The domain layer (internal/disputes/domain) holds value objects
// and errors using only the Go standard library. Objects, rites,
// costs and deadlines arrive per call in later tasks, never as
// ratified values here.
package disputes

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "disputes"
