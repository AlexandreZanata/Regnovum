// Package postgres is the PostgreSQL outbound adapter of the audit module
// (P14-T01). It implements the application Recorder against the
// append-only trail (migration 00025): inserts only, with caller-chosen
// idempotency resolving the original row on retry. When the context
// carries a shared transaction the record joins it, so a sanction and its
// audit event commit or roll back together.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	auditapp "github.com/AlexandreZanata/Regnovum/internal/audit/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Repository implements the audit application port using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var _ auditapp.Recorder = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for audit.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// Record validates and stores one fact, resolving the original row when
// the idempotency key was already recorded.
func (r *Repository) Record(ctx context.Context, event auditdomain.AuditEvent) (*auditapp.RecordResult, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}

	actor, err := pgUUIDFromString(event.Actor)
	if err != nil {
		return nil, fmt.Errorf("record audit event: %w", err)
	}
	metadata, err := encodeMetadata(event.Metadata)
	if err != nil {
		return nil, fmt.Errorf("record audit event: %w", err)
	}

	queries := r.queries
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		queries = r.queries.WithTx(tx)
	}

	row, err := queries.InsertAuditEvent(ctx, platformpg.InsertAuditEventParams{
		OccurredAt:     pgtype.Timestamptz{Time: event.OccurredAt.UTC(), Valid: true},
		ActorID:        actor,
		Action:         event.Action,
		TargetType:     event.TargetType,
		TargetID:       event.TargetID,
		ReasonCode:     event.ReasonCode,
		Metadata:       metadata,
		IdempotencyKey: pgTextOrNull(event.IdempotencyKey),
		CorrelationID:  pgTextOrNull(event.CorrelationID),
	})
	if err == nil {
		return &auditapp.RecordResult{ID: uuidToString(row.ID)}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("record audit event: %w", err)
	}

	// A conflicting idempotency key resolves the original row: a retry
	// repairs a lost record without duplicating it.
	existing, err := queries.GetAuditEventByIdempotencyKey(ctx, pgTextOrNull(event.IdempotencyKey))
	if err != nil {
		return nil, fmt.Errorf("load replayed audit event: %w", err)
	}
	return &auditapp.RecordResult{ID: uuidToString(existing.ID), Replayed: true}, nil
}

func encodeMetadata(metadata map[string]string) ([]byte, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func pgUUIDFromString(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid identifier format")
	}
	return id, nil
}

func pgTextOrNull(raw string) pgtype.Text {
	if raw == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: raw, Valid: true}
}

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
