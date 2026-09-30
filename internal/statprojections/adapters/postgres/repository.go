// Package postgres persists rebuildable public statistics projections.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	statapp "github.com/AlexandreZanata/Regnovum/internal/statprojections/application"
	transparencypg "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/postgres"
	transparencyapp "github.com/AlexandreZanata/Regnovum/internal/transparency/application"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var _ statapp.Repository = (*Repository)(nil)

// RebuildArenaBatch reads and writes at most limit Arenas. The cursor is a
// stable UUID keyset, and the short transaction commits each batch as a unit.
func (r *Repository) RebuildArenaBatch(ctx context.Context, cursor string, limit int) (statapp.ArenaRebuildBatch, error) {
	if r == nil || r.pool == nil {
		return statapp.ArenaRebuildBatch{}, errors.New("stat projections: postgres pool is nil")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return statapp.ArenaRebuildBatch{}, fmt.Errorf("begin arena projection batch: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT a.id,
		       (SELECT count(*) FROM app.arguments x
		          WHERE x.arena_id = a.id AND x.status = 'published')::bigint,
		       (SELECT count(*) FROM app.debate_positions p
		          JOIN app.accounts ac ON ac.id = p.account_id AND ac.status = 'active'
		          WHERE p.arena_id = a.id),
		       (SELECT count(*) FROM app.position_changes c WHERE c.arena_id = a.id),
		       (SELECT count(*) FROM app.persuasion_attributions t
		          JOIN app.position_changes c ON c.id = t.position_change_id
		          WHERE c.arena_id = a.id AND t.status = 'valid'),		       GREATEST(
			          a.created_at,
			          COALESCE((SELECT max(x.created_at) FROM app.arguments x WHERE x.arena_id = a.id), a.created_at),
			          COALESCE((SELECT max(c.changed_at) FROM app.position_changes c WHERE c.arena_id = a.id), a.created_at),
			          COALESCE((SELECT max(t.created_at) FROM app.persuasion_attributions t
			                    JOIN app.position_changes c ON c.id = t.position_change_id
			                    WHERE c.arena_id = a.id), a.created_at)
		       ) AS source_watermark

		FROM app.arenas a
		WHERE a.status IN ('published', 'closed', 'restricted')
		  AND (NULLIF($1, '')::uuid IS NULL OR a.id > NULLIF($1, '')::uuid)
		ORDER BY a.id ASC
		LIMIT $2`, cursor, limit)
	if err != nil {
		return statapp.ArenaRebuildBatch{}, fmt.Errorf("list arena projection sources: %w", err)
	}
	defer rows.Close()

	type sourceRow struct {
		id                                             pgtype.UUID
		watermark                                      pgtype.Timestamptz
		published, participants, changes, attributions int64
	}
	sources := make([]sourceRow, 0, limit)
	for rows.Next() {
		var source sourceRow
		if err := rows.Scan(&source.id, &source.published, &source.participants, &source.changes, &source.attributions, &source.watermark); err != nil {
			return statapp.ArenaRebuildBatch{}, fmt.Errorf("scan arena projection source: %w", err)
		}
		if !source.id.Valid || !source.watermark.Valid {
			return statapp.ArenaRebuildBatch{}, errors.New("scan arena projection source: incomplete source row")
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return statapp.ArenaRebuildBatch{}, fmt.Errorf("iterate arena projection sources: %w", err)
	}
	rows.Close()

	batch := statapp.ArenaRebuildBatch{}
	for _, source := range sources {
		stats, err := json.Marshal(map[string]int64{
			"published_arguments":   source.published,
			"eligible_participants": source.participants,
			"position_changes":      source.changes,
			"valid_attributions":    source.attributions,
		})
		if err != nil {
			return statapp.ArenaRebuildBatch{}, fmt.Errorf("encode arena projection: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.arena_public_stat_projections
				(arena_id, projection_version, source_watermark, stats, rebuilt_at)
			VALUES ($1, $2, $3, $4::jsonb, now())
			ON CONFLICT (arena_id) DO UPDATE SET
				projection_version = EXCLUDED.projection_version,
				source_watermark = EXCLUDED.source_watermark,
				stats = EXCLUDED.stats,
				rebuilt_at = EXCLUDED.rebuilt_at`,
			source.id, statapp.ProjectionVersion, source.watermark, stats); err != nil {
			return statapp.ArenaRebuildBatch{}, fmt.Errorf("write arena projection: %w", err)
		}
		batch.Processed++
		batch.NextCursor = uuidString(source.id)
		batch.Watermarks = append(batch.Watermarks, source.watermark.Time.UTC())
	}
	if err := tx.Commit(ctx); err != nil {
		return statapp.ArenaRebuildBatch{}, fmt.Errorf("commit arena projection batch: %w", err)
	}
	if batch.Processed == 0 {
		batch.NextCursor = ""
	}
	return batch, nil
}

// RebuildTransparency stores the same privacy-safe, suppressed counts exposed
// by the transparency use case. One period is one upsert, so a retry is safe.
func (r *Repository) RebuildTransparency(ctx context.Context, start, end time.Time) error {
	if r == nil || r.pool == nil {
		return errors.New("stat projections: postgres pool is nil")
	}
	source := transparencypg.NewRepository(r.pool)
	derive, err := transparencyapp.NewDeriveMetricsUseCase(source)
	if err != nil {
		return fmt.Errorf("compose transparency rebuild: %w", err)
	}
	period, err := domain.NewPeriod(start, end)
	if err != nil {
		return fmt.Errorf("build transparency period: %w", err)
	}
	snapshot, err := derive.Execute(ctx, period)
	if err != nil {
		return fmt.Errorf("derive transparency projection: %w", err)
	}
	stats, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode transparency projection: %w", err)
	}
	watermark, err := r.transparencyWatermark(ctx, start, end)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO app.transparency_stat_projections
			(period_start, period_end, methodology_version, source_watermark, stats, rebuilt_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, now())
		ON CONFLICT (period_start, period_end) DO UPDATE SET
			methodology_version = EXCLUDED.methodology_version,
			source_watermark = EXCLUDED.source_watermark,
			stats = EXCLUDED.stats,
			rebuilt_at = EXCLUDED.rebuilt_at`,
		start, end, domain.MethodologyVersion, watermark, stats)
	if err != nil {
		return fmt.Errorf("write transparency projection: %w", err)
	}
	return nil
}

func (r *Repository) transparencyWatermark(ctx context.Context, start, end time.Time) (time.Time, error) {
	var watermark pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `
		SELECT GREATEST(
			$1::timestamptz,
			COALESCE((SELECT max(created_at) FROM app.accounts WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.arenas WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(published_at) FROM app.arenas WHERE published_at >= $1 AND published_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.arguments WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(changed_at) FROM app.position_changes WHERE changed_at >= $1 AND changed_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.persuasion_attributions WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.wallet_operations WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.wallet_transactions WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.arena_pass_lots WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(consumed_at) FROM app.arena_pass_consumptions WHERE consumed_at >= $1 AND consumed_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.moderation_reports WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.moderation_actions WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(created_at) FROM app.moderation_appeals WHERE created_at >= $1 AND created_at < $2), $1),
			COALESCE((SELECT max(decided_at) FROM app.moderation_appeals WHERE decided_at >= $1 AND decided_at < $2), $1)
		)`, start, end).Scan(&watermark)
	if err != nil {
		return time.Time{}, fmt.Errorf("read transparency source watermark: %w", err)
	}
	return watermark.Time.UTC(), nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	b := value.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x", b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
