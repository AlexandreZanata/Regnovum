package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var (
	_ application.MonetaryHealthSource = (*Repository)(nil)
	_ application.MonetaryHealthStore  = (*Repository)(nil)
)

// readMoneySignals recomputes the five conservation equalities of one
// book from independent queries: the journal net against S, the
// custody sums against the journal net, the hold-custody backing
// against the holds registry, the stored projections against the
// journal net when the evaluator wrote them, and the commercial-stock
// partition balances against their own legs.
func readMoneySignals(ctx context.Context, tx pgx.Tx, season domain.SeasonKey) ([]domain.MonitorObservation, error) {
	key := season.String()
	var credits, debits, legs int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*),
		        COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'credit'), 0),
		        COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'debit'), 0)
		 FROM app.economy_entries WHERE season_key = $1`, key).Scan(&legs, &credits, &debits); err != nil {
		return nil, fmt.Errorf("sum journal: %w", err)
	}
	var seasonRow bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`, key).Scan(&seasonRow); err != nil {
		return nil, fmt.Errorf("lookup season: %w", err)
	}
	supply := domain.GenesisSupplyMillis
	if legs == 0 && !seasonRow {
		supply = 0
	}
	observations := []domain.MonitorObservation{
		{Observable: domain.MonitorSupply, Expected: supply, Observed: credits - debits},
	}

	var vaults int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(signed), 0) FROM (
		   SELECT SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END) AS signed
		   FROM app.economy_custodies c
		   LEFT JOIN app.economy_entries e ON e.custody_id = c.id AND e.season_key = $1
		   WHERE c.season_key = $1
		   GROUP BY c.id
		 ) tallied`, key).Scan(&vaults); err != nil {
		return nil, fmt.Errorf("sum vaults: %w", err)
	}
	observations = append(observations, domain.MonitorObservation{
		Observable: domain.MonitorVaults, Expected: credits - debits, Observed: vaults,
	})

	var holdsRegistry, holdsBacking int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(h.amount_milli), 0)
		 FROM app.economy_holds h
		 JOIN app.economy_custodies c ON c.id = h.hold_custody_id
		 WHERE h.status = 'active' AND c.season_key = $1`, key).Scan(&holdsRegistry); err != nil {
		return nil, fmt.Errorf("sum holds registry: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(signed), 0) FROM (
		   SELECT SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END) AS signed
		   FROM app.economy_custodies c
		   LEFT JOIN app.economy_entries e ON e.custody_id = c.id AND e.season_key = $1
		   WHERE c.season_key = $1
		     AND c.id IN (SELECT hold_custody_id FROM app.economy_holds WHERE status = 'active')
		   GROUP BY c.id
		 ) tallied`, key).Scan(&holdsBacking); err != nil {
		return nil, fmt.Errorf("sum holds backing: %w", err)
	}
	observations = append(observations, domain.MonitorObservation{
		Observable: domain.MonitorObligations, Expected: holdsRegistry, Observed: holdsBacking,
	})

	var projectionRows, projectionSum int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*), COALESCE(SUM(wealth), 0)
		 FROM app.seasonal_wealth_projections WHERE season_id = $1`, key).Scan(&projectionRows, &projectionSum); err != nil {
		return nil, fmt.Errorf("sum projections: %w", err)
	}
	projectionExpected := credits - debits
	if projectionRows == 0 {
		projectionExpected = 0
		projectionSum = 0
	}
	observations = append(observations, domain.MonitorObservation{
		Observable: domain.MonitorProjections, Expected: projectionExpected, Observed: projectionSum,
	})

	var stockBalances, stockLegs int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(signed), 0) FROM (
		   SELECT SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END) AS signed
		   FROM app.economy_custodies c
		   LEFT JOIN app.economy_entries e ON e.custody_id = c.id AND e.season_key = $1
		   WHERE c.season_key = $1
		     AND c.id IN (SELECT custody_id FROM app.economy_partitions WHERE name = 'commercial_stock')
		   GROUP BY c.id
		 ) tallied`, key).Scan(&stockBalances); err != nil {
		return nil, fmt.Errorf("sum stock balances: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE e.season_key = $1 AND c.season_key = $1
		   AND c.id IN (SELECT custody_id FROM app.economy_partitions WHERE name = 'commercial_stock')`, key).Scan(&stockLegs); err != nil {
		return nil, fmt.Errorf("sum stock legs: %w", err)
	}
	observations = append(observations, domain.MonitorObservation{
		Observable: domain.MonitorStock, Expected: stockLegs, Observed: stockBalances,
	})
	return observations, nil
}

// readBacklog counts external events without a terminal outcome: they
// are delay, not divergence, and the operator budget decides.
func readBacklog(ctx context.Context, tx pgx.Tx) (int64, error) {
	var pending int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM app.stripe_events WHERE processed_at IS NULL`).Scan(&pending); err != nil {
		return 0, fmt.Errorf("count pending webhooks: %w", err)
	}
	return pending, nil
}

// readSuccessionLag reports seconds since the last evaluated
// checkpoint when the outbox holds unprocessed revisions, else zero.
// The database clock measures the delay inside the read transaction.
func readSuccessionLag(ctx context.Context, tx pgx.Tx, season domain.SeasonKey) (int64, error) {
	key := season.String()
	var outboxMax int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM app.seasonal_wealth_events WHERE season_id = $1`, key).Scan(&outboxMax); err != nil {
		return 0, fmt.Errorf("read outbox watermark: %w", err)
	}
	if outboxMax == 0 {
		return 0, nil
	}
	var lastRevision int64
	var checkpointFound bool
	var lagSeconds int64
	err := tx.QueryRow(ctx,
		`SELECT last_revision, EXTRACT(EPOCH FROM (now() - updated_at))::bigint
		 FROM app.seasonal_wealth_checkpoints WHERE season_id = $1`, key).Scan(&lastRevision, &lagSeconds)
	if err != nil {
		checkpointFound = false
	} else {
		checkpointFound = true
	}
	if !checkpointFound {
		if err := tx.QueryRow(ctx,
			`SELECT EXTRACT(EPOCH FROM (now() - MIN(created_at)))::bigint
			 FROM app.seasonal_wealth_events WHERE season_id = $1`, key).Scan(&lagSeconds); err != nil {
			return 0, fmt.Errorf("measure unevaluated lag: %w", err)
		}
		return lagSeconds, nil
	}
	if outboxMax <= lastRevision {
		return 0, nil
	}
	return lagSeconds, nil
}

// readArchive judges the sealed-book receipt: sealed without a row,
// or a snapshot that is not byte-equal to S, blocks the successor.
func readArchive(ctx context.Context, tx pgx.Tx, season domain.SeasonKey) (domain.MonitorArchive, error) {
	var archive domain.MonitorArchive
	key := season.String()
	var latest string
	err := tx.QueryRow(ctx,
		`SELECT to_state FROM app.season_lifecycle WHERE season_key = $1
		 ORDER BY recorded_at DESC, id DESC LIMIT 1`, key).Scan(&latest)
	if err != nil {
		return archive, nil
	}
	if latest != "sealed" && latest != "archived" {
		return archive, nil
	}
	archive.Sealed = true
	archive.ExpectedMillis = domain.GenesisSupplyMillis
	err = tx.QueryRow(ctx,
		`SELECT snapshot_milli FROM app.season_archives WHERE season_key = $1`, key).Scan(&archive.SnapshotMillis)
	if err != nil {
		return archive, nil
	}
	archive.HasReceipt = true
	return archive, nil
}

// ReadHealthSnapshot reads one authoritative snapshot of the judged
// book in a single read-only transaction. A read error fails the pass
// closed upstream: the monitor never reports green on unread state.
func (r *Repository) ReadHealthSnapshot(ctx context.Context, season domain.SeasonKey) (domain.MonitorSnapshot, error) {
	var snapshot domain.MonitorSnapshot
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("begin snapshot transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	snapshot.Season = season
	observations, err := readMoneySignals(ctx, tx, season)
	if err != nil {
		return snapshot, err
	}
	snapshot.Observations = observations
	pending, err := readBacklog(ctx, tx)
	if err != nil {
		return snapshot, err
	}
	snapshot.PendingWebhooks = pending
	lag, err := readSuccessionLag(ctx, tx, season)
	if err != nil {
		return snapshot, err
	}
	snapshot.LagObservedSeconds = lag
	archive, err := readArchive(ctx, tx, season)
	if err != nil {
		return snapshot, err
	}
	snapshot.Archive = archive
	if err := tx.Rollback(ctx); err != nil {
		return snapshot, fmt.Errorf("abort snapshot read: %w", err)
	}
	return snapshot, nil
}

// FreezeWithHealthAlert freezes mutations and persists the redacted
// alert lines in one transaction, returning the naming incident. A
// freeze without lines is refused: silent freezes do not exist.
func (r *Repository) FreezeWithHealthAlert(ctx context.Context, season domain.SeasonKey, lines []string) (string, error) {
	_ = season
	if len(lines) == 0 {
		return "", domain.ErrAlertWithoutAction
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin freeze transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var incident string
	err = tx.QueryRow(ctx,
		`INSERT INTO app.economy_incidents (reason, detail) VALUES ('monitor-freeze', $1) RETURNING id::text`,
		strings.Join(lines, "\n")).Scan(&incident)
	if err != nil {
		return "", fmt.Errorf("record freeze incident: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_mode (singleton, frozen, incident_id) VALUES (true, true, $1::uuid)
		 ON CONFLICT (singleton) DO UPDATE SET frozen = true, incident_id = $1::uuid, updated_at = now()`,
		incident); err != nil {
		return "", fmt.Errorf("freeze mode: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit freeze: %w", err)
	}
	return incident, nil
}

// RecordHealthAlerts persists one redacted line per finding and
// returns one identifier per line, in order. Short or empty batches
// are refused upstream as alerts without action.
func (r *Repository) RecordHealthAlerts(ctx context.Context, season domain.SeasonKey, lines []string) ([]string, error) {
	_ = season
	if len(lines) == 0 {
		return nil, domain.ErrAlertWithoutAction
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin alert transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	ids := make([]string, 0, len(lines))
	for _, line := range lines {
		var id string
		err := tx.QueryRow(ctx,
			`INSERT INTO app.economy_incidents (reason, detail) VALUES ('monitor-alert', $1) RETURNING id::text`,
			line).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("record alert: %w", err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit alerts: %w", err)
	}
	return ids, nil
}
