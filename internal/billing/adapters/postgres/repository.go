// Package postgres is the PostgreSQL outbound adapter of the billing module.
// It implements the Arena Pass grant port against the entitlement schema
// (migration 00011): the grant is inserted with ON CONFLICT DO NOTHING on
// (account_id, origin, reference), so retries resolve the original lot
// untouched. Since P12-T04 it also implements the checkout surface — purchase
// eligibility, the account→provider customer correlation and the checkout
// intents — with the same replay rule, anchored on the provider session
// identifier that the schema keeps unique.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Repository implements the billing application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
	clock   ports.Clock
}

const pgUniqueViolation = "23505"

var (
	_ application.PassLotRepository      = (*Repository)(nil)
	_ application.ArenaPassConsumer      = (*Repository)(nil)
	_ application.PassLotQueryRepository = (*Repository)(nil)
)

// NewRepository creates a PostgreSQL repository adapter for billing.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// GrantPassLot inserts the lot exactly once per (account, origin,
// reference). A duplicate grant writes nothing and resolves the original
// lot, including its immutable expiration.
func (r *Repository) GrantPassLot(ctx context.Context, request application.GrantPassLotRequest) (*application.GrantPassLotResult, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("grant pass lot: %w", err)
	}

	row, err := r.queries.CreateArenaPassLotIfAbsent(ctx, platformpg.CreateArenaPassLotIfAbsentParams{
		AccountID:         pgUUID,
		Origin:            request.Origin.String(),
		Quantity:          request.Quantity.Int32(),
		RemainingQuantity: request.Quantity.Int32(),
		ExpiresAt:         timestamptz(request.ExpiresAt),
		Reference:         request.Reference.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := r.queries.GetArenaPassLotByGrant(ctx, platformpg.GetArenaPassLotByGrantParams{
			AccountID: pgUUID,
			Origin:    request.Origin.String(),
			Reference: request.Reference.String(),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errors.New("grant conflict without a stored lot")
			}
			return nil, fmt.Errorf("load replayed pass lot: %w", err)
		}
		lot, err := mapPassLotRow(existing)
		if err != nil {
			return nil, err
		}
		return &application.GrantPassLotResult{Lot: *lot, Replayed: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create pass lot: %w", err)
	}

	lot, err := mapPassLotRow(row)
	if err != nil {
		return nil, err
	}
	return &application.GrantPassLotResult{Lot: *lot}, nil
}

// ConsumeArenaPass locks the creator's consumable lots and consumes one pass
// in a single transaction. Selection follows the product rule: nearest
// expiration first, then lots that never expire; expired lots are never
// candidates and the same Arena resolves to its original consumption.
func (r *Repository) ConsumeArenaPass(ctx context.Context, request application.ConsumePassRequest) (*application.ConsumePassResult, error) {
	// Shared transaction (P07-T05): when the caller carries an active
	// transaction, the consumption joins it so publication and consumption
	// commit or roll back together.
	if sharedTx, ok := platformpg.TxFromContext(ctx); ok {
		result, err := r.consumeArenaPass(ctx, r.queries.WithTx(sharedTx), request)
		if errors.Is(err, errConsumptionConflict) {
			// The failed statement aborted the caller transaction; the
			// retry resolves the replay through the idempotency check.
			return nil, fmt.Errorf("consume arena pass: concurrent consumption of the same arena: %w", application.ErrArenaAlreadyConsumed)
		}
		return result, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	result, err := r.consumeArenaPass(ctx, r.queries.WithTx(tx), request)
	if errors.Is(err, errConsumptionConflict) {
		// A concurrent consumption of the same Arena won the unique
		// constraint: roll the owned transaction back entirely (decrement
		// included) and resolve the original consumption outside it.
		tx.Rollback(ctx)
		return r.resolveConsumptionReplay(ctx, request)
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return result, nil
}

// errConsumptionConflict reports that another transaction consumed the same
// Arena first. The current transaction must be rolled back before the
// consumption is resolved; it never leaves the adapter.
var errConsumptionConflict = errors.New("arena pass consumption conflict")

// resolveConsumptionReplay resolves a conflicted Arena to its original
// consumption, outside the rolled-back transaction.
func (r *Repository) resolveConsumptionReplay(ctx context.Context, request application.ConsumePassRequest) (*application.ConsumePassResult, error) {
	arenaUUID, err := pgUUIDFromArenaID(request.ArenaID)
	if err != nil {
		return nil, fmt.Errorf("consume arena pass: %w", err)
	}
	result, found, err := findArenaPassConsumption(ctx, r.queries, request, arenaUUID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("consumption conflict without a stored row")
	}
	return result, nil
}

// consumeArenaPass runs the consumption with the given queries. A
// unique-violation race is reported as errConsumptionConflict so the caller
// decides how to roll back and resolve the replay.
func (r *Repository) consumeArenaPass(
	ctx context.Context,
	qtx *platformpg.Queries,
	request application.ConsumePassRequest,
) (*application.ConsumePassResult, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("consume arena pass: %w", err)
	}
	arenaUUID, err := pgUUIDFromArenaID(request.ArenaID)
	if err != nil {
		return nil, fmt.Errorf("consume arena pass: %w", err)
	}

	// Idempotency: one consumption per Arena.
	if result, found, err := findArenaPassConsumption(ctx, qtx, request, arenaUUID); err != nil {
		return nil, err
	} else if found {
		return result, nil
	}

	candidates, err := qtx.ListAvailablePassLotsForUpdate(ctx, platformpg.ListAvailablePassLotsForUpdateParams{
		AccountID: pgUUID,
		At:        pgtype.Timestamptz{Time: request.ConsumedAt.UTC(), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("lock available pass lots: %w", err)
	}
	if len(candidates) == 0 {
		// A concurrent publication of the same Arena may have committed after
		// our first check: resolve the replay before reporting the lack of
		// passes.
		if result, found, err := findArenaPassConsumption(ctx, qtx, request, arenaUUID); err != nil {
			return nil, err
		} else if found {
			return result, nil
		}
		return nil, domain.ErrNoPassAvailable
	}

	selected := candidates[0]
	rows, err := qtx.ConsumeArenaPassLot(ctx, selected.ID)
	if err != nil {
		return nil, fmt.Errorf("consume pass lot: %w", err)
	}
	if rows != 1 {
		return nil, errors.New("pass lot changed under lock")
	}

	if _, err := qtx.CreateArenaPassConsumption(ctx, platformpg.CreateArenaPassConsumptionParams{
		LotID:   selected.ID,
		ArenaID: arenaUUID,
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return nil, errConsumptionConflict
		}
		return nil, fmt.Errorf("create pass consumption: %w", err)
	}

	lot, err := mapPassLotRow(selected)
	if err != nil {
		return nil, err
	}
	consumed, err := domain.ReconstitutePassLot(
		lot.ID(),
		lot.AccountID(),
		lot.Origin(),
		lot.Quantity(),
		selected.RemainingQuantity-1,
		lot.ExpiresAt(),
		lot.Reference(),
		lot.CreatedAt(),
	)
	if err != nil {
		return nil, err
	}
	return &application.ConsumePassResult{Lot: *consumed, Remaining: consumed.Remaining()}, nil
}

// findArenaPassConsumption resolves an Arena that already consumed a pass to
// its original outcome. The consumption must belong to the requesting
// account: passes are never transferable.
func findArenaPassConsumption(
	ctx context.Context,
	queries *platformpg.Queries,
	request application.ConsumePassRequest,
	arenaUUID pgtype.UUID,
) (*application.ConsumePassResult, bool, error) {
	existing, err := queries.GetArenaPassConsumptionByArena(ctx, arenaUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("load pass consumption: %w", err)
	}

	lotRow, err := queries.GetArenaPassLot(ctx, existing.LotID)
	if err != nil {
		return nil, false, fmt.Errorf("load consumed pass lot: %w", err)
	}
	if uuidToString(lotRow.AccountID) != request.AccountID.String() {
		return nil, false, application.ErrArenaAlreadyConsumed
	}

	lot, err := mapPassLotRow(lotRow)
	if err != nil {
		return nil, false, err
	}
	return &application.ConsumePassResult{
		Lot:       *lot,
		Remaining: lotRow.RemainingQuantity,
		Replayed:  true,
	}, true, nil
}

// ListAccountPassLots returns every lot of the account in canonical order
// (nearest expiration first, non-expiring lots last).
func (r *Repository) ListAccountPassLots(ctx context.Context, accountID domain.AccountID) ([]domain.PassLot, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("list account pass lots: %w", err)
	}

	rows, err := r.queries.ListArenaPassLotsByAccount(ctx, pgUUID)
	if err != nil {
		return nil, fmt.Errorf("list arena pass lots: %w", err)
	}

	lots := make([]domain.PassLot, 0, len(rows))
	for _, row := range rows {
		lot, err := mapPassLotRow(row)
		if err != nil {
			return nil, err
		}
		lots = append(lots, *lot)
	}
	return lots, nil
}

// ListExpiredPassLots derives the expired lots that still hold passes at the
// instant, bounded by limit.
func (r *Repository) ListExpiredPassLots(ctx context.Context, at time.Time, limit int) ([]domain.PassLot, error) {
	rows, err := r.queries.ListExpiredArenaPassLots(ctx, platformpg.ListExpiredArenaPassLotsParams{
		At:        pgtype.Timestamptz{Time: at.UTC(), Valid: true},
		PageLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list expired arena pass lots: %w", err)
	}

	lots := make([]domain.PassLot, 0, len(rows))
	for _, row := range rows {
		lot, err := mapPassLotRow(row)
		if err != nil {
			return nil, err
		}
		lots = append(lots, *lot)
	}
	return lots, nil
}

// ListConsumptionsPage returns one keyset page of the account consumption
// history, newest first. The account filter is applied on every page; the
// cursor only positions the window inside that account's own history.
func (r *Repository) ListConsumptionsPage(ctx context.Context, accountID domain.AccountID, after *application.ConsumptionPosition, limit int) ([]application.PassConsumptionRecord, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("list pass consumptions page: %w", err)
	}

	params := platformpg.ListArenaPassConsumptionsPageParams{
		AccountID: pgUUID,
		PageLimit: int32(limit),
	}
	if after != nil {
		var afterID pgtype.UUID
		if err := afterID.Scan(after.ConsumptionID); err != nil {
			return nil, application.ErrInvalidCursor
		}
		params.AfterConsumedAt = pgtype.Timestamptz{Time: after.ConsumedAt.UTC(), Valid: true}
		params.AfterID = afterID
	}

	rows, err := r.queries.ListArenaPassConsumptionsPage(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list pass consumptions page: %w", err)
	}

	records := make([]application.PassConsumptionRecord, 0, len(rows))
	for _, row := range rows {
		origin, err := domain.ParsePassOrigin(row.Origin)
		if err != nil {
			return nil, fmt.Errorf("stored pass origin is invalid: %w", err)
		}
		reference, err := domain.ParseReference(row.Reference)
		if err != nil {
			return nil, fmt.Errorf("stored pass reference is invalid: %w", err)
		}

		records = append(records, application.PassConsumptionRecord{
			ConsumptionID: uuidToString(row.ID),
			ArenaID:       uuidToString(row.ArenaID),
			Origin:        origin,
			Reference:     reference,
			ConsumedAt:    row.ConsumedAt.Time.UTC(),
		})
	}
	return records, nil
}

func mapPassLotRow(row platformpg.AppArenaPassLot) (*domain.PassLot, error) {
	origin, err := domain.ParsePassOrigin(row.Origin)
	if err != nil {
		return nil, fmt.Errorf("stored pass origin is invalid: %w", err)
	}
	quantity, err := domain.NewQuantity(row.Quantity)
	if err != nil {
		return nil, fmt.Errorf("stored pass quantity is invalid: %w", err)
	}
	reference, err := domain.ParseReference(row.Reference)
	if err != nil {
		return nil, fmt.Errorf("stored pass reference is invalid: %w", err)
	}

	var expiresAt *time.Time
	if row.ExpiresAt.Valid {
		instant := row.ExpiresAt.Time.UTC()
		expiresAt = &instant
	}

	return domain.ReconstitutePassLot(
		domain.LotID(uuidToString(row.ID)),
		domain.AccountID(uuidToString(row.AccountID)),
		origin,
		quantity,
		row.RemainingQuantity,
		expiresAt,
		reference,
		row.CreatedAt.Time,
	)
}

func pgUUIDFromArenaID(id domain.ArenaID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid arena id format: %w", err)
	}
	return pgUUID, nil
}

func pgUUIDFromAccountID(id domain.AccountID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid account id format: %w", err)
	}
	return pgUUID, nil
}

func timestamptz(instant *time.Time) pgtype.Timestamptz {
	if instant == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: instant.UTC(), Valid: true}
}

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
