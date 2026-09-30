package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestCommunicationPreferencesConservativeDefaults proves that a profile
// with no stored opt-in row reads as marketing opt-in false: no implicit
// consent exists at the schema level.
func TestCommunicationPreferencesConservativeDefaults(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "preferences-default@arena.example.com")
	mustCreateProfile(t, ctx, q, acc.ID, "PrefUser", "prefuser", "pt-BR")

	prefs, err := q.GetCommunicationPreferencesByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetCommunicationPreferencesByAccountID() error = %v", err)
	}
	if prefs.MarketingOptIn {
		t.Fatal("marketing opt-in must default to false")
	}
	if prefs.InterfaceLocale != "pt-BR" {
		t.Errorf("InterfaceLocale = %q, want pt-BR", prefs.InterfaceLocale)
	}
	if prefs.AccountID != acc.ID {
		t.Errorf("AccountID mismatch")
	}

	// An account without a profile has no preferences to read.
	orphan := mustCreateAccount(t, ctx, q, "preferences-orphan@arena.example.com")
	if _, err := q.GetCommunicationPreferencesByAccountID(ctx, orphan.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("account without profile error = %v, want ErrNoRows", err)
	}
}

// TestCommunicationPreferencesExplicitUpsertAndHistory covers the explicit
// write path and its append-only audit trail.
func TestCommunicationPreferencesExplicitUpsertAndHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "preferences-audit@arena.example.com")
	mustCreateProfile(t, ctx, q, acc.ID, "AuditUser", "audituser", "en-US")

	// Explicit opt-in.
	row, err := q.UpsertCommunicationPreferences(ctx, postgres.UpsertCommunicationPreferencesParams{
		AccountID:      acc.ID,
		MarketingOptIn: true,
	})
	if err != nil {
		t.Fatalf("upsert opt-in: %v", err)
	}
	if !row.MarketingOptIn {
		t.Fatal("marketing opt-in should be true after explicit upsert")
	}

	// Explicit opt-out: the same row is updated, never duplicated.
	row, err = q.UpsertCommunicationPreferences(ctx, postgres.UpsertCommunicationPreferencesParams{
		AccountID:      acc.ID,
		MarketingOptIn: false,
	})
	if err != nil {
		t.Fatalf("upsert opt-out: %v", err)
	}
	if row.MarketingOptIn {
		t.Fatal("marketing opt-in should be false after explicit opt-out")
	}

	var rows int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.communication_preferences WHERE account_id = $1", acc.ID).Scan(&rows); err != nil {
		t.Fatalf("count preferences: %v", err)
	}
	if rows != 1 {
		t.Fatalf("preferences rows = %d, want 1", rows)
	}

	// Audit trail records each explicit change, newest first.
	history := []struct {
		optIn     bool
		changedAt time.Time
	}{
		{true, time.Now().Add(-2 * time.Minute).Truncate(time.Microsecond)},
		{false, time.Now().Add(-1 * time.Minute).Truncate(time.Microsecond)},
		{true, time.Now().Truncate(time.Microsecond)},
	}
	for _, entry := range history {
		if err := q.CreateCommunicationPreferenceHistoryEntry(ctx, postgres.CreateCommunicationPreferenceHistoryEntryParams{
			AccountID:      acc.ID,
			MarketingOptIn: entry.optIn,
			ChangedAt:      pgtype.Timestamptz{Time: entry.changedAt, Valid: true},
		}); err != nil {
			t.Fatalf("append history: %v", err)
		}
	}

	entries, err := q.ListCommunicationPreferenceHistoryByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(entries) != len(history) {
		t.Fatalf("history entries = %d, want %d", len(entries), len(history))
	}
	for i, entry := range entries {
		want := history[len(history)-1-i]
		if entry.MarketingOptIn != want.optIn {
			t.Errorf("entry %d opt-in = %v, want %v", i, entry.MarketingOptIn, want.optIn)
		}
		if !entry.ChangedAt.Time.Equal(want.changedAt) {
			t.Errorf("entry %d changed_at = %v, want %v", i, entry.ChangedAt.Time, want.changedAt)
		}
	}
}

// TestCommunicationPreferencesConstraintsAndCascade verifies the primary key
// and cascade cleanup of preferences and their audit trail.
func TestCommunicationPreferencesConstraintsAndCascade(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "preferences-cascade@arena.example.com")
	mustCreateProfile(t, ctx, q, acc.ID, "CascadePref", "cascadepref", "pt-BR")

	if _, err := q.UpsertCommunicationPreferences(ctx, postgres.UpsertCommunicationPreferencesParams{
		AccountID:      acc.ID,
		MarketingOptIn: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := q.CreateCommunicationPreferenceHistoryEntry(ctx, postgres.CreateCommunicationPreferenceHistoryEntryParams{
		AccountID:      acc.ID,
		MarketingOptIn: true,
		ChangedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("append history: %v", err)
	}

	// Direct duplicate insert violates the primary key.
	_, err := db.Pool.Exec(ctx,
		"INSERT INTO app.communication_preferences (account_id, marketing_opt_in) VALUES ($1, false)", acc.ID)
	if err == nil {
		t.Fatal("expected unique_violation for duplicate preferences row")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate insert error = %v, want 23505", err)
	}

	// Preferences for a non-existent account fail the foreign key.
	orphan := pgtype.UUID{
		Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0, 0x01, 0x02},
		Valid: true,
	}
	_, err = db.Pool.Exec(ctx,
		"INSERT INTO app.communication_preferences (account_id, marketing_opt_in) VALUES ($1, false)", orphan)
	if err == nil {
		t.Fatal("expected foreign_key_violation for orphan preferences row")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("orphan insert error = %v, want 23503", err)
	}

	// Deleting the account cascades to preferences and history.
	if _, err := db.Pool.Exec(ctx, "DELETE FROM app.accounts WHERE id = $1", acc.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	var preferenceRows, historyRows int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.communication_preferences WHERE account_id = $1", acc.ID).Scan(&preferenceRows)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.communication_preference_history WHERE account_id = $1", acc.ID).Scan(&historyRows)
	if preferenceRows != 0 || historyRows != 0 {
		t.Fatalf("after cascade: preferences=%d history=%d, want 0/0", preferenceRows, historyRows)
	}
}
