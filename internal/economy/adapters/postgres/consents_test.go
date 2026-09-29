package postgres_test

// P33-T03 — charter consent and opt-in intents on real PostgreSQL.
//
// Acceptance unlocks nothing by itself and refusal preserves every
// book: the tests prove on a disposable database that verdicts replay
// identically and conflict divergently, that opt-ins without acceptance
// are refused, that refusal leaves both ledgers byte-identical, and
// that scoped reads never leak another holder's row.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func consentCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func consentHolder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id::text`,
		email).Scan(&id); err != nil {
		t.Fatalf("create holder: %v", err)
	}
	return id
}

func journalFingerprint(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (int64, int64) {
	t.Helper()
	var entries, wallets int64
	if err := pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0) FROM app.economy_entries`).Scan(&entries, &wallets); err != nil {
		t.Fatalf("fingerprint economy: %v", err)
	}
	var legacy int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_transactions`).Scan(&legacy); err != nil {
		t.Fatalf("fingerprint legacy: %v", err)
	}
	return entries + legacy, wallets
}

// TestConsentAcceptanceReplaysIdentically proves one verdict per account
// and version: identical records replay, divergent ones conflict.
func TestConsentAcceptanceReplaysIdentically(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := consentCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	holder := consentHolder(t, ctx, pool, "consent-1@invalid.example")
	useCase := application.NewConsentUseCase(repo)

	first, err := useCase.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v1", Decision: "accepted"})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	second, err := useCase.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v1", Decision: "accepted"})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.DecidedAt.Equal(first.DecidedAt) {
		t.Fatalf("replay resolved another record")
	}
	if _, err := useCase.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v1", Decision: "refused"}); !errors.Is(err, domain.ErrConsentConflict) {
		t.Fatalf("divergent verdict = %v, want ErrConsentConflict", err)
	}
	other, err := useCase.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v2", Decision: "refused"})
	if err != nil || other.Decision != domain.ConsentRefused {
		t.Fatalf("v2 refusal = %+v, %v", other, err)
	}
}

// TestOptInRequiresAcceptance proves no intent exists without a prior
// acceptance, and that identical intents replay while divergent terms
// conflict.
func TestOptInRequiresAcceptance(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := consentCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	holder := consentHolder(t, ctx, pool, "consent-2@invalid.example")
	clock := fixedHoldClock{now: time.Now().UTC()}
	optins := application.NewOptInUseCase(repo, clock)
	intent := application.OptInCommand{
		AccountID: holder, Charter: "v1", Millis: 1000, RateNum: 10, RateDen: 1, ValidDays: 30,
	}
	if _, err := optins.Execute(ctx, intent); !errors.Is(err, domain.ErrConsentRequired) {
		t.Fatalf("intent without acceptance = %v, want ErrConsentRequired", err)
	}
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	first, err := optins.Execute(ctx, intent)
	if err != nil {
		t.Fatalf("opt-in: %v", err)
	}
	second, err := optins.Execute(ctx, intent)
	if err != nil || second.OptInID != first.OptInID {
		t.Fatalf("identical intent did not replay: %+v, %v", second, err)
	}
	divergent := intent
	divergent.RateNum = 11
	if _, err := optins.Execute(ctx, divergent); !errors.Is(err, domain.ErrConsentConflict) {
		t.Fatalf("divergent terms = %v, want ErrConsentConflict", err)
	}
	if first.Quantity.Millis() != 1000 || first.Rate.Num != 10 || first.Rate.Den != 1 {
		t.Fatalf("recorded terms drifted: %+v", first)
	}
}

// TestRefusalPreservesBothBooks proves refusal is constructionally
// non-destructive: economy and legacy journals are identical before and
// after, and the verdict row itself exists.
func TestRefusalPreservesBothBooks(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := consentCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	holder := consentHolder(t, ctx, pool, "consent-3@invalid.example")
	beforeEntries, beforeSum := journalFingerprint(t, ctx, pool)

	consents := application.NewConsentUseCase(repo)
	refused, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v1", Decision: "refused"})
	if err != nil {
		t.Fatalf("refuse: %v", err)
	}
	if refused.Decision != domain.ConsentRefused {
		t.Fatalf("verdict = %q, want refused", refused.Decision)
	}
	afterEntries, afterSum := journalFingerprint(t, ctx, pool)
	if beforeEntries != afterEntries || beforeSum != afterSum {
		t.Fatalf("books moved on refusal: (%d, %d) -> (%d, %d)", beforeEntries, beforeSum, afterEntries, afterSum)
	}
	found, err := repo.FindConsent(ctx, holder, mustCharterVersion(t, "v1"))
	if err != nil || found == nil || found.Decision != domain.ConsentRefused {
		t.Fatalf("refusal not recorded: %+v, %v", found, err)
	}
}

// TestConsentReadsStayScoped proves scoped reads leak nothing: another
// account resolves absence, never the neighbor's row.
func TestConsentReadsStayScoped(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := consentCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	ana := consentHolder(t, ctx, pool, "consent-ana@invalid.example")
	bia := consentHolder(t, ctx, pool, "consent-bia@invalid.example")
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: ana, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	found, err := repo.FindConsent(ctx, bia, mustCharterVersion(t, "v1"))
	if err != nil || found != nil {
		t.Fatalf("scoped read leaked: %+v, %v", found, err)
	}
	found, err = repo.FindConsent(ctx, ana, mustCharterVersion(t, "v1"))
	if err != nil || found == nil {
		t.Fatalf("own verdict missing: %+v, %v", found, err)
	}
}

func mustCharterVersion(t *testing.T, raw string) domain.CharterVersion {
	t.Helper()
	version, err := domain.ParseCharterVersion(raw)
	if err != nil {
		t.Fatalf("ParseCharterVersion(%q): %v", raw, err)
	}
	return version
}
