package postgres_test

// P46-T08 — Migalhas e métricas sem herança econômica.
//
// Identidade de época (season_id, iso_week), elegibilidade por
// pessoa por temporada, R4 só de semanas completas internas e
// refluxo/estoque por livro. Refluxo filtra pelo livro original:
// publicação, Dízimo e refunds contam no livro do original, nunca
// no vizinho; refund tardio do livro antigo não alimenta o novo;
// selo mantém leitura do arquivo; replay em outro livro resolve
// lá. Produto econômico segue desativado: temporadas são fixtures,
// sem wiring.

import (
	"context"
	"errors"
	"testing"
	"time"

	crumbspg "github.com/AlexandreZanata/Regnovum/internal/crumbs/adapters/postgres"
	crumbsdomain "github.com/AlexandreZanata/Regnovum/internal/crumbs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func seasonCrumbsCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func seedCrumbSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int) {
	t.Helper()
	starts := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3::timestamptz, $3::timestamptz + make_interval(secs => 7776000), 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica',
		 '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`,
		key, ordinal, starts.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
}

func sealCrumbSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) {
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

func crumbCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label, season string) string {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, kind, label, season); err != nil {
		t.Fatalf("provision custody %s/%s/%s: %v", kind, label, season, err)
	}
	var id string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2 AND season_key = $3`, kind, label, season).Scan(&id); err != nil {
		t.Fatalf("read custody %s/%s/%s: %v", kind, label, season, err)
	}
	return id
}

func crumbTransfer(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("mint transfer: %v", err)
	}
	return id
}

func crumbLeg(t *testing.T, ctx context.Context, db *dbtest.TestDB, transfer, custody, direction string, amount int64, season string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, transfer, custody, direction, amount, season); err != nil {
		t.Fatalf("record leg: %v", err)
	}
}

// seasonPubOrder bundles one seasonal publication seed: a single
// struct keeps the helper under the parameter budget.
type seasonPubOrder struct {
	key    string
	holder string
	amount int64
	posted time.Time
	season string
}

func seedSeasonPub(t *testing.T, ctx context.Context, db *dbtest.TestDB, order seasonPubOrder) {
	t.Helper()
	transfer := crumbTransfer(t, ctx, db)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id, posted_at, season_key)
		 VALUES ($1, $2, 'reflux-service', 1, 10, $3, 'content-seal', 'quote-seal', 'payload-seal', $4::uuid, $5, $6)`,
		order.key, order.holder, order.amount, transfer, order.posted, order.season); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	crumbLeg(t, ctx, db, transfer, crumbCustody(t, ctx, db, "user", order.holder, order.season), "debit", order.amount, order.season)
	crumbLeg(t, ctx, db, transfer, crumbCustody(t, ctx, db, "treasury", "main", order.season), "credit", order.amount, order.season)
}

// seasonRefundOrder bundles one metering refund seed linked to its
// cause publication.
type seasonRefundOrder struct {
	key      string
	holder   string
	original string
	amount   int64
	posted   time.Time
	season   string
}

func seedSeasonMeteringRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, order seasonRefundOrder) {
	t.Helper()
	transfer := crumbTransfer(t, ctx, db)
	var cause string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.metering_publications WHERE intention_key = $1`, order.original).Scan(&cause); err != nil {
		t.Fatalf("read cause: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason, posted_at)
		 VALUES ($1, $2, $3::uuid, $4, $5::uuid, 'refluxo tardio', $6)`,
		order.key, order.holder, cause, order.amount, transfer, order.posted); err != nil {
		t.Fatalf("seed metering refund: %v", err)
	}
	crumbLeg(t, ctx, db, transfer, crumbCustody(t, ctx, db, "treasury", "main", order.season), "debit", order.amount, order.season)
	crumbLeg(t, ctx, db, transfer, crumbCustody(t, ctx, db, "user", order.holder, order.season), "credit", order.amount, order.season)
}

func checkSeasonNet(t *testing.T, ctx context.Context, repo *crumbspg.RefluxRepository, season crumbsdomain.SeasonKey, start, end time.Time, wantServices, wantBack int64) {
	t.Helper()
	got, err := repo.RegularRefluxForSeason(ctx, season, start, end)
	if err != nil {
		t.Fatalf("RegularRefluxForSeason(%s): %v", season, err)
	}
	if got.ServiceCharges != wantServices || got.MeteringReversals != wantBack {
		t.Fatalf("book %s services/back = %d/%d, want %d/%d", season, got.ServiceCharges, got.MeteringReversals, wantServices, wantBack)
	}
	if want := wantServices - wantBack; got.Net != want {
		t.Fatalf("book %s net = %d, want %d", season, got.Net, want)
	}
}

// TestSeasonalRefluxIsolatesBooks prova o refluxo por livro: a mesma
// janela soma só o livro pedido; refund tardio do livro antigo não
// alimenta o novo; arquivo selado continua legível; livro
// desconhecido recusa antes de qualquer leitura.
func TestSeasonalRefluxIsolatesBooks(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonCrumbsCtx()
	defer cancel()

	seedCrumbSeason(t, ctx, db, "temporada-migalha-a", 91)
	seedCrumbSeason(t, ctx, db, "temporada-migalha-b", 92)

	now := time.Now().UTC()
	epoch := crumbsdomain.EpochOf(now)
	w0start, err := epoch.Start()
	if err != nil {
		t.Fatalf("epoch start: %v", err)
	}
	w0end, err := epoch.End()
	if err != nil {
		t.Fatalf("epoch end: %v", err)
	}
	at := func(weeksBack int, offset time.Duration) time.Time {
		return w0start.AddDate(0, 0, -7*weeksBack).Add(offset)
	}

	repo, err := crumbspg.NewRefluxRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewRefluxRepository: %v", err)
	}

	seasonA := crumbsdomain.SeasonKey("temporada-migalha-a")
	seasonB := crumbsdomain.SeasonKey("temporada-migalha-b")

	// Mesma janela, dois livros: cada publicação conta só no seu.
	seedSeasonPub(t, ctx, db, seasonPubOrder{key: "migalha-a-1", holder: "titular-a", amount: 10000, posted: at(1, time.Hour), season: "temporada-migalha-a"})
	seedSeasonPub(t, ctx, db, seasonPubOrder{key: "migalha-b-1", holder: "titular-b", amount: 5000, posted: at(1, 2*time.Hour), season: "temporada-migalha-b"})
	checkSeasonNet(t, ctx, repo, seasonA, at(1, 0), at(0, 0), 10000, 0)
	checkSeasonNet(t, ctx, repo, seasonB, at(1, 0), at(0, 0), 5000, 0)

	// Replay em outro livro resolve lá: mesma chave em B conta em B,
	// sem redirecionar A.
	seedSeasonPub(t, ctx, db, seasonPubOrder{key: "migalha-a-1", holder: "titular-b", amount: 7000, posted: at(1, 3*time.Hour), season: "temporada-migalha-b"})
	checkSeasonNet(t, ctx, repo, seasonA, at(1, 0), at(0, 0), 10000, 0)
	checkSeasonNet(t, ctx, repo, seasonB, at(1, 0), at(0, 0), 12000, 0)

	// Refund tardio do livro antigo não alimenta o novo: causa em A
	// com postagem na janela viva conta no livro original, nunca em B.
	seedSeasonMeteringRefund(t, ctx, db, seasonRefundOrder{key: "migalha-r-1", holder: "titular-a", original: "migalha-a-1", amount: 3000, posted: at(0, time.Hour), season: "temporada-migalha-a"})
	checkSeasonNet(t, ctx, repo, seasonB, w0start, w0end, 0, 0)
	checkSeasonNet(t, ctx, repo, seasonA, w0start, w0end, 0, 3000)

	// Arquivo selado continua legível: selo preserva história.
	sealCrumbSeason(t, ctx, db, "temporada-migalha-a")
	checkSeasonNet(t, ctx, repo, seasonA, at(1, 0), at(0, 0), 10000, 0)

	// Livro desconhecido e janela malformada recusam antes de ler.
	if _, err := repo.RegularRefluxForSeason(ctx, crumbsdomain.SeasonKey("temporada-fantasma"), at(1, 0), at(0, 0)); !errors.Is(err, crumbsdomain.ErrInvalidNewcomer) {
		t.Fatalf("unknown book = %v, want ErrInvalidNewcomer", err)
	}
	if _, err := repo.RegularRefluxForSeason(ctx, seasonA, w0start, w0start); !errors.Is(err, crumbsdomain.ErrInvalidReflux) {
		t.Fatalf("empty window = %v, want ErrInvalidReflux", err)
	}
	if _, err := repo.RegularRefluxForSeason(ctx, crumbsdomain.SeasonKey(""), at(1, 0), at(0, 0)); !errors.Is(err, crumbsdomain.ErrInvalidNewcomer) {
		t.Fatalf("empty book = %v, want ErrInvalidNewcomer", err)
	}
}
