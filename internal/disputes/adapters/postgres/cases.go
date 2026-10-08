package postgres

// P56-T03 — the narrow persistence bridge for the staged private
// case lifecycle (P39-T08): the same CaseRecords port the memory
// store served, backed by app.dispute_cases.
//
// The domain structs stay the authority: the adapter seals one
// CaseRecord envelope per negotiation key as JSONB and reads it
// back unchanged. It creates no tribunal, rule or power, and an
// unknown key reads as the domain absent error, so strangers learn
// nothing from a miss. The port carries no context, so the adapter
// bounds its own queries instead of borrowing the caller's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	disputesapp "github.com/AlexandreZanata/Regnovum/internal/disputes/application"
	disputesdomain "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
)

// queryTimeout bounds one envelope read or write: the staged file
// is small, and a stuck store must fail instead of hanging the
// lifecycle move that filed it.
const queryTimeout = 10 * time.Second

// Repository persists staged case records in PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository opens the case-record store on pool, refusing a nil
// pool before any query can fail on it.
func NewRepository(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errors.New("disputes store needs a pool")
	}
	return &Repository{pool: pool}, nil
}

// Get reads one filed record by negotiation key. Unknown keys
// answer the domain absent error, never a guess.
func (r *Repository) Get(key string) (disputesapp.CaseRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var raw []byte
	err := r.pool.QueryRow(ctx,
		`SELECT record FROM app.dispute_cases WHERE key = $1`, key).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return disputesapp.CaseRecord{}, disputesdomain.ErrUnknownCase
		}
		return disputesapp.CaseRecord{}, fmt.Errorf("read case record: %w", err)
	}
	var record disputesapp.CaseRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return disputesapp.CaseRecord{}, fmt.Errorf("decode case record: %w", err)
	}
	return record, nil
}

// Put files one record under its negotiation key, overwriting the
// previous envelope: every lifecycle move re-files the whole sealed
// record, so a reload always observes the latest filed state.
func (r *Repository) Put(record disputesapp.CaseRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode case record: %w", err)
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO app.dispute_cases (key, record, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (key) DO UPDATE SET record = EXCLUDED.record, updated_at = now()`,
		record.Proposal.Key, raw); err != nil {
		return fmt.Errorf("file case record: %w", err)
	}
	return nil
}
