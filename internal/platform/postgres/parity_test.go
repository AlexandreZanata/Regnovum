package postgres_test

// P25-T03 — paridade repository/domínio sobre PostgreSQL descartável real.
//
// Três propriedades, três testes, um banco por teste:
//   - valores: NULL, UTC (fuso, nanos e monotônico), bigint no limite,
//     minor units com moeda ISO, UUID e texto Unicode sobrevivem ao
//     round-trip com o valor canônico;
//   - corrupção: a linha que o schema aceita e o domínio recusa falha alto
//     na leitura (nunca valor zero silencioso);
//   - erros: UNIQUE/FK/CHECK viram o erro de aplicação documentado, e a
//     mensagem nunca carrega detalhe do driver.
//
// O schema não usa NUMERIC: dinheiro é minor units em bigint com moeda ISO
// (decisão do plano), então a precisão é travada pelo bigint exato.

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenasrepo "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	identityrepo "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	positionsrepo "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	profilesrepo "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// driverLeakMarkers é o vocabulário do driver que nenhuma mensagem de erro
// de aplicação pode carregar: SQLSTATE, detalhe cru do PostgreSQL e nomes
// físicos (constraint, tabela, coluna) pertencem ao log do operador, nunca
// à resposta que atravessa a fronteira do adapter.
var driverLeakMarkers = []string{
	"SQLSTATE", "pq:", "pgconn", "duplicate key", "violates",
	"constraint", "DETAIL", "HINT", "relation \"", "column \"",
}

func driverLeakMarker(message string) string {
	for _, marker := range driverLeakMarkers {
		if strings.Contains(message, marker) {
			return marker
		}
	}
	return ""
}

func assertNoDriverLeak(t *testing.T, context string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want an application error, got nil", context)
	}
	if marker := driverLeakMarker(err.Error()); marker != "" {
		t.Fatalf("%s: application error leaks the driver (%q in %q)", context, marker, err.Error())
	}
}

func TestDriverLeakMarkerRefusesRawDriverErrors(t *testing.T) {
	t.Parallel()
	raw := `ERROR: duplicate key value violates unique constraint "accounts_email_key" (SQLSTATE 23505)`
	if marker := driverLeakMarker(raw); marker == "" {
		t.Fatal("raw driver error was accepted as clean")
	}
	for _, clean := range []string{
		"application: account with this email address already exists",
		"positions: position version conflict",
		"stored timezone is invalid: time: unknown time zone Narnia/Imaginaria",
		"",
	} {
		if marker := driverLeakMarker(clean); marker != "" {
			t.Fatalf("application error %q refused as leak (%q)", clean, marker)
		}
	}
}

// parityAccount inserts a minimal account through raw SQL and returns its
// identifier as text. Status is explicit so eligibility-gated surfaces can
// reuse the row.
func parityAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email, status string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, $2) RETURNING id::text`, email, status).Scan(&id); err != nil {
		t.Fatalf("seed account %s: %v", email, err)
	}
	return id
}

// parityArenaDraft inserts a minimal draft arena and returns its identifier.
func parityArenaDraft(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creatorID, statement string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1::uuid, $2, 'technology', 'pt-BR', 'draft')
		RETURNING id::text`, creatorID, statement).Scan(&id); err != nil {
		t.Fatalf("seed arena draft: %v", err)
	}
	return id
}

func parityEligibleAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) string {
	t.Helper()
	id := parityAccount(t, ctx, pool, email, "active")
	if _, err := pool.Exec(ctx, `UPDATE app.accounts SET email_verified_at = now() WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("verify account %s: %v", email, err)
	}
	return id
}

func TestRepositoryParityValueRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := db.Pool.Pool()

	accountID := parityAccount(t, ctx, pool, "t03-values@arena.example.com", "pending")
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.profiles (account_id, username, username_normalized) VALUES ($1::uuid, 't03null', 't03null')`, accountID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_accounts (account_id) VALUES ($1::uuid)`, accountID); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	arenaID := parityArenaDraft(t, ctx, pool, accountID, "Afirmação t03 para paridade de valores")

	// NULL sobrevive como NULL, nunca como zero.
	var timezone *string
	if err := pool.QueryRow(ctx, `SELECT timezone FROM app.profiles WHERE account_id = $1::uuid`, accountID).Scan(&timezone); err != nil {
		t.Fatalf("read null timezone: %v", err)
	}
	if timezone != nil {
		t.Fatalf("timezone = %q, want NULL", *timezone)
	}
	var closesAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT closes_at FROM app.arenas WHERE id = $1::uuid`, arenaID).Scan(&closesAt); err != nil {
		t.Fatalf("read null closes_at: %v", err)
	}
	if closesAt != nil {
		t.Fatalf("closes_at = %v, want NULL", *closesAt)
	}

	// UTC: fuso, nanos e monotônico se perdem no timestamptz; o instante
	// sobrevive ao microssegundo e a leitura volta em UTC.
	probe := time.Now().Add(-time.Hour).In(time.FixedZone("T03", -3*3600))
	if _, err := pool.Exec(ctx, `UPDATE app.accounts SET updated_at = $1 WHERE id = $2::uuid`, probe, accountID); err != nil {
		t.Fatalf("write timestamptz: %v", err)
	}
	var back time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM app.accounts WHERE id = $1::uuid`, accountID).Scan(&back); err != nil {
		t.Fatalf("read timestamptz: %v", err)
	}
	wantInstant := probe.UTC().Truncate(time.Microsecond)
	if !back.Equal(wantInstant) {
		t.Fatalf("timestamptz round-trip = %v, want %v", back, wantInstant)
	}
	// O driver entrega na zona local; a fronteira normaliza com .UTC()
	// (timeFromPg): a normalização é total sobre o instante preservado.
	if !back.UTC().Equal(wantInstant) {
		t.Fatalf("timestamptz UTC normalization = %v, want %v", back.UTC(), wantInstant)
	}

	// bigint no limite é exato (minor units usam bigint: precisão por construção).
	if _, err := pool.Exec(ctx, `UPDATE app.wallet_accounts SET balance_free = $1 WHERE account_id = $2::uuid`,
		math.MaxInt64, accountID); err != nil {
		t.Fatalf("write bigint limit: %v", err)
	}
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance_free FROM app.wallet_accounts WHERE account_id = $1::uuid`, accountID).Scan(&balance); err != nil {
		t.Fatalf("read bigint limit: %v", err)
	}
	if balance != math.MaxInt64 {
		t.Fatalf("bigint round-trip = %d, want %d", balance, int64(math.MaxInt64))
	}

	// Minor units viajam com a moeda ISO (o schema trava os pares
	// market/moeda: BR/BRL e INTERNATIONAL/USD; checkout exige amount > 0,
	// então o limite inferior testável é 1). O intent exige customer
	// Stripe: criado via SQL direto para o par (account, modo).
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ($1::uuid, 'cus_t03parity0001', false)`,
		accountID); err != nil {
		t.Fatalf("seed stripe customer: %v", err)
	}
	for _, money := range []struct {
		market   string
		amount   int64
		currency string
	}{
		{"BR", 1099, "BRL"},
		{"INTERNATIONAL", 1, "USD"},
	} {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.checkout_intents (account_id, market, product_id, catalog_version, currency, amount_minor, livemode)
			VALUES ($1::uuid, $2, 't03_probe', 1, $3, $4, false)
			RETURNING id::text`,
			accountID, money.market, money.currency, money.amount).Scan(&id); err != nil {
			t.Fatalf("seed checkout intent: %v", err)
		}
		var gotAmount int64
		var gotCurrency string
		if err := pool.QueryRow(ctx, `SELECT amount_minor, currency FROM app.checkout_intents WHERE id = $1::uuid`,
			id).Scan(&gotAmount, &gotCurrency); err != nil {
			t.Fatalf("read checkout intent: %v", err)
		}
		if gotAmount != money.amount || gotCurrency != money.currency {
			t.Fatalf("money round-trip = (%d, %q), want (%d, %q)", gotAmount, gotCurrency, money.amount, money.currency)
		}
	}

	// UUID gerado e UUID literal com todos os nibbles sobrevivem.
	fixedUUID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	var fixedID string
	if err := pool.QueryRow(ctx, `INSERT INTO app.accounts (id, email, status) VALUES ($1::uuid, 't03-uuid@arena.example.com', 'pending') RETURNING id::text`,
		fixedUUID).Scan(&fixedID); err != nil {
		t.Fatalf("seed fixed uuid account: %v", err)
	}
	if fixedID != fixedUUID {
		t.Fatalf("uuid round-trip = %q, want %q", fixedID, fixedUUID)
	}

	// Texto Unicode sobrevive byte a byte.
	statement := "Arguição t03: ação, reação e reflexão — 日本語テスト 🗳️ café\u0301 naïve rtl: مرحبا"
	unicodeArena := parityArenaDraft(t, ctx, pool, accountID, statement)
	var backStatement string
	if err := pool.QueryRow(ctx, `SELECT statement FROM app.arenas WHERE id = $1::uuid`, unicodeArena).Scan(&backStatement); err != nil {
		t.Fatalf("read unicode statement: %v", err)
	}
	if backStatement != statement {
		t.Fatalf("unicode round-trip = %q, want %q", backStatement, statement)
	}
}

func TestRepositoryParityCorruptionFailsLoud(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := db.Pool.Pool()

	// O schema aceita qualquer timezone não-vazia ≤64; o domínio só IANA.
	// A leitura deve recusar a linha, nunca devolver timezone zero.
	accountID := parityEligibleAccount(t, ctx, pool, "t03-corrupt@arena.example.com")
	profiles := profilesrepo.NewRepository(pool)
	username, err := profilesdomain.ParseUsername("t03corr")
	if err != nil {
		t.Fatalf("ParseUsername: %v", err)
	}
	locale, err := profilesdomain.ParseLocale("pt-BR")
	if err != nil {
		t.Fatalf("ParseLocale: %v", err)
	}
	if _, err := profiles.CreateProfileWithUsernameHistory(ctx, profilesdomain.AccountID(accountID), username, locale, time.Now().UTC()); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.profiles SET timezone = 'Narnia/Imaginaria' WHERE account_id = $1::uuid`, accountID); err != nil {
		t.Fatalf("corrupt timezone: %v", err)
	}
	_, err = profiles.GetProfileByAccountID(ctx, profilesdomain.AccountID(accountID))
	if err == nil || !strings.Contains(err.Error(), "stored timezone is invalid") {
		t.Fatalf("corrupt timezone read = %v, want a loud stored-timezone refusal", err)
	}
	assertNoDriverLeak(t, "corrupt timezone", err)

	// updated_at anterior a created_at passa no schema e quebra o
	// invariante do job: a leitura deve recusar a linha incoerente.
	jobs := jobsrepo.NewRepository(pool)
	now := time.Now().UTC()
	enqueued, _, err := jobs.Enqueue(ctx, jobsapp.EnqueueRecord{
		Type:        jobsdomain.TypeEmailDelivery,
		Version:     1,
		Payload:     []byte(`{"account_id":"t03corr"}`),
		AvailableAt: now,
		MaxAttempts: 3,
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.jobs SET updated_at = created_at - interval '1 second' WHERE id = $1::uuid`, enqueued.ID); err != nil {
		t.Fatalf("corrupt job timestamps: %v", err)
	}
	_, err = jobs.JobByID(ctx, enqueued.ID)
	if err == nil || !strings.Contains(err.Error(), "is incoherent") {
		t.Fatalf("corrupt job read = %v, want a loud incoherence refusal", err)
	}
	assertNoDriverLeak(t, "corrupt job", err)
}

func TestRepositoryParityConstraintMapping(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()

	// UNIQUE violado vira o erro de aplicação documentado (identity).
	identity := identityrepo.NewRepository(pool)
	email, err := identitydomain.ParseEmail("t03-dup@arena.example.com")
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	if _, err := identity.CreateAccountWithPassword(ctx, email, "t03-not-a-real-hash"); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	_, err = identity.CreateAccountWithPassword(ctx, email, "t03-not-a-real-hash")
	if !errors.Is(err, identityapp.ErrDuplicateEmail) {
		t.Fatalf("duplicate email = %v, want ErrDuplicateEmail", err)
	}
	assertNoDriverLeak(t, "duplicate email", err)

	// Versão da cadeia disputada vira conflito de versão (positions).
	positions := positionsrepo.NewRepository(pool)
	positionAccount := parityAccount(t, ctx, pool, "t03-pos@arena.example.com", "active")
	positionArena := parityArenaDraft(t, ctx, pool, positionAccount, "Afirmação t03 para posições")
	arenaID, err := positionsdomain.ParseArenaID(positionArena)
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	accountID, err := positionsdomain.ParseAccountID(positionAccount)
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	agree, err := positionsdomain.ParsePosition("agree")
	if err != nil {
		t.Fatalf("ParsePosition agree: %v", err)
	}
	disagree, err := positionsdomain.ParsePosition("disagree")
	if err != nil {
		t.Fatalf("ParsePosition disagree: %v", err)
	}
	at := time.Now().UTC()
	if _, _, err := positions.ConfirmInitialPosition(ctx, arenaID, accountID, agree, at); err != nil {
		t.Fatalf("confirm initial position: %v", err)
	}
	change, err := positionsdomain.NewPositionChange(arenaID, accountID, agree, disagree, 2, at)
	if err != nil {
		t.Fatalf("NewPositionChange: %v", err)
	}
	if _, err := positions.CreatePositionChange(ctx, change); err != nil {
		t.Fatalf("seed position change: %v", err)
	}
	_, err = positions.CreatePositionChange(ctx, change)
	if !errors.Is(err, positionsapp.ErrVersionConflict) {
		t.Fatalf("duplicate chain version = %v, want ErrVersionConflict", err)
	}
	assertNoDriverLeak(t, "duplicate chain version", err)

	// Slug publicado duas vezes vira conflito de slug (arenas).
	arenas := arenasrepo.NewRepository(pool)
	arenaAccount := parityAccount(t, ctx, pool, "t03-arenas@arena.example.com", "active")
	creator := arenasdomain.CreatorID(arenaAccount)
	publish := func(statement, slug string) error {
		policy := arenasdomain.DefaultStatementPolicy()
		parsedStatement, err := arenasdomain.ParseStatement(statement, policy)
		if err != nil {
			t.Fatalf("ParseStatement: %v", err)
		}
		category, err := arenasdomain.ParseCategory("technology")
		if err != nil {
			t.Fatalf("ParseCategory: %v", err)
		}
		language, err := arenasdomain.ParseLanguage("pt-BR")
		if err != nil {
			t.Fatalf("ParseLanguage: %v", err)
		}
		draft, err := arenas.CreateArena(ctx, arenasapp.CreateArenaRequest{
			CreatorID: creator,
			Statement: parsedStatement,
			Category:  category,
			Language:  language,
		})
		if err != nil {
			t.Fatalf("seed draft: %v", err)
		}
		parsedSlug, err := arenasdomain.ParseSlug(slug)
		if err != nil {
			t.Fatalf("ParseSlug: %v", err)
		}
		_, err = arenas.PublishArenaDraft(ctx, draft.ID(), creator, parsedSlug, time.Now().UTC(), draft.Version())
		return err
	}
	if err := publish("Primeira afirmação t03", "t03-dup-slug"); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	err = publish("Segunda afirmação t03", "t03-dup-slug")
	if !errors.Is(err, arenasapp.ErrSlugConflict) {
		t.Fatalf("duplicate slug = %v, want ErrSlugConflict", err)
	}
	assertNoDriverLeak(t, "duplicate slug", err)

	// Username disputado vira erro de aplicação (profiles).
	profiles := profilesrepo.NewRepository(pool)
	firstAccount := parityEligibleAccount(t, ctx, pool, "t03-prof-1@arena.example.com")
	secondAccount := parityEligibleAccount(t, ctx, pool, "t03-prof-2@arena.example.com")
	locale, err := profilesdomain.ParseLocale("pt-BR")
	if err != nil {
		t.Fatalf("ParseLocale: %v", err)
	}
	taken, err := profilesdomain.ParseUsername("t03taken")
	if err != nil {
		t.Fatalf("ParseUsername: %v", err)
	}
	if _, err := profiles.CreateProfileWithUsernameHistory(ctx, profilesdomain.AccountID(firstAccount), taken, locale, time.Now().UTC()); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	_, err = profiles.CreateProfileWithUsernameHistory(ctx, profilesdomain.AccountID(secondAccount), taken, locale, time.Now().UTC())
	if !errors.Is(err, profilesapp.ErrUsernameTaken) {
		t.Fatalf("duplicate username = %v, want ErrUsernameTaken", err)
	}
	assertNoDriverLeak(t, "duplicate username", err)
}
