package postgres_test

// P34-T08 — privacy-safe public custody totals on real PostgreSQL.
//
// The public document aggregates supply, Treasury, reserve,
// circulation and locks without naming any person or secret: the five
// readings re-derive from the journal on every fresh call, small
// third-party cells suppress to zero, titles render in pt and en, one
// derivation serves the whole cache window and the next window
// rebuilds from the journal as written. The tests prove on a
// disposable database: a rich book matches the audited snapshot with
// no person-bound string in the document, the window shares one
// derivation and then rebuilds, and a sparse book suppresses exactly
// the third-party amounts.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// totalsNow pins the public derivation instant.
func totalsNow() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

// totalsTestClock is a movable clock: the public window is observable
// without waiting out the hour.
type totalsTestClock struct {
	now time.Time
}

func (c *totalsTestClock) Now() time.Time { return c.now }

// totalsFixture carries the rich-book actors: six paid holders with
// their two governors.
type totalsFixture struct {
	payees              []string
	approverOne         string
	approverTwo         string
	firstBeneficiary    string
	disbursedPerPayee   int64
	commercialCommitted int64
}

// totalsPayees opens holder accounts without touching the journal:
// labels stay person-bound by construction for the privacy probe.
func totalsPayees(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) []string {
	t.Helper()
	payees := make([]string, 0, count)
	for range count {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO app.accounts (email, status) VALUES ('totals-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
			t.Fatalf("seed payee: %v", err)
		}
		payees = append(payees, id)
	}
	return payees
}

// totalsRichSetup funds three vaults, commits five holds and pays six
// holders through the single disbursement port: every public class
// carries a rich cell, so nothing suppresses.
func totalsRichSetup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *postgres.Repository) totalsFixture {
	t.Helper()
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "sovereign_reserve", 1000)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	moveToVault(t, ctx, pool, "commercial_stock", 5000)
	for i := 1; i <= 5; i++ {
		commitFunds(t, ctx, repo, "commercial_stock", fmt.Sprintf("totals hold #%d", i), 600, commitNow().Add(time.Hour))
	}
	first, approverOne, approverTwo := disburseParties(t, ctx, pool)
	payees := append([]string{first}, totalsPayees(t, ctx, pool, 5)...)
	for i, payee := range payees {
		cmd := disburseCmd(fmt.Sprintf("totals-sale-%d", i+1), "operating_cash", payee, "due_payment", 100, approverOne, approverTwo)
		if _, err := application.NewDisburseUseCase(repo).Execute(ctx, cmd); err != nil {
			t.Fatalf("pay holder %d: %v", i+1, err)
		}
	}
	return totalsFixture{
		payees:              payees,
		approverOne:         approverOne,
		approverTwo:         approverTwo,
		firstBeneficiary:    first,
		disbursedPerPayee:   100,
		commercialCommitted: 3000,
	}
}

// totalsUserSum re-sums holder custodies directly: the independent
// audit the public circulation must match.
func totalsUserSum(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var sum int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'user'`).Scan(&sum); err != nil {
		t.Fatalf("sum holders: %v", err)
	}
	return sum
}

// totalsPersonStrings collects every person-bound string the journal
// holds: holder labels and transfer identifiers.
func totalsPersonStrings(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	secrets := []string{}
	rows, err := pool.Query(ctx, `SELECT label FROM app.economy_custodies WHERE kind = 'user'`)
	if err != nil {
		t.Fatalf("read holder labels: %v", err)
	}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			rows.Close()
			t.Fatalf("scan holder label: %v", err)
		}
		secrets = append(secrets, label)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate holder labels: %v", err)
	}
	transfers, err := pool.Query(ctx, `SELECT DISTINCT transfer_id::text FROM app.economy_entries`)
	if err != nil {
		t.Fatalf("read transfers: %v", err)
	}
	for transfers.Next() {
		var transfer string
		if err := transfers.Scan(&transfer); err != nil {
			transfers.Close()
			t.Fatalf("scan transfer: %v", err)
		}
		secrets = append(secrets, transfer)
	}
	transfers.Close()
	if err := transfers.Err(); err != nil {
		t.Fatalf("iterate transfers: %v", err)
	}
	if len(secrets) == 0 {
		t.Fatalf("no person-bound strings in the book: the probe judged nothing")
	}
	return secrets
}

func totalsUseCase(t *testing.T, repo *postgres.Repository, clock *totalsTestClock) *application.CustodyTotalsUseCase {
	t.Helper()
	uc, err := application.NewCustodyTotalsUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewCustodyTotalsUseCase: %v", err)
	}
	return uc
}

// TestCustodyTotalsMatchAuditedSnapshot proves the rich path: every
// public class matches its independent audit, titles render in pt and
// no holder label or transfer identifier reaches the document.
func TestCustodyTotalsMatchAuditedSnapshot(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fixture := totalsRichSetup(t, ctx, pool, repo)
	clock := &totalsTestClock{now: totalsNow()}
	snapshot, err := totalsUseCase(t, repo, clock).Totals(ctx, "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}

	if snapshot.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("supply = %d, want S", snapshot.SupplyMillis)
	}
	economySupply(t, ctx, pool)
	vaults, err := application.NewTreasuryVaultsUseCase(repo).Execute(ctx, application.TreasuryCommand{Season: domain.CompatSeasonKey})
	if err != nil {
		t.Fatalf("vault partitions: %v", err)
	}
	if snapshot.TreasuryMillis != vaults.TotalMillis {
		t.Fatalf("treasury = %d, want the audited %d", snapshot.TreasuryMillis, vaults.TotalMillis)
	}
	balances := map[domain.TreasuryVault]int64{}
	for _, position := range snapshot.Vaults {
		balances[position.Vault] = position.Millis
	}
	want := map[domain.TreasuryVault]int64{
		domain.TreasuryVaultGenesisHome:      domain.GenesisSupplyMillis - 11000,
		domain.TreasuryVaultSovereignReserve: 1000,
		domain.TreasuryVaultCommercialStock:  2000,
		domain.TreasuryVaultOperatingCash:    5000 - int64(len(fixture.payees))*fixture.disbursedPerPayee,
		domain.TreasuryVaultFree:             0,
	}
	for vault, millis := range want {
		if balances[vault] != millis {
			t.Fatalf("vault %q = %d, want %d", vault, balances[vault], millis)
		}
	}
	if snapshot.ReserveMillis != 1000 {
		t.Fatalf("reserve = %d, want 1000", snapshot.ReserveMillis)
	}
	if got := totalsUserSum(t, ctx, pool); snapshot.CirculationMillis != got || got != 600 {
		t.Fatalf("circulation = %d, want the audited %d", snapshot.CirculationMillis, got)
	}
	if snapshot.CirculationSuppressed {
		t.Fatalf("rich circulation suppressed")
	}
	if got := committedTotal(t, ctx, pool); snapshot.LockedMillis != got || got != fixture.commercialCommitted {
		t.Fatalf("locked = %d, want the audited %d", snapshot.LockedMillis, got)
	}
	if snapshot.LockedHolds != 5 || snapshot.LockedSuppressed || snapshot.Frozen {
		t.Fatalf("holds/frozen changed: %+v", snapshot)
	}
	if snapshot.Locale != "pt" || snapshot.Labels.Title != domain.TotalsLabelsFor(domain.TotalsLocalePortuguese).Title {
		t.Fatalf("document not rendered in pt: %+v", snapshot.Labels)
	}

	document := []string{
		snapshot.Locale, snapshot.Labels.Title, snapshot.Labels.Supply,
		snapshot.Labels.Treasury, snapshot.Labels.Reserve, snapshot.Labels.Circulation,
		snapshot.Labels.Locked, snapshot.Labels.Vaults,
	}
	for _, position := range snapshot.Vaults {
		document = append(document, position.Vault.String())
	}
	for _, secret := range totalsPersonStrings(t, ctx, pool) {
		for _, held := range document {
			if strings.Contains(held, secret) {
				t.Fatalf("document carries person-bound %q", secret)
			}
		}
	}
}

// TestCustodyTotalsRebuildsAfterWindow proves cache and re-derivation:
// one derivation serves the window even as the journal moves, the
// next window rebuilds from the journal as written, and en renders
// the same numbers under translated titles.
func TestCustodyTotalsRebuildsAfterWindow(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fixture := totalsRichSetup(t, ctx, pool, repo)
	clock := &totalsTestClock{now: totalsNow()}
	uc := totalsUseCase(t, repo, clock)

	first, err := uc.Totals(ctx, "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("first Totals: %v", err)
	}
	extra, err := application.NewDisburseUseCase(repo).Execute(ctx,
		disburseCmd("totals-sale-extra", "operating_cash", fixture.firstBeneficiary, "due_payment", 50, fixture.approverOne, fixture.approverTwo))
	if err != nil {
		t.Fatalf("settle extra: %v", err)
	}
	_ = extra
	second, err := uc.Totals(ctx, "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("cached Totals: %v", err)
	}
	if second.CirculationMillis != first.CirculationMillis || !second.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatalf("window shared nothing: %+v vs %+v", second, first)
	}
	clock.now = totalsNow().Add((domain.TotalsCacheSeconds + 1) * time.Second)
	third, err := uc.Totals(ctx, "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("rebuilt Totals: %v", err)
	}
	if third.CirculationMillis != 650 {
		t.Fatalf("circulation = %d, want 650 re-derived from the journal", third.CirculationMillis)
	}
	if !third.GeneratedAt.Equal(clock.now) {
		t.Fatalf("re-derivation did not carry the clock: %+v", third.GeneratedAt)
	}
	if got := totalsUserSum(t, ctx, pool); third.CirculationMillis != got {
		t.Fatalf("rebuilt circulation = %d, want the audited %d", third.CirculationMillis, got)
	}
	if third.TreasuryMillis != first.TreasuryMillis-50 {
		t.Fatalf("treasury = %d, want 50 below the sealed window", third.TreasuryMillis)
	}
	english, err := uc.Totals(ctx, "en", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("en Totals: %v", err)
	}
	if english.CirculationMillis != third.CirculationMillis || english.SupplyMillis != third.SupplyMillis {
		t.Fatalf("en numbers diverged from pt: %+v vs %+v", english, third)
	}
	if english.Labels.Title == first.Labels.Title {
		t.Fatalf("en title did not translate: %q", english.Labels.Title)
	}
}

// TestCustodyTotalsSuppressSmallCells proves the privacy rule on a
// sparse book: two holders and one hold suppress exactly the
// third-party amounts while every sovereign class stays exact.
func TestCustodyTotalsSuppressSmallCells(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	moveToVault(t, ctx, pool, "commercial_stock", 1000)
	commitFunds(t, ctx, repo, "commercial_stock", "sparse hold", 600, commitNow().Add(time.Hour))
	first, approverOne, approverTwo := disburseParties(t, ctx, pool)
	second := totalsPayees(t, ctx, pool, 1)[0]
	for i, payee := range []string{first, second} {
		cmd := disburseCmd(fmt.Sprintf("sparse-sale-%d", i+1), "operating_cash", payee, "due_payment", 100, approverOne, approverTwo)
		if _, err := application.NewDisburseUseCase(repo).Execute(ctx, cmd); err != nil {
			t.Fatalf("pay holder %d: %v", i+1, err)
		}
	}

	snapshot, err := totalsUseCase(t, repo, &totalsTestClock{now: totalsNow()}).Totals(ctx, "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if snapshot.CirculationMillis != 0 || !snapshot.CirculationSuppressed {
		t.Fatalf("sparse circulation not suppressed: %+v", snapshot)
	}
	if snapshot.LockedMillis != 0 || !snapshot.LockedSuppressed {
		t.Fatalf("sparse lock cell not suppressed: %+v", snapshot)
	}
	if snapshot.LockedHolds != 1 {
		t.Fatalf("hold count changed: %+v", snapshot)
	}
	if snapshot.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("supply = %d, want S", snapshot.SupplyMillis)
	}
	balances := map[domain.TreasuryVault]int64{}
	for _, position := range snapshot.Vaults {
		balances[position.Vault] = position.Millis
	}
	if balances[domain.TreasuryVaultOperatingCash] != 4800 || balances[domain.TreasuryVaultCommercialStock] != 400 {
		t.Fatalf("sovereign vaults changed: %+v", balances)
	}
	if snapshot.TreasuryMillis != domain.GenesisSupplyMillis-800 {
		t.Fatalf("treasury = %d, want S-800", snapshot.TreasuryMillis)
	}
	economySupply(t, ctx, pool)
}
