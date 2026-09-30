package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.ReconciliationRepository = (*Repository)(nil)

// isFrozen reads the single read-only flag: a missing row means a book
// that never froze, which is open.
func isFrozen(ctx context.Context, q rowQuerier) (bool, error) {
	var frozen bool
	err := q.QueryRow(ctx, `SELECT frozen FROM app.economy_mode`).Scan(&frozen)
	if err != nil {
		return false, nil
	}
	return frozen, nil
}

// requireUnfrozen refuses any mutation while the book is frozen on a
// conservation break. Reads never call it.
func requireUnfrozen(ctx context.Context, q rowQuerier) error {
	frozen, err := isFrozen(ctx, q)
	if err != nil {
		return err
	}
	if frozen {
		return domain.ErrEconomyFrozen
	}
	return nil
}

// pairing tallies one transfer: how many debit and credit legs it holds.
type pairing struct {
	debits  int64
	credits int64
}

// reconcileData is one conservation pass over the journal.
type reconcileData struct {
	credits   int64
	debits    int64
	custodies []application.CustodyBalance
	pairs     map[string]pairing
	genesisID string
}

// readReconciliation recomputes supply, custody positions and leg
// pairing of one book in one transaction without writing anything.
func readReconciliation(ctx context.Context, tx pgx.Tx, season domain.SeasonKey) (reconcileData, error) {
	var data reconcileData
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'credit'), 0),
		        COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'debit'), 0)
		 FROM app.economy_entries WHERE season_key = $1`, season.String()).Scan(&data.credits, &data.debits); err != nil {
		return data, fmt.Errorf("sum journal: %w", err)
	}

	rows, err := tx.Query(ctx,
		`SELECT c.id::text, c.kind, c.label,
		        COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0),
		        COUNT(e.id), COALESCE(MAX(e.id::text), '')
		 FROM app.economy_custodies c
		 LEFT JOIN app.economy_entries e ON e.custody_id = c.id
		 WHERE c.season_key = $1
		 GROUP BY c.id, c.kind, c.label
		 ORDER BY c.kind, c.label`, season.String())
	if err != nil {
		return data, fmt.Errorf("sum custodies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var custody application.CustodyBalance
		var kindRaw string
		if err := rows.Scan(&custody.CustodyID, &kindRaw, &custody.Label, &custody.Millis, &custody.Legs, &custody.LastEntry); err != nil {
			return data, fmt.Errorf("scan custody: %w", err)
		}
		kind, err := domain.ParseCustodyKind(kindRaw)
		if err != nil {
			return data, fmt.Errorf("stored custody kind %q: %w", kindRaw, err)
		}
		custody.Kind = kind
		data.custodies = append(data.custodies, custody)
	}
	if err := rows.Err(); err != nil {
		return data, fmt.Errorf("iterate custodies: %w", err)
	}

	legs, err := tx.Query(ctx,
		`SELECT transfer_id::text, direction, count(*) FROM app.economy_entries WHERE season_key = $1 GROUP BY transfer_id, direction`, season.String())
	if err != nil {
		return data, fmt.Errorf("tally legs: %w", err)
	}
	defer legs.Close()
	data.pairs = map[string]pairing{}
	for legs.Next() {
		var transfer, direction string
		var count int64
		if err := legs.Scan(&transfer, &direction, &count); err != nil {
			return data, fmt.Errorf("scan tally: %w", err)
		}
		tallied := data.pairs[transfer]
		if direction == "debit" {
			tallied.debits = count
		} else {
			tallied.credits = count
		}
		data.pairs[transfer] = tallied
	}
	if err := legs.Err(); err != nil {
		return data, fmt.Errorf("iterate tally: %w", err)
	}

	if err := tx.QueryRow(ctx,
		`SELECT e.transfer_id::text FROM app.economy_entries e
		 JOIN app.economy_genesis g ON g.treasury_custody_id = e.custody_id AND g.season_key = e.season_key
		 WHERE e.direction = 'credit' AND e.amount_milli = $1 AND e.season_key = $2
		 ORDER BY e.created_at LIMIT 1`,
		domain.GenesisSupplyMillis, season.String()).Scan(&data.genesisID); err != nil {
		data.genesisID = ""
	}
	return data, nil
}

// mismatchesOf names every conservation break in a stable vocabulary:
// supply drift, negative custodies and unpaired transfers. The Genesis
// credit is the single lawful unpaired leg: anything else missing a
// side is an orphan, and even a second S-sized credit would break the
// supply rule jointly.
func mismatchesOf(data reconcileData) []string {
	mismatches := []string{}
	if data.credits-data.debits != domain.GenesisSupplyMillis {
		mismatches = append(mismatches, fmt.Sprintf("supply != S: credits %d - debits %d", data.credits, data.debits))
	}
	for _, custody := range data.custodies {
		if custody.Millis < 0 {
			mismatches = append(mismatches, fmt.Sprintf("negative custody %s/%s: %d", custody.Kind, custody.Label, custody.Millis))
		}
	}
	unpaired := []string{}
	for transfer, tally := range data.pairs {
		if transfer == data.genesisID {
			continue
		}
		if tally.debits != 1 || tally.credits != 1 {
			unpaired = append(unpaired, transfer)
		}
	}
	sort.Strings(unpaired)
	for _, transfer := range unpaired {
		mismatches = append(mismatches, fmt.Sprintf("unpaired transfer %s", transfer))
	}
	return mismatches
}

// freezeIncident records the break and flips the single mode row in the
// caller transaction, so the report and the flag commit together.
func freezeIncident(ctx context.Context, tx pgx.Tx, mismatches []string) (string, error) {
	var incident string
	err := tx.QueryRow(ctx,
		`INSERT INTO app.economy_incidents (reason, detail) VALUES ('conservation-break', $1) RETURNING id::text`,
		strings.Join(mismatches, "; ")).Scan(&incident)
	if err != nil {
		return "", fmt.Errorf("record incident: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_mode (singleton, frozen, incident_id) VALUES (true, true, $1::uuid)
		 ON CONFLICT (singleton) DO UPDATE SET frozen = true, incident_id = $1::uuid, updated_at = now()`,
		incident); err != nil {
		return "", fmt.Errorf("freeze mode: %w", err)
	}
	return incident, nil
}

// Reconcile recomputes conservation of one season book and freezes
// the book with an incident when anything diverges. Clean books
// report no mismatch and stay open, writing nothing.
func (r *Repository) Reconcile(ctx context.Context, season domain.SeasonKey) (*application.ReconciliationReport, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reconcile transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	data, err := readReconciliation(ctx, tx, season)
	if err != nil {
		return nil, err
	}
	report := &application.ReconciliationReport{
		SupplyMillis: data.credits - data.debits,
		Custodies:    data.custodies,
	}
	report.Mismatch = mismatchesOf(data)
	for _, mismatch := range report.Mismatch {
		if strings.HasPrefix(mismatch, "unpaired transfer ") {
			report.Unpaired = append(report.Unpaired, strings.TrimPrefix(mismatch, "unpaired transfer "))
		}
	}
	if len(report.Mismatch) == 0 {
		if err := tx.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("abort clean reconcile: %w", err)
		}
		return report, nil
	}
	incident, err := freezeIncident(ctx, tx, report.Mismatch)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit freeze: %w", err)
	}
	report.Frozen = true
	report.IncidentID = incident
	return report, nil
}

// Resolve records the compensated resolution of one open break and
// reopens the book. Breaks without an open incident, and incidents that
// never froze the book, are refused.
func (r *Repository) Resolve(ctx context.Context, cmd application.ResolveCommand) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin resolve transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var frozen bool
	var incidentID string
	err = tx.QueryRow(ctx,
		`SELECT frozen, incident_id::text FROM app.economy_mode FOR UPDATE`).Scan(&frozen, &incidentID)
	if err != nil || !frozen || incidentID != cmd.IncidentID {
		return domain.ErrIncidentNotFound
	}
	var resolves *string
	if err := tx.QueryRow(ctx,
		`SELECT resolves::text FROM app.economy_incidents WHERE id = $1::uuid`,
		cmd.IncidentID).Scan(&resolves); err != nil {
		return domain.ErrIncidentNotFound
	}
	if resolves != nil {
		return domain.ErrIncidentNotFound
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_incidents (reason, detail, resolves) VALUES ('compensation', $1, $2::uuid)`,
		cmd.Note, cmd.IncidentID); err != nil {
		return fmt.Errorf("record compensation: %w", err)
	}
	// The mode row reopens only from frozen: a concurrent resolution
	// wins once, and the loser finds no frozen row to flip.
	if tag, err := tx.Exec(ctx,
		`UPDATE app.economy_mode SET frozen = false, updated_at = now() WHERE singleton AND frozen`); err != nil || tag.RowsAffected() != 1 {
		return fmt.Errorf("reopen mode: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit resolve: %w", err)
	}
	return nil
}
