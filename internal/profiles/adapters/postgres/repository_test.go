package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustUsername(t *testing.T, raw string) domain.Username {
	t.Helper()
	username, err := domain.ParseUsername(raw)
	if err != nil {
		t.Fatalf("parse username %q: %v", raw, err)
	}
	return username
}

func mustLocale(t *testing.T, raw string) domain.Locale {
	t.Helper()
	locale, err := domain.ParseLocale(raw)
	if err != nil {
		t.Fatalf("parse locale %q: %v", raw, err)
	}
	return locale
}

func createEligibleAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "pending"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	verified, err := q.SetEmailVerified(ctx, acc.ID)
	if err != nil {
		t.Fatalf("verify account %s: %v", email, err)
	}
	return verified
}

func TestRepository_CreateProfilePersistsProfileAndAudit(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := createEligibleAccount(t, ctx, q, "profile-create@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	changedAt := time.Now().UTC().Truncate(time.Microsecond)

	profile, err := repo.CreateProfileWithUsernameHistory(ctx, accountID, mustUsername(t, "ArenaUser"), mustLocale(t, "en-US"), changedAt)
	if err != nil {
		t.Fatalf("CreateProfileWithUsernameHistory() error = %v", err)
	}
	if profile.ID() != accountID {
		t.Errorf("profile.ID() = %q, want %q", profile.ID(), accountID)
	}
	if profile.Username().Normalized() != "arenauser" || profile.Username().String() != "ArenaUser" {
		t.Errorf("username = %q/%q, want ArenaUser/arenauser", profile.Username().String(), profile.Username().Normalized())
	}
	if profile.Locale().String() != domain.LocaleAmericanEnglish {
		t.Errorf("locale = %q, want en-US", profile.Locale().String())
	}

	stored, err := repo.GetProfileByAccountID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetProfileByAccountID() error = %v", err)
	}
	if !stored.Username().Equals(profile.Username()) || !stored.Locale().Equals(profile.Locale()) {
		t.Errorf("stored profile = %q/%q, want %q/%q", stored.Username(), stored.Locale(), profile.Username(), profile.Locale())
	}

	lastChange, err := repo.LastUsernameChangeAt(ctx, accountID)
	if err != nil {
		t.Fatalf("LastUsernameChangeAt() error = %v", err)
	}
	if !lastChange.Equal(changedAt) {
		t.Errorf("LastUsernameChangeAt() = %v, want %v", lastChange, changedAt)
	}

	history, err := q.ListUsernameHistoryByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("ListUsernameHistoryByAccountID() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history entries = %d, want 1", len(history))
	}
	if !history[0].ChangedAt.Time.Equal(changedAt) {
		t.Errorf("audit changed_at = %v, want %v", history[0].ChangedAt.Time, changedAt)
	}
}

func TestRepository_CreateProfileRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	first := createEligibleAccount(t, ctx, q, "duplicate-a@arena.example.com")
	second := createEligibleAccount(t, ctx, q, "duplicate-b@arena.example.com")
	firstID := domain.AccountID(uuidString(first.ID))
	secondID := domain.AccountID(uuidString(second.ID))

	if _, err := repo.CreateProfileWithUsernameHistory(ctx, firstID, mustUsername(t, "DuplicateUser"), mustLocale(t, "pt-BR"), time.Now().UTC()); err != nil {
		t.Fatalf("first create error = %v", err)
	}

	if _, err := repo.CreateProfileWithUsernameHistory(ctx, firstID, mustUsername(t, "OtherUser"), mustLocale(t, "pt-BR"), time.Now().UTC()); !errors.Is(err, application.ErrProfileAlreadyExists) {
		t.Fatalf("duplicate account error = %v, want ErrProfileAlreadyExists", err)
	}

	if _, err := repo.CreateProfileWithUsernameHistory(ctx, secondID, mustUsername(t, "DUPLICATEUSER"), mustLocale(t, "pt-BR"), time.Now().UTC()); !errors.Is(err, application.ErrUsernameTaken) {
		t.Fatalf("duplicate username error = %v, want ErrUsernameTaken", err)
	}

	var profiles, history int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM app.profiles").Scan(&profiles)
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM app.username_history").Scan(&history)
	if profiles != 1 || history != 1 {
		t.Fatalf("after conflicts: profiles=%d history=%d, want 1/1", profiles, history)
	}
}

func TestRepository_UsernameConcurrency(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(10, 1))
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	const workers = 10
	accountIDs := make([]domain.AccountID, workers)
	for i := range accountIDs {
		acc := createEligibleAccount(t, ctx, q, fmt.Sprintf("race-%d@arena.example.com", i))
		accountIDs[i] = domain.AccountID(uuidString(acc.ID))
	}

	// Parse every presentation form before spawning goroutines: testing.T
	// must not fail from a non-test goroutine.
	proposed := make([]domain.Username, workers)
	for i := range proposed {
		if i%2 == 0 {
			proposed[i] = mustUsername(t, "DISPUTED-NAME")
		} else {
			proposed[i] = mustUsername(t, "disputed-name")
		}
	}
	ptBR := mustLocale(t, "pt-BR")

	var successes, taken atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repo.CreateProfileWithUsernameHistory(ctx, accountIDs[index], proposed[index], ptBR, time.Now().UTC())
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, application.ErrUsernameTaken):
				taken.Add(1)
			default:
				t.Errorf("worker %d unexpected error: %v", index, err)
			}
		}(i)
	}
	wg.Wait()

	if successes.Load() != 1 || taken.Load() != workers-1 {
		t.Fatalf("successes=%d taken=%d, want 1/%d", successes.Load(), taken.Load(), workers-1)
	}

	var profiles, history int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM app.profiles WHERE username_normalized = 'disputed-name'").Scan(&profiles)
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM app.username_history WHERE username_normalized = 'disputed-name'").Scan(&history)
	if profiles != 1 || history != 1 {
		t.Fatalf("after race: profiles=%d history=%d, want 1/1 (no partial state)", profiles, history)
	}
}

func TestRepository_ApplyUsernameChangeIsAtomic(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	accountA := createEligibleAccount(t, ctx, q, "change-a@arena.example.com")
	accountB := createEligibleAccount(t, ctx, q, "change-b@arena.example.com")
	accountC := createEligibleAccount(t, ctx, q, "change-c@arena.example.com")
	idA := domain.AccountID(uuidString(accountA.ID))
	idB := domain.AccountID(uuidString(accountB.ID))
	idC := domain.AccountID(uuidString(accountC.ID))

	alpha := mustUsername(t, "AlphaUser")
	beta := mustUsername(t, "BetaUser")
	if _, err := repo.CreateProfileWithUsernameHistory(ctx, idA, alpha, mustLocale(t, "pt-BR"), time.Now().UTC()); err != nil {
		t.Fatalf("create A: %v", err)
	}
	if _, err := repo.CreateProfileWithUsernameHistory(ctx, idB, beta, mustLocale(t, "pt-BR"), time.Now().UTC()); err != nil {
		t.Fatalf("create B: %v", err)
	}

	// A taken change must not update the profile nor append an audit entry.
	conflict := domain.UsernameChange{
		Previous:  alpha,
		Current:   mustUsername(t, "BETAUSER"),
		ChangedAt: time.Now().UTC(),
	}
	if _, err := repo.ApplyUsernameChange(ctx, idA, conflict); !errors.Is(err, application.ErrUsernameTaken) {
		t.Fatalf("conflicting change error = %v, want ErrUsernameTaken", err)
	}
	storedA, err := repo.GetProfileByAccountID(ctx, idA)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	if storedA.Username().Normalized() != "alphauser" {
		t.Errorf("username after failed change = %q, want alphauser", storedA.Username().Normalized())
	}
	historyA, err := q.ListUsernameHistoryByAccountID(ctx, accountA.ID)
	if err != nil {
		t.Fatalf("history A: %v", err)
	}
	if len(historyA) != 1 {
		t.Fatalf("history A = %d entries, want 1 (failed change must not append)", len(historyA))
	}

	// A successful change updates the profile and appends exactly one entry.
	gamma := mustUsername(t, "GammaUser")
	changedAt := time.Now().UTC().Truncate(time.Microsecond)
	updated, err := repo.ApplyUsernameChange(ctx, idA, domain.UsernameChange{Previous: alpha, Current: gamma, ChangedAt: changedAt})
	if err != nil {
		t.Fatalf("successful change error = %v", err)
	}
	if updated.Username().Normalized() != "gammauser" {
		t.Errorf("username = %q, want gammauser", updated.Username().Normalized())
	}
	historyA, err = q.ListUsernameHistoryByAccountID(ctx, accountA.ID)
	if err != nil {
		t.Fatalf("history A after change: %v", err)
	}
	if len(historyA) != 2 {
		t.Fatalf("history A = %d entries, want 2", len(historyA))
	}
	if !historyA[0].ChangedAt.Time.Equal(changedAt) {
		t.Errorf("latest audit instant = %v, want %v", historyA[0].ChangedAt.Time, changedAt)
	}

	// Changing a profile that does not exist fails without side effects.
	if _, err := repo.ApplyUsernameChange(ctx, idC, domain.UsernameChange{Previous: domain.Username{}, Current: gamma, ChangedAt: changedAt}); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
	}
	historyC, err := q.ListUsernameHistoryByAccountID(ctx, accountC.ID)
	if err != nil {
		t.Fatalf("history C: %v", err)
	}
	if len(historyC) != 0 {
		t.Fatalf("history C = %d entries, want 0", len(historyC))
	}
}

func TestRepository_EligibilityNegativeAuthorization(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	pending, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: "pending@arena.example.com", Status: "pending"})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	unverified, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: "unverified@arena.example.com", Status: "active"})
	if err != nil {
		t.Fatalf("create active unverified: %v", err)
	}

	suspended := createEligibleAccount(t, ctx, q, "suspended@arena.example.com")
	if _, err := q.UpdateAccountStatus(ctx, platformpg.UpdateAccountStatusParams{ID: suspended.ID, Status: "suspended"}); err != nil {
		t.Fatalf("suspend account: %v", err)
	}
	deleted := createEligibleAccount(t, ctx, q, "deleted@arena.example.com")
	if _, err := q.UpdateAccountStatus(ctx, platformpg.UpdateAccountStatusParams{ID: deleted.ID, Status: "deleted"}); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	missing := pgtype.UUID{
		Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0},
		Valid: true,
	}

	tests := []struct {
		name  string
		id    domain.AccountID
		notes string
	}{
		{name: "pending", id: domain.AccountID(uuidString(pending.ID)), notes: "email not verified"},
		{name: "active unverified", id: domain.AccountID(uuidString(unverified.ID)), notes: "no email_verified_at"},
		{name: "suspended", id: domain.AccountID(uuidString(suspended.ID))},
		{name: "deleted", id: domain.AccountID(uuidString(deleted.ID))},
		{name: "missing", id: domain.AccountID(uuidString(missing))},
		{name: "malformed id", id: domain.AccountID("not-a-uuid")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := repo.EnsureEligible(ctx, tc.id); !errors.Is(err, application.ErrAccountNotEligible) {
				t.Fatalf("EnsureEligible() error = %v, want ErrAccountNotEligible", err)
			}
		})
	}

	var profiles int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM app.profiles").Scan(&profiles)
	if profiles != 0 {
		t.Fatalf("profiles = %d, want 0 for ineligible accounts", profiles)
	}
}

func TestRepository_GetPublicProfileProjection(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := createEligibleAccount(t, ctx, q, "public-projection@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	if _, err := repo.CreateProfileWithUsernameHistory(ctx, accountID, mustUsername(t, "PublicHero"), mustLocale(t, "en-US"), time.Now().UTC()); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	// The public lookup resolves exclusively by the canonical normalized form.
	public, err := repo.GetPublicProfileByUsername(ctx, "publichero")
	if err != nil {
		t.Fatalf("GetPublicProfileByUsername() error = %v", err)
	}
	if public.Username != "PublicHero" {
		t.Errorf("Username = %q, want PublicHero", public.Username)
	}
	if public.InterfaceLocale != domain.LocaleAmericanEnglish {
		t.Errorf("InterfaceLocale = %q, want en-US", public.InterfaceLocale)
	}
	if public.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// Casing variants are not the adapter's job: the use case normalizes
	// before reaching the repository.
	if _, err := repo.GetPublicProfileByUsername(ctx, "PublicHero"); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("non-canonical lookup error = %v, want ErrProfileNotFound", err)
	}
	if _, err := repo.GetPublicProfileByUsername(ctx, "ghosthandle"); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("unknown lookup error = %v, want ErrProfileNotFound", err)
	}
}

func TestRepository_GetPrivateProfileProjection(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := createEligibleAccount(t, ctx, q, "private-projection@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	if _, err := repo.CreateProfileWithUsernameHistory(ctx, accountID, mustUsername(t, "PrivateHero"), mustLocale(t, "pt-BR"), time.Now().UTC()); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	private, err := repo.GetPrivateProfileByAccountID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetPrivateProfileByAccountID() error = %v", err)
	}
	if private.Username != "PrivateHero" || private.InterfaceLocale != domain.LocaleBrazilianPortuguese {
		t.Errorf("profile = %+v, want PrivateHero/pt-BR", private)
	}
	if private.CreatedAt.IsZero() || private.UpdatedAt.IsZero() {
		t.Error("timestamps are zero")
	}

	withoutProfile := createEligibleAccount(t, ctx, q, "private-missing@arena.example.com")
	if _, err := repo.GetPrivateProfileByAccountID(ctx, domain.AccountID(uuidString(withoutProfile.ID))); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
	}
	if _, err := repo.GetPrivateProfileByAccountID(ctx, domain.AccountID("not-a-uuid")); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("malformed id error = %v, want ErrProfileNotFound", err)
	}
}

func TestRepository_CommunicationPreferencesDefaultsAndAudit(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := createEligibleAccount(t, ctx, q, "preferences-repo@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	if _, err := repo.CreateProfileWithUsernameHistory(ctx, accountID, mustUsername(t, "PreferenceUser"), mustLocale(t, "pt-BR"), time.Now().UTC()); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	// No stored opt-in row yet: the read resolves to the conservative default.
	defaults, err := repo.PreferencesFor(ctx, accountID)
	if err != nil {
		t.Fatalf("PreferencesFor() error = %v", err)
	}
	if defaults.MarketingOptIn {
		t.Fatal("marketing opt-in must default to false")
	}
	if defaults.InterfaceLocale.String() != domain.LocaleBrazilianPortuguese {
		t.Errorf("InterfaceLocale = %q, want pt-BR", defaults.InterfaceLocale)
	}

	// Explicit opt-in updates the row and appends exactly one audit entry.
	changedAt := time.Now().UTC().Truncate(time.Microsecond)
	optedIn, err := repo.SetMarketingOptIn(ctx, accountID, true, changedAt)
	if err != nil {
		t.Fatalf("SetMarketingOptIn(true) error = %v", err)
	}
	if !optedIn.MarketingOptIn {
		t.Fatal("marketing opt-in should be true after the explicit command")
	}
	reread, err := repo.PreferencesFor(ctx, accountID)
	if err != nil {
		t.Fatalf("PreferencesFor() after opt-in error = %v", err)
	}
	if !reread.MarketingOptIn {
		t.Fatal("stored marketing opt-in should be true")
	}

	history, err := q.ListCommunicationPreferenceHistoryByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 || !history[0].MarketingOptIn {
		t.Fatalf("history = %+v, want one opt-in entry", history)
	}
	if !history[0].ChangedAt.Time.Equal(changedAt) {
		t.Errorf("audit changed_at = %v, want %v", history[0].ChangedAt.Time, changedAt)
	}

	// Explicit opt-out updates the same row and appends a second entry.
	if _, err := repo.SetMarketingOptIn(ctx, accountID, false, changedAt.Add(time.Minute)); err != nil {
		t.Fatalf("SetMarketingOptIn(false) error = %v", err)
	}
	history, err = q.ListCommunicationPreferenceHistoryByAccountID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list history after opt-out: %v", err)
	}
	if len(history) != 2 || history[0].MarketingOptIn {
		t.Fatalf("history = %+v, want opt-out as the newest entry", history)
	}
	var preferenceRows int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM app.communication_preferences WHERE account_id = $1", acc.ID).Scan(&preferenceRows)
	if preferenceRows != 1 {
		t.Fatalf("preferences rows = %d, want 1 (upsert, never duplicate)", preferenceRows)
	}

	// Accounts without a profile have no preferences to read or write.
	withoutProfile := createEligibleAccount(t, ctx, q, "preferences-missing@arena.example.com")
	missingID := domain.AccountID(uuidString(withoutProfile.ID))
	if _, err := repo.PreferencesFor(ctx, missingID); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("missing profile read error = %v, want ErrProfileNotFound", err)
	}
	if _, err := repo.SetMarketingOptIn(ctx, missingID, true, changedAt); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("missing profile write error = %v, want ErrProfileNotFound", err)
	}
}

func TestRepository_UpdateProfileTimezone(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := createEligibleAccount(t, ctx, q, "timezone@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	created, err := repo.CreateProfileWithUsernameHistory(ctx, accountID, mustUsername(t, "TimezoneHero"), mustLocale(t, "pt-BR"), time.Now().UTC())
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if !created.Timezone().IsZero() {
		t.Fatal("new profiles must start with an unset timezone")
	}

	saoPaulo, err := domain.ParseTimezone("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("parse timezone: %v", err)
	}
	updated, err := repo.UpdateProfileTimezone(ctx, accountID, saoPaulo, time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateProfileTimezone() error = %v", err)
	}
	if updated.Timezone().String() != "America/Sao_Paulo" {
		t.Errorf("Timezone = %q, want America/Sao_Paulo", updated.Timezone())
	}
	if updated.Locale().String() != domain.LocaleBrazilianPortuguese {
		t.Errorf("locale changed with timezone: %q", updated.Locale())
	}

	reloaded, err := repo.GetProfileByAccountID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if !reloaded.Timezone().Equals(saoPaulo) {
		t.Errorf("reloaded timezone = %q, want America/Sao_Paulo", reloaded.Timezone())
	}

	cleared, err := repo.UpdateProfileTimezone(ctx, accountID, domain.Timezone{}, time.Now().UTC())
	if err != nil {
		t.Fatalf("clear timezone: %v", err)
	}
	if !cleared.Timezone().IsZero() {
		t.Errorf("Timezone = %q, want unset after clear", cleared.Timezone())
	}

	missing := createEligibleAccount(t, ctx, q, "timezone-missing@arena.example.com")
	if _, err := repo.UpdateProfileTimezone(ctx, domain.AccountID(uuidString(missing.ID)), saoPaulo, time.Now().UTC()); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
	}
}

func TestRepository_UpdateProfileLocale(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := createEligibleAccount(t, ctx, q, "locale@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	if _, err := repo.CreateProfileWithUsernameHistory(ctx, accountID, mustUsername(t, "LocaleUser"), mustLocale(t, "pt-BR"), time.Now().UTC()); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	updated, err := repo.UpdateProfileLocale(ctx, accountID, mustLocale(t, "en-US"), time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateProfileLocale() error = %v", err)
	}
	if updated.Locale().String() != domain.LocaleAmericanEnglish {
		t.Errorf("locale = %q, want en-US", updated.Locale().String())
	}
	if updated.Username().Normalized() != "localeuser" {
		t.Errorf("username = %q, want localeuser", updated.Username().Normalized())
	}

	reloaded, err := repo.GetProfileByAccountID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if !reloaded.Locale().Equals(updated.Locale()) {
		t.Errorf("reloaded locale = %q, want %q", reloaded.Locale(), updated.Locale())
	}

	missing := createEligibleAccount(t, ctx, q, "locale-missing@arena.example.com")
	_, err = repo.UpdateProfileLocale(ctx, domain.AccountID(uuidString(missing.ID)), mustLocale(t, "en-US"), time.Now().UTC())
	if !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
	}
}
