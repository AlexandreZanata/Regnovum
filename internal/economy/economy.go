// Package economy owns the fixed-supply Genesis currency: exact milliINK
// arithmetic, single Genesis custody and conservation. Production stays
// disabled until P44; this module holds decision-free mechanics only.
//
// The domain layer (internal/economy/domain) holds value objects and errors
// using only the Go standard library. It is deliberately isolated from the
// legacy wallet ledger (FREE_INK/PURCHASED_INK): the two books never mix by
// rename, cast or alias. Adapters arrive in later P32 tasks and must live
// under internal/economy/adapters.
package economy

// ModuleName identifies this module in logs, metrics and audit events.
const ModuleName = "economy"
