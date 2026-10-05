package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// ReignRecord models one reign row from app.seasonal_reigns.
type ReignRecord struct {
	ID                 string
	SeasonID           string
	ReignVersion       int
	HolderSubject      string
	IsRegent           bool
	Reason             string
	InstitutionalC     int64
	WinningWealth      int64
	AttainedRevision   int64
	TransitionRevision int64
	Predecessor        string
	IsActive           bool
	StartedAt          time.Time
	EndedAt            *time.Time
}

// SuccessionNotificationEvent models one notification outbox entry in app.seasonal_succession_events.
type SuccessionNotificationEvent struct {
	ID                 string
	SeasonID           string
	ReignVersion       int
	EventKind          string
	SuccessorSubject   string
	PredecessorSubject string
	InstitutionalC     int64
	WinningWealth      int64
	TransitionRevision int64
	Payload            string
	CreatedAt          time.Time
}

// TransitionOutcome models the result of a single evaluated revision.
type TransitionOutcome struct {
	SeasonID            string
	TransitionRevision  int64
	ReignVersion        int
	HolderSubject       string
	Predecessor         string
	IsRegent            bool
	Reason              string
	WinningWealth       int64
	ReignVersionChanged bool
}

// SuccessionEvaluatorAdapter coordinates outbox-driven sovereign evaluations,
// atomic reign transitions, single-active-reign enforcement, and backlog detection.
type SuccessionEvaluatorAdapter struct {
	db DBQuerier
}

// NewSuccessionEvaluatorAdapter constructs a new adapter instance.
func NewSuccessionEvaluatorAdapter(db DBQuerier) *SuccessionEvaluatorAdapter {
	return &SuccessionEvaluatorAdapter{db: db}
}

// InitSeasonEvaluatorTx sets up initial active reign and evaluator checkpoint if not present.
func (a *SuccessionEvaluatorAdapter) InitSeasonEvaluatorTx(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
	initialMonarch string,
	initialRevision int64,
) error {
	if seasonID == "" || initialMonarch == "" {
		return crowndomain.ErrAmbiguousFixture
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO app.seasonal_reigns (
			season_id, reign_version, holder_subject, is_regent, reason,
			institutional_c, winning_wealth, attained_revision, transition_revision,
			predecessor, is_active, started_at, updated_at
		) VALUES ($1, 1, $2, false, 'conquest', 0, 0, $3, $3, '', true, now(), now())
		ON CONFLICT (season_id, reign_version) DO NOTHING
	`, seasonID, initialMonarch, initialRevision)
	if err != nil {
		return fmt.Errorf("init active reign: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO app.seasonal_succession_evaluators (
			season_id, last_evaluated_revision, active_worker_id, lease_expires_at, updated_at
		) VALUES ($1, $2, '', '1970-01-01 00:00:00Z', now())
		ON CONFLICT (season_id) DO NOTHING
	`, seasonID, initialRevision)
	if err != nil {
		return fmt.Errorf("init evaluator checkpoint: %w", err)
	}

	return nil
}

// AcquireLease attempts to acquire or renew worker lease for the given season.
func (a *SuccessionEvaluatorAdapter) AcquireLease(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
	workerID string,
	ttl time.Duration,
) (bool, error) {
	if seasonID == "" || workerID == "" {
		return false, crowndomain.ErrAmbiguousFixture
	}

	var lastRev int64
	err := q.QueryRow(ctx, `
		UPDATE app.seasonal_succession_evaluators
		SET active_worker_id = $2,
			lease_expires_at = now() + $3,
			updated_at = now()
		WHERE season_id = $1
		  AND (lease_expires_at < now() OR active_worker_id = $2)
		RETURNING last_evaluated_revision
	`, seasonID, workerID, ttl.String()).Scan(&lastRev)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("acquire lease query: %w", err)
	}

	// Verify if another worker holds the lease or row is missing:
	var activeWorker string
	var isActiveLease bool
	err = q.QueryRow(ctx, `
		SELECT active_worker_id, (lease_expires_at > now())
		FROM app.seasonal_succession_evaluators
		WHERE season_id = $1
	`, seasonID).Scan(&activeWorker, &isActiveLease)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, crowndomain.ErrSeasonMismatch
		}
		return false, fmt.Errorf("check lease ownership: %w", err)
	}

	if isActiveLease && activeWorker != workerID {
		return false, crowndomain.ErrWorkerLeaseBusy
	}

	return false, nil
}

// GetActiveReign reads the current active reign for the season.
func (a *SuccessionEvaluatorAdapter) GetActiveReign(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
) (ReignRecord, bool, error) {
	var r ReignRecord
	err := q.QueryRow(ctx, `
		SELECT id, season_id, reign_version, holder_subject, is_regent, reason,
		       institutional_c, winning_wealth, attained_revision, transition_revision,
		       predecessor, is_active, started_at, ended_at
		FROM app.seasonal_reigns
		WHERE season_id = $1 AND is_active = true
	`, seasonID).Scan(
		&r.ID, &r.SeasonID, &r.ReignVersion, &r.HolderSubject, &r.IsRegent, &r.Reason,
		&r.InstitutionalC, &r.WinningWealth, &r.AttainedRevision, &r.TransitionRevision,
		&r.Predecessor, &r.IsActive, &r.StartedAt, &r.EndedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReignRecord{}, false, nil
		}
		return ReignRecord{}, false, fmt.Errorf("get active reign: %w", err)
	}
	return r, true, nil
}

// CheckBacklog verifies that all confirmed financial events have been evaluated.
// Halts royal acts if unevaluated economic events exist in the journal.
func (a *SuccessionEvaluatorAdapter) CheckBacklog(
	ctx context.Context,
	q DBQuerier,
	seasonID string,
) error {
	var lastCommittedRev, lastEvaluatedRev int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(c.last_revision, 0), COALESCE(e.last_evaluated_revision, 0)
		FROM app.seasonal_wealth_checkpoints c
		LEFT JOIN app.seasonal_succession_evaluators e ON e.season_id = c.season_id
		WHERE c.season_id = $1
	`, seasonID).Scan(&lastCommittedRev, &lastEvaluatedRev)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("check backlog: %w", err)
	}

	if lastEvaluatedRev < lastCommittedRev {
		return crowndomain.ErrBacklogUnevaluated
	}
	return nil
}

func (a *SuccessionEvaluatorAdapter) verifyLeaseAndNextRevision(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
	workerID string,
) (int64, bool, error) {
	var lastEvaluatedRev int64
	var activeWorker string
	var isExpired bool

	err := tx.QueryRow(ctx, `
		SELECT last_evaluated_revision, active_worker_id, (lease_expires_at < now())
		FROM app.seasonal_succession_evaluators
		WHERE season_id = $1
		FOR UPDATE
	`, seasonID).Scan(&lastEvaluatedRev, &activeWorker, &isExpired)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, crowndomain.ErrSeasonMismatch
		}
		return 0, false, fmt.Errorf("lock evaluator row: %w", err)
	}

	if activeWorker != workerID || isExpired {
		return 0, false, crowndomain.ErrStaleWorkerLease
	}

	var lastCommittedRev int64
	err = tx.QueryRow(ctx, `
		SELECT last_revision
		FROM app.seasonal_wealth_checkpoints
		WHERE season_id = $1
	`, seasonID).Scan(&lastCommittedRev)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read checkpoint revision: %w", err)
	}

	if lastEvaluatedRev >= lastCommittedRev {
		return 0, false, nil // no backlog
	}

	targetRev := lastEvaluatedRev + 1
	var eventRev int64
	err = tx.QueryRow(ctx, `
		SELECT revision
		FROM app.seasonal_wealth_events
		WHERE season_id = $1 AND revision = $2
	`, seasonID, targetRev).Scan(&eventRev)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, crowndomain.ErrRevisionGap
		}
		return 0, false, fmt.Errorf("check outbox event: %w", err)
	}

	return targetRev, true, nil
}

func (a *SuccessionEvaluatorAdapter) loadSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
) (int64, []crowndomain.Candidate, crowndomain.IncumbentSnapshot, crowndomain.HolderSubject, ReignRecord, error) {
	var c int64
	_ = tx.QueryRow(ctx, `
		SELECT wealth
		FROM app.seasonal_wealth_projections
		WHERE season_id = $1 AND subject_id = $2
	`, seasonID, string(crowndomain.InstitutionalCrownSubject)).Scan(&c)

	rows, err := tx.Query(ctx, `
		SELECT subject_id, beneficiary_kind, wealth, attained_revision, eligible
		FROM app.seasonal_wealth_projections
		WHERE season_id = $1 AND frozen = false AND eligible = true
		ORDER BY wealth DESC, attained_revision ASC, subject_id ASC
		LIMIT 20
	`, seasonID)
	if err != nil {
		return 0, nil, crowndomain.IncumbentSnapshot{}, "", ReignRecord{}, fmt.Errorf("list candidates: %w", err)
	}
	defer rows.Close()

	var candidates []crowndomain.Candidate
	for rows.Next() {
		var cand crowndomain.Candidate
		var subj, kind string
		if err := rows.Scan(&subj, &kind, &cand.Wealth, &cand.AttainedRevision, &cand.Eligible); err != nil {
			return 0, nil, crowndomain.IncumbentSnapshot{}, "", ReignRecord{}, fmt.Errorf("scan candidate: %w", err)
		}
		cand.Subject = crowndomain.HolderSubject(subj)
		cand.Kind = crowndomain.BeneficiaryKind(kind)
		candidates = append(candidates, cand)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, crowndomain.IncumbentSnapshot{}, "", ReignRecord{}, fmt.Errorf("iterate candidates: %w", err)
	}

	prevReign, found, err := a.GetActiveReign(ctx, tx, seasonID)
	if err != nil {
		return 0, nil, crowndomain.IncumbentSnapshot{}, "", ReignRecord{}, err
	}

	var incumbent crowndomain.IncumbentSnapshot
	if found {
		incumbent = crowndomain.IncumbentSnapshot{
			Subject:          crowndomain.HolderSubject(prevReign.HolderSubject),
			Wealth:           prevReign.WinningWealth,
			AttainedRevision: prevReign.AttainedRevision,
			Eligible:         true,
		}
		_ = tx.QueryRow(ctx, `
			SELECT wealth, attained_revision, eligible
			FROM app.seasonal_wealth_projections
			WHERE season_id = $1 AND subject_id = $2
		`, seasonID, prevReign.HolderSubject).Scan(&incumbent.Wealth, &incumbent.AttainedRevision, &incumbent.Eligible)
	}

	var regentStr string
	_ = tx.QueryRow(ctx, `SELECT regent FROM app.seasons WHERE season_key = $1`, seasonID).Scan(&regentStr)

	return c, candidates, incumbent, crowndomain.HolderSubject(regentStr), prevReign, nil
}

func (a *SuccessionEvaluatorAdapter) commitTransition(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
	targetRev int64,
	prevReign ReignRecord,
	outcome crowndomain.SuccessionOutcome,
) (TransitionOutcome, error) {
	reignVersion := prevReign.ReignVersion
	if reignVersion < 1 {
		reignVersion = 1
	}

	if outcome.ReignVersionChange {
		reignVersion++
		_, err := tx.Exec(ctx, `
			UPDATE app.seasonal_reigns
			SET is_active = false, ended_at = now(), updated_at = now()
			WHERE season_id = $1 AND is_active = true
		`, seasonID)
		if err != nil {
			return TransitionOutcome{}, fmt.Errorf("deactivate current reign: %w", err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO app.seasonal_reigns (
				season_id, reign_version, holder_subject, is_regent, reason,
				institutional_c, winning_wealth, attained_revision, transition_revision,
				predecessor, is_active, started_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, now(), now())
		`, seasonID, reignVersion, string(outcome.SelectedSovereign), outcome.IsRegent, string(outcome.Reason),
			outcome.InstitutionalC, outcome.WinningWealth, outcome.AttainedRevision, targetRev, prevReign.HolderSubject)
		if err != nil {
			return TransitionOutcome{}, fmt.Errorf("insert new reign: %w", err)
		}

		payloadBytes, _ := json.Marshal(map[string]any{
			"season":              seasonID,
			"reign_version":       reignVersion,
			"reason":              outcome.Reason,
			"successor":           string(outcome.SelectedSovereign),
			"predecessor":         prevReign.HolderSubject,
			"winning_wealth":      outcome.WinningWealth,
			"transition_revision": targetRev,
		})

		_, err = tx.Exec(ctx, `
			INSERT INTO app.seasonal_succession_events (
				season_id, reign_version, event_kind, successor_subject, predecessor_subject,
				institutional_c, winning_wealth, transition_revision, payload, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		`, seasonID, reignVersion, string(outcome.Reason), string(outcome.SelectedSovereign), prevReign.HolderSubject,
			outcome.InstitutionalC, outcome.WinningWealth, targetRev, string(payloadBytes))
		if err != nil {
			return TransitionOutcome{}, fmt.Errorf("insert succession event: %w", err)
		}
	}

	_, err := tx.Exec(ctx, `
		UPDATE app.seasonal_succession_evaluators
		SET last_evaluated_revision = $2, updated_at = now()
		WHERE season_id = $1
	`, seasonID, targetRev)
	if err != nil {
		return TransitionOutcome{}, fmt.Errorf("advance evaluated revision: %w", err)
	}

	return TransitionOutcome{
		SeasonID:            seasonID,
		TransitionRevision:  targetRev,
		ReignVersion:        reignVersion,
		HolderSubject:       string(outcome.SelectedSovereign),
		Predecessor:         prevReign.HolderSubject,
		IsRegent:            outcome.IsRegent,
		Reason:              string(outcome.Reason),
		WinningWealth:       outcome.WinningWealth,
		ReignVersionChanged: outcome.ReignVersionChange,
	}, nil
}

// EvaluateNextRevisionTx evaluates one pending contiguous revision atomically within tx.
func (a *SuccessionEvaluatorAdapter) EvaluateNextRevisionTx(
	ctx context.Context,
	tx pgx.Tx,
	seasonID string,
	workerID string,
) (TransitionOutcome, bool, error) {
	targetRev, hasWork, err := a.verifyLeaseAndNextRevision(ctx, tx, seasonID, workerID)
	if err != nil || !hasWork {
		return TransitionOutcome{}, false, err
	}

	c, candidates, incumbent, regent, prevReign, err := a.loadSnapshot(ctx, tx, seasonID)
	if err != nil {
		return TransitionOutcome{}, false, err
	}

	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              crowndomain.SeasonID(seasonID),
		SeasonOpen:          true,
		InstitutionalWealth: c,
		Incumbent:           incumbent,
		Regent:              regent,
		Candidates:          candidates,
	})
	if err != nil {
		return TransitionOutcome{}, false, fmt.Errorf("select sovereign: %w", err)
	}

	res, err := a.commitTransition(ctx, tx, seasonID, targetRev, prevReign, outcome)
	if err != nil {
		return TransitionOutcome{}, false, err
	}

	return res, true, nil
}

// Current implements application.ReignResolver by reading the invested active reign.
// Halts with ErrBacklogUnevaluated if confirmed events remain unevaluated.
func (a *SuccessionEvaluatorAdapter) Current(
	ctx context.Context,
	season crowndomain.SeasonID,
) (crowndomain.CurrentReign, error) {
	if a.db == nil {
		return crowndomain.CurrentReign{}, crowndomain.ErrInvalidAuthority
	}

	seasonStr := string(season)
	if err := a.CheckBacklog(ctx, a.db, seasonStr); err != nil {
		return crowndomain.CurrentReign{}, err
	}

	reign, found, err := a.GetActiveReign(ctx, a.db, seasonStr)
	if err != nil {
		return crowndomain.CurrentReign{}, err
	}
	if !found {
		return crowndomain.CurrentReign{}, crowndomain.ErrSeasonMismatch
	}

	var startsAt, endsAt time.Time
	var isOpen bool
	err = a.db.QueryRow(ctx, `
		SELECT starts_at, ends_at, (now() >= starts_at AND now() < ends_at)
		FROM app.seasons
		WHERE season_key = $1
	`, seasonStr).Scan(&startsAt, &endsAt, &isOpen)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return crowndomain.CurrentReign{}, crowndomain.ErrSeasonMismatch
		}
		return crowndomain.CurrentReign{}, fmt.Errorf("query season dates: %w", err)
	}

	return crowndomain.CurrentReign{
		Season:   season,
		Holder:   crowndomain.HolderSubject(reign.HolderSubject),
		Reign:    crowndomain.ReignVersion(reign.ReignVersion),
		StartsAt: startsAt,
		EndsAt:   endsAt,
		Open:     isOpen,
	}, nil
}
