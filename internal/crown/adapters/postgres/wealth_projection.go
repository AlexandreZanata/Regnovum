package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// DBQuerier abstracts *pgxpool.Pool and pgx.Tx for database queries.
type DBQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// WealthEvent defines one confirmed financial event recorded in the linearizable outbox.
type WealthEvent struct {
	SeasonID         string
	SubjectID        string
	BeneficiaryKind  string
	EventKind        string // transfer, debt, reserve, refund, freeze, eligibility
	DeltaAssets      int64
	DeltaLiabilities int64
	SourceRef        string
}

// WealthProjectionView models one projection row in app.seasonal_wealth_projections.
type WealthProjectionView struct {
	SeasonID         string
	SubjectID        string
	BeneficiaryKind  string
	AssetsMilli      int64
	LiabilitiesMilli int64
	Wealth           int64
	AttainedRevision int64
	Frozen           bool
	Eligible         bool
	UpdatedAt        time.Time
}

// LeaderView models the candidate at the top of the seasonal wealth ranking.
type LeaderView struct {
	SubjectID        string
	Wealth           int64
	AttainedRevision int64
	BeneficiaryKind  string
	AssetsMilli      int64
	LiabilitiesMilli int64
	Frozen           bool
	Eligible         bool
}

// RebuildSummary details the result of an independent oracle rebuild.
type RebuildSummary struct {
	SeasonID        string
	SubjectCount    int
	TotalAssets     int64
	TotalWealth     int64
	RebuiltRevision int64
	CheckpointHash  string
}

// WealthProjectionAdapter provides incremental outbox indexing, top-1 ranking reads,
// and independent verification against the append-only journal.
type WealthProjectionAdapter struct{}

// NewWealthProjectionAdapter creates a new adapter instance.
func NewWealthProjectionAdapter() *WealthProjectionAdapter {
	return &WealthProjectionAdapter{}
}

// RecordOutboxEventTx records an incremental wealth modification in the outbox
// within the caller's financial transaction, updates the affected subject's projection
// without scanning other accounts, and advances the season revision monotonically.
func (a *WealthProjectionAdapter) RecordOutboxEventTx(
	ctx context.Context,
	tx pgx.Tx,
	event WealthEvent,
) (int64, error) {
	if event.SeasonID == "" || event.SubjectID == "" {
		return 0, crowndomain.ErrAmbiguousFixture
	}
	kind := event.BeneficiaryKind
	if kind == "" {
		kind = string(crowndomain.BeneficiaryKindParticipant)
	}

	eventKind := event.EventKind
	if eventKind == "" {
		eventKind = "transfer"
	}

	// 1. Advance linearizable monotonic revision per season book:
	var revision int64
	err := tx.QueryRow(ctx, `
		INSERT INTO app.seasonal_wealth_checkpoints (season_id, last_revision, updated_at)
		VALUES ($1, 1, now())
		ON CONFLICT (season_id) DO UPDATE
		SET last_revision = app.seasonal_wealth_checkpoints.last_revision + 1, updated_at = now()
		RETURNING last_revision
	`, event.SeasonID).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("advance season revision: %w", err)
	}

	// 2. Insert outbox record:
	_, err = tx.Exec(ctx, `
		INSERT INTO app.seasonal_wealth_events
			(season_id, revision, subject_id, beneficiary_kind, event_kind, delta_assets, delta_liabilities, source_ref, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
	`, event.SeasonID, revision, event.SubjectID, kind, eventKind, event.DeltaAssets, event.DeltaLiabilities, event.SourceRef)
	if err != nil {
		return 0, fmt.Errorf("insert wealth outbox event: %w", err)
	}

	// 3. Read current projection for this subject (locked):
	var currentAssets, currentLiabilities, currentWealth, currentAttained int64
	var currentFrozen, currentEligible bool
	rowErr := tx.QueryRow(ctx, `
		SELECT assets_milli, liabilities_milli, wealth, attained_revision, frozen, eligible
		FROM app.seasonal_wealth_projections
		WHERE season_id = $1 AND subject_id = $2
		FOR UPDATE
	`, event.SeasonID, event.SubjectID).Scan(
		&currentAssets,
		&currentLiabilities,
		&currentWealth,
		&currentAttained,
		&currentFrozen,
		&currentEligible,
	)

	isNew := false
	if rowErr != nil {
		if errors.Is(rowErr, pgx.ErrNoRows) {
			isNew = true
			currentAssets = 0
			currentLiabilities = 0
			currentWealth = 0
			currentAttained = revision
			currentFrozen = false
			currentEligible = (kind == string(crowndomain.BeneficiaryKindParticipant))
		} else {
			return 0, fmt.Errorf("read current wealth projection: %w", rowErr)
		}
	}

	newAssets := currentAssets + event.DeltaAssets
	if newAssets < 0 {
		return 0, crowndomain.ErrInvalidWealthAmount
	}
	if newAssets > crowndomain.MaxSeasonalSupplyMillis {
		return 0, crowndomain.ErrWealthOverflow
	}

	newLiabilities := currentLiabilities + event.DeltaLiabilities
	if newLiabilities < 0 {
		return 0, crowndomain.ErrInvalidWealthAmount
	}
	if newLiabilities > crowndomain.MaxSeasonalSupplyMillis {
		return 0, crowndomain.ErrWealthOverflow
	}

	var newWealth int64 = 0
	if newAssets > newLiabilities {
		newWealth = newAssets - newLiabilities
	}

	newAttained := currentAttained
	if isNew || newWealth != currentWealth {
		newAttained = revision
	}

	// 4. Upsert projection row:
	_, err = tx.Exec(ctx, `
		INSERT INTO app.seasonal_wealth_projections
			(season_id, subject_id, beneficiary_kind, assets_milli, liabilities_milli, wealth, attained_revision, frozen, eligible, source_watermark, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())
		ON CONFLICT (season_id, subject_id) DO UPDATE
		SET assets_milli = EXCLUDED.assets_milli,
			liabilities_milli = EXCLUDED.liabilities_milli,
			wealth = EXCLUDED.wealth,
			attained_revision = EXCLUDED.attained_revision,
			frozen = EXCLUDED.frozen,
			eligible = EXCLUDED.eligible,
			source_watermark = now(),
			updated_at = now()
	`, event.SeasonID, event.SubjectID, kind, newAssets, newLiabilities, newWealth, newAttained, currentFrozen, currentEligible)
	if err != nil {
		return 0, fmt.Errorf("upsert wealth projection: %w", err)
	}

	return revision, nil
}

// RegisterObligationParams carries parameters to register a confirmed obligation.
type RegisterObligationParams struct {
	SeasonID        string
	Debtor          string
	Kind            string
	Amount          int64
	AlreadyDeducted bool
	SourceRef       string
}

// RegisterObligationTx records a confirmed seasonal debt and updates the projection incrementally.
func (a *WealthProjectionAdapter) RegisterObligationTx(
	ctx context.Context,
	tx pgx.Tx,
	params RegisterObligationParams,
) (int64, error) {
	if params.SeasonID == "" || params.Debtor == "" {
		return 0, crowndomain.ErrAmbiguousFixture
	}
	if params.Amount < 0 {
		return 0, crowndomain.ErrInvalidWealthAmount
	}
	if params.Amount > crowndomain.MaxSeasonalSupplyMillis {
		return 0, crowndomain.ErrWealthOverflow
	}
	oblKind, err := crowndomain.ParseObligationKind(params.Kind)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO app.seasonal_wealth_obligations
			(season_id, debtor_subject, obligation_kind, amount_milli, already_deducted, source_ref, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
	`, params.SeasonID, params.Debtor, string(oblKind), params.Amount, params.AlreadyDeducted, params.SourceRef)
	if err != nil {
		return 0, fmt.Errorf("insert obligation: %w", err)
	}

	deltaLiabilities := int64(0)
	if crowndomain.IsDeductibleObligation(oblKind, params.AlreadyDeducted) {
		deltaLiabilities = params.Amount
	}

	return a.RecordOutboxEventTx(ctx, tx, WealthEvent{
		SeasonID:         params.SeasonID,
		SubjectID:        params.Debtor,
		BeneficiaryKind:  string(crowndomain.BeneficiaryKindParticipant),
		EventKind:        "debt",
		DeltaAssets:      0,
		DeltaLiabilities: deltaLiabilities,
		SourceRef:        params.SourceRef,
	})
}

// GetProjection reads one subject's wealth projection.
func (a *WealthProjectionAdapter) GetProjection(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
	subjectID string,
) (WealthProjectionView, bool, error) {
	var view WealthProjectionView
	err := q.QueryRow(ctx, `
		SELECT season_id, subject_id, beneficiary_kind, assets_milli, liabilities_milli, wealth, attained_revision, frozen, eligible, updated_at
		FROM app.seasonal_wealth_projections
		WHERE season_id = $1 AND subject_id = $2
	`, seasonID, subjectID).Scan(
		&view.SeasonID,
		&view.SubjectID,
		&view.BeneficiaryKind,
		&view.AssetsMilli,
		&view.LiabilitiesMilli,
		&view.Wealth,
		&view.AttainedRevision,
		&view.Frozen,
		&view.Eligible,
		&view.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WealthProjectionView{}, false, nil
		}
		return WealthProjectionView{}, false, fmt.Errorf("get projection: %w", err)
	}
	return view, true, nil
}

// GetTopLeader queries the top-1 sovereign candidate using the index
// (season_id, wealth DESC, attained_revision, subject_id).
func (a *WealthProjectionAdapter) GetTopLeader(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
) (LeaderView, bool, error) {
	var leader LeaderView
	err := q.QueryRow(ctx, `
		SELECT subject_id, wealth, attained_revision, beneficiary_kind, assets_milli, liabilities_milli, frozen, eligible
		FROM app.seasonal_wealth_projections
		WHERE season_id = $1 AND frozen = false AND eligible = true
		ORDER BY wealth DESC, attained_revision ASC, subject_id ASC
		LIMIT 1
	`, seasonID).Scan(
		&leader.SubjectID,
		&leader.Wealth,
		&leader.AttainedRevision,
		&leader.BeneficiaryKind,
		&leader.AssetsMilli,
		&leader.LiabilitiesMilli,
		&leader.Frozen,
		&leader.Eligible,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LeaderView{}, false, nil
		}
		return LeaderView{}, false, fmt.Errorf("get top leader: %w", err)
	}
	return leader, true, nil
}

// CheckRevisionFreshness validates that the expected revision matches the database checkpoint,
// detecting gaps or stale revisions to prevent executing royal acts on outdated projections.
func (a *WealthProjectionAdapter) CheckRevisionFreshness(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
	expectedRevision int64,
) error {
	var lastRevision int64
	var frozen bool
	err := q.QueryRow(ctx, `
		SELECT last_revision, frozen
		FROM app.seasonal_wealth_checkpoints
		WHERE season_id = $1
	`, seasonID).Scan(&lastRevision, &frozen)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if expectedRevision == 0 {
				return nil
			}
			return crowndomain.ErrStaleRevision
		}
		return fmt.Errorf("query checkpoint: %w", err)
	}
	if frozen {
		return crowndomain.ErrProjectionFrozen
	}
	if expectedRevision < lastRevision {
		return crowndomain.ErrStaleRevision
	}
	if expectedRevision > lastRevision {
		return crowndomain.ErrRevisionGap
	}
	return nil
}

// SetSeasonFreeze freezes or unfreezes the seasonal projections and checkpoint.
func (a *WealthProjectionAdapter) SetSeasonFreeze(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
	frozen bool,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE app.seasonal_wealth_checkpoints
		SET frozen = $2, updated_at = now()
		WHERE season_id = $1
	`, seasonID, frozen)
	if err != nil {
		return fmt.Errorf("freeze checkpoint: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE app.seasonal_wealth_projections
		SET frozen = $2, updated_at = now()
		WHERE season_id = $1
	`, seasonID, frozen)
	if err != nil {
		return fmt.Errorf("freeze projections: %w", err)
	}
	return nil
}

// SetSubjectEligibility marks a subject as eligible or ineligible for succession.
func (a *WealthProjectionAdapter) SetSubjectEligibility(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
	subjectID string,
	eligible bool,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE app.seasonal_wealth_projections
		SET eligible = $3, updated_at = now()
		WHERE season_id = $1 AND subject_id = $2
	`, seasonID, subjectID, eligible)
	if err != nil {
		return fmt.Errorf("update subject eligibility: %w", err)
	}
	return nil
}

func fetchHoldingsFromJournal(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
	subjectID string,
	isCrown bool,
) ([]crowndomain.AssetHolding, error) {
	var custodiesSQL string
	var args []any
	if isCrown {
		custodiesSQL = `
			SELECT c.id::text, c.label,
			       COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0) as balance
			FROM app.economy_custodies c
			LEFT JOIN app.economy_entries e ON e.custody_id = c.id AND e.season_key = c.season_key
			WHERE c.season_key = $1 AND c.kind = 'treasury'
			GROUP BY c.id, c.label
		`
		args = []any{seasonID}
	} else {
		custodiesSQL = `
			SELECT c.id::text, c.label,
			       COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0) as balance
			FROM app.economy_custodies c
			LEFT JOIN app.economy_entries e ON e.custody_id = c.id AND e.season_key = c.season_key
			WHERE c.season_key = $1 AND (c.owner_account_id::text = $2 OR c.label = $2)
			GROUP BY c.id, c.label
		`
		args = []any{seasonID, subjectID}
	}

	rows, err := q.Query(ctx, custodiesSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("query journal balances: %w", err)
	}
	defer rows.Close()

	var holdings []crowndomain.AssetHolding
	for rows.Next() {
		var cusID, label string
		var balance int64
		if err := rows.Scan(&cusID, &label, &balance); err != nil {
			return nil, fmt.Errorf("scan journal balance: %w", err)
		}
		if balance < 0 {
			balance = 0
		}
		vault := ""
		if isCrown {
			switch label {
			case "free_treasury", "tesouro-livre":
				vault = "tesouro-livre"
			case "sovereign_reserve", "reserva-soberana":
				vault = "reserva-soberana"
			case "commercial_stock", "estoque-comercial":
				vault = "estoque-comercial"
			case "operating_cash", "caixa-operacional":
				vault = "caixa-operacional"
			default:
				vault = label
			}
		}
		holdings = append(holdings, crowndomain.AssetHolding{
			CustodyID:   cusID,
			Beneficiary: crowndomain.HolderSubject(subjectID),
			Season:      crowndomain.SeasonID(seasonID),
			Kind:        crowndomain.AssetKindLiquidUnconditional,
			Amount:      balance,
			Vault:       vault,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate journal balances: %w", err)
	}
	return holdings, nil
}

func fetchObligations(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
	subjectID string,
) ([]crowndomain.Obligation, error) {
	oblRows, err := q.Query(ctx, `
		SELECT id::text, obligation_kind, amount_milli, already_deducted
		FROM app.seasonal_wealth_obligations
		WHERE season_id = $1 AND debtor_subject = $2
	`, seasonID, subjectID)
	if err != nil {
		return nil, fmt.Errorf("query obligations: %w", err)
	}
	defer oblRows.Close()

	var obligations []crowndomain.Obligation
	for oblRows.Next() {
		var oblID, oblKind string
		var amount int64
		var alreadyDeducted bool
		if err := oblRows.Scan(&oblID, &oblKind, &amount, &alreadyDeducted); err != nil {
			return nil, fmt.Errorf("scan obligation: %w", err)
		}
		obligations = append(obligations, crowndomain.Obligation{
			ID:              oblID,
			Debtor:          crowndomain.HolderSubject(subjectID),
			Season:          crowndomain.SeasonID(seasonID),
			Kind:            crowndomain.ObligationKind(oblKind),
			Amount:          amount,
			AlreadyDeducted: alreadyDeducted,
		})
	}
	if err := oblRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate obligations: %w", err)
	}
	return obligations, nil
}

// DeriveWealthFromJournal rebuilds the beneficial wealth for one subject directly
// from the immutable journal legs and confirmed obligations without using the projection table.
func (a *WealthProjectionAdapter) DeriveWealthFromJournal(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
	subjectID string,
) (crowndomain.BeneficialWealth, error) {
	isCrown := (subjectID == string(crowndomain.InstitutionalCrownSubject))

	holdings, err := fetchHoldingsFromJournal(ctx, q, seasonID, subjectID, isCrown)
	if err != nil {
		return crowndomain.BeneficialWealth{}, err
	}

	obligations, err := fetchObligations(ctx, q, seasonID, subjectID)
	if err != nil {
		return crowndomain.BeneficialWealth{}, err
	}

	beneficiaryKind := crowndomain.BeneficiaryKindParticipant
	if isCrown {
		beneficiaryKind = crowndomain.BeneficiaryKindInstitutional
	}

	return crowndomain.EvaluateBeneficialWealth(
		crowndomain.WealthPolicyV1,
		crowndomain.SeasonID(seasonID),
		crowndomain.Beneficiary{
			Subject: crowndomain.HolderSubject(subjectID),
			Kind:    beneficiaryKind,
		},
		holdings,
		obligations,
	)
}

// RebuildAllProjections acts as the independent oracle verifier:
// it derives the true wealth for all subjects from the journal and obligations,
// reconciles with app.seasonal_wealth_projections, and computes an audited integrity hash.
func (a *WealthProjectionAdapter) RebuildAllProjections(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
) (RebuildSummary, error) {
	// Find all unique subjects in this season (custody owners and debtors):
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT 
		       CASE WHEN c.kind = 'treasury' THEN 'coroa-institucional' ELSE COALESCE(c.owner_account_id::text, c.label) END as subject_id,
		       CASE WHEN c.kind = 'treasury' THEN 'institucional' ELSE 'participante' END as kind
		FROM app.economy_custodies c
		WHERE c.season_key = $1
		UNION
		SELECT DISTINCT debtor_subject as subject_id, 'participante' as kind
		FROM app.seasonal_wealth_obligations
		WHERE season_id = $1
	`, seasonID)
	if err != nil {
		return RebuildSummary{}, fmt.Errorf("list season subjects: %w", err)
	}
	defer rows.Close()

	type subjectEntry struct {
		id   string
		kind string
	}
	var subjects []subjectEntry
	for rows.Next() {
		var sub subjectEntry
		if err := rows.Scan(&sub.id, &sub.kind); err != nil {
			return RebuildSummary{}, fmt.Errorf("scan subject: %w", err)
		}
		subjects = append(subjects, sub)
	}
	if err := rows.Err(); err != nil {
		return RebuildSummary{}, fmt.Errorf("iterate subjects: %w", err)
	}

	hasher := sha256.New()
	totalAssets := int64(0)
	totalWealth := int64(0)

	for _, sub := range subjects {
		bw, err := a.DeriveWealthFromJournal(ctx, tx, seasonID, sub.id)
		if err != nil {
			return RebuildSummary{}, fmt.Errorf("derive wealth for %s: %w", sub.id, err)
		}

		proj, found, err := a.GetProjection(ctx, tx, seasonID, sub.id)
		if err != nil {
			return RebuildSummary{}, fmt.Errorf("get existing projection for %s: %w", sub.id, err)
		}

		// Reconcile and check divergence:
		if found {
			if proj.AssetsMilli != bw.Assets || proj.LiabilitiesMilli != bw.Liabilities || proj.Wealth != bw.NetWealth {
				return RebuildSummary{}, fmt.Errorf("%w: subject %s proj=%+v oracle=%+v",
					crowndomain.ErrProjectionMismatch, sub.id, proj, bw)
			}
		} else {
			// Insert missing projection row from oracle:
			_, err = tx.Exec(ctx, `
				INSERT INTO app.seasonal_wealth_projections
					(season_id, subject_id, beneficiary_kind, assets_milli, liabilities_milli, wealth, attained_revision, frozen, eligible, source_watermark, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, 1, false, true, now(), now())
			`, seasonID, sub.id, string(bw.Kind), bw.Assets, bw.Liabilities, bw.NetWealth)
			if err != nil {
				return RebuildSummary{}, fmt.Errorf("insert rebuilt projection: %w", err)
			}
		}

		totalAssets += bw.Assets
		totalWealth += bw.NetWealth
		fmt.Fprintf(hasher, "%s|%s|%d|%d|%d\n", seasonID, sub.id, bw.Assets, bw.Liabilities, bw.NetWealth)
	}

	checkpointHash := hex.EncodeToString(hasher.Sum(nil))

	var currentRevision int64
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(last_revision, 0) FROM app.seasonal_wealth_checkpoints WHERE season_id = $1
	`, seasonID).Scan(&currentRevision)

	_, err = tx.Exec(ctx, `
		INSERT INTO app.seasonal_wealth_checkpoints (season_id, last_revision, checkpoint_hash, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (season_id) DO UPDATE
		SET checkpoint_hash = EXCLUDED.checkpoint_hash, updated_at = now()
	`, seasonID, currentRevision, checkpointHash)
	if err != nil {
		return RebuildSummary{}, fmt.Errorf("update checkpoint: %w", err)
	}

	return RebuildSummary{
		SeasonID:        seasonID,
		SubjectCount:    len(subjects),
		TotalAssets:     totalAssets,
		TotalWealth:     totalWealth,
		RebuiltRevision: currentRevision,
		CheckpointHash:  checkpointHash,
	}, nil
}
