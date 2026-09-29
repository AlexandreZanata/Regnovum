package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Purchase reconciliation surface of the billing adapter (P35-T07):
// one pass compares gateway events, intents, settlements, holds and
// legs, alerts divergences without fixing them, and advances proven
// terminal failures with their hold release. Every transition is
// reported: nothing here corrects silently, and holds release only
// on terminal failure, never on divergence, never on review.
var _ application.PurchaseReconciliationRepository = (*ReconciliationRepository)(nil)

// ReconciliationRepository runs purchase reconciliation passes
// against PostgreSQL.
type ReconciliationRepository struct {
	pool  *pgxpool.Pool
	clock ports.Clock
}

// NewReconciliationRepository builds the repository with explicit
// wiring: the pool and the clock judging quotation expiry.
func NewReconciliationRepository(pool *pgxpool.Pool, clock ports.Clock) (*ReconciliationRepository, error) {
	if pool == nil || clock == nil {
		return nil, application.ErrInvalidPurchaseIntentConfig
	}
	return &ReconciliationRepository{pool: pool, clock: clock}, nil
}

// pendingIntent is one awaiting purchase as the pass reads it.
type pendingIntent struct {
	id           string
	key          string
	account      string
	holdID       string
	quoteExpires time.Time
}

// Reconcile compares the four books intent by intent and advances
// proven terminals, returning everything it found and everything it
// did. Settled intents transition with a paid capture behind them;
// failed verdicts and silent lapses transition with their hold
// released; divergences alert without moving anything; live pending
// intents wait untouched. Re-running is safe: settled and failed
// never reopen, so a second pass only repeats the alerts.
func (r *ReconciliationRepository) Reconcile(ctx context.Context) (*application.ReconciliationReport, error) {
	now := r.clock.Now().UTC()
	intents, err := r.pendingIntents(ctx)
	if err != nil {
		return nil, err
	}
	report := &application.ReconciliationReport{}
	for _, intent := range intents {
		paid, err := r.hasVerdict(ctx, intent, "paid")
		if err != nil {
			return nil, err
		}
		settled, err := r.hasSettlement(ctx, intent.id)
		if err != nil {
			return nil, err
		}
		switch {
		case settled:
			if err := r.transition(ctx, intent.id, "settled", report); err != nil {
				return nil, err
			}
			if !paid {
				report.Alerts = append(report.Alerts, application.ReconciliationAlert{
					Kind:      application.AlertTransferWithoutCapture,
					IntentKey: intent.key,
					Detail:    "settlement without a paid gateway event: hold for human review, do not adjust",
				})
			}
		case paid:
			report.Alerts = append(report.Alerts, application.ReconciliationAlert{
				Kind:      application.AlertCapturedWithoutTransfer,
				IntentKey: intent.key,
				Detail:    "paid gateway event without settlement: retry the event, do not adjust",
			})
		default:
			failed, err := r.hasVerdict(ctx, intent, "failed")
			if err != nil {
				return nil, err
			}
			expired := !now.Before(intent.quoteExpires)
			if !failed && !expired {
				continue
			}
			released, err := r.failIntent(ctx, intent)
			if err != nil {
				return nil, err
			}
			if released {
				report.Transitions = append(report.Transitions, application.ReconciliationTransition{
					IntentKey: intent.key, From: "pending", To: "failed", Released: true,
				})
			}
		}
	}
	return report, nil
}

// pendingIntents lists every awaiting purchase with its backing hold
// and quotation expiry, oldest first.
func (r *ReconciliationRepository) pendingIntents(ctx context.Context) ([]pendingIntent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT i.id::text, i.intent_key, i.account_id::text, i.hold_id::text, q.expires_at
		 FROM app.billing_ink_intents i JOIN app.pricing_quotes q ON q.id = i.quote_id
		 WHERE i.status = 'pending' ORDER BY i.decided_at, i.id`)
	if err != nil {
		return nil, fmt.Errorf("list pending intents: %w", err)
	}
	defer rows.Close()
	intents := []pendingIntent{}
	for rows.Next() {
		var intent pendingIntent
		if err := rows.Scan(&intent.id, &intent.key, &intent.account, &intent.holdID, &intent.quoteExpires); err != nil {
			return nil, fmt.Errorf("scan pending intent: %w", err)
		}
		intents = append(intents, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending intents: %w", err)
	}
	return intents, nil
}

// hasVerdict reports whether the gateway declared the verdict for the
// intent: redelivered verdicts count once, silence counts as none.
func (r *ReconciliationRepository) hasVerdict(ctx context.Context, intent pendingIntent, verdict string) (bool, error) {
	var found bool
	err := r.pool.QueryRow(ctx,
		`SELECT true FROM app.billing_ink_charge_events
		 WHERE account_id = $1::uuid AND intent_key = $2 AND status = $3 LIMIT 1`,
		intent.account, intent.key, verdict).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read charge verdicts: %w", err)
	}
	return found, nil
}

// hasSettlement reports whether the intent already liquidated.
func (r *ReconciliationRepository) hasSettlement(ctx context.Context, intentID string) (bool, error) {
	var found bool
	err := r.pool.QueryRow(ctx,
		`SELECT true FROM app.billing_ink_settlements WHERE intent_id = $1::uuid LIMIT 1`,
		intentID).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read settlements: %w", err)
	}
	return found, nil
}

// transition moves one pending intent along a closed edge, reporting
// it. A concurrent pass that won the edge first leaves zero rows:
// the intent is already where this pass aimed, so silence is honest.
func (r *ReconciliationRepository) transition(ctx context.Context, intentID, to string, report *application.ReconciliationReport) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.billing_ink_intents SET status = $2 WHERE id = $1::uuid AND status = 'pending'`,
		intentID, to)
	if err != nil {
		return fmt.Errorf("transition intent: %w", err)
	}
	if tag.RowsAffected() == 1 {
		var key string
		if err := r.pool.QueryRow(ctx,
			`SELECT intent_key FROM app.billing_ink_intents WHERE id = $1::uuid`, intentID).Scan(&key); err != nil {
			return fmt.Errorf("resolve transitioned intent: %w", err)
		}
		report.Transitions = append(report.Transitions, application.ReconciliationTransition{
			IntentKey: key, From: "pending", To: to,
		})
	}
	return nil
}

// failIntent releases one intent with its hold back to the commercial
// vault in a single transaction: the intent fails, the hold releases,
// and the legs return every unit. Anything less than a full commit
// rolls back, so a torn failure never strands half a release.
func (r *ReconciliationRepository) failIntent(ctx context.Context, intent pendingIntent) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin fail intent transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE app.billing_ink_intents SET status = 'failed' WHERE id = $1::uuid AND status = 'pending'`,
		intent.id)
	if err != nil {
		return false, fmt.Errorf("fail intent: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	var owner, holdCustody string
	var amount int64
	err = tx.QueryRow(ctx,
		`SELECT owner_custody_id::text, hold_custody_id::text, amount_milli
		 FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`, intent.holdID).Scan(&owner, &holdCustody, &amount)
	if err != nil {
		return false, fmt.Errorf("lock backing hold: %w", err)
	}
	var transfer string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		return false, fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transfer, holdCustody, owner, amount); err != nil {
		return false, fmt.Errorf("record release legs: %w", err)
	}
	now := r.clock.Now().UTC()
	if _, err := tx.Exec(ctx,
		`UPDATE app.economy_holds SET status = 'released', closed_at = $2 WHERE id = $1::uuid`,
		intent.holdID, now); err != nil {
		return false, fmt.Errorf("release backing hold: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit fail intent: %w", err)
	}
	return true, nil
}
