package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// SeasonOpener persists archive receipts and successor books: one
// archive row per sealed book, one season row per successor and
// append-only lifecycle stages. Every mutation is idempotent: a second
// opener replays the stored outcome instead of duplicating Genesis or
// ACTIVE. It writes only seasons, lifecycle and archives: money moves
// solely through the injected Genesis port, and liquidation never
// enters any competitive ranking.
type SeasonOpener struct {
	pool *pgxpool.Pool
}

var _ application.OpenStore = (*SeasonOpener)(nil)

// NewSeasonOpener builds the archive-and-open store with explicit
// wiring. Only the composition root instantiates it; tests stand in
// for the root.
func NewSeasonOpener(pool *pgxpool.Pool) (*SeasonOpener, error) {
	if pool == nil {
		return nil, fmt.Errorf("seasons: opener needs a pool")
	}
	return &SeasonOpener{pool: pool}, nil
}

// seasonRow is one registry row without driver types.
type seasonRow struct {
	key      string
	ordinal  int
	startsAt time.Time
	endsAt   time.Time
	charter  string
	policy   string
	monarch  string
	regent   string
	hash     string
}

// loadSeasonRow reads one registry row.
func loadSeasonRow(ctx context.Context, q closeQuerier, key string) (seasonRow, error) {
	var row seasonRow
	err := q.QueryRow(ctx,
		`SELECT season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash
		 FROM app.seasons WHERE season_key = $1`, key).Scan(
		&row.key, &row.ordinal, &row.startsAt, &row.endsAt,
		&row.charter, &row.policy, &row.monarch, &row.regent, &row.hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return seasonRow{}, seasondomain.ErrInvalidSeason
		}
		return seasonRow{}, fmt.Errorf("read season registry: %w", err)
	}
	return row, nil
}

// seasonManifestOf rebuilds the sealed manifesto of one row and
// refuses adulterated checksums before anything opens.
func seasonManifestOf(row seasonRow) (seasondomain.Manifest, error) {
	manifest, err := seasondomain.NewManifest(seasondomain.ManifestRequest{
		ID: row.key, Ordinal: row.ordinal, StartsAt: row.startsAt,
		CharterVersion: row.charter, PolicyRef: row.policy,
		InitialMonarch: row.monarch, Regent: row.regent,
	})
	if err != nil {
		return seasondomain.Manifest{}, err
	}
	if manifest.Hash != row.hash {
		return seasondomain.Manifest{}, seasondomain.ErrInvalidSeason
	}
	return manifest, nil
}

// seasonOf rebuilds one staged season value for continuity checks.
func seasonOf(row seasonRow, manifest seasondomain.Manifest) (seasondomain.Season, error) {
	season, err := seasondomain.NewSeason(manifest)
	if err != nil {
		return seasondomain.Season{}, err
	}
	if !season.EndsAt.Equal(row.endsAt) {
		return seasondomain.Season{}, seasondomain.ErrInvalidSeason
	}
	return season, nil
}

// hasStage reports whether one book carries one lifecycle stage.
func hasStage(ctx context.Context, q closeQuerier, season, stage string) (bool, error) {
	var present bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state = $2)`,
		season, stage).Scan(&present); err != nil {
		return false, fmt.Errorf("read book stage: %w", err)
	}
	return present, nil
}

// closeCutoff reads the barrier cutoff and snapshot of one book.
func closeCutoff(ctx context.Context, q closeQuerier, season string) (time.Time, seasondomain.SealSnapshot, error) {
	var cutoff time.Time
	var snap seasondomain.SealSnapshot
	err := q.QueryRow(ctx,
		`SELECT cutoff_at, snapshot_milli, snapshot_legs, snapshot_intentions
		 FROM app.season_close_runs WHERE season_key = $1`, season).Scan(
		&cutoff, &snap.Milli, &snap.Legs, &snap.Intentions)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, seasondomain.SealSnapshot{}, seasondomain.ErrCloseBlocked
		}
		return time.Time{}, seasondomain.SealSnapshot{}, fmt.Errorf("read close snapshot: %w", err)
	}
	return cutoff, snap, nil
}

// currentSnapshot counts one book without locking: the seal, not the
// snapshot, orders admitted work.
func currentSnapshot(ctx context.Context, q closeQuerier, season string) (seasondomain.SealSnapshot, error) {
	var snap seasondomain.SealSnapshot
	if err := q.QueryRow(ctx,
		`SELECT COALESCE((SELECT amount_milli FROM app.economy_genesis WHERE season_key = $1), 0)`,
		season).Scan(&snap.Milli); err != nil {
		return seasondomain.SealSnapshot{}, fmt.Errorf("read genesis snapshot: %w", err)
	}
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, season).Scan(&snap.Legs); err != nil {
		return seasondomain.SealSnapshot{}, fmt.Errorf("read legs snapshot: %w", err)
	}
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_intentions WHERE season_key = $1`, season).Scan(&snap.Intentions); err != nil {
		return seasondomain.SealSnapshot{}, fmt.Errorf("read intentions snapshot: %w", err)
	}
	return snap, nil
}

// isActiveGuard reports whether err is the current-active guard: another
// book still holds ACTIVE, so the successor waits with custody
// preserved.
func isActiveGuard(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "season_lifecycle_single_active_current"
}

// recordStage inserts one lifecycle stage idempotently: a repeated
// stage replays, a guarded active refuses with the domain block.
func recordStage(ctx context.Context, q closeQuerier, season, from, to string) (bool, error) {
	var fromArg any
	if from == "" {
		fromArg = nil
	} else {
		fromArg = from
	}
	_, err := q.Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ($1, $2, $3, now())`,
		season, fromArg, to)
	if err != nil {
		if isActiveGuard(err) {
			return false, seasondomain.ErrCloseBlocked
		}
		if isStageConflict(err) {
			return true, nil
		}
		return false, fmt.Errorf("record stage: %w", err)
	}
	return false, nil
}

// archiveInputs bundles the predecessor seal the archive verifies.
type archiveInputs struct {
	row      seasonRow
	manifest seasondomain.Manifest
	cutoff   time.Time
	sealedAt time.Time
	snapshot seasondomain.SealSnapshot
	current  seasondomain.SealSnapshot
}

// loadArchiveInputs reads the predecessor seal for archiving: registry,
// manifesto checksum, sealed stage, barrier snapshot and current
// conservation. A missing seal refuses with custody preserved.
func loadArchiveInputs(ctx context.Context, q closeQuerier, predecessor string) (archiveInputs, error) {
	var in archiveInputs
	row, err := loadSeasonRow(ctx, q, predecessor)
	if err != nil {
		return archiveInputs{}, err
	}
	manifest, err := seasonManifestOf(row)
	if err != nil {
		return archiveInputs{}, err
	}
	sealed, err := hasStage(ctx, q, predecessor, "sealed")
	if err != nil {
		return archiveInputs{}, err
	}
	if !sealed {
		return archiveInputs{}, seasondomain.ErrCloseBlocked
	}
	cutoff, snapshot, err := closeCutoff(ctx, q, predecessor)
	if err != nil {
		return archiveInputs{}, err
	}
	current, err := currentSnapshot(ctx, q, predecessor)
	if err != nil {
		return archiveInputs{}, err
	}
	if err := seasondomain.VerifyConservationForOpening(snapshot, current); err != nil {
		return archiveInputs{}, err
	}
	var sealedAt time.Time
	if err := q.QueryRow(ctx,
		`SELECT decided_at FROM app.season_lifecycle WHERE season_key = $1 AND to_state = 'sealed'`,
		predecessor).Scan(&sealedAt); err != nil {
		return archiveInputs{}, fmt.Errorf("read seal instant: %w", err)
	}
	in.row = row
	in.manifest = manifest
	in.cutoff = cutoff
	in.sealedAt = sealedAt
	in.snapshot = snapshot
	in.current = current
	return in, nil
}

// writeArchiveRow inserts the receipt once: a second archiver replays
// it after proving the stored receipt matches.
func writeArchiveRow(ctx context.Context, q closeQuerier, in archiveInputs) (bool, error) {
	_, err := q.Exec(ctx,
		`INSERT INTO app.season_archives
		 (season_key, cutoff_at, sealed_at, snapshot_milli, snapshot_legs, snapshot_intentions, manifest_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (season_key) DO NOTHING`,
		in.row.key, in.cutoff, in.sealedAt,
		in.snapshot.Milli, in.current.Legs, in.current.Intentions, in.row.hash)
	if err != nil {
		return false, fmt.Errorf("record archive receipt: %w", err)
	}
	var storedHash string
	var storedMilli int64
	if err := q.QueryRow(ctx,
		`SELECT manifest_hash, snapshot_milli FROM app.season_archives WHERE season_key = $1`,
		in.row.key).Scan(&storedHash, &storedMilli); err != nil {
		return false, fmt.Errorf("read archive receipt: %w", err)
	}
	if storedHash != in.row.hash || storedMilli != in.snapshot.Milli {
		return false, seasondomain.ErrInvalidSeason
	}
	return true, nil
}

// Archive records the predecessor seal and its archived stage. A book
// without a seal refuses; a diverged S or an adulterated checksum
// refuses; a stored archive replays.
func (o *SeasonOpener) Archive(ctx context.Context, predecessor string) (application.ArchiveProof, error) {
	if _, err := seasondomain.ParseCloseOwner(predecessor); err != nil {
		return application.ArchiveProof{}, err
	}
	in, err := loadArchiveInputs(ctx, o.pool, predecessor)
	if err != nil {
		return application.ArchiveProof{}, err
	}
	if err := in.manifest.VerifyManifestHash(); err != nil {
		return application.ArchiveProof{}, err
	}
	archived, err := hasStage(ctx, o.pool, predecessor, "archived")
	if err != nil {
		return application.ArchiveProof{}, err
	}
	replayedArchive := false
	if !archived {
		if _, err := writeArchiveRow(ctx, o.pool, in); err != nil {
			return application.ArchiveProof{}, err
		}
		if _, err := recordStage(ctx, o.pool, predecessor, "sealed", "archived"); err != nil {
			return application.ArchiveProof{}, err
		}
	} else {
		if _, err := writeArchiveRow(ctx, o.pool, in); err != nil {
			return application.ArchiveProof{}, err
		}
		replayedArchive = true
	}
	return application.ArchiveProof{
		Season: predecessor, CutoffAt: in.cutoff, SealedAt: in.sealedAt,
		Milli: in.snapshot.Milli, Legs: in.current.Legs,
		Intentions: in.current.Intentions, Replayed: replayedArchive,
	}, nil
}

// successorPlan validates the calendar link and seals the successor
// manifesto before any row is written.
func successorPlan(predecessorRow seasonRow, next seasondomain.ManifestRequest) (seasondomain.Manifest, seasondomain.Season, error) {
	manifest, err := seasonManifestOf(predecessorRow)
	if err != nil {
		return seasondomain.Manifest{}, seasondomain.Season{}, err
	}
	previous, err := seasonOf(predecessorRow, manifest)
	if err != nil {
		return seasondomain.Manifest{}, seasondomain.Season{}, err
	}
	nextManifest, err := seasondomain.VerifySuccessorContinuity(previous, next)
	if err != nil {
		return seasondomain.Manifest{}, seasondomain.Season{}, err
	}
	return nextManifest, previous, nil
}

// ensureSeasonRow inserts the successor registry row once: a second
// opener replays it only when every field matches.
func ensureSeasonRow(ctx context.Context, q closeQuerier, next seasondomain.Manifest, endsAt time.Time) (bool, error) {
	_, err := q.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (season_key) DO NOTHING`,
		next.ID, next.Ordinal, next.StartsAt, endsAt,
		next.CharterVersion, next.PolicyRef, next.InitialMonarch, next.Regent, next.Hash)
	if err != nil {
		return false, fmt.Errorf("record successor season: %w", err)
	}
	stored, err := loadSeasonRow(ctx, q, next.ID)
	if err != nil {
		return false, err
	}
	if stored.ordinal != next.Ordinal || !stored.startsAt.Equal(next.StartsAt) ||
		!stored.endsAt.Equal(endsAt) || stored.hash != next.Hash {
		return false, seasondomain.ErrInvalidSeason
	}
	return true, nil
}

// EnsurePrepared records the successor season and its prepared stage.
// Continuity is exact and downtime never fills gaps.
func (o *SeasonOpener) EnsurePrepared(ctx context.Context, predecessor string, next seasondomain.ManifestRequest) (bool, error) {
	if _, err := seasondomain.ParseCloseOwner(predecessor); err != nil {
		return false, err
	}
	predecessorRow, err := loadSeasonRow(ctx, o.pool, predecessor)
	if err != nil {
		return false, err
	}
	sealed, err := hasStage(ctx, o.pool, predecessor, "sealed")
	if err != nil {
		return false, err
	}
	if !sealed {
		return false, seasondomain.ErrCloseBlocked
	}
	nextManifest, _, err := successorPlan(predecessorRow, next)
	if err != nil {
		return false, err
	}
	nextEnds, err := seasonEndOf(nextManifest)
	if err != nil {
		return false, err
	}
	if _, err := ensureSeasonRow(ctx, o.pool, nextManifest, nextEnds); err != nil {
		return false, err
	}
	prepared, err := hasStage(ctx, o.pool, next.ID, "prepared")
	if err != nil {
		return false, err
	}
	if prepared {
		return true, nil
	}
	if _, err := recordStage(ctx, o.pool, next.ID, "", "prepared"); err != nil {
		return false, err
	}
	return false, nil
}

// seasonEndOf derives the exclusive end of one sealed manifesto.
func seasonEndOf(manifest seasondomain.Manifest) (time.Time, error) {
	season, err := seasondomain.NewSeason(manifest)
	if err != nil {
		return time.Time{}, err
	}
	return season.EndsAt, nil
}

// freshBalances reads the successor economy for carry-over: the
// Treasury balance and every other custody summed together.
func freshBalances(ctx context.Context, q closeQuerier, successor string) (treasury, others, genesis int64, err error) {
	if err := q.QueryRow(ctx,
		`SELECT COALESCE((SELECT amount_milli FROM app.economy_genesis WHERE season_key = $1), 0)`,
		successor).Scan(&genesis); err != nil {
		return 0, 0, 0, fmt.Errorf("read successor genesis: %w", err)
	}
	if genesis == 0 {
		return 0, 0, 0, seasondomain.ErrInvalidSeason
	}
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.season_key = $1 AND c.kind = 'treasury' AND c.label = 'main'`, successor).Scan(&treasury); err != nil {
		return 0, 0, 0, fmt.Errorf("read successor treasury: %w", err)
	}
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.season_key = $1 AND NOT (c.kind = 'treasury' AND c.label = 'main')`, successor).Scan(&others); err != nil {
		return 0, 0, 0, fmt.Errorf("read successor others: %w", err)
	}
	return treasury, others, genesis, nil
}

// Activate records the successor active stage once its Genesis is
// conserved and fresh. Two activators record exactly one ACTIVE.
func (o *SeasonOpener) Activate(ctx context.Context, predecessor, successor string) (bool, error) {
	if _, err := seasondomain.ParseCloseOwner(predecessor); err != nil {
		return false, err
	}
	if _, err := seasondomain.ParseCloseOwner(successor); err != nil {
		return false, err
	}
	sealed, err := hasStage(ctx, o.pool, predecessor, "sealed")
	if err != nil {
		return false, err
	}
	if !sealed {
		return false, seasondomain.ErrCloseBlocked
	}
	prepared, err := hasStage(ctx, o.pool, successor, "prepared")
	if err != nil {
		return false, err
	}
	if !prepared {
		return false, seasondomain.ErrInvalidSeason
	}
	treasury, others, genesis, err := freshBalances(ctx, o.pool, successor)
	if err != nil {
		return false, err
	}
	if err := seasondomain.VerifyFreshBook(treasury, others, genesis); err != nil {
		return false, err
	}
	active, err := hasStage(ctx, o.pool, successor, "active")
	if err != nil {
		return false, err
	}
	if active {
		return true, nil
	}
	if _, err := recordStage(ctx, o.pool, successor, "prepared", "active"); err != nil {
		return false, err
	}
	return false, nil
}
