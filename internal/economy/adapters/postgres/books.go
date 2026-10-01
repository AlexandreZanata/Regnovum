package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.SeasonBooks = (*Repository)(nil)

// terminalStages are the lifecycle stages past admission: books
// carrying any of them move nothing anymore.
var terminalStages = []string{"closing", "sealed", "archived"}

// bookHasStage reports whether one book carries any of the given
// lifecycle stages. Unknown books read as absent.
func bookHasStage(ctx context.Context, q rowQuerier, season domain.SeasonKey, stages []string) (bool, error) {
	var present bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state = ANY($2)
		)`,
		season.String(), stages).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("read book lifecycle: %w", err)
	}
	return present, nil
}

// bookExists reports whether one season book is registered.
func bookExists(ctx context.Context, q rowQuerier, season domain.SeasonKey) (bool, error) {
	var present bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season.String()).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("read book registry: %w", err)
	}
	return present, nil
}

// RequirePrepared refuses Genesis outside a prepared book: unknown
// books never mint, and books with any active, closing, sealed or
// archived event mint nothing more. Activation stays a separate
// step this port never performs.
func (r *Repository) RequirePrepared(ctx context.Context, season domain.SeasonKey) error {
	exists, err := bookExists(ctx, r.pool, season)
	if err != nil {
		return err
	}
	if !exists {
		return domain.ErrMissingSeason
	}
	past, err := bookHasStage(ctx, r.pool, season, []string{"active", "closing", "sealed", "archived"})
	if err != nil {
		return err
	}
	if past {
		return domain.ErrBookNotPrepared
	}
	return nil
}

// RequireActive refuses mutations on books past admission: unknown
// books move nothing, and books with any closing, sealed or
// archived event are readable history, never a live ledger.
func (r *Repository) RequireActive(ctx context.Context, season domain.SeasonKey) error {
	exists, err := bookExists(ctx, r.pool, season)
	if err != nil {
		return err
	}
	if !exists {
		return domain.ErrMissingSeason
	}
	closed, err := bookHasStage(ctx, r.pool, season, terminalStages)
	if err != nil {
		return err
	}
	if closed {
		return domain.ErrBookSealed
	}
	return nil
}

// requirePreparedTx judges one book inside the caller transaction,
// so the admission check and the Genesis write commit together.
func requirePreparedTx(ctx context.Context, tx pgx.Tx, season domain.SeasonKey) error {
	var present bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season.String()).Scan(&present)
	if err != nil {
		return fmt.Errorf("read book registry: %w", err)
	}
	if !present {
		return domain.ErrMissingSeason
	}
	past, err := bookHasStage(ctx, tx, season, []string{"active", "closing", "sealed", "archived"})
	if err != nil {
		return err
	}
	if past {
		return domain.ErrBookNotPrepared
	}
	return nil
}

// requireActiveTx judges one book inside the caller transaction, so
// the admission check and the mutation commit together: a seal
// landing between the check and the write still refuses.
func requireActiveTx(ctx context.Context, tx pgx.Tx, season domain.SeasonKey) error {
	var present bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season.String()).Scan(&present)
	if err != nil {
		return fmt.Errorf("read book registry: %w", err)
	}
	if !present {
		return domain.ErrMissingSeason
	}
	closed, err := bookHasStage(ctx, tx, season, terminalStages)
	if err != nil {
		return err
	}
	if closed {
		return domain.ErrBookSealed
	}
	return nil
}
