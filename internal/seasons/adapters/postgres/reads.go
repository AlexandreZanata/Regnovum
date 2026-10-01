package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// SeasonReader serves the staged lifecycle reads from the registry:
// the current ACTIVE book, one allowlisted book and the allowlisted
// history. It reads seasons and lifecycle only: balances, holders and
// accounts never leave this store.
type SeasonReader struct {
	pool *pgxpool.Pool
}

var _ application.SeasonReads = (*SeasonReader)(nil)

// NewSeasonReader builds the lifecycle read store with explicit
// wiring. Only the composition root instantiates it; tests stand in
// for the root.
func NewSeasonReader(pool *pgxpool.Pool) (*SeasonReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("seasons: reader needs a pool")
	}
	return &SeasonReader{pool: pool}, nil
}

// readView is one registry row with its derived lifecycle state.
type readView struct {
	view  application.SeasonView
	state string
}

// scanSeasonView reads one registry row with its latest lifecycle
// stage: the newest event by recorded order. Books without any event
// read as prepared history, never as live.
func scanSeasonView(ctx context.Context, q closeQuerier, season string) (readView, error) {
	var row readView
	err := q.QueryRow(ctx,
		`SELECT s.season_key, s.ordinal, s.starts_at, s.ends_at,
		        COALESCE((SELECT l.to_state FROM app.season_lifecycle l
		                  WHERE l.season_key = s.season_key
		                  ORDER BY l.recorded_at DESC, l.id DESC LIMIT 1), 'prepared')
		 FROM app.seasons s WHERE s.season_key = $1`, season).Scan(
		&row.view.SeasonKey, &row.view.Ordinal, &row.view.StartsAt, &row.view.EndsAt, &row.state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return readView{}, seasondomain.ErrSeasonUnknown
		}
		return readView{}, fmt.Errorf("read season view: %w", err)
	}
	row.view.StartsAt = row.view.StartsAt.UTC()
	row.view.EndsAt = row.view.EndsAt.UTC()
	row.view.State = row.state
	return row, nil
}

// currentActiveKey resolves the single book whose latest stage is
// active: at most one by the current-active guard.
func currentActiveKey(ctx context.Context, q closeQuerier) (string, bool, error) {
	var key string
	err := q.QueryRow(ctx,
		`SELECT season_key FROM (
		   SELECT DISTINCT ON (season_key) season_key, to_state
		   FROM app.season_lifecycle ORDER BY season_key, recorded_at DESC, id DESC
		 ) latest WHERE to_state = 'active' LIMIT 1`).Scan(&key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read active season: %w", err)
	}
	return key, true, nil
}

// suspensionCode distinguishes fechamento from arquivo when no ACTIVE
// serves live: a closing or sealed latest names fechamento, an
// archived latest names arquivo, anything else names suspensão.
func suspensionCode(ctx context.Context, q closeQuerier) error {
	var latest string
	err := q.QueryRow(ctx,
		`SELECT to_state FROM app.season_lifecycle ORDER BY recorded_at DESC, id DESC LIMIT 1`).Scan(&latest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return seasondomain.ErrSeasonClosed
		}
		return fmt.Errorf("read suspension stage: %w", err)
	}
	switch latest {
	case "closing", "sealed":
		return seasondomain.ErrSeasonClosed
	case "archived":
		return seasondomain.ErrSeasonArchived
	default:
		return seasondomain.ErrSeasonClosed
	}
}

// checkCaller refuses empty callers before any row is read.
func checkCaller(accountID string) error {
	if accountID == "" || strings.TrimSpace(accountID) != accountID {
		return apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required")
	}
	return nil
}

// GetCurrent resolves the single ACTIVE book. Suspended reads refuse
// with the stable closed/archived codes, never with an empty answer.
func (r *SeasonReader) GetCurrent(ctx context.Context, accountID string) (application.SeasonView, error) {
	if err := checkCaller(accountID); err != nil {
		return application.SeasonView{}, err
	}
	key, found, err := currentActiveKey(ctx, r.pool)
	if err != nil {
		return application.SeasonView{}, err
	}
	if !found {
		return application.SeasonView{}, suspensionCode(ctx, r.pool)
	}
	row, err := scanSeasonView(ctx, r.pool, key)
	if err != nil {
		return application.SeasonView{}, err
	}
	return row.view, nil
}

// GetSeason resolves one allowlisted book. The compat-legacy
// namespace is explicitly inactive and refuses as mismatch; unknown
// books refuse as unknown without leaking the allowlist.
func (r *SeasonReader) GetSeason(ctx context.Context, accountID, seasonKey string) (application.SeasonView, error) {
	if err := checkCaller(accountID); err != nil {
		return application.SeasonView{}, err
	}
	if seasonKey == "" || strings.TrimSpace(seasonKey) != seasonKey {
		return application.SeasonView{}, apperr.New(apperr.KindValidation, "season_invalid", "the season reference is malformed")
	}
	if seasonKey == "compat-legacy" {
		return application.SeasonView{}, seasondomain.ErrSeasonMismatch
	}
	row, err := scanSeasonView(ctx, r.pool, seasonKey)
	if err != nil {
		return application.SeasonView{}, err
	}
	return row.view, nil
}

// ListHistory resolves the allowlisted history in ordinal order: every
// book except the explicitly inactive namespace, each with its latest
// stage. Empty history serves empty, never a refusal.
func (r *SeasonReader) ListHistory(ctx context.Context, accountID string) ([]application.SeasonView, error) {
	if err := checkCaller(accountID); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT s.season_key, s.ordinal, s.starts_at, s.ends_at,
		        COALESCE((SELECT l.to_state FROM app.season_lifecycle l
		                  WHERE l.season_key = s.season_key
		                  ORDER BY l.recorded_at DESC, l.id DESC LIMIT 1), 'prepared')
		 FROM app.seasons s WHERE s.season_key <> 'compat-legacy' ORDER BY s.ordinal`)
	if err != nil {
		return nil, fmt.Errorf("list season history: %w", err)
	}
	defer rows.Close()
	views := []application.SeasonView{}
	for rows.Next() {
		var view application.SeasonView
		var state string
		var startsAt, endsAt time.Time
		if err := rows.Scan(&view.SeasonKey, &view.Ordinal, &startsAt, &endsAt, &state); err != nil {
			return nil, fmt.Errorf("scan season history: %w", err)
		}
		view.StartsAt = startsAt.UTC()
		view.EndsAt = endsAt.UTC()
		view.State = state
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read season history: %w", err)
	}
	return views, nil
}
