// Controlled transaction fault-injection port (P25-T04).
//
// Tests of critical-transaction atomicity arm one failure inside
// TxManager.WithinTransaction through the context: FailBeforeCommit runs
// the writes and then refuses the commit (proving total rollback), while
// FailAfterCommit commits for real and then reports failure (proving the
// caller resolves after-commit uncertainty by idempotency or
// reconciliation). The zero value disables the port, so production paths
// that carry no fault behave exactly as before; there is no global switch
// and no environment variable that could leak a fault into production.
package postgres

import (
	"context"
	"errors"
)

// TxFault arms one controlled failure inside a shared transaction.
type TxFault string

const (
	// TxFaultNone disables fault injection (the default).
	TxFaultNone TxFault = ""
	// TxFaultBeforeCommit runs the writes, then refuses the commit.
	TxFaultBeforeCommit TxFault = "fail-before-commit"
	// TxFaultAfterCommit commits, then reports failure.
	TxFaultAfterCommit TxFault = "fail-after-commit"
)

// ErrInjectedTxFault is the sentinel a faulted transaction reports. Tests
// match it with errors.Is to tell an injected failure apart from a genuine
// one.
var ErrInjectedTxFault = errors.New("postgres: injected transaction fault")

// txFaultKey is the unexported context key of the armed fault.
type txFaultKey struct{}

// WithTxFault returns a context carrying the given fault. The none fault
// leaves the context untouched so an unarmed path is always explicit.
func WithTxFault(ctx context.Context, fault TxFault) context.Context {
	if fault == TxFaultNone {
		return ctx
	}
	return context.WithValue(ctx, txFaultKey{}, fault)
}

// txFaultFromContext returns the fault a context carries, if any.
func txFaultFromContext(ctx context.Context) TxFault {
	if ctx == nil {
		return TxFaultNone
	}
	fault, _ := ctx.Value(txFaultKey{}).(TxFault)
	return fault
}
