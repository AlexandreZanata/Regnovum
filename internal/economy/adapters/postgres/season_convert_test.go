package postgres_test

// P46-T05 — legado, opt-in e benefícios periódicos em livros sazonais.
//
// Conversão carrega temporada explícita no recibo com seu fim, e a
// intenção de crédito legado é consumida globalmente uma vez: crédito
// pago sem novo aceite continua válido no livro compat, reset omitido
// ou aceite antigo nunca autorizam conversão sazonal, aceitar a Carta
// nunca concede saldo ou cargo, replay na temporada seguinte nunca
// concede de novo, passagem de temporada nunca renova passe/grant nem
// apaga período pago, e Tesouro curto ou crash revertem tudo no
// PostgreSQL. Produto econômico segue desativado: linhas de temporada
// são fixtures de teste, sem wiring.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func seasonalConvertCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func seasonalHolder(t *testing.T, ctx context.Context, db *dbtest.TestDB, email string) string {
	t.Helper()
	pool := db.Pool.Pool()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id::text`,
		email).Scan(&id); err != nil {
		t.Fatalf("create holder: %v", err)
	}
	return id
}

func seasonalFundLegacy(t *testing.T, ctx context.Context, db *dbtest.TestDB, accountID string, free, purchased int64) {
	t.Helper()
	pool := db.Pool.Pool()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, $2, $3)`,
		accountID, free, purchased); err != nil {
		t.Fatalf("fund legacy: %v", err)
	}
	seed := func(op, bucket string, amount int64) {
		t.Helper()
		var opID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
			 VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
			accountID, op, "season-seed-"+accountID+"-"+op, "season-seed").Scan(&opID); err != nil {
			t.Fatalf("seed operation: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, $2, $3)`,
			opID, bucket, amount); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	if free > 0 {
		seed("credit_free", "FREE_INK", free)
	}
	if purchased > 0 {
		seed("credit_purchase", "PURCHASED_INK", purchased)
	}
}

func seasonalOptIn(t *testing.T, ctx context.Context, repo *postgres.Repository, holder, charter string, millis int64) {
	t.Helper()
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: charter, Decision: "accepted"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	optins := application.NewOptInUseCase(repo, fixedHoldClock{now: time.Now().UTC()})
	if _, err := optins.Execute(ctx, application.OptInCommand{
		AccountID: holder, Charter: charter, Millis: millis, RateNum: 1000, RateDen: 1, ValidDays: 30,
	}); err != nil {
		t.Fatalf("opt-in: %v", err)
	}
}

func seasonalConvert(t *testing.T, ctx context.Context, repo *postgres.Repository, holder, charter, season string, reset bool) (*application.ConversionResult, error) {
	t.Helper()
	uc := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
	return uc.Execute(ctx, application.ConvertCommand{
		AccountID: holder, Charter: charter, RateNum: 1000, RateDen: 1,
		Season: season, ResetAcknowledged: reset,
	})
}

// TestSeasonalConversionSettlesWithDatedReceipt proves the happy path
// in a season book: the receipt names the season and its exclusive
// end, S is conserved in that book and the legacy right is
// extinguished free-first.
func TestSeasonalConversionSettlesWithDatedReceipt(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := seasonalConvertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedSeasonBook(t, ctx, db, "temporada-legado-1", 11, "2026-10-04T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-legado-1", "temporada-legado-1")

	holder := seasonalHolder(t, ctx, db, "legado-1@invalid.example")
	seasonalFundLegacy(t, ctx, db, holder, 5000, 0)
	seasonalOptIn(t, ctx, repo, holder, "v1", 5000)

	result, err := seasonalConvert(t, ctx, repo, holder, "v1", "temporada-legado-1", true)
	if err != nil {
		t.Fatalf("seasonal Convert: %v", err)
	}
	if result.Replayed {
		t.Fatal("first seasonal conversion replayed: it must settle")
	}
	if string(result.Season) != "temporada-legado-1" {
		t.Fatalf("receipt season = %q, want temporada-legado-1", result.Season)
	}
	if result.SeasonEndsAt.IsZero() {
		t.Fatal("receipt ends_at is zero: the receipt must identify the season end")
	}
	var wantEnds time.Time
	if err := pool.QueryRow(ctx, `SELECT ends_at FROM app.seasons WHERE season_key = 'temporada-legado-1'`).Scan(&wantEnds); err != nil {
		t.Fatalf("read ends_at: %v", err)
	}
	if !result.SeasonEndsAt.Equal(wantEnds.UTC()) {
		t.Fatalf("receipt ends_at = %v, want %v", result.SeasonEndsAt, wantEnds.UTC())
	}
	if result.Converted.Millis() != 5000 {
		t.Fatalf("converted = %d, want 5000", result.Converted.Millis())
	}
	if got := bookBalance(t, ctx, db, "user", holder, "temporada-legado-1"); got != 5000 {
		t.Fatalf("holder custody = %d, want 5000 in the season book", got)
	}
	if got := bookBalance(t, ctx, db, "treasury", "main", "temporada-legado-1"); got != domain.GenesisSupplyMillis-5000 {
		t.Fatalf("treasury = %d, want S-5000: Member never mints, stock pays", got)
	}
}

// TestSeasonalConversionRefusesWithoutReset proves an omitted reset
// or a legacy-only acceptance never authorizes a seasonal
// conversion: nothing is written anywhere.
func TestSeasonalConversionRefusesWithoutReset(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := seasonalConvertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedSeasonBook(t, ctx, db, "temporada-legado-2", 12, "2026-10-04T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-legado-2", "temporada-legado-2")

	holder := seasonalHolder(t, ctx, db, "legado-2@invalid.example")
	seasonalFundLegacy(t, ctx, db, holder, 2000, 0)
	seasonalOptIn(t, ctx, repo, holder, "v1", 2000)

	beforeLegs := bookLegs(t, ctx, db, "temporada-legado-2")
	if _, err := seasonalConvert(t, ctx, repo, holder, "v1", "temporada-legado-2", false); !errors.Is(err, domain.ErrConsentRequired) {
		t.Fatalf("reset omitido = %v, want ErrConsentRequired", err)
	}
	if got := bookLegs(t, ctx, db, "temporada-legado-2"); got != beforeLegs {
		t.Fatalf("refused conversion wrote %d legs, want %d", got, beforeLegs)
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 0 {
		t.Fatalf("refused conversion recorded %d intentions", intentions)
	}
}

// TestSeasonalConversionConsumedGloballyOnce proves the legacy intent
// is consumed globally once: a replay in the next book resolves the
// original receipt with its original season, writing no second grant.
func TestSeasonalConversionConsumedGloballyOnce(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := seasonalConvertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedSeasonBook(t, ctx, db, "temporada-legado-3a", 13, "2026-10-04T12:00:00Z")
	seedSeasonBook(t, ctx, db, "temporada-legado-3b", 14, "2027-01-02T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-legado-3a", "temporada-legado-3a")
	genesisBook(t, ctx, repo, "genesis-legado-3b", "temporada-legado-3b")

	holder := seasonalHolder(t, ctx, db, "legado-3@invalid.example")
	seasonalFundLegacy(t, ctx, db, holder, 4000, 0)
	seasonalOptIn(t, ctx, repo, holder, "v1", 4000)

	first, err := seasonalConvert(t, ctx, repo, holder, "v1", "temporada-legado-3a", true)
	if err != nil {
		t.Fatalf("first seasonal Convert: %v", err)
	}
	legsA := bookLegs(t, ctx, db, "temporada-legado-3a")
	legsB := bookLegs(t, ctx, db, "temporada-legado-3b")

	second, err := seasonalConvert(t, ctx, repo, holder, "v1", "temporada-legado-3b", true)
	if err != nil {
		t.Fatalf("second-book replay: %v", err)
	}
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("second book = %+v, want replay of %+v without a second grant", second, first)
	}
	if string(second.Season) != "temporada-legado-3a" {
		t.Fatalf("replay season = %q, want the original temporada-legado-3a", second.Season)
	}
	if got := bookLegs(t, ctx, db, "temporada-legado-3a"); got != legsA {
		t.Fatalf("original book legs moved: replay writes nothing")
	}
	if got := bookLegs(t, ctx, db, "temporada-legado-3b"); got != legsB {
		t.Fatalf("next book legs moved: replay grants nothing in the new season")
	}
	if got := bookBalance(t, ctx, db, "user", holder, "temporada-legado-3b"); got != 0 {
		t.Fatalf("next book holder = %d, want 0: no carry-over", got)
	}
}

// TestPaidCreditWithoutNewAcceptanceStaysValid proves non-converted
// rights keep their utility and expiration: a legacy conversion in
// the compat book still settles after seasons open, without any
// seasonal reset.
func TestPaidCreditWithoutNewAcceptanceStaysValid(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := seasonalConvertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	seedSeasonBook(t, ctx, db, "temporada-legado-4", 15, "2026-10-04T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-legado-4", "temporada-legado-4")

	holder := seasonalHolder(t, ctx, db, "legado-4@invalid.example")
	seasonalFundLegacy(t, ctx, db, holder, 3000, 0)
	seasonalOptIn(t, ctx, repo, holder, "v1", 3000)

	uc := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
	result, err := uc.Execute(ctx, application.ConvertCommand{
		AccountID: holder, Charter: "v1", RateNum: 1000, RateDen: 1,
	})
	if err != nil {
		t.Fatalf("legacy Convert without reset: %v", err)
	}
	if string(result.Season) != domain.CompatSeasonKey {
		t.Fatalf("legacy receipt season = %q, want compat-legacy", result.Season)
	}
	if got := custodyBalance(t, ctx, pool, "user", holder); got != 3000 {
		t.Fatalf("legacy holder = %d, want 3000: paid credit stays valid", got)
	}
}

// TestSeasonalConversionFailsClosed proves short Treasury stock and a
// crash revert whole: the legacy right stays intact and S is
// conserved in the season book.
func TestSeasonalConversionFailsClosed(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := seasonalConvertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedSeasonBook(t, ctx, db, "temporada-legado-5", 16, "2026-10-04T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-legado-5", "temporada-legado-5")

	drain := "dreno-legado-5"
	makeBookCustody(t, ctx, db, "user", drain, "temporada-legado-5")
	treasury, err := domain.NewMilliInk(bookBalance(t, ctx, db, "treasury", "main", "temporada-legado-5") - 100)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: "temporada-legado-5", FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToSeason: "temporada-legado-5", ToKind: domain.CustodyUser, ToLabel: drain,
		Amount: treasury,
	}); err != nil {
		t.Fatalf("drain treasury: %v", err)
	}

	holder := seasonalHolder(t, ctx, db, "legado-5@invalid.example")
	seasonalFundLegacy(t, ctx, db, holder, 200000, 0)
	seasonalOptIn(t, ctx, repo, holder, "v1", 200000)

	if _, err := seasonalConvert(t, ctx, repo, holder, "v1", "temporada-legado-5", true); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("stockout = %v, want ErrInsufficientMilliInk", err)
	}
	var free, purchased int64
	if err := pool.QueryRow(ctx, `SELECT balance_free, balance_purchased FROM app.wallet_accounts WHERE account_id = $1::uuid`, holder).Scan(&free, &purchased); err != nil {
		t.Fatalf("legacy sums: %v", err)
	}
	if free != 200000 || purchased != 0 {
		t.Fatalf("legacy moved on refused conversion: %d/%d", free, purchased)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	uc := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
	if _, err := uc.Execute(cancelled, application.ConvertCommand{
		AccountID: holder, Charter: "v1", RateNum: 1000, RateDen: 1,
		Season: "temporada-legado-5", ResetAcknowledged: true,
	}); err == nil {
		t.Fatal("cancelled seasonal conversion succeeded")
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions WHERE season_key = 'temporada-legado-5'`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 0 {
		t.Fatalf("cancelled conversion recorded %d intentions in the season", intentions)
	}
	report, err := repo.Reconcile(ctx, domain.SeasonKey("temporada-legado-5"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Frozen || report.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("season book = %+v, want one clean S after rollback", report)
	}
}

// TestSeasonalGrantGuardIsolatesBooks proves Member never mints S:
// grants draw from Treasury stock of the current book, and books do
// not share stock.
func TestSeasonalGrantGuardIsolatesBooks(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := seasonalConvertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedSeasonBook(t, ctx, db, "temporada-legado-6a", 17, "2026-10-04T12:00:00Z")
	seedSeasonBook(t, ctx, db, "temporada-legado-6b", 18, "2027-01-02T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-legado-6a", "temporada-legado-6a")

	guard := application.NewGrantGuardUseCase(repo)
	if source, err := guard.Execute(ctx, application.GrantCommand{Millis: 100, Season: "temporada-legado-6a"}); err != nil || source != domain.GrantSourceTreasury {
		t.Fatalf("funded book = %q, %v; want treasury (Member draws from stock, never mints)", source, err)
	}
	if source, err := guard.Execute(ctx, application.GrantCommand{Millis: 100, Season: "temporada-legado-6b"}); err != nil || source != domain.GrantSourceLegacy {
		t.Fatalf("unfounded book = %q, %v; want legacy (no Genesis, no treasury grant)", source, err)
	}
	beforeEconomy, beforeLegacy := journalFingerprint(t, ctx, pool)
	if _, err := guard.Execute(ctx, application.GrantCommand{Millis: 100, Season: "temporada-legado-6a"}); err != nil {
		t.Fatalf("guard read: %v", err)
	}
	if afterEconomy, afterLegacy := journalFingerprint(t, ctx, pool); beforeEconomy != afterEconomy || beforeLegacy != afterLegacy {
		t.Fatal("grant guard moved the journals: reads never mint")
	}
}
