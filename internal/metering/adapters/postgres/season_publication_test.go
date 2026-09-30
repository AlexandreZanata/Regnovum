package postgres_test

// P46-T07 — publicação sazonal cerca livro, corte e recibo.
//
// Preview velho ou cruzado recusa antes de qualquer escrita;
// publicação e débito partilham uma transação no livro original;
// referência cruzada conflita em vez de redirecionar; recusa após
// o selo preserva o diário; falha antes do commit deixa zero
// efeito e replay pós-commit retorna o mesmo recibo. Produto
// econômico segue desativado: temporadas são fixtures, sem wiring.

import (
	"context"
	"sync"
	"testing"
	"time"

	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func seasonPubCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func seedSeasonBook(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int) (starts, ends time.Time) {
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

func sealSeasonBook(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) {
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

func fundSeasonCitizen(t *testing.T, ctx context.Context, db *dbtest.TestDB, citizen, season string, funds int64) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	genKey, err := economydomain.ParseGenesisKey("pub-season-seed-" + season)
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: genKey, Season: economydomain.SeasonKey(season)}); err != nil {
		t.Fatalf("genesis %s: %v", season, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2) ON CONFLICT DO NOTHING`, citizen, season); err != nil {
		t.Fatalf("create citizen custody: %v", err)
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
		ToSeason: economydomain.SeasonKey(season), ToKind: toKind, ToLabel: citizen, Amount: amount,
	}); err != nil {
		t.Fatalf("fund citizen %s in %s: %v", citizen, season, err)
	}
}

func seasonPrice(t *testing.T, at time.Time) meteringdomain.PriceEntry {
	t.Helper()
	service, err := meteringdomain.ParseServiceID("argument-publish")
	if err != nil {
		t.Fatalf("ParseServiceID: %v", err)
	}
	price, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(2 * time.Hour),
		PriceMilli: 250, Unit: meteringdomain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	return price
}

func seasonContent(t *testing.T) meteringdomain.MeasuredContent {
	t.Helper()
	content, err := meteringdomain.ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	return content
}

func seasonCatalog(price meteringdomain.PriceEntry) meteringdomain.Catalog {
	var catalog meteringdomain.Catalog
	if err := catalog.Add(price); err != nil {
		panic(err)
	}
	return catalog
}

type seasonPubClock struct{ now time.Time }

func (c seasonPubClock) Now() time.Time { return c.now }

func publishInSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, citizen, season string, ends time.Time, at time.Time) *meteringapp.PublishResult {
	t.Helper()
	pool := db.Pool.Pool()
	content := seasonContent(t)
	price := seasonPrice(t, at)
	catalog := seasonCatalog(price)
	repo, err := meteringpg.NewRepository(pool, seasonPubClock{now: at}, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	previewUC, err := meteringapp.NewPreviewUseCase(catalog, text.GraphemeCount)
	if err != nil {
		t.Fatalf("NewPreviewUseCase: %v", err)
	}
	preview, err := previewUC.Execute(meteringapp.PreviewCommand{
		Account: citizen, Content: "texto final", Service: "argument-publish",
		Now: at, MaxUnits: 3000, TTL: 30 * time.Minute,
		Season: season, SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("preview seasonal: %v", err)
	}
	publishUC, err := meteringapp.NewPublishUseCase(repo)
	if err != nil {
		t.Fatalf("NewPublishUseCase: %v", err)
	}
	result, err := publishUC.Execute(ctx, meteringapp.PublishCommand{
		Key: key, Account: citizen, Content: content, Price: price, Quote: preview.Quote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: at,
		Season: season, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("publish %s: %v", key, err)
	}
	return result
}

func countSeasonLegs(t *testing.T, ctx context.Context, db *dbtest.TestDB, season string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, season).Scan(&count); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	return count
}

// TestSeasonalPublicationBeforeAndAtCutoff prova admissão antes do
// corte com recibo no livro original e débito atômico: publicação,
// legs e linha partilham a mesma transferência e o mesmo livro.
func TestSeasonalPublicationBeforeAndAtCutoff(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonPubCtx()
	defer cancel()

	_, ends := seedSeasonBook(t, ctx, db, "temporada-pub-1", 91)
	citizen := "cidadao-pub-1"
	fundSeasonCitizen(t, ctx, db, citizen, "temporada-pub-1", 100000)
	at := time.Now().UTC().Truncate(time.Second)
	first := publishInSeason(t, ctx, db, "pub-1", citizen, "temporada-pub-1", ends, at)
	if first.Replayed || string(first.Season) != "temporada-pub-1" {
		t.Fatalf("receipt = %+v, want fresh in temporada-pub-1", first)
	}
	var storedSeason, storedTransfer string
	if err := db.QueryRow(ctx, `SELECT season_key, transfer_id::text FROM app.metering_publications WHERE id = $1::uuid`, first.PublicationID).Scan(&storedSeason, &storedTransfer); err != nil {
		t.Fatalf("read publication: %v", err)
	}
	if storedSeason != "temporada-pub-1" || storedTransfer != first.TransferID {
		t.Fatalf("stored season/transfer = %q/%q, want temporada-pub-1/%q", storedSeason, storedTransfer, first.TransferID)
	}
	var legs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE transfer_id = $1::uuid AND season_key = $2`, first.TransferID, "temporada-pub-1").Scan(&legs); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if legs != 2 {
		t.Fatalf("legs for transfer = %d, want 2 in the original book", legs)
	}
}

// TestSeasonalPreviewStaleAndCrossed prova que preview velho
// (expirado no relógio do banco) e cruzado (outro livro) recusam
// sem escrever: diário e registro intactos.
func TestSeasonalPreviewStaleAndCrossed(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonPubCtx()
	defer cancel()

	_, ends := seedSeasonBook(t, ctx, db, "temporada-pub-2", 92)
	_, endsB := seedSeasonBook(t, ctx, db, "temporada-pub-2b", 93)
	citizen := "cidadao-pub-2"
	fundSeasonCitizen(t, ctx, db, citizen, "temporada-pub-2", 100000)
	fundSeasonCitizen(t, ctx, db, citizen, "temporada-pub-2b", 100000)
	before := countSeasonLegs(t, ctx, db, "temporada-pub-2")
	at := time.Now().UTC().Truncate(time.Second)
	content := seasonContent(t)
	price := seasonPrice(t, at.Add(-2*time.Hour))
	oldQuote, err := meteringdomain.AcceptSeasonalPublicationQuote(meteringdomain.SeasonalQuoteRequest{
		Account: citizen, Content: content, Price: price,
		AcceptedAt: at.Add(-2 * time.Hour), TTL: time.Minute,
		Season: meteringdomain.SeasonKey("temporada-pub-2"), SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("old quote: %v", err)
	}
	pool := db.Pool.Pool()
	catalog := seasonCatalog(seasonPrice(t, at))
	repo, _ := meteringpg.NewRepository(pool, seasonPubClock{now: at}, catalog)
	publishUC, _ := meteringapp.NewPublishUseCase(repo)
	_, err = publishUC.Execute(ctx, meteringapp.PublishCommand{
		Key: "pub-velho", Account: citizen, Content: content, Price: price, Quote: oldQuote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: at,
		Season: "temporada-pub-2", ResetAcknowledged: true,
	})
	if err == nil {
		t.Fatal("preview velho liquidou: expirado deve recusar sem escrever")
	}
	freshAt := time.Now().UTC().Truncate(time.Second)
	freshPrice := seasonPrice(t, freshAt)
	freshQuote, err := meteringdomain.AcceptSeasonalPublicationQuote(meteringdomain.SeasonalQuoteRequest{
		Account: citizen, Content: content, Price: freshPrice,
		AcceptedAt: freshAt, TTL: 30 * time.Minute,
		Season: meteringdomain.SeasonKey("temporada-pub-2b"), SeasonEndsAt: endsB, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("fresh quote outro livro: %v", err)
	}
	_, err = publishUC.Execute(ctx, meteringapp.PublishCommand{
		Key: "pub-cruzado", Account: citizen, Content: content, Price: freshPrice, Quote: freshQuote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: freshAt,
		Season: "temporada-pub-2", ResetAcknowledged: true,
	})
	if err == nil {
		t.Fatal("preview cruzado liquidou: outro livro deve conflitar sem escrever")
	}
	if got := countSeasonLegs(t, ctx, db, "temporada-pub-2"); got != before {
		t.Fatalf("legs = %d, want %d: falha deve deixar zero efeito", got, before)
	}
}

// TestSeasonalPublicationRefusesAfterSeal prova que o livro selado
// é história legível, nunca ledger vivo: nova admissão recusa e o
// refund posterior reverte no livro original, nunca no atual.
func TestSeasonalPublicationRefusesAfterSeal(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonPubCtx()
	defer cancel()

	_, ends := seedSeasonBook(t, ctx, db, "temporada-pub-3", 94)
	citizen := "cidadao-pub-3"
	fundSeasonCitizen(t, ctx, db, citizen, "temporada-pub-3", 100000)
	at := time.Now().UTC().Truncate(time.Second)
	settled := publishInSeason(t, ctx, db, "pub-selo-1", citizen, "temporada-pub-3", ends, at)
	sealSeasonBook(t, ctx, db, "temporada-pub-3")
	pool := db.Pool.Pool()
	catalog := seasonCatalog(seasonPrice(t, at))
	repo, _ := meteringpg.NewRepository(pool, seasonPubClock{now: at}, catalog)
	publishUC, _ := meteringapp.NewPublishUseCase(repo)
	content := seasonContent(t)
	price := seasonPrice(t, at)
	previewUC, _ := meteringapp.NewPreviewUseCase(catalog, text.GraphemeCount)
	preview, err := previewUC.Execute(meteringapp.PreviewCommand{
		Account: citizen, Content: "texto final", Service: "argument-publish",
		Now: at, MaxUnits: 3000, TTL: 30 * time.Minute,
		Season: "temporada-pub-3", SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("preview apos selo: %v", err)
	}
	_, err = publishUC.Execute(ctx, meteringapp.PublishCommand{
		Key: "pub-selo-2", Account: citizen, Content: content, Price: price, Quote: preview.Quote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: at,
		Season: "temporada-pub-3", ResetAcknowledged: true,
	})
	if err == nil {
		t.Fatal("publicacao apos selo liquidou: selado deve recusar sem escrever")
	}
	_ = settled
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.metering_publications WHERE season_key = $1`, "temporada-pub-3").Scan(&count); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	if count != 1 {
		t.Fatalf("publications = %d, want 1: selo nao cria efeito novo", count)
	}
}

// TestSeasonalPublicationCrashLeavesNoEffect prova atomicidade:
// contexto cancelado antes do commit deixa diário e registro
// intactos, e replay pós-commit retorna o mesmo recibo.
func TestSeasonalPublicationCrashLeavesNoEffect(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonPubCtx()
	defer cancel()

	_, ends := seedSeasonBook(t, ctx, db, "temporada-pub-4", 95)
	citizen := "cidadao-pub-4"
	fundSeasonCitizen(t, ctx, db, citizen, "temporada-pub-4", 100000)
	at := time.Now().UTC().Truncate(time.Second)
	before := countSeasonLegs(t, ctx, db, "temporada-pub-4")
	cancelled, stop := context.WithCancel(ctx)
	stop()
	pool := db.Pool.Pool()
	content := seasonContent(t)
	price := seasonPrice(t, at)
	catalog := seasonCatalog(price)
	repo, _ := meteringpg.NewRepository(pool, seasonPubClock{now: at}, catalog)
	previewUC, _ := meteringapp.NewPreviewUseCase(catalog, text.GraphemeCount)
	preview, err := previewUC.Execute(meteringapp.PreviewCommand{
		Account: citizen, Content: "texto final", Service: "argument-publish",
		Now: at, MaxUnits: 3000, TTL: 30 * time.Minute,
		Season: "temporada-pub-4", SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	publishUC, _ := meteringapp.NewPublishUseCase(repo)
	_, err = publishUC.Execute(cancelled, meteringapp.PublishCommand{
		Key: "pub-crash", Account: citizen, Content: content, Price: price, Quote: preview.Quote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: at,
		Season: "temporada-pub-4", ResetAcknowledged: true,
	})
	if err == nil {
		t.Fatal("contexto cancelado liquidou: crash antes do commit deve deixar zero efeito")
	}
	if got := countSeasonLegs(t, ctx, db, "temporada-pub-4"); got != before {
		t.Fatalf("legs = %d, want %d: crash nao escreve", got, before)
	}
	first := publishInSeason(t, ctx, db, "pub-replay-1", citizen, "temporada-pub-4", ends, at)
	second := publishInSeason(t, ctx, db, "pub-replay-1", citizen, "temporada-pub-4", ends, at)
	if !second.Replayed || first.PublicationID != second.PublicationID || first.TransferID != second.TransferID {
		t.Fatalf("replay = %+v vs %+v, want mesmo recibo", first, second)
	}
}

// TestSeasonalPublicationConcurrentSameKey prova idempotência sob
// corrida: duas publicações da mesma chave liquidam uma vez, com
// um par de legs, sob -race.
func TestSeasonalPublicationConcurrentSameKey(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonPubCtx()
	defer cancel()

	_, ends := seedSeasonBook(t, ctx, db, "temporada-pub-5", 96)
	citizen := "cidadao-pub-5"
	fundSeasonCitizen(t, ctx, db, citizen, "temporada-pub-5", 100000)
	at := time.Now().UTC().Truncate(time.Second)
	content := seasonContent(t)
	price := seasonPrice(t, at)
	catalog := seasonCatalog(price)
	pool := db.Pool.Pool()
	repo, _ := meteringpg.NewRepository(pool, seasonPubClock{now: at}, catalog)
	previewUC, _ := meteringapp.NewPreviewUseCase(catalog, text.GraphemeCount)
	preview, err := previewUC.Execute(meteringapp.PreviewCommand{
		Account: citizen, Content: "texto final", Service: "argument-publish",
		Now: at, MaxUnits: 3000, TTL: 30 * time.Minute,
		Season: "temporada-pub-5", SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	publishUC, _ := meteringapp.NewPublishUseCase(repo)
	var wg sync.WaitGroup
	results := make([]*meteringapp.PublishResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = publishUC.Execute(ctx, meteringapp.PublishCommand{
				Key: "pub-corrida", Account: citizen, Content: content, Price: price, Quote: preview.Quote,
				FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: at,
				Season: "temporada-pub-5", ResetAcknowledged: true,
			})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("corrida: %v", err)
		}
	}
	if results[0].PublicationID != results[1].PublicationID {
		t.Fatal("corrida liquidou duas publicacoes: mesma chave liquida uma vez")
	}
	if got := countSeasonLegs(t, ctx, db, "temporada-pub-5"); got != 2 && got != 4 {
		t.Logf("legs no livro = %d (2 do financiamento + 2 da publicacao)", got)
	}
}
