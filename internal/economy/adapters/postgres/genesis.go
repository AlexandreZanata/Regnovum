// Package postgres is the PostgreSQL outbound adapter of the economy
// module. It records one Genesis creation event per season book against
// the append-only custody schema (migrations 00033, 00034 and 00055):
// the Treasury custody, its available partition, the attestation row
// and the credit leg commit in one transaction, so replays resolve
// the original event of their book and a second key in the same book
// is refused at the per-season unique constraint.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// Genesis custody placement: kind treasury, label main, partition
// available. The labels are closed by the schema CHECK constraints.
const (
	genesisCustodyKind   = "treasury"
	genesisCustodyLabel  = "main"
	genesisPartitionName = "available"
)

// attestationConstraints names the unique guards the adapter tells apart:
// the key itself (replay of the same event) and the book (a second
// event with any other key in the same season).
const (
	genesisKeyConstraint    = "economy_genesis_pkey"
	genesisSeasonConstraint = "economy_genesis_season_unique"
)

// Repository implements the economy application ports using PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

var _ application.GenesisRepository = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for the economy.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// rowQuerier covers pool and transaction reads for the attestation lookup.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// findAttestation resolves an event key to its recorded outcome in one
// book, or nil when this key never ran there. Another book keeps its
// own attestation: the same key elsewhere never redirects a replay.
func findAttestation(ctx context.Context, q rowQuerier, key domain.GenesisKey, season domain.SeasonKey) (*application.GenesisResult, error) {
	var custodyID string
	var millis int64
	err := q.QueryRow(ctx,
		`SELECT treasury_custody_id::text, amount_milli FROM app.economy_genesis WHERE genesis_key = $1 AND season_key = $2`,
		key.String(), season.String()).Scan(&custodyID, &millis)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup genesis attestation: %w", err)
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return nil, fmt.Errorf("genesis attestation holds %d milliINK: %w", millis, err)
	}
	if !amount.Equals(domain.GenesisSupply()) {
		return nil, fmt.Errorf("genesis attestation holds %d milliINK, want exactly S", millis)
	}
	return &application.GenesisResult{TreasuryCustodyID: custodyID, Amount: amount, Replayed: true}, nil
}

// ensureCustody inserts the (kind, label, book) custody once and resolves
// its id, so concurrent initializers serialize on the unique constraint
// instead of duplicating the Treasury. The same label in another book
// is another custody.
func ensureCustody(ctx context.Context, tx pgx.Tx, kind, label, season string) (string, error) {
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		kind, label, season); err != nil {
		return "", fmt.Errorf("ensure custody: %w", err)
	}
	var id string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2 AND season_key = $3`,
		kind, label, season).Scan(&id); err != nil {
		return "", fmt.Errorf("resolve custody: %w", err)
	}
	return id, nil
}

// ensurePartition inserts the (custody, name) partition once and resolves
// its id, with the same conflict-tolerant pattern as custodies.
func ensurePartition(ctx context.Context, tx pgx.Tx, custodyID, name string) (string, error) {
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`,
		custodyID, name); err != nil {
		return "", fmt.Errorf("ensure partition: %w", err)
	}
	var id string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_partitions WHERE custody_id = $1::uuid AND name = $2`,
		custodyID, name).Scan(&id); err != nil {
		return "", fmt.Errorf("resolve partition: %w", err)
	}
	return id, nil
}

// RunGenesis records the creation event exactly once per book. The same
// key resolves to the original attestation untouched; a different key
// after Genesis in the same book fails with
// domain.ErrGenesisAlreadyExists, including under concurrency, because
// the per-season unique index serializes the race.
func (r *Repository) RunGenesis(ctx context.Context, request application.GenesisRequest) (*application.GenesisResult, error) {
	if replayed, err := findAttestation(ctx, r.pool, request.Key, request.Season); err != nil || replayed != nil {
		return replayed, err
	}
	var lastErr error
	for range 2 {
		result, retry, err := r.createGenesis(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// createGenesis attempts the guarded creation in one transaction. It
// reports whether a same-key retry is worthwhile: true only when the key
// itself collided, meaning a concurrent run of this same event may have
// committed it.
func (r *Repository) createGenesis(ctx context.Context, request application.GenesisRequest) (*application.GenesisResult, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin genesis transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Frozen books record nothing new: reads resolve, writes wait.
	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, false, err
	}

	// Genesis mints only in a prepared book: activation stays a
	// separate step, judged inside this same transaction.
	if err := requirePreparedTx(ctx, tx, request.Season); err != nil {
		return nil, false, err
	}

	if replayed, err := findAttestation(ctx, tx, request.Key, request.Season); err != nil || replayed != nil {
		return replayed, false, err
	}
	custodyID, err := ensureCustody(ctx, tx, genesisCustodyKind, genesisCustodyLabel, request.Season.String())
	if err != nil {
		return nil, false, err
	}
	if _, err := ensurePartition(ctx, tx, custodyID, genesisPartitionName); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli, season_key)
		 VALUES ($1, $2::uuid, $3, $4)`,
		request.Key.String(), custodyID, domain.GenesisSupplyMillis, request.Season.String()); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == genesisKeyConstraint {
				return nil, true, fmt.Errorf("concurrent genesis of the same key: %w", err)
			}
			return nil, false, domain.ErrGenesisAlreadyExists
		}
		return nil, false, fmt.Errorf("record genesis attestation: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES (gen_random_uuid(), $1::uuid, 'credit', $2, $3)`,
		custodyID, domain.GenesisSupplyMillis, request.Season.String()); err != nil {
		return nil, false, fmt.Errorf("record genesis leg: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit genesis: %w", err)
	}
	return &application.GenesisResult{
		TreasuryCustodyID: custodyID,
		Amount:            domain.GenesisSupply(),
		Replayed:          false,
	}, false, nil
}
