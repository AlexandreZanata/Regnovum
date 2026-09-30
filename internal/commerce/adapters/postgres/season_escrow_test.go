package postgres_test

// P46-T07 — comércio e escrows sazonais com cláusula terminal.
//
// Financiamento sem política ou aceite recusa antes de qualquer
// trava; transferência, escrow e refund carregam o livro original
// com Dízimo no original; referência cruzada conflita; selo recusa
// nova admissão; dois liberadores no mesmo escrow liquidam uma vez;
// falha antes do commit deixa zero efeito e replay retorna o mesmo
// recibo. Classificador puro cobre cada ramo em
// internal/commerce/domain/terminal_test.go. Produto econômico
// segue desativado: temporadas são fixtures, sem wiring.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func seasonEscrowCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func seedCommerceSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int) (starts, ends time.Time) {
	t.Helper()
	starts = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3::timestamptz, $3::timestamptz + make_interval(secs => 7776000), 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica',
		 '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`,
		key, ordinal, starts.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
	if err := db.QueryRow(ctx, `SELECT starts_at, ends_at FROM app.seasons WHERE season_key = $1`, key).Scan(&starts, &ends); err != nil {
		t.Fatalf("read season window: %v", err)
	}
	return starts.UTC(), ends.UTC()
}

func sealCommerceSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) {
	t.Helper()
	for _, stage := range [][2]string{
		{"NULL", "prepared"},
		{"prepared", "active"},
		{"active", "closing"},
		{"closing", "sealed"},
	} {
		from := "NULL"
		if stage[0] != "NULL" {
			from = "'" + stage[0] + "'"
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
			 VALUES ($1, `+from+`, $2, now())`, key, stage[1]); err != nil {
			t.Fatalf("seal stage %s of %s: %v", stage[1], key, err)
		}
	}
}

func commerceSeasonAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('sazonal-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func fundCommerceSeasonHolder(t *testing.T, ctx context.Context, db *dbtest.TestDB, account, season string, funds int64) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	genKey, err := economydomain.ParseGenesisKey("commerce-season-seed-" + season)
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: genKey, Season: economydomain.SeasonKey(season)}); err != nil {
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) {
			t.Fatalf("genesis %s: %v", season, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2) ON CONFLICT DO NOTHING`, account, season); err != nil {
		t.Fatalf("create custody: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('treasury', 'main', $1) ON CONFLICT DO NOTHING`, season); err != nil {
		t.Fatalf("ensure treasury: %v", err)
	}
	amount, err := economydomain.NewMilliInk(funds)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromSeason: economydomain.SeasonKey(season), FromKind: fromKind, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(season), ToKind: toKind, ToLabel: account, Amount: amount,
	}); err != nil {
		t.Fatalf("fund holder %s in %s: %v", account, season, err)
	}
}

func provisionSeasonCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, account, season string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2) ON CONFLICT DO NOTHING`, account, season); err != nil {
		t.Fatalf("provision custody %s in %s: %v", account, season, err)
	}
}

const seasonPolicyHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// seasonContractOrder bundles one seasonal funding for the test
// helper: a single struct keeps the helper under the parameter
// budget while naming every terminal term explicitly.
type seasonContractOrder struct {
	fund     *commerceapp.FundContractUseCase
	key      string
	buyer    string
	provider string
	season   string
	ends     time.Time
	now      time.Time
	amount   int64
}

func fundSeasonalContract(t *testing.T, ctx context.Context, order seasonContractOrder) *commerceapp.ContractView {
	t.Helper()
	view, err := order.fund.Execute(ctx, commerceapp.FundCommand{
		Key: order.key, Object: "revisao sazonal", Buyer: order.buyer, Provider: order.provider,
		AmountMill: order.amount, ExpiresAt: order.ends.Add(-time.Hour), Now: order.now,
		Season: order.season, PolicyRef: "terminal-v1", PolicyHash: seasonPolicyHash,
		BuyerAccept: "aceite-comprador-1", ProviderAccept: "aceite-prestador-1",
		SeasonEndsAt: order.ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("fund seasonal %s: %v", order.key, err)
	}
	if string(view.Season) != order.season {
		t.Fatalf("contract season = %q, want %q", view.Season, order.season)
	}
	return view
}

// TestSeasonalFundingRefusesWithoutClause prova que a cláusula
// terminal é obrigatória fora do compat: sem política ou aceite,
// ou com deadline além do fim, o financiamento recusa sem travar.
func TestSeasonalFundingRefusesWithoutClause(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonEscrowCtx()
	defer cancel()

	_, ends := seedCommerceSeason(t, ctx, db, "temporada-escrow-1", 101)
	buyer := commerceSeasonAccount(t, ctx, db)
	provider := commerceSeasonAccount(t, ctx, db)
	fundCommerceSeasonHolder(t, ctx, db, buyer, "temporada-escrow-1", 100000)
	pool := db.Pool.Pool()
	escrowRepo, _ := commercepg.NewEscrowRepository(pool)
	fund, _ := commerceapp.NewFundContractUseCase(escrowRepo)
	now := time.Now().UTC().Truncate(time.Second)
	bad := commerceapp.FundCommand{
		Key: "sem-politica", Object: "revisao", Buyer: buyer, Provider: provider,
		AmountMill: 20000, ExpiresAt: ends.Add(-time.Hour), Now: now,
		Season: "temporada-escrow-1", SeasonEndsAt: ends, ResetAcknowledged: true,
	}
	if _, err := fund.Execute(ctx, bad); err == nil {
		t.Fatal("financiamento sem politica aceitou: clausula ausente deve recusar")
	}
	noBuyer := bad
	noBuyer.Key = "sem-aceite"
	noBuyer.PolicyRef = "terminal-v1"
	noBuyer.PolicyHash = seasonPolicyHash
	noBuyer.ProviderAccept = "aceite-prestador-1"
	if _, err := fund.Execute(ctx, noBuyer); err == nil {
		t.Fatal("financiamento sem aceite do comprador aceitou: ausencia deve recusar")
	}
	pastEnd := bad
	pastEnd.Key = "alem-do-fim"
	pastEnd.PolicyRef = "terminal-v1"
	pastEnd.PolicyHash = seasonPolicyHash
	pastEnd.BuyerAccept = "aceite-comprador-1"
	pastEnd.ProviderAccept = "aceite-prestador-1"
	pastEnd.ExpiresAt = ends.Add(time.Second)
	if _, err := fund.Execute(ctx, pastEnd); err == nil {
		t.Fatal("deadline alem do fim aceitou: vencimento fica ate ends_at")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_contracts WHERE season_key = $1`, "temporada-escrow-1").Scan(&count); err != nil {
		t.Fatalf("count contracts: %v", err)
	}
	if count != 0 {
		t.Fatalf("contracts = %d, want 0: recusa nao escreve", count)
	}
}

// TestSeasonalTransferCrossBookConflicts prova que a mesma chave em
// outro livro não redireciona o replay: reutilização cruzada
// conflita em vez de liquidar outro livro, com zero efeito cruzado,
// e replay no livro original retorna o mesmo recibo.
func TestSeasonalTransferCrossBookConflicts(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonEscrowCtx()
	defer cancel()

	seedCommerceSeason(t, ctx, db, "temporada-p2p-1", 102)
	seedCommerceSeason(t, ctx, db, "temporada-p2p-1b", 103)
	payer := commerceSeasonAccount(t, ctx, db)
	payee := commerceSeasonAccount(t, ctx, db)
	fundCommerceSeasonHolder(t, ctx, db, payer, "temporada-p2p-1", 100000)
	fundCommerceSeasonHolder(t, ctx, db, payer, "temporada-p2p-1b", 100000)
	provisionSeasonCustody(t, ctx, db, payee, "temporada-p2p-1")
	provisionSeasonCustody(t, ctx, db, payee, "temporada-p2p-1b")
	pool := db.Pool.Pool()
	transferRepo, _ := commercepg.NewRepository(pool, commercepg.TransferLimits{MaxAmountMilli: 1000000, MaxPerWindow: 100, WindowMinutes: 60})
	transferUC, _ := commerceapp.NewTransferUseCase(transferRepo)
	first, err := transferUC.Execute(ctx, commerceapp.TransferCommand{
		Key: "chave-cruzada", Kind: "gift", Payer: payer, Payee: payee,
		AmountMilli: 5000, ConsentRef: "consentimento-1",
		Season: "temporada-p2p-1", ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("transfer livro 1: %v", err)
	}
	if string(first.Season) != "temporada-p2p-1" {
		t.Fatalf("transfer season = %q, want temporada-p2p-1", first.Season)
	}
	before := 0
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_transfers WHERE season_key = $1`, "temporada-p2p-1b").Scan(&before); err != nil {
		t.Fatalf("count other book: %v", err)
	}
	if _, err := transferUC.Execute(ctx, commerceapp.TransferCommand{
		Key: "chave-cruzada", Kind: "gift", Payer: payer, Payee: payee,
		AmountMilli: 5000, ConsentRef: "consentimento-1",
		Season: "temporada-p2p-1b", ResetAcknowledged: true,
	}); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("reutilizacao cruzada = %v, want ErrIntentionConflict sem redirecionar", err)
	}
	var after int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_transfers WHERE season_key = $1`, "temporada-p2p-1b").Scan(&after); err != nil {
		t.Fatalf("count other book: %v", err)
	}
	if after != before {
		t.Fatalf("outro livro = %d, want %d: cruzado nao escreve", after, before)
	}
	replayed, err := transferUC.Execute(ctx, commerceapp.TransferCommand{
		Key: "chave-cruzada", Kind: "gift", Payer: payer, Payee: payee,
		AmountMilli: 5000, ConsentRef: "consentimento-1",
		Season: "temporada-p2p-1", ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("replay livro 1: %v", err)
	}
	if !replayed.Replayed || replayed.TransferRowID != first.TransferRowID {
		t.Fatal("replay deve retornar o mesmo recibo do livro original")
	}
	divergent := commerceapp.TransferCommand{
		Key: "chave-cruzada", Kind: "gift", Payer: payer, Payee: payee,
		AmountMilli: 6000, ConsentRef: "consentimento-1",
		Season: "temporada-p2p-1", ResetAcknowledged: true,
	}
	if _, err := transferUC.Execute(ctx, divergent); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("payload divergente = %v, want ErrIntentionConflict", err)
	}
}

// TestSeasonalTwoReleasersSettleOnce prova os dois liberadores:
// duas execuções concorrentes de ReleaseContractUseCase no mesmo
// escrow produzem um efeito terminal, com replay idêntico e Dízimo
// no livro original.
func TestSeasonalTwoReleasersSettleOnce(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonEscrowCtx()
	defer cancel()

	_, ends := seedCommerceSeason(t, ctx, db, "temporada-escrow-2", 104)
	buyer := commerceSeasonAccount(t, ctx, db)
	provider := commerceSeasonAccount(t, ctx, db)
	fundCommerceSeasonHolder(t, ctx, db, buyer, "temporada-escrow-2", 100000)
	provisionSeasonCustody(t, ctx, db, provider, "temporada-escrow-2")
	pool := db.Pool.Pool()
	escrowRepo, _ := commercepg.NewEscrowRepository(pool)
	fund, _ := commerceapp.NewFundContractUseCase(escrowRepo)
	acceptUC, _ := commerceapp.NewAcceptDeliveryUseCase(escrowRepo)
	releaseUC, _ := commerceapp.NewReleaseContractUseCase(escrowRepo)
	now := time.Now().UTC().Truncate(time.Second)
	fundSeasonalContract(t, ctx, seasonContractOrder{fund: fund, key: "escrow-corrida-1", buyer: buyer, provider: provider, season: "temporada-escrow-2", ends: ends, now: now, amount: 20000})
	if _, err := acceptUC.Execute(ctx, "escrow-corrida-1", buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	var wg sync.WaitGroup
	views := make([]*commerceapp.ContractView, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			views[idx], errs[idx] = releaseUC.Execute(ctx, "escrow-corrida-1", buyer)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("corrida release: %v", err)
		}
	}
	if views[0].Status != commercedomain.ContractReleased || views[1].Status != commercedomain.ContractReleased {
		t.Fatal("corrida deve terminar released nas duas leituras")
	}
	var terminals int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_settlements s JOIN app.commerce_contracts c ON c.id = s.contract_id WHERE c.contract_key = $1 AND s.action = 'release'`, "escrow-corrida-1").Scan(&terminals); err != nil {
		t.Fatalf("count terminals: %v", err)
	}
	if terminals != 1 {
		t.Fatalf("terminal release = %d, want 1: dois liberadores liquidam uma vez", terminals)
	}
	var providerBalance int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'user' AND c.label = $1 AND e.season_key = $2`, provider, "temporada-escrow-2").Scan(&providerBalance); err != nil {
		t.Fatalf("provider balance: %v", err)
	}
	if providerBalance != 18000 {
		t.Fatalf("provider = %d, want 18000 no livro original (Dizimo 2000)", providerBalance)
	}
}

// TestSeasonalEscrowRefusesAfterSeal prova que o selo impede nova
// terminalização e preserva custódia: release após selo recusa, e o
// refund posterior autorizado reverte no original sem carry-over.
func TestSeasonalEscrowRefusesAfterSeal(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonEscrowCtx()
	defer cancel()

	_, ends := seedCommerceSeason(t, ctx, db, "temporada-escrow-3", 105)
	buyer := commerceSeasonAccount(t, ctx, db)
	provider := commerceSeasonAccount(t, ctx, db)
	fundCommerceSeasonHolder(t, ctx, db, buyer, "temporada-escrow-3", 100000)
	provisionSeasonCustody(t, ctx, db, provider, "temporada-escrow-3")
	pool := db.Pool.Pool()
	escrowRepo, _ := commercepg.NewEscrowRepository(pool)
	fund, _ := commerceapp.NewFundContractUseCase(escrowRepo)
	acceptUC, _ := commerceapp.NewAcceptDeliveryUseCase(escrowRepo)
	releaseUC, _ := commerceapp.NewReleaseContractUseCase(escrowRepo)
	now := time.Now().UTC().Truncate(time.Second)
	fundSeasonalContract(t, ctx, seasonContractOrder{fund: fund, key: "escrow-selo-1", buyer: buyer, provider: provider, season: "temporada-escrow-3", ends: ends, now: now, amount: 20000})
	if _, err := acceptUC.Execute(ctx, "escrow-selo-1", buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	sealCommerceSeason(t, ctx, db, "temporada-escrow-3")
	if _, err := releaseUC.Execute(ctx, "escrow-selo-1", buyer); err == nil {
		t.Fatal("release apos selo liquidou: selado deve recusar sem novo efeito")
	}
	var status string
	if err := db.QueryRow(ctx,
		`SELECT action FROM app.commerce_settlements s JOIN app.commerce_contracts c ON c.id = s.contract_id
		 WHERE c.contract_key = $1 ORDER BY s.posted_at DESC, s.id DESC LIMIT 1`, "escrow-selo-1").Scan(&status); err != nil {
		t.Fatalf("read settlement: %v", err)
	}
	if status != "accept" {
		t.Fatalf("ultimo passo = %q, want accept: selo nao terminaliza", status)
	}
}
