// Package postgres is the outbound PostgreSQL adapter of the positions
// module (P09-T04). Every method joins the caller transaction when the
// context carries one, so a change and its projection update commit or roll
// back together; without a shared transaction each statement stays
// autocommit.
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

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// pgUniqueViolation is the PostgreSQL error code for unique constraint
// violations; on the change chain it means a concurrent change won.
const pgUniqueViolation = "23505"

// Repository implements the positions application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var _ application.PositionRepository = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for positions.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// queriesFor binds the queries to the caller transaction when one is
// carried by the context, so the history append and the projection update
// participate in the same transaction.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

// GetByAccountAndArena returns the stored projection of one account in one
// Arena.
func (r *Repository) GetByAccountAndArena(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID) (*domain.DebatePosition, error) {
	arenaParam, accountParam, err := positionScope(arenaID, accountID)
	if err != nil {
		return nil, err
	}

	row, err := r.queriesFor(ctx).GetDebatePosition(ctx, platformpg.GetDebatePositionParams{
		ArenaID:   arenaParam,
		AccountID: accountParam,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrPositionNotFound
		}
		return nil, fmt.Errorf("get debate position: %w", err)
	}
	return mapDebatePosition(row)
}

// ConfirmInitialPosition atomically inserts the initial projection; inserted
// is false when the pair already holds a position.
func (r *Repository) ConfirmInitialPosition(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID, position domain.Position, at time.Time) (*domain.DebatePosition, bool, error) {
	arenaParam, accountParam, err := positionScope(arenaID, accountID)
	if err != nil {
		return nil, false, err
	}

	row, err := r.queriesFor(ctx).ConfirmInitialPosition(ctx, platformpg.ConfirmInitialPositionParams{
		ArenaID:   arenaParam,
		AccountID: accountParam,
		Position:  position.String(),
		At:        pgtype.Timestamptz{Time: at.UTC(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The pair already has a projection: the caller re-reads it to
			// resolve a replay or a conflict.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("confirm initial position: %w", err)
	}

	created, err := mapDebatePosition(row)
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// CreatePositionChange appends one change to the history chain and returns
// the stored row identifier.
func (r *Repository) CreatePositionChange(ctx context.Context, change domain.PositionChange) (string, error) {
	arenaParam, accountParam, err := positionScope(change.ArenaID(), change.AccountID())
	if err != nil {
		return "", err
	}

	id, err := r.queriesFor(ctx).CreatePositionChange(ctx, platformpg.CreatePositionChangeParams{
		ArenaID:      arenaParam,
		AccountID:    accountParam,
		FromPosition: change.From().String(),
		ToPosition:   change.To().String(),
		Version:      change.Version(),
		ChangedAt:    pgtype.Timestamptz{Time: change.ChangedAt().UTC(), Valid: true},
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			// A concurrent change already appended this chain version.
			return "", application.ErrVersionConflict
		}
		return "", fmt.Errorf("create position change: %w", err)
	}
	return uuidToString(id), nil
}

// UpdateCurrentPosition moves the projection to the change target under the
// optimistic version check.
func (r *Repository) UpdateCurrentPosition(ctx context.Context, change domain.PositionChange, expectedVersion int32) error {
	arenaParam, accountParam, err := positionScope(change.ArenaID(), change.AccountID())
	if err != nil {
		return err
	}

	rows, err := r.queriesFor(ctx).UpdateCurrentPosition(ctx, platformpg.UpdateCurrentPositionParams{
		ArenaID:         arenaParam,
		AccountID:       accountParam,
		CurrentPosition: change.To().String(),
		Version:         change.Version(),
		At:              pgtype.Timestamptz{Time: change.ChangedAt().UTC(), Valid: true},
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return fmt.Errorf("update current position: %w", err)
	}
	if rows == 0 {
		return r.diagnoseProjectionMiss(ctx, arenaParam, accountParam)
	}
	return nil
}

// ListPositionChanges returns the account's change history in one Arena,
// newest first.
func (r *Repository) ListPositionChanges(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID) ([]application.PositionChangeRecord, error) {
	arenaParam, accountParam, err := positionScope(arenaID, accountID)
	if err != nil {
		return nil, err
	}

	rows, err := r.queriesFor(ctx).ListPositionChanges(ctx, platformpg.ListPositionChangesParams{
		ArenaID:   arenaParam,
		AccountID: accountParam,
	})
	if err != nil {
		return nil, fmt.Errorf("list position changes: %w", err)
	}

	records := make([]application.PositionChangeRecord, 0, len(rows))
	for _, row := range rows {
		change, err := mapPositionChange(row)
		if err != nil {
			return nil, err
		}
		records = append(records, application.PositionChangeRecord{
			ID:     uuidToString(row.ID),
			Change: change,
		})
	}
	return records, nil
}

// CountEligiblePositions derives the public aggregate of one Arena. The
// projection counts only active accounts (BUSINESS_RULES §7) and returns
// counts only: no account identifier leaves the database.
func (r *Repository) CountEligiblePositions(ctx context.Context, arenaID domain.ArenaID) (application.PositionDistribution, application.PositionDistribution, error) {
	arenaParam, ok := uuidParam(arenaID.String())
	if !ok {
		return application.PositionDistribution{}, application.PositionDistribution{}, application.ErrArenaNotFound
	}

	row, err := r.queriesFor(ctx).CountEligiblePositionsByArena(ctx, arenaParam)
	if err != nil {
		return application.PositionDistribution{}, application.PositionDistribution{}, fmt.Errorf("count eligible positions: %w", err)
	}

	initial := application.PositionDistribution{
		Agree:     row.InitialAgree,
		Disagree:  row.InitialDisagree,
		Undecided: row.InitialUndecided,
	}
	current := application.PositionDistribution{
		Agree:     row.CurrentAgree,
		Disagree:  row.CurrentDisagree,
		Undecided: row.CurrentUndecided,
	}
	return initial, current, nil
}

// diagnoseProjectionMiss explains why the projection update affected no
// row: a missing projection or a version that moved since it was read.
func (r *Repository) diagnoseProjectionMiss(ctx context.Context, arenaParam, accountParam pgtype.UUID) error {
	_, err := r.queriesFor(ctx).GetDebatePosition(ctx, platformpg.GetDebatePositionParams{
		ArenaID:   arenaParam,
		AccountID: accountParam,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrPositionNotFound
		}
		return fmt.Errorf("diagnose projection miss: %w", err)
	}
	return application.ErrVersionConflict
}

// positionScope converts opaque identifiers into database parameters.
// Malformed values can never match a row: reads treat them as not found and
// writes as a missing Arena or position.
func positionScope(arenaID domain.ArenaID, accountID domain.AccountID) (pgtype.UUID, pgtype.UUID, error) {
	arenaParam, ok := uuidParam(arenaID.String())
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, application.ErrArenaNotFound
	}
	accountParam, ok := uuidParam(accountID.String())
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, application.ErrPositionNotFound
	}
	return arenaParam, accountParam, nil
}

// uuidParam parses a canonical UUID string into its database parameter.
func uuidParam(raw string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, false
	}
	return id, id.Valid
}

// mapDebatePosition rebuilds the projection from a stored row.
func mapDebatePosition(row platformpg.AppDebatePosition) (*domain.DebatePosition, error) {
	arenaID, err := domain.ParseArenaID(uuidToString(row.ArenaID))
	if err != nil {
		return nil, fmt.Errorf("stored arena id is invalid: %w", err)
	}
	accountID, err := domain.ParseAccountID(uuidToString(row.AccountID))
	if err != nil {
		return nil, fmt.Errorf("stored account id is invalid: %w", err)
	}
	initial, err := domain.ParsePosition(row.InitialPosition)
	if err != nil {
		return nil, fmt.Errorf("stored initial position is invalid: %w", err)
	}
	current, err := domain.ParsePosition(row.CurrentPosition)
	if err != nil {
		return nil, fmt.Errorf("stored current position is invalid: %w", err)
	}
	position, err := domain.ReconstituteDebatePosition(arenaID, accountID, initial, current, row.Version, row.CreatedAt.Time, row.UpdatedAt.Time)
	if err != nil {
		return nil, fmt.Errorf("stored debate position is invalid: %w", err)
	}
	return position, nil
}

// mapPositionChange rebuilds one history row from stored state.
func mapPositionChange(row platformpg.AppPositionChange) (domain.PositionChange, error) {
	arenaID, err := domain.ParseArenaID(uuidToString(row.ArenaID))
	if err != nil {
		return domain.PositionChange{}, fmt.Errorf("stored arena id is invalid: %w", err)
	}
	accountID, err := domain.ParseAccountID(uuidToString(row.AccountID))
	if err != nil {
		return domain.PositionChange{}, fmt.Errorf("stored account id is invalid: %w", err)
	}
	from, err := domain.ParsePosition(row.FromPosition)
	if err != nil {
		return domain.PositionChange{}, fmt.Errorf("stored from position is invalid: %w", err)
	}
	to, err := domain.ParsePosition(row.ToPosition)
	if err != nil {
		return domain.PositionChange{}, fmt.Errorf("stored to position is invalid: %w", err)
	}
	change, err := domain.NewPositionChange(arenaID, accountID, from, to, row.Version, row.ChangedAt.Time)
	if err != nil {
		return domain.PositionChange{}, fmt.Errorf("stored position change is invalid: %w", err)
	}
	return change, nil
}

// uuidToString renders a database UUID in canonical form.
func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
