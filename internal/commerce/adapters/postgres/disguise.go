package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Disguise review surface of the commerce adapter (P37-T06): a
// settled personal gift is signalled for review with a closed reason
// and a minimized evidence digest, contested by its parties and
// settled once by dismissal (false positive, no movement) or by
// confirmation (explicit act with basis, trail, floor(10%) charge
// owed and appeal window, still no debit). Every step shares one
// transaction with the rows it writes and never writes ledger legs,
// or nothing is stored at all.
var _ application.DisguiseReviewRepository = (*DisguiseReviewRepository)(nil)

// DisguiseReviewRepository reviews suspected disguised trade against
// PostgreSQL.
type DisguiseReviewRepository struct {
	pool *pgxpool.Pool
}

// NewDisguiseReviewRepository builds the repository with explicit
// wiring.
func NewDisguiseReviewRepository(pool *pgxpool.Pool) (*DisguiseReviewRepository, error) {
	if pool == nil {
		return nil, application.ErrInvalidTransferConfig
	}
	return &DisguiseReviewRepository{pool: pool}, nil
}

// giftTransfer is one settled gift the review names.
type giftTransfer struct {
	rowID  string
	payee  string
	amount int64
}

// disguiseFlag is one stored flag with its derived status and,
// when terminal, its act.
type disguiseFlag struct {
	reviewID  string
	reviewKey string
	gift      giftTransfer
	reason    commercedomain.DisguiseReason
	evidence  string
	reporter  string
	status    commercedomain.DisguiseStatus
	basis     string
	trail     string
	charge    int64
	appeal    time.Time
	posted    time.Time
}

// FlagDisguise signals one settled gift for review, keyed
// idempotently by payer, transfer and review token. Replays resolve
// the original flag untouched; divergent terms under one key
// conflict; non-gifts refuse without writing and move nothing.
func (r *DisguiseReviewRepository) FlagDisguise(ctx context.Context, request application.FlagDisguiseRequest) (*application.DisguiseReviewView, error) {
	if _, err := commercedomain.ParseDisguiseReason(request.Reason); err != nil {
		return nil, err
	}
	if replayed, err := r.lookupDisguise(ctx, r.pool, request.Payer, request.TransferKey, request.ReviewKey); err != nil || replayed != nil {
		if err != nil {
			return nil, err
		}
		if replayed.Reason.String() != request.Reason || replayed.EvidenceHash != request.EvidenceHash || replayed.Reporter != request.Reporter {
			return nil, commercedomain.ErrIntentionConflict
		}
		return replayed, nil
	}
	var last error
	for range 2 {
		result, retry, err := r.createFlag(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupDisguise(ctx, r.pool, request.Payer, request.TransferKey, request.ReviewKey); err != nil || replayed != nil {
			if err != nil {
				return nil, err
			}
			if replayed.Reason.String() != request.Reason || replayed.EvidenceHash != request.EvidenceHash || replayed.Reporter != request.Reporter {
				return nil, commercedomain.ErrIntentionConflict
			}
			return replayed, nil
		}
	}
	return nil, fmt.Errorf("disguise flag unsettled after conflict: %w", last)
}

// ContestDisguise records one payer-or-payee challenge on a flagged
// review. Strangers refuse; contested and terminal reviews refuse
// further contests; contesting moves nothing.
func (r *DisguiseReviewRepository) ContestDisguise(ctx context.Context, request application.ContestDisguiseRequest) (*application.DisguiseReviewView, error) {
	var last error
	for range 2 {
		result, retry, err := r.createContest(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupDisguise(ctx, r.pool, request.Payer, request.TransferKey, request.ReviewKey); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("disguise contest unsettled after conflict: %w", last)
}

// ResolveDisguise settles one flagged or contested review exactly
// once: dismiss clears a false positive with no movement, confirm
// records the act with basis, trail, explicit charge and appeal
// window, still with no debit.
func (r *DisguiseReviewRepository) ResolveDisguise(ctx context.Context, request application.ResolveDisguiseRequest) (*application.DisguiseReviewView, error) {
	if _, err := commercedomain.ParseDisguiseDecision(request.Decision); err != nil {
		return nil, err
	}
	if request.Now.IsZero() || request.AppealUntil.IsZero() || !request.AppealUntil.After(request.Now) {
		return nil, commercedomain.ErrInvalidDisguise
	}
	var last error
	for range 2 {
		result, retry, err := r.createResolution(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupDisguise(ctx, r.pool, request.Payer, request.TransferKey, request.ReviewKey); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("disguise resolution unsettled after conflict: %w", last)
}

// disguiseQuerier covers pool and transaction reads for the review
// lookup: single-row and multi-row.
type disguiseQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// lookupDisguise resolves one flag with its derived status without
// writing: the post-commit retry path that makes crash recovery
// exactly-once. Unknown transfers and unknown flags resolve to
// absence; divergent terms under one key are a conflict, never a
// replay.
func (r *DisguiseReviewRepository) lookupDisguise(ctx context.Context, q disguiseQuerier, payer, transferKey, reviewKey string) (*application.DisguiseReviewView, error) {
	flag, err := readDisguiseFlag(ctx, q, payer, transferKey, reviewKey)
	if err != nil || flag == nil {
		return nil, err
	}
	view := disguiseView(*flag, transferKey, payer)
	view.Replayed = true
	return &view, nil
}

// readGiftTransfer resolves one settled transfer and refuses
// anything but a personal gift: formal trades already bear tithe,
// refunds compensate through linked entries and treasury movements
// are not payments.
func readGiftTransfer(ctx context.Context, q platformpg.LedgerQuerier, payer, transferKey string) (giftTransfer, error) {
	var gift giftTransfer
	var kind, payee string
	var amount int64
	err := q.QueryRow(ctx,
		`SELECT id::text, kind, payee_id::text, amount_milli
		 FROM app.commerce_transfers WHERE payer_id = $1::uuid AND intention_key = $2`,
		payer, transferKey).Scan(&gift.rowID, &kind, &payee, &amount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return giftTransfer{}, commercedomain.ErrContractNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return giftTransfer{}, commercedomain.ErrUnknownAccount
		}
		return giftTransfer{}, fmt.Errorf("read flagged transfer: %w", err)
	}
	if kind != commercedomain.TransferGift.String() {
		return giftTransfer{}, commercedomain.ErrInvalidDisguise
	}
	gift.payee = payee
	gift.amount = amount
	return gift, nil
}

// readDisguiseFlag resolves one stored flag with the status derived
// from its steps: flagged with no steps yet, contested with a
// contest, terminal with a dismissal or confirmation.
func readDisguiseFlag(ctx context.Context, q disguiseQuerier, payer, transferKey, reviewKey string) (*disguiseFlag, error) {
	gift, err := readGiftTransfer(ctx, q, payer, transferKey)
	if err != nil {
		if errors.Is(err, commercedomain.ErrContractNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var flag disguiseFlag
	var reason string
	err = q.QueryRow(ctx,
		`SELECT id::text, reason, evidence_hash, reporter, posted_at
		 FROM app.commerce_disguise_reviews WHERE transfer_id = $1::uuid AND review_key = $2`,
		gift.rowID, reviewKey).Scan(&flag.reviewID, &reason, &flag.evidence, &flag.reporter, &flag.posted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read disguise review: %w", err)
	}
	parsed, err := commercedomain.ParseDisguiseReason(reason)
	if err != nil {
		return nil, fmt.Errorf("stored review holds reason %q: %w", reason, err)
	}
	flag.gift = gift
	flag.reason = parsed
	flag.reviewKey = reviewKey
	if err := fillDisguiseStatus(ctx, q, &flag); err != nil {
		return nil, err
	}
	return &flag, nil
}

// fillDisguiseStatus derives the lifecycle status from the stored
// steps: no steps is flagged, a contest is contested, a dismissal
// or confirmation is terminal with its act.
func fillDisguiseStatus(ctx context.Context, q disguiseQuerier, flag *disguiseFlag) error {
	rows, err := q.Query(ctx,
		`SELECT action, decided_by, basis, trail_hash, charge_milli, appeal_until
		 FROM app.commerce_disguise_steps WHERE review_id = $1::uuid ORDER BY posted_at, id`,
		flag.reviewID)
	if err != nil {
		return fmt.Errorf("read disguise steps: %w", err)
	}
	defer rows.Close()
	status := commercedomain.DisguiseFlagged
	for rows.Next() {
		var action, decided string
		var basis, trail *string
		var charge *int64
		var appeal *time.Time
		if err := rows.Scan(&action, &decided, &basis, &trail, &charge, &appeal); err != nil {
			return fmt.Errorf("scan disguise step: %w", err)
		}
		switch action {
		case "contest":
			status = commercedomain.DisguiseContested
		case "dismiss":
			status = commercedomain.DisguiseDismissed
			if appeal != nil {
				flag.appeal = appeal.UTC()
			}
		case "confirm":
			status = commercedomain.DisguiseConfirmed
			if basis != nil {
				flag.basis = *basis
			}
			if trail != nil {
				flag.trail = *trail
			}
			if charge != nil {
				flag.charge = *charge
			}
			if appeal != nil {
				flag.appeal = appeal.UTC()
			}
		default:
			return fmt.Errorf("stored step holds action %q: %w", action, commercedomain.ErrInvalidDisguise)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate disguise steps: %w", err)
	}
	flag.status = status
	return nil
}

// disguiseView renders one stored flag for the application port.
func disguiseView(flag disguiseFlag, transferKey, payer string) application.DisguiseReviewView {
	return application.DisguiseReviewView{
		ReviewID: flag.reviewID, TransferKey: transferKey, Payer: payer,
		Payee: flag.gift.payee, AmountMill: flag.gift.amount,
		Reason: flag.reason, EvidenceHash: flag.evidence, Reporter: flag.reporter,
		Status: flag.status, Basis: flag.basis, TrailHash: flag.trail,
		ChargeMill: flag.charge, AppealUntil: flag.appeal, PostedAt: flag.posted,
	}
}

// createFlag attempts one signal in a single transaction: the gift,
// the domain seal and the review row share it, so a flag records
// whole or not at all. No ledger leg is written: flagging never
// taxes a present.
func (r *DisguiseReviewRepository) createFlag(ctx context.Context, request application.FlagDisguiseRequest) (*application.DisguiseReviewView, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin flag transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if frozen, err := platformpg.LedgerFrozen(ctx, tx); err != nil || frozen {
		if err != nil {
			return nil, false, err
		}
		return nil, false, economydomain.ErrEconomyFrozen
	}
	gift, err := readGiftTransfer(ctx, tx, request.Payer, request.TransferKey)
	if err != nil {
		return nil, false, err
	}
	sealed, err := sealFlagRequest(request, gift)
	if err != nil {
		return nil, false, err
	}
	if replayed, err := r.lookupDisguise(ctx, tx, request.Payer, request.TransferKey, request.ReviewKey); err != nil || replayed != nil {
		if err != nil {
			return nil, false, err
		}
		if replayed.Reason != sealed.Reason || replayed.EvidenceHash != sealed.EvidenceHash || replayed.Reporter != sealed.Reporter {
			return nil, false, commercedomain.ErrIntentionConflict
		}
		return replayed, false, nil
	}
	posted, err := insertDisguiseReview(ctx, tx, gift, request)
	if err != nil {
		if isDisguiseConflict(err) {
			return nil, true, fmt.Errorf("concurrent flag race: %w", err)
		}
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit flag: %w", err)
	}
	flag := disguiseFlag{reviewID: posted.id, reviewKey: request.ReviewKey, gift: gift, reason: sealed.Reason,
		evidence: request.EvidenceHash, reporter: request.Reporter,
		status: commercedomain.DisguiseFlagged, posted: posted.at}
	view := disguiseView(flag, request.TransferKey, request.Payer)
	return &view, false, nil
}

// sealFlagRequest validates the signal envelope against the sealed
// gift: the domain owns the shape, the adapter owns the transfer.
func sealFlagRequest(request application.FlagDisguiseRequest, gift giftTransfer) (commercedomain.DisguiseFlag, error) {
	reason, err := commercedomain.ParseDisguiseReason(request.Reason)
	if err != nil {
		return commercedomain.DisguiseFlag{}, err
	}
	// The payee travels from the settled gift, never from the
	// caller: a forged request cannot re-address a flag.
	return commercedomain.FlagDisguise(commercedomain.FlagRequest{
		ReviewKey: request.ReviewKey, TransferKey: request.TransferKey,
		Payer: request.Payer, Payee: gift.payee, AmountMill: gift.amount,
		Reason: reason, EvidenceHash: request.EvidenceHash, Reporter: request.Reporter,
	})
}

// postedStamp is the stored instant of one appended row.
type postedStamp struct {
	id string
	at time.Time
}

// insertDisguiseReview appends one flag row.
func insertDisguiseReview(ctx context.Context, tx pgx.Tx, gift giftTransfer, request application.FlagDisguiseRequest) (postedStamp, error) {
	var stamped postedStamp
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.commerce_disguise_reviews (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text, posted_at`,
		gift.rowID, request.ReviewKey, request.Reason, request.EvidenceHash, request.Reporter).Scan(&stamped.id, &stamped.at); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return postedStamp{}, commercedomain.ErrContractNotFound
		}
		return postedStamp{}, fmt.Errorf("record disguise review: %w", err)
	}
	return stamped, nil
}

// createContest attempts one challenge in a single transaction: the
// party check, the domain transition and the contest step share it.
// Contesting moves nothing.
func (r *DisguiseReviewRepository) createContest(ctx context.Context, request application.ContestDisguiseRequest) (*application.DisguiseReviewView, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin contest transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if frozen, err := platformpg.LedgerFrozen(ctx, tx); err != nil || frozen {
		if err != nil {
			return nil, false, err
		}
		return nil, false, economydomain.ErrEconomyFrozen
	}
	flag, err := readDisguiseFlag(ctx, tx, request.Payer, request.TransferKey, request.ReviewKey)
	if err != nil {
		return nil, false, err
	}
	if flag == nil {
		return nil, false, commercedomain.ErrContractNotFound
	}
	sealed := reconstituteFlag(*flag, request.TransferKey, request.Payer)
	if _, err := sealed.Contest(request.By); err != nil {
		return nil, false, err
	}
	if err := insertDisguiseStep(ctx, tx, disguiseStep{reviewID: flag.reviewID, action: "contest", decided: request.By}); err != nil {
		if isDisguiseStepConflict(err) {
			return nil, true, fmt.Errorf("concurrent contest race: %w", err)
		}
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit contest: %w", err)
	}
	view := disguiseView(*flag, request.TransferKey, request.Payer)
	view.Status = commercedomain.DisguiseContested
	return &view, false, nil
}

// createResolution attempts one outcome in a single transaction: the
// state guard, the domain transition and the terminal step share
// it. Dismissal moves nothing; confirmation records the explicit
// charge owed with basis, trail and appeal, still with no debit.
func (r *DisguiseReviewRepository) createResolution(ctx context.Context, request application.ResolveDisguiseRequest) (*application.DisguiseReviewView, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin resolve transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if frozen, err := platformpg.LedgerFrozen(ctx, tx); err != nil || frozen {
		if err != nil {
			return nil, false, err
		}
		return nil, false, economydomain.ErrEconomyFrozen
	}
	flag, err := readDisguiseFlag(ctx, tx, request.Payer, request.TransferKey, request.ReviewKey)
	if err != nil {
		return nil, false, err
	}
	if flag == nil {
		return nil, false, commercedomain.ErrContractNotFound
	}
	decision, err := commercedomain.ParseDisguiseDecision(request.Decision)
	if err != nil {
		return nil, false, err
	}
	sealed := reconstituteFlag(*flag, request.TransferKey, request.Payer)
	if _, err := sealed.Resolve(commercedomain.ResolveRequest{
		Decision: decision, Basis: request.Basis, TrailHash: request.TrailHash,
		AppealUntil: request.AppealUntil, Now: request.Now,
	}); err != nil {
		return nil, false, err
	}
	charge, basis, trail, appeal, err := resolutionAct(*flag, request, decision)
	if err != nil {
		return nil, false, err
	}
	if err := insertDisguiseStep(ctx, tx, disguiseStep{reviewID: flag.reviewID, action: string(decision), decided: "reviewer", basis: basis, trail: trail, charge: charge, appeal: appeal}); err != nil {
		if isDisguiseStepConflict(err) {
			return nil, true, fmt.Errorf("concurrent resolve race: %w", err)
		}
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit resolution: %w", err)
	}
	view := disguiseView(*flag, request.TransferKey, request.Payer)
	if decision == commercedomain.DisguiseDismiss {
		view.Status = commercedomain.DisguiseDismissed
		view.AppealUntil = request.AppealUntil.UTC()
	} else {
		view.Status = commercedomain.DisguiseConfirmed
		view.Basis = request.Basis
		view.TrailHash = request.TrailHash
		view.ChargeMill = *charge
		view.AppealUntil = request.AppealUntil.UTC()
	}
	return &view, false, nil
}

// reconstituteFlag rebuilds the sealed in-memory flag from the
// stored row so the domain owns every transition check. The seal
// binds review key, transfer, parties, amount, reason, evidence and
// reporter; status rides alongside and is checked explicitly.
func reconstituteFlag(flag disguiseFlag, transferKey, payer string) commercedomain.DisguiseFlag {
	sealed, err := commercedomain.FlagDisguise(commercedomain.FlagRequest{
		ReviewKey: flag.reviewKey, TransferKey: transferKey, Payer: payer,
		Payee: flag.gift.payee, AmountMill: flag.gift.amount,
		Reason: flag.reason, EvidenceHash: flag.evidence, Reporter: flag.reporter,
	})
	if err != nil {
		return commercedomain.DisguiseFlag{Status: flag.status}
	}
	sealed.Status = flag.status
	return sealed
}

// resolutionAct derives the terminal act: dismissals carry only the
// appeal window, confirmations carry basis, trail, the floor(10%)
// charge computed server-side and the appeal window.
func resolutionAct(flag disguiseFlag, request application.ResolveDisguiseRequest, decision commercedomain.DisguiseDecision) (*int64, *string, *string, *time.Time, error) {
	appeal := request.AppealUntil.UTC()
	if decision == commercedomain.DisguiseDismiss {
		return nil, nil, nil, &appeal, nil
	}
	charge, err := commercedomain.DisguiseCharge(flag.gift.amount)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	basis := request.Basis
	trail := request.TrailHash
	return &charge, &basis, &trail, &appeal, nil
}

// disguiseStep is one lifecycle step to append: the review, the
// action, who decided and, for terminal acts, the basis, trail,
// charge and appeal window.
type disguiseStep struct {
	reviewID string
	action   string
	decided  string
	basis    *string
	trail    *string
	charge   *int64
	appeal   *time.Time
}

// insertDisguiseStep appends one lifecycle step.
func insertDisguiseStep(ctx context.Context, tx pgx.Tx, step disguiseStep) error {
	var basisArg, trailArg, chargeArg, appealArg any
	if step.basis != nil {
		basisArg = *step.basis
	}
	if step.trail != nil {
		trailArg = *step.trail
	}
	if step.charge != nil {
		chargeArg = *step.charge
	}
	if step.appeal != nil {
		appealArg = *step.appeal
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by, basis, trail_hash, charge_milli, appeal_until)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
		step.reviewID, step.action, step.decided, basisArg, trailArg, chargeArg, appealArg); err != nil {
		return fmt.Errorf("record disguise step: %w", err)
	}
	return nil
}

// isDisguiseConflict reports whether err is a flag unique collision
// that a re-lookup can resolve: a concurrent run of the same key.
func isDisguiseConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "commerce_disguise_reviews_transfer_key_unique"
}

// isDisguiseStepConflict reports whether err is a step unique
// collision that a re-lookup can resolve: a concurrent contest or a
// concurrent terminal outcome.
func isDisguiseStepConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "commerce_disguise_steps_contest_unique" ||
		pgErr.ConstraintName == "commerce_disguise_steps_terminal_unique"
}
