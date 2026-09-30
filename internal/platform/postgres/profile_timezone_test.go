package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestProfileTimezoneSchema bounds documents the division of validation:
// the schema enforces only cheap structural bounds and accepts any non-empty
// bounded name (it cannot know tzdata); semantic IANA validation belongs to
// the profiles domain.
func TestProfileTimezoneSchema(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "timezone-schema@arena.example.com")
	profile := mustCreateProfile(t, ctx, q, acc.ID, "TimezoneUser", "timezoneuser", "pt-BR")
	if profile.Timezone.Valid {
		t.Fatal("new profiles must leave timezone unset (NULL)")
	}

	// A valid IANA name round-trips.
	updated, err := q.UpdateProfileTimezone(ctx, postgres.UpdateProfileTimezoneParams{
		AccountID: acc.ID,
		Timezone:  pgtype.Text{String: "America/Sao_Paulo", Valid: true},
	})
	if err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	if !updated.Timezone.Valid || updated.Timezone.String != "America/Sao_Paulo" {
		t.Fatalf("Timezone = %+v, want America/Sao_Paulo", updated.Timezone)
	}

	// Clearing the preference stores NULL again.
	cleared, err := q.UpdateProfileTimezone(ctx, postgres.UpdateProfileTimezoneParams{
		AccountID: acc.ID,
		Timezone:  pgtype.Text{},
	})
	if err != nil {
		t.Fatalf("clear timezone: %v", err)
	}
	if cleared.Timezone.Valid {
		t.Fatalf("Timezone = %+v, want NULL after clear", cleared.Timezone)
	}

	// Blank strings and oversized names violate the structural CHECK.
	for _, invalid := range []string{"   ", strings.Repeat("A", 65)} {
		_, err := q.UpdateProfileTimezone(ctx, postgres.UpdateProfileTimezoneParams{
			AccountID: acc.ID,
			Timezone:  pgtype.Text{String: invalid, Valid: true},
		})
		if err == nil {
			t.Fatalf("expected check_violation for %q", invalid)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("invalid timezone %q error = %v, want 23514", invalid, err)
		}
	}

	// Semantic validation is deliberately absent from the schema: any
	// non-empty bounded name is stored; the domain rejects unknowns.
	unknown, err := q.UpdateProfileTimezone(ctx, postgres.UpdateProfileTimezoneParams{
		AccountID: acc.ID,
		Timezone:  pgtype.Text{String: "Mars/Phobos", Valid: true},
	})
	if err != nil {
		t.Fatalf("schema must not attempt IANA validation: %v", err)
	}
	if unknown.Timezone.String != "Mars/Phobos" {
		t.Fatalf("Timezone = %+v, want stored Mars/Phobos", unknown.Timezone)
	}
}
