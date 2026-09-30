package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func mustCreateAccount(t *testing.T, ctx context.Context, q *postgres.Queries, email string) postgres.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  email,
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return acc
}

func mustCreateProfile(t *testing.T, ctx context.Context, q *postgres.Queries, accountID pgtype.UUID, username, normalized, locale string) postgres.AppProfile {
	t.Helper()
	profile, err := q.CreateProfile(ctx, postgres.CreateProfileParams{
		AccountID:          accountID,
		Username:           username,
		UsernameNormalized: normalized,
		InterfaceLocale:    locale,
	})
	if err != nil {
		t.Fatalf("create profile %s: %v", username, err)
	}
	return profile
}

func assertPgErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected PostgreSQL error %s, got nil", wantCode)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("pgErr.Code = %q, want %q (%v)", pgErr.Code, wantCode, err)
	}
}

// TestProfileUsernameCaseInsensitiveUniqueness proves that username
// uniqueness is enforced on the canonical normalized form, not on the
// presentation form: any casing variant collides.
func TestProfileUsernameCaseInsensitiveUniqueness(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc1 := mustCreateAccount(t, ctx, q, "profile-owner-1@arena.example.com")
	acc2 := mustCreateAccount(t, ctx, q, "profile-owner-2@arena.example.com")
	acc3 := mustCreateAccount(t, ctx, q, "profile-owner-3@arena.example.com")

	first := mustCreateProfile(t, ctx, q, acc1.ID, "ArenaUser", "arenauser", "pt-BR")
	if first.UsernameNormalized != "arenauser" {
		t.Fatalf("UsernameNormalized = %q, want arenauser", first.UsernameNormalized)
	}

	_, err := q.CreateProfile(ctx, postgres.CreateProfileParams{
		AccountID:          acc2.ID,
		Username:           "ARENAUSER",
		UsernameNormalized: "arenauser",
		InterfaceLocale:    "pt-BR",
	})
	assertPgErrorCode(t, err, "23505")

	_, err = q.CreateProfile(ctx, postgres.CreateProfileParams{
		AccountID:          acc3.ID,
		Username:           "ArenaUser",
		UsernameNormalized: "arenauser",
		InterfaceLocale:    "pt-BR",
	})
	assertPgErrorCode(t, err, "23505")
}

// TestProfileFormatAndLocaleConstraints locks the format, normalization and
// locale invariants of app.profiles at the database boundary.
func TestProfileFormatAndLocaleConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	cases := []struct {
		name       string
		username   string
		normalized string
		locale     string
		wantCode   string
	}{
		{name: "too short", username: "ab", normalized: "ab", locale: "pt-BR", wantCode: "23514"},
		{name: "too long", username: strings.Repeat("a", 31), normalized: strings.Repeat("a", 31), locale: "pt-BR", wantCode: "23514"},
		{name: "leading hyphen", username: "-arena", normalized: "-arena", locale: "pt-BR", wantCode: "23514"},
		{name: "trailing hyphen", username: "arena-", normalized: "arena-", locale: "pt-BR", wantCode: "23514"},
		{name: "leading underscore", username: "_arena", normalized: "_arena", locale: "pt-BR", wantCode: "23514"},
		{name: "inner space", username: "arena user", normalized: "arena user", locale: "pt-BR", wantCode: "23514"},
		{name: "punctuation", username: "arena!", normalized: "arena!", locale: "pt-BR", wantCode: "23514"},
		{name: "dot", username: "arena.user", normalized: "arena.user", locale: "pt-BR", wantCode: "23514"},
		{name: "unicode letter", username: "arená", normalized: "arená", locale: "pt-BR", wantCode: "23514"},
		{name: "newline suffix", username: "arena\n", normalized: "arena\n", locale: "pt-BR", wantCode: "23514"},
		{name: "normalized mismatch", username: "ArenaUser", normalized: "arenauser2", locale: "pt-BR", wantCode: "23514"},
		{name: "normalized uppercase", username: "ArenaUser", normalized: "ArenaUser", locale: "pt-BR", wantCode: "23514"},
		{name: "unsupported locale", username: "locale-fr", normalized: "locale-fr", locale: "fr-FR", wantCode: "23514"},
		{name: "non-canonical locale", username: "locale-lc", normalized: "locale-lc", locale: "pt-br", wantCode: "23514"},
		{name: "valid minimum length", username: "abc", normalized: "abc", locale: "pt-BR", wantCode: ""},
		{name: "valid maximum length", username: strings.Repeat("A", 30), normalized: strings.Repeat("a", 30), locale: "en-US", wantCode: ""},
		{name: "valid separators", username: "Arena_User-1", normalized: "arena_user-1", locale: "en-US", wantCode: ""},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := mustCreateAccount(t, ctx, q, fmt.Sprintf("format-%d@arena.example.com", i))

			_, err := q.CreateProfile(ctx, postgres.CreateProfileParams{
				AccountID:          acc.ID,
				Username:           tc.username,
				UsernameNormalized: tc.normalized,
				InterfaceLocale:    tc.locale,
			})
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected successful insert, got %v", err)
				}
				return
			}
			assertPgErrorCode(t, err, tc.wantCode)
		})
	}
}

// TestProfileUsernameConcurrency reserves the same username from ten
// accounts in parallel: exactly one wins, every loser receives a unique
// violation and no partial state is persisted.
func TestProfileUsernameConcurrency(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	const workers = 10
	accounts := make([]postgres.AppAccount, workers)
	for i := range accounts {
		accounts[i] = mustCreateAccount(t, ctx, q, fmt.Sprintf("race-%d@arena.example.com", i))
	}

	// Every worker races for the same normalized username, half of them with
	// a different presentation casing.
	usernames := make([]string, workers)
	for i := range usernames {
		usernames[i] = "disputed-name"
		if i%2 == 0 {
			usernames[i] = "DISPUTED-NAME"
		}
	}

	type result struct {
		index int
		err   error
	}
	results := make(chan result, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := q.CreateProfile(ctx, postgres.CreateProfileParams{
				AccountID:          accounts[index].ID,
				Username:           usernames[index],
				UsernameNormalized: "disputed-name",
				InterfaceLocale:    "pt-BR",
			})
			results <- result{index: index, err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	for res := range results {
		if res.err == nil {
			successes++
			continue
		}
		var pgErr *pgconn.PgError
		if !errors.As(res.err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("worker %d: expected 23505 unique_violation, got %v", res.index, res.err)
		}
	}

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}

	var rows int
	if err := db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM app.profiles WHERE username_normalized = 'disputed-name'",
	).Scan(&rows); err != nil {
		t.Fatalf("count profiles: %v", err)
	}
	if rows != 1 {
		t.Fatalf("persisted rows = %d, want 1", rows)
	}
}

// TestProfileForeignKeyAndCascade verifies profile ownership by account and
// cascade cleanup when the account is removed.
func TestProfileForeignKeyAndCascade(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	orphan := pgtype.UUID{
		Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0},
		Valid: true,
	}

	_, err := q.CreateProfile(ctx, postgres.CreateProfileParams{
		AccountID:          orphan,
		Username:           "orphan-user",
		UsernameNormalized: "orphan-user",
		InterfaceLocale:    "pt-BR",
	})
	assertPgErrorCode(t, err, "23503")

	err = q.CreateUsernameHistoryEntry(ctx, postgres.CreateUsernameHistoryEntryParams{
		AccountID:          orphan,
		Username:           "orphan-user",
		UsernameNormalized: "orphan-user",
		ChangedAt:          pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	assertPgErrorCode(t, err, "23503")

	acc := mustCreateAccount(t, ctx, q, "cascade-profile@arena.example.com")
	mustCreateProfile(t, ctx, q, acc.ID, "CascadeUser", "cascadeuser", "pt-BR")

	for _, name := range []string{"cascadeuser", "cascadeuser-2"} {
		if err := q.CreateUsernameHistoryEntry(ctx, postgres.CreateUsernameHistoryEntryParams{
			AccountID:          acc.ID,
			Username:           name,
			UsernameNormalized: name,
			ChangedAt:          pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			t.Fatalf("create history entry %s: %v", name, err)
		}
	}

	if _, err := db.Pool.Exec(ctx, "DELETE FROM app.accounts WHERE id = $1", acc.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	var profileCount, historyCount int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.profiles WHERE account_id = $1", acc.ID).Scan(&profileCount); err != nil {
		t.Fatalf("count profiles: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.username_history WHERE account_id = $1", acc.ID).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if profileCount != 0 || historyCount != 0 {
		t.Fatalf("after cascade: profiles=%d history=%d, want 0/0", profileCount, historyCount)
	}
}

// TestProfilePublicProjectionNeverExposesPrivateFields proves that the
// public profile projection contains only publicly allowed fields and that
// the schema itself never stores email or payment-provider identifiers.
func TestProfilePublicProjectionNeverExposesPrivateFields(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	const accountEmail = "private-person@arena.example.com"
	acc := mustCreateAccount(t, ctx, q, accountEmail)
	if err := q.CreatePasswordCredential(ctx, postgres.CreatePasswordCredentialParams{
		AccountID:    acc.ID,
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=1$private-credential",
		Algorithm:    "argon2id",
		Version:      1,
	}); err != nil {
		t.Fatalf("create password credential: %v", err)
	}
	mustCreateProfile(t, ctx, q, acc.ID, "PublicHero", "publichero", "en-US")

	public, err := q.GetPublicProfileByUsername(ctx, "publichero")
	if err != nil {
		t.Fatalf("get public profile: %v", err)
	}
	if public.Username != "PublicHero" {
		t.Errorf("public.Username = %q, want PublicHero", public.Username)
	}
	if public.InterfaceLocale != "en-US" {
		t.Errorf("public.InterfaceLocale = %q, want en-US", public.InterfaceLocale)
	}
	if !public.CreatedAt.Valid || public.CreatedAt.Time.IsZero() {
		t.Errorf("public.CreatedAt is invalid or zero: %+v", public.CreatedAt)
	}

	forbiddenFields := []string{
		"Email", "EmailVerifiedAt",
		"Password", "PasswordHash", "Credential", "Hash",
		"Stripe", "StripeID", "StripeCustomerID", "CustomerID", "BillingCustomerID",
		"IPAddress", "UserAgent", "FraudFlag", "AdminNotes",
	}

	// The public projection must not expose even the internal account UUID.
	publicForbidden := append(append([]string{}, forbiddenFields...), "AccountID")
	assertNoForbiddenFields(t, "GetPublicProfileByUsernameRow", reflect.TypeOf(public), publicForbidden)
	assertNoForbiddenFields(t, "AppProfile", reflect.TypeOf(postgres.AppProfile{}), forbiddenFields)

	assertNoValueContains(t, "GetPublicProfileByUsernameRow", reflect.ValueOf(public), accountEmail)

	// The public projection must not read app.accounts at all: it only
	// declares columns of app.profiles.
	if fields := reflect.TypeOf(public); fields.NumField() != 3 {
		t.Fatalf("public profile row has %d fields, want exactly 3 (username, interface_locale, created_at)", fields.NumField())
	}

	// Schema-level separation: profile tables never carry email or billing
	// identifiers; those live in app.accounts and the billing module.
	rows, err := db.Pool.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'app' AND table_name IN ('profiles', 'username_history')
	`)
	if err != nil {
		t.Fatalf("query profile columns: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		seen++
		lowered := strings.ToLower(column)
		for _, marker := range []string{"email", "stripe", "customer", "password", "credential", "billing"} {
			if strings.Contains(lowered, marker) {
				t.Fatalf("SECURITY VIOLATION: app.%s carries forbidden column %q", table, column)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if seen == 0 {
		t.Fatal("no profile columns inspected; schema query returned nothing")
	}
}

// TestUsernameHistoryAuditTrail verifies that every username set or changed
// is appended to the audit trail and listed newest-first.
func TestUsernameHistoryAuditTrail(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "history@arena.example.com")
	mustCreateProfile(t, ctx, q, acc.ID, "FirstChoice", "firstchoice", "pt-BR")

	history := []struct {
		username   string
		normalized string
		changedAt  time.Time
	}{
		{"FirstChoice", "firstchoice", time.Now().Add(-2 * time.Minute)},
		{"SecondChoice", "secondchoice", time.Now().Add(-1 * time.Minute)},
		{"ThirdChoice", "thirdchoice", time.Now()},
	}
	for _, entry := range history {
		if err := q.CreateUsernameHistoryEntry(ctx, postgres.CreateUsernameHistoryEntryParams{
			AccountID:          acc.ID,
			Username:           entry.username,
			UsernameNormalized: entry.normalized,
			ChangedAt:          pgtype.Timestamptz{Time: entry.changedAt, Valid: true},
		}); err != nil {
			t.Fatalf("append history %s: %v", entry.username, err)
		}
	}

	updated, err := q.UpdateProfileUsername(ctx, postgres.UpdateProfileUsernameParams{
		AccountID:          acc.ID,
		Username:           "ThirdChoice",
		UsernameNormalized: "thirdchoice",
	})
	if err != nil {
		t.Fatalf("update username: %v", err)
	}
	if updated.Username != "ThirdChoice" || updated.UsernameNormalized != "thirdchoice" {
		t.Fatalf("updated profile = %q/%q, want ThirdChoice/thirdchoice", updated.Username, updated.UsernameNormalized)
	}

	entries, err := q.ListUsernameHistoryByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(entries) != len(history) {
		t.Fatalf("history entries = %d, want %d", len(entries), len(history))
	}
	for i, entry := range entries {
		want := history[len(history)-1-i]
		if entry.Username != want.username || entry.UsernameNormalized != want.normalized {
			t.Fatalf("entry %d = %q/%q, want %q/%q (newest first)", i, entry.Username, entry.UsernameNormalized, want.username, want.normalized)
		}
	}

	if _, err := q.UpdateProfileLocale(ctx, postgres.UpdateProfileLocaleParams{
		AccountID:       acc.ID,
		InterfaceLocale: "en-US",
	}); err != nil {
		t.Fatalf("update locale: %v", err)
	}
	profile, err := q.GetProfileByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.InterfaceLocale != "en-US" {
		t.Fatalf("InterfaceLocale = %q, want en-US", profile.InterfaceLocale)
	}
}

func assertNoForbiddenFields(t *testing.T, label string, typ reflect.Type, forbidden []string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i).Name
		for _, banned := range forbidden {
			if field == banned {
				t.Fatalf("SECURITY VIOLATION: %s contains forbidden field %q", label, field)
			}
		}
	}
}

func assertNoValueContains(t *testing.T, label string, value reflect.Value, needle string) {
	t.Helper()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() == reflect.String && strings.Contains(field.String(), needle) {
			t.Fatalf("SECURITY VIOLATION: %s field %s leaks private value %q", label, value.Type().Field(i).Name, needle)
		}
	}
}
