package postgres

import (
	"context"
	"errors"
	"fmt"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

// maintenanceLockKey is the advisory lock of the maintenance scheduler.
//
// The value is written down and must never change: two deployments that
// disagreed about it would both run the same pass during a rolling update,
// which is exactly the duplicate work the lock exists to prevent. It is a
// plain documented constant, not a hash of the module name, so changing it is
// a visible edit in a review.
const maintenanceLockKey int64 = 0x4a4f4253_4d41494e

// Locker implements the maintenance exclusivity port with a PostgreSQL
// advisory lock (P15-T05).
//
// The lock is transaction-scoped rather than session-scoped, and that is the
// whole design. A session lock has to be released on the same connection that
// took it, so the adapter would hold a dedicated connection and would leak the
// lock whenever the process died without releasing it. A transaction-scoped
// lock is released by the database on commit, on rollback and on a dropped
// connection, so a crashed instance cannot leave the platform unable to run
// maintenance — the failure mode that matters, because an operator noticing a
// stuck lock at three in the morning has no way to release it safely by hand.
//
// The scheduler's pass therefore runs inside one transaction: the periods it
// queues commit together, under a lock the database guarantees is held once.
type Locker struct {
	manager *platformpg.TxManager
}

var _ jobsapp.MaintenanceLock = (*Locker)(nil)

// NewLocker wires the adapter with the shared transaction manager.
func NewLocker(manager *platformpg.TxManager) (*Locker, error) {
	if manager == nil {
		return nil, errors.New("jobs: maintenance locker needs a transaction manager")
	}
	return &Locker{manager: manager}, nil
}

// WithLock runs fn while holding the maintenance lock. When another instance
// holds it, fn is not called and the result reports that the lock was not
// acquired — the normal outcome of a duplicated deployment, not a failure.
func (l *Locker) WithLock(ctx context.Context, fn func(ctx context.Context) error) (bool, error) {
	if l == nil || l.manager == nil {
		return false, errors.New("jobs: maintenance locker is not wired")
	}
	if ctx == nil {
		return false, errors.New("jobs: nil context")
	}
	if fn == nil {
		return false, errors.New("jobs: maintenance callback is nil")
	}
	acquired := false
	err := l.manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		tx, ok := platformpg.TxFromContext(txCtx)
		if !ok {
			return errors.New("jobs: shared transaction is missing")
		}
		var took bool
		if err := tx.QueryRow(txCtx, "SELECT pg_try_advisory_xact_lock($1)", maintenanceLockKey).Scan(&took); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("jobs: advisory lock query returned no row")
			}
			return fmt.Errorf("jobs: try advisory lock: %w", err)
		}
		if !took {
			// Not acquired: the transaction has nothing to commit. It is
			// ended by the manager, which releases nothing because there is
			// nothing to release.
			return nil
		}
		acquired = true
		return fn(txCtx)
	})
	if err != nil {
		return acquired, err
	}
	return acquired, nil
}
