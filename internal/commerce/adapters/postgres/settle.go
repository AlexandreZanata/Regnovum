package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// AcceptDelivery records the buyer delivery acceptance on a funded
// contract. Repeat acceptances resolve without writing: the state
// is already acceptance.
func (r *EscrowRepository) AcceptDelivery(ctx context.Context, key, buyer string) (*application.ContractView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin accept transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	row, err := scanEscrowRow(ctx, tx, buyer, key)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, commercedomain.ErrContractNotFound
	}
	terms, err := escrowTerms(row)
	if err != nil {
		return nil, err
	}
	if _, err := terms.Accept(buyer); err != nil {
		if errors.Is(err, commercedomain.ErrContractState) {
			return r.lookupContract(ctx, tx, buyer, key)
		}
		return nil, err
	}
	if err := recordAcceptStep(ctx, tx, row.view.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit acceptance: %w", err)
	}
	return r.lookupContract(ctx, r.pool, buyer, key)
}

// ReleaseContract pays an accepted escrow to the provider. The buyer
// acceptance recorded beforehand is the authorization: a release
// without it refuses, and a second release refuses on the terminal
// state and the single-settlement guard.
func (r *EscrowRepository) ReleaseContract(ctx context.Context, key, buyer string) (*application.ContractView, error) {
	return r.moveEscrow(ctx, settleOrder{key: key, buyer: buyer, action: "release", toProvider: true}, func(row *escrowRow) error {
		terms, err := escrowTerms(row)
		if err != nil {
			return err
		}
		_, err = terms.Release()
		return err
	})
}

// CancelContract refunds a funded contract to the buyer: funded
// contracts only, before any acceptance.
func (r *EscrowRepository) CancelContract(ctx context.Context, key, buyer string) (*application.ContractView, error) {
	return r.moveEscrow(ctx, settleOrder{key: key, buyer: buyer, action: "refund"}, func(row *escrowRow) error {
		terms, err := escrowTerms(row)
		if err != nil {
			return err
		}
		_, err = terms.Cancel()
		return err
	})
}

// ExpireContract marks a lapsed contract without moving any leg:
// funded and accepted contracts past their expiry only. Expiry
// never delivers by itself.
func (r *EscrowRepository) ExpireContract(ctx context.Context, key, buyer string, at time.Time) (*application.ContractView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin expire transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	row, err := scanEscrowRow(ctx, tx, buyer, key)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, commercedomain.ErrContractNotFound
	}
	terms, err := escrowTerms(row)
	if err != nil {
		return nil, err
	}
	if _, err := terms.Expire(at); err != nil {
		if errors.Is(err, commercedomain.ErrContractState) {
			return r.lookupContract(ctx, tx, buyer, key)
		}
		return nil, err
	}
	if err := recordExpireStep(ctx, tx, row.view.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit expiry: %w", err)
	}
	return r.lookupContract(ctx, r.pool, buyer, key)
}

// ResolveContract settles an expired contract by competent
// decision, paying the provider or refunding the buyer under a new
// transfer. The decider travels recorded; who may decide is
// governance beyond this phase.
func (r *EscrowRepository) ResolveContract(ctx context.Context, key, buyer, decider string, decision commercedomain.ResolveDecision) (*application.ContractView, error) {
	action := "resolve-refund"
	toProvider := false
	if decision == commercedomain.ResolveRelease {
		action = "resolve-release"
		toProvider = true
	}
	return r.moveEscrow(ctx, settleOrder{key: key, buyer: buyer, action: action, decider: decider, toProvider: toProvider}, func(row *escrowRow) error {
		terms, err := escrowTerms(row)
		if err != nil {
			return err
		}
		_, err = terms.Resolve(decision)
		return err
	})
}

// settleOrder carries one money-moving lifecycle step: the key,
// the owner scope, the settlement action, the recorded decider and
// whether the escrow pays the provider instead of the buyer.
type settleOrder struct {
	key        string
	buyer      string
	action     string
	decider    string
	toProvider bool
}

// moveEscrow runs one money-moving lifecycle step in a single
// transaction: the domain machine authorizes from the current
// status, the escrow balance is rechecked exact, the legs move
// under a new transfer and the settlement row names the contract. A
// unique collision on the terminal guard re-reads the state instead
// of paying twice.
func (r *EscrowRepository) moveEscrow(ctx context.Context, order settleOrder, authorize func(*escrowRow) error) (*application.ContractView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin escrow transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	row, replay, err := r.loadAuthorizedEscrow(ctx, tx, order, authorize)
	if err != nil || replay != nil {
		return replay, err
	}
	if err := requireCommerceBookActiveTx(ctx, tx, row.view.Season); err != nil {
		return nil, err
	}
	if order.toProvider {
		return r.settleProvider(ctx, tx, row, order)
	}
	return r.settleBuyer(ctx, tx, row, order)
}

// loadAuthorizedEscrow scans one buyer contract and authorizes the
// step from its status. A repeat of the settled action replays the
// current view instead of refusing: repetition of one intention
// observes one effect. Another action on a moved state refuses.
func (r *EscrowRepository) loadAuthorizedEscrow(ctx context.Context, tx pgx.Tx, order settleOrder, authorize func(*escrowRow) error) (*escrowRow, *application.ContractView, error) {
	row, err := scanEscrowRow(ctx, tx, order.buyer, order.key)
	if err != nil || row == nil {
		if err == nil {
			return nil, nil, commercedomain.ErrContractNotFound
		}
		return nil, nil, err
	}
	if err := authorize(row); err != nil {
		if errors.Is(err, commercedomain.ErrContractState) && terminalMatchesAction(row.view.Status, order.action) {
			view, err := r.lookupContract(ctx, tx, order.buyer, order.key)
			return nil, view, err
		}
		return nil, nil, err
	}
	return row, nil, nil
}

// resolveProviderPayout computes the tithe split and resolves the
// provider and Treasury custodies in the original book before any
// lock is taken. The tithe parks in the genesis home of the same
// book the escrow locked in.
func resolveProviderPayout(ctx context.Context, tx pgx.Tx, row *escrowRow) (payoutPlan, error) {
	tithe, net, err := commercedomain.SplitTithe(row.view.AmountMill)
	if err != nil {
		return payoutPlan{}, err
	}
	season := row.view.Season.String()
	if season == "" {
		season = commercedomain.CompatSeasonKey
	}
	toID, err := resolveSeasonCommerceCustody(ctx, tx, "user", row.providerID, row.view.Season)
	if err != nil {
		return payoutPlan{}, err
	}
	plan := payoutPlan{toID: toID, tithe: tithe, net: net}
	if tithe > 0 {
		treasuryID, err := resolveTitheTreasury(ctx, tx, season)
		if err != nil {
			return payoutPlan{}, err
		}
		plan.treasuryID = treasuryID
	}
	return plan, nil
}

// resolveBuyerPayout resolves the buyer custody in the original
// book for a whole refund: refunds never bear tithe.
func resolveBuyerPayout(ctx context.Context, tx pgx.Tx, row *escrowRow) (payoutPlan, error) {
	toID, err := resolveSeasonCommerceCustody(ctx, tx, "user", row.view.Buyer, row.view.Season)
	if err != nil {
		return payoutPlan{}, err
	}
	return payoutPlan{toID: toID}, nil
}

// verifyEscrowLocked rechecks the exact escrow balance inside the
// locks. A concurrent terminal settlement may have drained the
// escrow between the status read and this check: the race resolves
// instead of mistaking a won race for a broken lock. A handled race
// returns its view with handled true; covered escrow returns
// handled false.
func (r *EscrowRepository) verifyEscrowLocked(ctx context.Context, tx pgx.Tx, row *escrowRow, order settleOrder) (bool, *application.ContractView, error) {
	locked, err := platformpg.LedgerBalanceMillis(ctx, tx, row.escrowID)
	if err != nil {
		return false, nil, err
	}
	if locked == row.view.AmountMill {
		return false, nil, nil
	}
	if rbErr := tx.Rollback(ctx); rbErr != nil {
		return false, nil, fmt.Errorf("abort drained escrow: %w", rbErr)
	}
	view, err := r.resolveDrainedRace(ctx, row.view.ID, order.buyer, order.key, order.action)
	return true, view, err
}

// commitPayoutSettlement stores the terminal step and commits the
// payout. A concurrent terminal settlement winning the guard
// replays its view instead of paying twice.
func (r *EscrowRepository) commitPayoutSettlement(ctx context.Context, tx pgx.Tx, row *escrowRow, order settleOrder, transferID string) (*application.ContractView, error) {
	terminal, err := recordTerminalStep(ctx, tx, row.view.ID, order.action, transferID, order.decider)
	if err != nil {
		return nil, err
	}
	if terminal {
		return r.resolveTerminalRace(ctx, tx, order.buyer, order.key, order.action)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit settlement: %w", err)
	}
	return r.lookupContract(ctx, r.pool, order.buyer, order.key)
}

// settleProvider pays an accepted escrow to the provider with the
// floor(10%) tithe split in the same transaction. The tithe parks
// in the genesis home of the original book.
func (r *EscrowRepository) settleProvider(ctx context.Context, tx pgx.Tx, row *escrowRow, order settleOrder) (*application.ContractView, error) {
	plan, err := resolveProviderPayout(ctx, tx, row)
	if err != nil {
		return nil, err
	}
	if err := lockPayoutCustodies(ctx, tx, row.escrowID, plan.toID, plan.treasuryID, plan.tithe); err != nil {
		return nil, err
	}
	if handled, view, err := r.verifyEscrowLocked(ctx, tx, row, order); err != nil || handled {
		return view, err
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	legs := PayoutLegs{
		TransferID: transferID, EscrowID: row.escrowID, ToID: plan.toID,
		TreasuryID: plan.treasuryID, Amount: row.view.AmountMill,
		Tithe: plan.tithe, Net: plan.net, Season: row.view.Season.String(),
	}
	if err := recordPayoutLegs(ctx, tx, legs); err != nil {
		return nil, err
	}
	return r.commitPayoutSettlement(ctx, tx, row, order, transferID)
}

// settleBuyer refunds one escrow whole to the buyer: refunds never
// bear tithe. The return carries the original book.
func (r *EscrowRepository) settleBuyer(ctx context.Context, tx pgx.Tx, row *escrowRow, order settleOrder) (*application.ContractView, error) {
	plan, err := resolveBuyerPayout(ctx, tx, row)
	if err != nil {
		return nil, err
	}
	if err := lockPayoutCustodies(ctx, tx, row.escrowID, plan.toID, "", 0); err != nil {
		return nil, err
	}
	if handled, view, err := r.verifyEscrowLocked(ctx, tx, row, order); err != nil || handled {
		return view, err
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	legs := PayoutLegs{
		TransferID: transferID, EscrowID: row.escrowID, ToID: plan.toID,
		Amount: row.view.AmountMill, Season: row.view.Season.String(),
	}
	if err := recordPayoutLegs(ctx, tx, legs); err != nil {
		return nil, err
	}
	return r.commitPayoutSettlement(ctx, tx, row, order, transferID)
}

// resolveTerminalRace settles a lost terminal race inside the
// doomed transaction: same action replays the winner view, another
// action meets the moved state. The caller rolls back either way,
// so no second payment exists.
func (r *EscrowRepository) resolveTerminalRace(ctx context.Context, q rowQuerier, buyer, key, action string) (*application.ContractView, error) {
	latest, err := latestSettlementAction(ctx, q, buyer, key)
	if err != nil {
		return nil, err
	}
	if latest != action {
		return nil, commercedomain.ErrContractState
	}
	return r.lookupContract(ctx, q, buyer, key)
}

// resolveDrainedRace settles a race lost between the status read
// and the escrow balance check: the winner view replays for the
// same action, another action meets the moved state.
func (r *EscrowRepository) resolveDrainedRace(ctx context.Context, contractID, buyer, key, action string) (*application.ContractView, error) {
	latest, err := latestSettlementAction(ctx, r.pool, buyer, key)
	if err != nil {
		return nil, err
	}
	if latest != action {
		return nil, commercedomain.ErrContractState
	}
	return r.lookupContract(ctx, r.pool, buyer, key)
}

// latestSettlementAction reads the newest settlement step of one
// buyer contract.
func latestSettlementAction(ctx context.Context, q rowQuerier, buyer, key string) (string, error) {
	var contractID, latest string
	err := q.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE buyer_id = $1::uuid AND contract_key = $2`,
		buyer, key).Scan(&contractID)
	if err != nil {
		return "", fmt.Errorf("read race contract: %w", err)
	}
	err = q.QueryRow(ctx,
		`SELECT action FROM app.commerce_settlements WHERE contract_id = $1::uuid
		 ORDER BY posted_at DESC, id DESC LIMIT 1`, contractID).Scan(&latest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", commercedomain.ErrContractState
		}
		return "", fmt.Errorf("read race action: %w", err)
	}
	return latest, nil
}

// terminalMatchesAction reports whether a terminal status is the
// outcome of the requested action: repeated settlements replay,
// crossed ones refuse.
func terminalMatchesAction(status commercedomain.ContractStatus, action string) bool {
	switch action {
	case "release":
		return status == commercedomain.ContractReleased
	case "refund":
		return status == commercedomain.ContractRefunded
	case "resolve-release", "resolve-refund":
		return status == commercedomain.ContractResolved
	default:
		return false
	}
}

// escrowTerms rebuilds the domain contract from one stored row for
// machine judgments.
func escrowTerms(row *escrowRow) (commercedomain.TradeContract, error) {
	terms := commercedomain.TradeContract{
		Key: row.view.Key, Object: row.view.Object, Buyer: row.view.Buyer,
		Provider: row.view.Provider, AmountMill: row.view.AmountMill,
		ExpiresAt: row.view.ExpiresAt, Status: row.view.Status, Hash: row.view.Hash,
	}
	if err := terms.VerifyHash(); err != nil {
		return commercedomain.TradeContract{}, fmt.Errorf("stored contract fails its seal: %w", err)
	}
	return terms, nil
}

// recordAcceptStep stores one acceptance or lapse marking without
// legs. Repeats resolve through the caller re-read instead.
func recordAcceptStep(ctx context.Context, tx pgx.Tx, contractID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO app.commerce_settlements (contract_id, action) VALUES ($1::uuid, 'accept')`,
		contractID)
	if err != nil {
		return fmt.Errorf("record acceptance: %w", err)
	}
	return nil
}

// recordExpireStep stores one lapse marking without legs.
func recordExpireStep(ctx context.Context, tx pgx.Tx, contractID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO app.commerce_settlements (contract_id, action) VALUES ($1::uuid, 'expire')`,
		contractID)
	if err != nil {
		return fmt.Errorf("record expiry: %w", err)
	}
	return nil
}

// recordTerminalStep stores one money-moving settlement, reporting
// whether a concurrent terminal settlement won the race through the
// single-settlement guard.
func recordTerminalStep(ctx context.Context, tx pgx.Tx, contractID, action, transferID, decidedBy string) (bool, error) {
	_, err := tx.Exec(ctx,
		`INSERT INTO app.commerce_settlements (contract_id, action, transfer_id, decided_by)
		 VALUES ($1::uuid, $2, $3::uuid, NULLIF($4, ''))`,
		contractID, action, transferID, decidedBy)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return true, nil
		}
		return false, fmt.Errorf("record settlement: %w", err)
	}
	return false, nil
}
