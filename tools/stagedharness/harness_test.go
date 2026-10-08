package stagedharness_test

// P56-T02 — the isolated staged browser harness: the four staged HTTP
// handlers (seasons, metering, commerce, disputes) mounted together on
// one local mux, with real PostgreSQL behind seasons/metering/commerce,
// in-memory case records behind disputes, and synthetic accounts and
// seasons only.
//
// What this proves, and what it does not:
//
//   - every one of the 15 staged METHOD+path pairs reaches its real
//     handler here (a 200 JSON document with the handler's keys), so
//     the four route sets compose without pattern conflicts — a
//     conflicting Go 1.22 pattern would panic the ServeMux at
//     registration, before any assertion;
//   - the same 15 pairs stay unreachable on the delivered process: the
//     negative half lives in tools/e2e/specs/staged.spec.js and in
//     isolation_test.go, and nothing here imports cmd/ or bootstrap,
//     adds an env flag, or mounts anything outside this test.
//
// Seeding mirrors the per-module suites (same tables, same catalog
// shape, same lifecycle states); it is duplicated on purpose so this
// harness never depends on unexported helpers of another package's
// tests.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	commercehttp "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/http"
	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	disputeshttp "github.com/AlexandreZanata/Regnovum/internal/disputes/adapters/http"
	disputesapp "github.com/AlexandreZanata/Regnovum/internal/disputes/application"
	disputesdomain "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	meteringhttp "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/http"
	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	seasonshttp "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/http"
	seasonpg "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/postgres"
	seasonapp "github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	stagedSeasonOwnerToken   = "staged-season-owner-token"
	stagedSeasonOtherToken   = "staged-season-other-token"
	stagedMeteringOwnerToken = "staged-metering-owner-token"
	stagedMeteringOtherToken = "staged-metering-other-token"
	stagedCommerceOwnerToken = "staged-commerce-owner-token"
	stagedCommerceOtherToken = "staged-commerce-other-token"
	stagedClaimantToken      = "staged-dispute-claimant-token"
	stagedRespondentToken    = "staged-dispute-respondent-token"
	stagedStrangerToken      = "staged-dispute-stranger-token"
)

const (
	stagedCurrentSeason  = "temporada-harness-b"
	stagedArchivedSeason = "temporada-harness-a"
)

func mustStagedSecurity(t *testing.T) *security.Manager {
	t.Helper()
	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}
	return secMgr
}

func stagedAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB, label string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1 || '-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`,
		label).Scan(&id); err != nil {
		t.Fatalf("create %s account: %v", label, err)
	}
	return id
}

func seedStagedSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int, starts time.Time, stages [][2]string) {
	t.Helper()
	manifest, err := seasondomain.NewManifest(seasondomain.ManifestRequest{
		ID: key, Ordinal: ordinal, StartsAt: starts.UTC(),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	ends := manifest.StartsAt.Add(7776000 * time.Second)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3, $4, 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica', $5)`,
		key, ordinal, manifest.StartsAt, ends, manifest.Hash); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
	for _, stage := range stages {
		from := "NULL"
		if stage[0] != "" {
			from = "'" + stage[0] + "'"
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
			 VALUES ($1, `+from+`, $2, now())`, key, stage[1]); err != nil {
			t.Fatalf("stage %s of %s: %v", stage[1], key, err)
		}
	}
}

// stubStagedChampions serves one fixed redacted champions document for
// the harness current season, exactly like the per-module stub: exact
// wealth never leaves, unknown books refuse with 404.
type stubStagedChampions struct{}

func (stubStagedChampions) GetChampions(_ context.Context, _, seasonKey, locale string, _, _ bool) (seasonapp.ChampionsView, error) {
	if seasonKey != stagedCurrentSeason {
		return seasonapp.ChampionsView{}, seasondomain.ErrSeasonUnknown
	}
	titles := seasondomain.ChampionTitlesFor(seasondomain.SeasonLocale(locale))
	if locale != "pt" && locale != "en" {
		titles = seasondomain.ChampionTitlesFor(seasondomain.SeasonLocaleEnglish)
	}
	return seasonapp.ChampionsView{
		Season: stagedCurrentSeason, CutoffRevision: 7, Hash: "harness",
		Version: 1, LastKing: "alias-reservado",
		Leaders: []seasonapp.ExportedLeader{
			{Subject: "ana", Display: "coruja-azul"},
		},
		RichestTitle: titles.Richest, LastKingTitle: titles.LastKing, HistoryTitle: titles.History,
	}, nil
}

type stagedClock struct{ now time.Time }

func (c stagedClock) Now() time.Time { return c.now }

var _ ports.Clock = stagedClock{}

func stagedMeteringCatalog(t *testing.T, at time.Time) meteringdomain.Catalog {
	t.Helper()
	service, _ := meteringdomain.ParseServiceID("argument-publish")
	price, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
		PriceMilli: 250, Unit: meteringdomain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	var catalog meteringdomain.Catalog
	if err := catalog.Add(price); err != nil {
		t.Fatalf("Add price: %v", err)
	}
	return catalog
}

func fundStagedCitizen(t *testing.T, ctx context.Context, db *dbtest.TestDB, genesisKey string, funds int64, citizen string) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	key, err := economydomain.ParseGenesisKey(genesisKey)
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: key, Season: economydomain.SeasonKey(economydomain.CompatSeasonKey)}); err != nil {
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) {
			t.Fatalf("seed Genesis: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, citizen); err != nil {
		t.Fatalf("create custody: %v", err)
	}
	amount, _ := economydomain.NewMilliInk(funds)
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromSeason: economydomain.SeasonKey(economydomain.CompatSeasonKey),
		FromKind:   fromKind, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(economydomain.CompatSeasonKey),
		ToKind:   toKind, ToLabel: citizen, Amount: amount,
	}); err != nil {
		t.Fatalf("fund citizen: %v", err)
	}
}

// stagedMemoryCases is the disputes record store: memory only, like the
// per-module suite — persistent disputes storage is P56-T03, and this
// harness must not mask its absence with a fixture that pretends to
// persist.
type stagedMemoryCases struct {
	files map[string]disputesapp.CaseRecord
}

func (m *stagedMemoryCases) Get(key string) (disputesapp.CaseRecord, error) {
	record, ok := m.files[key]
	if !ok {
		return disputesapp.CaseRecord{}, disputesdomain.ErrUnknownCase
	}
	return record, nil
}

func (m *stagedMemoryCases) Put(record disputesapp.CaseRecord) error {
	m.files[record.Proposal.Key] = record
	return nil
}

func stagedPropose(t *testing.T, key string, deadline time.Time) disputesdomain.Proposal {
	t.Helper()
	proposal, err := disputesdomain.Propose(disputesdomain.ProposalRequest{
		Key: key, Version: 1, Object: "entrega do lote 7",
		Claimant: "requerente", Respondent: "requerida", ValueMilli: 20000,
		EscrowRef: "caucao-" + key, Rite: "rito-acordo-v1", Evidence: "regras-prova-v1",
		Costs: disputesdomain.CostsSplit, ExpiresAt: deadline, Execution: "liberar caucao ao adimplente",
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return proposal
}

type stagedHarness struct {
	mux        http.Handler
	sec        *security.Manager
	seasonArch string
	commerceID string
}

// stagedBuilder carries the shared composition state while each
// staged module is set up by its own function: one disposable
// database, one clock-bearing context, one security manager and the
// session map every handler authenticates against.
type stagedBuilder struct {
	t      *testing.T
	ctx    context.Context
	db     *dbtest.TestDB
	pool   *pgxpool.Pool
	sec    *security.Manager
	tokens map[string]security.AuthIdentity
}

func (b *stagedBuilder) identify(token, account string) {
	b.tokens[token] = security.AuthIdentity{AccountID: account, SessionID: "session-" + token}
}

func newStagedHarness(t *testing.T) *stagedHarness {
	t.Helper()
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b := &stagedBuilder{
		t: t, ctx: ctx, db: testDB, pool: testDB.Pool.Pool(),
		sec: mustStagedSecurity(t), tokens: map[string]security.AuthIdentity{},
	}
	seasonsHandler := b.setupSeasons()
	meteringHandler := b.setupMetering()
	commerceHandler, contractID := b.setupCommerce()
	disputesHandler := b.setupDisputes()
	// One mux for all four handlers: a conflicting pattern panics
	// here, which is itself the composition proof — staged routes
	// share the /api/v1/me namespace and must not shadow each other.
	mux := http.NewServeMux()
	seasonsHandler.RegisterRoutes(mux)
	meteringHandler.RegisterRoutes(mux)
	commerceHandler.RegisterRoutes(mux)
	disputesHandler.RegisterRoutes(mux)
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		identity, ok := b.tokens[rawToken]
		if !ok {
			return security.AuthIdentity{}, errors.New("unknown session")
		}
		return identity, nil
	})
	return &stagedHarness{
		mux: b.sec.AuthenticateMiddleware(validator)(mux), sec: b.sec,
		seasonArch: stagedArchivedSeason, commerceID: contractID,
	}
}

func (b *stagedBuilder) setupSeasons() *seasonshttp.Handler {
	b.t.Helper()

	// Seasons: one archived book plus the current ACTIVE book.
	seasonOwner := stagedAccount(b.t, b.ctx, b.db, "staged-season-owner")
	seasonOther := stagedAccount(b.t, b.ctx, b.db, "staged-season-other")
	b.identify(stagedSeasonOwnerToken, seasonOwner)
	b.identify(stagedSeasonOtherToken, seasonOther)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	seedStagedSeason(b.t, b.ctx, b.db, stagedArchivedSeason, 311, base,
		[][2]string{{"", "prepared"}, {"prepared", "active"}, {"active", "closing"}, {"closing", "sealed"}, {"sealed", "archived"}})
	seedStagedSeason(b.t, b.ctx, b.db, stagedCurrentSeason, 312, base.Add(7776000*time.Second),
		[][2]string{{"", "prepared"}, {"prepared", "active"}})
	seasonReads, err := seasonpg.NewSeasonReader(b.pool)
	if err != nil {
		b.t.Fatalf("NewSeasonReader: %v", err)
	}
	seasonsHandler, err := seasonshttp.NewHandler(seasonshttp.HandlerConfig{
		Reads: seasonReads, Champions: stubStagedChampions{}, Security: b.sec,
	})
	if err != nil {
		b.t.Fatalf("seasons NewHandler: %v", err)
	}

	return seasonsHandler
}

func (b *stagedBuilder) setupMetering() *meteringhttp.Handler {
	b.t.Helper()

	// Metering: two funded citizens and a priced catalog.
	meteringOwner := "staged-metering-owner"
	meteringOther := "staged-metering-other"
	b.identify(stagedMeteringOwnerToken, meteringOwner)
	b.identify(stagedMeteringOtherToken, meteringOther)
	fundStagedCitizen(b.t, b.ctx, b.db, "staged-metering-funding", 1000000, meteringOwner)
	fundStagedCitizen(b.t, b.ctx, b.db, "staged-metering-funding", 1000000, meteringOther)
	meteringAt := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	meteringCatalog := stagedMeteringCatalog(b.t, meteringAt)
	meteringClk := stagedClock{now: meteringAt.Add(time.Minute)}
	previewUC, err := meteringapp.NewPreviewUseCase(meteringCatalog, text.GraphemeCount)
	if err != nil {
		b.t.Fatalf("NewPreviewUseCase: %v", err)
	}
	publishRepo, err := meteringpg.NewRepository(b.pool, meteringClk, meteringCatalog)
	if err != nil {
		b.t.Fatalf("NewRepository: %v", err)
	}
	publishUC, err := meteringapp.NewPublishUseCase(publishRepo)
	if err != nil {
		b.t.Fatalf("NewPublishUseCase: %v", err)
	}
	meteringHandler, err := meteringhttp.NewHandler(meteringhttp.HandlerConfig{
		Preview: previewUC, Publish: publishUC, Reads: publishRepo,
		Clock: meteringClk, Security: b.sec,
		FromKind: "user", ToKind: "treasury", ToLabel: "main",
		MaxUnits: 3000, TTL: 30 * time.Minute,
	})
	if err != nil {
		b.t.Fatalf("metering NewHandler: %v", err)
	}

	return meteringHandler
}

func (b *stagedBuilder) setupCommerce() (*commercehttp.Handler, string) {
	b.t.Helper()

	// Commerce: one funded, accepted and released contract.
	buyer := stagedAccount(b.t, b.ctx, b.db, "staged-commerce-buyer")
	provider := stagedAccount(b.t, b.ctx, b.db, "staged-commerce-provider")
	b.identify(stagedCommerceOwnerToken, buyer)
	b.identify(stagedCommerceOtherToken, provider)
	fundStagedCitizen(b.t, b.ctx, b.db, "staged-commerce-funding", 100000, buyer)
	if _, err := b.pool.Exec(b.ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, provider); err != nil {
		b.t.Fatalf("create provider custody: %v", err)
	}
	escrows, err := commercepg.NewEscrowRepository(b.pool)
	if err != nil {
		b.t.Fatalf("NewEscrowRepository: %v", err)
	}
	fundUC, err := commerceapp.NewFundContractUseCase(escrows)
	if err != nil {
		b.t.Fatalf("NewFundContractUseCase: %v", err)
	}
	acceptUC, err := commerceapp.NewAcceptDeliveryUseCase(escrows)
	if err != nil {
		b.t.Fatalf("NewAcceptDeliveryUseCase: %v", err)
	}
	releaseUC, err := commerceapp.NewReleaseContractUseCase(escrows)
	if err != nil {
		b.t.Fatalf("NewReleaseContractUseCase: %v", err)
	}
	commerceNow := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	funded, err := fundUC.Execute(b.ctx, commerceapp.FundCommand{
		Key: "receipt-harness-1", Object: "serviço com recibo", Buyer: buyer, Provider: provider,
		AmountMill: 20000, ExpiresAt: commerceNow.Add(time.Hour), Now: commerceNow,
	})
	if err != nil {
		b.t.Fatalf("fund contract: %v", err)
	}
	if _, err := acceptUC.Execute(b.ctx, "receipt-harness-1", buyer); err != nil {
		b.t.Fatalf("accept delivery: %v", err)
	}
	if _, err := releaseUC.Execute(b.ctx, "receipt-harness-1", buyer); err != nil {
		b.t.Fatalf("release contract: %v", err)
	}
	commerceReads, err := commercepg.NewTradeReceiptRepository(b.pool)
	if err != nil {
		b.t.Fatalf("NewTradeReceiptRepository: %v", err)
	}
	commerceHandler, err := commercehttp.NewHandler(commercehttp.HandlerConfig{
		Reads: commerceReads, Security: b.sec,
	})
	if err != nil {
		b.t.Fatalf("commerce NewHandler: %v", err)
	}

	return commerceHandler, funded.ID
}

func (b *stagedBuilder) setupDisputes() *disputeshttp.Handler {
	b.t.Helper()

	// Disputes: memory records with a proposal case, a journey case
	// with an instructed hearing, and a ruled case the parties read
	// and appeal once.
	b.identify(stagedClaimantToken, "requerente")
	b.identify(stagedRespondentToken, "requerida")
	b.identify(stagedStrangerToken, "estranha")
	stores := &stagedMemoryCases{files: map[string]disputesapp.CaseRecord{}}
	disputeNow := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	disputeClk := stagedClock{now: disputeNow.Add(time.Hour)}
	proposal, err := stagedPropose(b.t, "caso-proposta", disputeNow.Add(48*time.Hour)).Accept("requerente", disputeNow)
	if err != nil {
		b.t.Fatalf("proposal first Accept: %v", err)
	}
	if err := stores.Put(disputesapp.CaseRecord{Proposal: proposal}); err != nil {
		b.t.Fatalf("store proposal: %v", err)
	}
	journeyBound, err := stagedPropose(b.t, "caso-jornada", disputeNow.Add(48*time.Hour)).Accept("requerente", disputeNow)
	if err != nil {
		b.t.Fatalf("journey first Accept: %v", err)
	}
	journeyBound, err = journeyBound.Accept("requerida", disputeNow)
	if err != nil {
		b.t.Fatalf("journey second Accept: %v", err)
	}
	journeyEntry, err := disputesdomain.OpenConsentCase(disputesdomain.CaseArbitration, journeyBound, "requerente", disputeNow)
	if err != nil {
		b.t.Fatalf("journey OpenConsentCase: %v", err)
	}
	journeyHearing, err := disputesdomain.OpenHearing(journeyEntry, journeyBound, "arbitro-1", disputeNow.Add(48*time.Hour))
	if err != nil {
		b.t.Fatalf("journey OpenHearing: %v", err)
	}
	if err := stores.Put(disputesapp.CaseRecord{
		Proposal: journeyBound, Entry: journeyEntry, Hearing: &journeyHearing,
	}); err != nil {
		b.t.Fatalf("store journey: %v", err)
	}
	ruledBound, err := stagedPropose(b.t, "caso-sentenca", disputeNow.Add(48*time.Hour)).Accept("requerente", disputeNow)
	if err != nil {
		b.t.Fatalf("ruled first Accept: %v", err)
	}
	ruledBound, err = ruledBound.Accept("requerida", disputeNow)
	if err != nil {
		b.t.Fatalf("ruled second Accept: %v", err)
	}
	ruledEntry, err := disputesdomain.OpenConsentCase(disputesdomain.CaseArbitration, ruledBound, "requerente", disputeNow)
	if err != nil {
		b.t.Fatalf("ruled OpenConsentCase: %v", err)
	}
	ruledHearing, err := disputesdomain.OpenHearing(ruledEntry, ruledBound, "arbitro-1", disputeNow.Add(20*time.Minute))
	if err != nil {
		b.t.Fatalf("ruled OpenHearing: %v", err)
	}
	ruledHearing, err = ruledHearing.SubmitEvidence("requerente", "prova-requerente", disputeNow.Add(10*time.Minute))
	if err != nil {
		b.t.Fatalf("ruled claimant evidence: %v", err)
	}
	ruledHearing, err = ruledHearing.SubmitEvidence("requerida", "prova-requerida", disputeNow.Add(10*time.Minute))
	if err != nil {
		b.t.Fatalf("ruled respondent evidence: %v", err)
	}
	decision, err := ruledHearing.Decide("arbitro-1", disputesdomain.VerdictUpholdClaimant, 10000,
		"aplica o termo aceito ao lote 7", disputeNow.Add(30*time.Minute), disputeNow.Add(48*time.Hour))
	if err != nil {
		b.t.Fatalf("ruled Decide: %v", err)
	}
	if err := stores.Put(disputesapp.CaseRecord{
		Proposal: ruledBound, Entry: ruledEntry, Hearing: &ruledHearing, Decision: &decision,
	}); err != nil {
		b.t.Fatalf("store ruled: %v", err)
	}
	acceptCaseUC, err := disputesapp.NewAcceptCaseUseCase(stores)
	if err != nil {
		b.t.Fatalf("NewAcceptCaseUseCase: %v", err)
	}
	defendCaseUC, err := disputesapp.NewDefendCaseUseCase(stores)
	if err != nil {
		b.t.Fatalf("NewDefendCaseUseCase: %v", err)
	}
	appealCaseUC, err := disputesapp.NewAppealCaseUseCase(stores)
	if err != nil {
		b.t.Fatalf("NewAppealCaseUseCase: %v", err)
	}
	readCaseUC, err := disputesapp.NewReadCaseFileUseCase(stores)
	if err != nil {
		b.t.Fatalf("NewReadCaseFileUseCase: %v", err)
	}
	disputesHandler, err := disputeshttp.NewHandler(disputeshttp.HandlerConfig{
		Accept: acceptCaseUC, Defend: defendCaseUC, Appeal: appealCaseUC, Read: readCaseUC,
		Clock: disputeClk, Security: b.sec, Logger: func(string) {},
	})
	if err != nil {
		b.t.Fatalf("disputes NewHandler: %v", err)
	}

	return disputesHandler
}

func stagedDo(t *testing.T, h *stagedHarness, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	if method == http.MethodPost {
		csrf, err := h.sec.CSRF().GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		request.AddCookie(&http.Cookie{Name: "arena_csrf", Value: csrf})
		request.Header.Set("X-CSRF-Token", csrf)
	}
	request.Header.Set("Accept-Language", "pt-BR")
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func stagedDocument(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want a JSON document: the request did not reach a staged handler", contentType)
	}
	var document map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, recorder.Body.String())
	}
	return document
}

// TestStagedHarnessReachesAllFifteenMethods drives every staged
// METHOD+path pair against the combined harness and requires the
// handler's own 200 document — not a mux miss, not a placeholder.
// The Content-Type assertion is the tripwire: an unmounted path
// answers Go's bare 404 (text/plain), while every staged handler
// answers JSON.
func TestStagedHarnessReachesAllFifteenMethods(t *testing.T) {
	h := newStagedHarness(t)

	seasons := []struct{ path string }{
		{"/api/v1/me/seasons/current"},
		{"/api/v1/me/seasons/history"},
		{"/api/v1/me/seasons/" + h.seasonArch},
		{"/api/v1/me/seasons/" + stagedCurrentSeason + "/champions"},
	}
	for _, target := range seasons {
		recorder := stagedDo(t, h, http.MethodGet, target.path, stagedSeasonOwnerToken, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body %s", target.path, recorder.Code, recorder.Body.String())
		}
		stagedDocument(t, recorder)
	}

	quote := stagedDo(t, h, http.MethodPost, "/api/v1/me/metering/quotes",
		stagedMeteringOwnerToken, `{"content":"texto final","service":"argument-publish"}`)
	if quote.Code != http.StatusOK {
		t.Fatalf("POST metering/quotes status = %d, body %s", quote.Code, quote.Body.String())
	}
	stagedDocument(t, quote)

	publication := stagedDo(t, h, http.MethodPost, "/api/v1/me/metering/publications",
		stagedMeteringOwnerToken, `{"intention_key":"harness-1","content":"texto final","service":"argument-publish"}`)
	if publication.Code != http.StatusOK {
		t.Fatalf("POST metering/publications status = %d, body %s", publication.Code, publication.Body.String())
	}
	settlement := stagedDocument(t, publication)
	publicationID, _ := settlement["publication_id"].(string)
	if publicationID == "" {
		t.Fatalf("settlement has no publication_id: %v", settlement)
	}

	receipt := stagedDo(t, h, http.MethodGet, "/api/v1/me/metering/publications/"+publicationID, stagedMeteringOwnerToken, "")
	if receipt.Code != http.StatusOK {
		t.Fatalf("GET metering receipt status = %d, body %s", receipt.Code, receipt.Body.String())
	}
	stagedDocument(t, receipt)

	statement := stagedDo(t, h, http.MethodGet, "/api/v1/me/metering/statement", stagedMeteringOwnerToken, "")
	if statement.Code != http.StatusOK {
		t.Fatalf("GET metering statement status = %d, body %s", statement.Code, statement.Body.String())
	}
	stagedDocument(t, statement)

	trade := stagedDo(t, h, http.MethodGet, "/api/v1/me/commerce/contracts/"+h.commerceID, stagedCommerceOwnerToken, "")
	if trade.Code != http.StatusOK {
		t.Fatalf("GET commerce receipt status = %d, body %s", trade.Code, trade.Body.String())
	}
	tradeDocument := stagedDocument(t, trade)
	if tradeDocument["contract_id"] != h.commerceID {
		t.Fatalf("trade receipt contract_id = %v, want %q", tradeDocument["contract_id"], h.commerceID)
	}

	extract := stagedDo(t, h, http.MethodGet, "/api/v1/me/commerce/statement", stagedCommerceOwnerToken, "")
	if extract.Code != http.StatusOK {
		t.Fatalf("GET commerce statement status = %d, body %s", extract.Code, extract.Body.String())
	}
	stagedDocument(t, extract)

	private := []struct {
		method string
		path   string
		token  string
		body   string
	}{
		{http.MethodGet, "/api/v1/me/disputes/cases/caso-proposta", stagedClaimantToken, ""},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-proposta/accepts", stagedRespondentToken, ""},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/defenses", stagedClaimantToken, `{"digest":"prova-harness-1"}`},
		{http.MethodGet, "/api/v1/me/disputes/cases/caso-sentenca/ruling", stagedClaimantToken, ""},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-sentenca/appeals", stagedRespondentToken, `{"reason":"o valor apurado diverge do termo"}`},
	}
	for _, target := range private {
		recorder := stagedDo(t, h, target.method, target.path, target.token, target.body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d, body %s", target.method, target.path, recorder.Code, recorder.Body.String())
		}
		stagedDocument(t, recorder)
	}
}

// TestStagedHarnessRefusesAnonymousCallers sweeps the same 15 pairs
// without a session: every staged handler must refuse with 401 before
// touching any rule.
func TestStagedHarnessRefusesAnonymousCallers(t *testing.T) {
	h := newStagedHarness(t)
	targets := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/me/seasons/current", ""},
		{http.MethodGet, "/api/v1/me/seasons/history", ""},
		{http.MethodGet, "/api/v1/me/seasons/" + h.seasonArch, ""},
		{http.MethodGet, "/api/v1/me/seasons/" + stagedCurrentSeason + "/champions", ""},
		{http.MethodPost, "/api/v1/me/metering/quotes", `{"content":"texto final","service":"argument-publish"}`},
		{http.MethodPost, "/api/v1/me/metering/publications", `{"intention_key":"anon-1","content":"texto final","service":"argument-publish"}`},
		{http.MethodGet, "/api/v1/me/metering/publications/00000000-0000-4000-8000-000000000000", ""},
		{http.MethodGet, "/api/v1/me/metering/statement", ""},
		{http.MethodGet, "/api/v1/me/commerce/contracts/" + h.commerceID, ""},
		{http.MethodGet, "/api/v1/me/commerce/statement", ""},
		{http.MethodGet, "/api/v1/me/disputes/cases/caso-proposta", ""},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-proposta/accepts", ""},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/defenses", `{"digest":"x"}`},
		{http.MethodGet, "/api/v1/me/disputes/cases/caso-sentenca/ruling", ""},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-sentenca/appeals", `{"reason":"x"}`},
	}
	for _, target := range targets {
		recorder := stagedDo(t, h, target.method, target.path, "", target.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", target.method, target.path, recorder.Code)
		}
	}
}
