package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var (
	_ application.RestoreSnapshotter = (*Repository)(nil)
	_ application.RestoreResumeGuard = (*Repository)(nil)
)

// SnapshotBook reads one book fingerprint in a single read-only
// transaction: the journal net, every custody balance keyed by kind
// and label, the history counts, the archive receipt with the latest
// lifecycle state, and the active reign with its predecessors. A read
// error fails the verification closed upstream.
func (r *Repository) SnapshotBook(ctx context.Context, season domain.SeasonKey) (domain.BookFingerprint, error) {
	var snapshot domain.BookFingerprint
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("begin snapshot transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	key := season.String()
	snapshot.Season = season
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0),
		        count(*)
		 FROM app.economy_entries WHERE season_key = $1`, key).Scan(&snapshot.SupplyMillis, &snapshot.Legs); err != nil {
		return snapshot, fmt.Errorf("sum journal: %w", err)
	}
	rows, err := tx.Query(ctx,
		`SELECT c.kind || '/' || c.label,
		        COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_custodies c
		 LEFT JOIN app.economy_entries e ON e.custody_id = c.id AND e.season_key = $1
		 WHERE c.season_key = $1
		 GROUP BY c.kind, c.label
		 ORDER BY c.kind, c.label`, key)
	if err != nil {
		return snapshot, fmt.Errorf("sum custodies: %w", err)
	}
	snapshot.Custodies = map[string]int64{}
	for rows.Next() {
		var name string
		var balance int64
		if err := rows.Scan(&name, &balance); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan custody: %w", err)
		}
		snapshot.Custodies[name] = balance
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate custodies: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_intentions WHERE season_key = $1`, key).Scan(&snapshot.Intentions); err != nil {
		return snapshot, fmt.Errorf("count intentions: %w", err)
	}
	var latest string
	err = tx.QueryRow(ctx,
		`SELECT to_state FROM app.season_lifecycle WHERE season_key = $1
		 ORDER BY recorded_at DESC, id DESC LIMIT 1`, key).Scan(&latest)
	if err == nil && (latest == "sealed" || latest == "archived") {
		snapshot.ArchiveSealed = true
	}
	err = tx.QueryRow(ctx,
		`SELECT manifest_hash FROM app.season_archives WHERE season_key = $1`, key).Scan(&snapshot.ArchiveDigest)
	if err != nil {
		snapshot.ArchiveDigest = ""
	}
	err = tx.QueryRow(ctx,
		`SELECT holder_subject, reign_version FROM app.seasonal_reigns
		 WHERE season_id = $1 AND is_active ORDER BY reign_version DESC LIMIT 1`, key).Scan(&snapshot.ActiveReignHolder, &snapshot.ActiveReignVersion)
	if err == nil {
		snapshot.HasActiveReign = true
	}
	predecessors, err := tx.Query(ctx,
		`SELECT holder_subject FROM app.seasonal_reigns
		 WHERE season_id = $1 AND NOT is_active ORDER BY reign_version`, key)
	if err != nil {
		return snapshot, fmt.Errorf("read predecessors: %w", err)
	}
	for predecessors.Next() {
		var holder string
		if err := predecessors.Scan(&holder); err != nil {
			predecessors.Close()
			return snapshot, fmt.Errorf("scan predecessor: %w", err)
		}
		snapshot.FormerHolders = append(snapshot.FormerHolders, holder)
	}
	predecessors.Close()
	if err := predecessors.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate predecessors: %w", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		return snapshot, fmt.Errorf("abort snapshot read: %w", err)
	}
	return snapshot, nil
}

// BlockRestore freezes the restored cluster and persists the redacted
// divergence lines in one transaction, returning the naming block. A
// block without lines is refused: silent blocks do not exist.
func (r *Repository) BlockRestore(ctx context.Context, season domain.SeasonKey, lines []string) (string, error) {
	_ = season
	if len(lines) == 0 {
		return "", domain.ErrAlertWithoutAction
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin block transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var block string
	err = tx.QueryRow(ctx,
		`INSERT INTO app.economy_incidents (reason, detail) VALUES ('restore-divergence', $1) RETURNING id::text`,
		strings.Join(lines, "\n")).Scan(&block)
	if err != nil {
		return "", fmt.Errorf("record restore block: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_mode (singleton, frozen, incident_id) VALUES (true, true, $1::uuid)
		 ON CONFLICT (singleton) DO UPDATE SET frozen = true, incident_id = $1::uuid, updated_at = now()`,
		block); err != nil {
		return "", fmt.Errorf("freeze restored mode: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit restore block: %w", err)
	}
	return block, nil
}
