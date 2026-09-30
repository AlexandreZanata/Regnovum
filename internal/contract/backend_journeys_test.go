package contract_test

// P25-T09 — jornadas E2E do backend sobre app e worker reais.
//
// O teste serve a API real (handlers, casos de uso e adapters reais sobre
// PostgreSQL descartável) por um listener real e a dirige por HTTP, nunca
// chamando um caso de uso direto: signup→participação, wallet→argumento,
// compra→webhook→benefício, report→appeal e export→delete, cada uma no
// positivo, no negativo e no retry, com DB/ledger/audit/job/email
// conferidos. A primeira jornada atravessa um restart do processo (o
// servidor fecha e a composição é refeita sobre o mesmo banco): a sessão
// sobrevive porque o estado da jornada é durável, não memória.
// O worker é o jobsapp.Worker real com o handler real de limpeza de
// sessões. Compras falam com o simulador Stripe da P25-T08 (sem rede) e o
// webhook entra por um endpoint fino de teste que embrulha o caso de uso
// real — a rota de webhook ainda não é composta no processo, e o embrulho
// documenta exatamente o que a composição futura deve servir.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenasbilling "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	auditpg "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	billinghttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingwallet "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/wallet"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identityargon "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	identityfake "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	identityhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	identityjobs "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/jobs"
	identitymfa "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/mfamechanism"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobspg "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationhttp "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	moderationpg "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	platformmfa "github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	profileshttp "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// journeyFixedInstant é o instante que assina os webhooks da jornada de
// compra: fixo e igual ao relógio do verificador, sem leitura do relógio da
// máquina.
var journeyFixedInstant = time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

// journeyWorld é a composição real servida por HTTP: banco descartável,
// simulador Stripe e os adapters reais. O servidor é (re)montado sobre o
// mesmo pool a cada restart, como um processo que volta com o banco intacto.
type journeyWorld struct {
	pool   *pgxpool.Pool
	sender *identityfake.Sender
	stripe *providersim.Simulator
	clock  clockseed.System
	random clockseed.CryptoRandom
	secMgr *security.Manager
}

func newJourneyWorld(t *testing.T) *journeyWorld {
	t.Helper()
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	secMgr, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}
	return &journeyWorld{
		pool:   pool,
		stripe: providersim.NewStripe(t),
		clock:  clock,
		random: random,
		secMgr: secMgr,
	}
}

// mountJourneyIdentity monta o identity com o validador de sessão REAL: o
// token opaco do cookie é resolvido no banco, então login e restart exercem
// o caminho de produção em vez de um mapa fixo.
func (world *journeyWorld) mountJourneyIdentity(t *testing.T) (*identityhttp.Handler, *identityfake.Sender, security.SessionValidator) {
	t.Helper()
	repo := identitypg.NewRepository(world.pool)
	hasher, err := identityargon.New(identityargon.FastParams(), world.random)
	if err != nil {
		t.Fatalf("setup hasher: %v", err)
	}
	sender := identityfake.NewSender()
	vPolicy := identitydomain.DefaultVerificationPolicy()
	sPolicy := identitydomain.DefaultSessionPolicy()
	rPolicy := identitydomain.DefaultPasswordResetPolicy()
	authUC := identityapp.NewAuthenticateSessionUseCase(repo, repo, world.clock, sPolicy, 5*time.Minute)
	sealer, err := platformmfa.NewSealer(make([]byte, platformmfa.KeySize), world.random)
	if err != nil {
		t.Fatalf("mfa sealer: %v", err)
	}
	mechanism, err := identitymfa.New(platformmfa.Config{}, sealer, world.random)
	if err != nil {
		t.Fatalf("mfa mechanism: %v", err)
	}
	handler := identityhttp.NewHandler(identityhttp.HandlerConfig{
		RegisterUseCase:              identityapp.NewRegisterAccountUseCase(repo, repo, hasher, sender, world.clock, world.random, vPolicy),
		VerifyEmailUseCase:           identityapp.NewVerifyEmailUseCase(repo, repo, world.clock),
		LoginUseCase:                 identityapp.NewLoginUseCase(repo, repo, repo, hasher, world.clock, world.random, sPolicy),
		LogoutUseCase:                identityapp.NewLogoutUseCase(repo),
		ListSessionsUseCase:          identityapp.NewListSessionsUseCase(repo, world.clock, sPolicy),
		RequestPasswordResetUseCase:  identityapp.NewRequestPasswordResetUseCase(repo, repo, sender, world.clock, world.random, rPolicy),
		CompletePasswordResetUseCase: identityapp.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, sender, world.clock),
		AuthenticateSessionUseCase:   authUC,
		BeginMFAEnrollmentUseCase:    identityapp.NewBeginMFAEnrollmentUseCase(repo, mechanism),
		ConfirmMFAEnrollmentUseCase:  identityapp.NewConfirmMFAEnrollmentUseCase(repo, mechanism, hasher, world.clock, journeyMFAAudit{}),
		StepUpMFAUseCase:             identityapp.NewStepUpMFAUseCase(repo, mechanism, world.clock),
		RecoverMFAUseCase:            identityapp.NewRecoverMFAUseCase(repo, mechanism, hasher, world.clock, journeyMFAAudit{}),
		SecurityManager:              world.secMgr,
	})
	validator := security.SessionValidatorFunc(func(ctx context.Context, rawToken string) (security.AuthIdentity, error) {
		result, err := authUC.Execute(ctx, identityapp.AuthenticateSessionCommand{RawToken: rawToken})
		if err != nil {
			return security.AuthIdentity{}, err
		}
		return security.AuthIdentity{AccountID: result.Account.ID().String(), SessionID: result.Session.ID().String()}, nil
	})
	return handler, sender, validator
}

// journeyCatalog é o catálogo da jornada de compra: o ink_10000 a 2490 BRL,
// exatamente o total que o simulador responde, para que a comparação
// servidor×provider do caso de uso passe com o documento fixo do fake.
func journeyCatalog(t *testing.T) *billingdomain.Catalog {
	t.Helper()
	grant, err := billingdomain.NewINKGrant(10000)
	if err != nil {
		t.Fatalf("NewINKGrant: %v", err)
	}
	productID, err := billingdomain.ParseProductID("ink_10000")
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	amount, err := billingdomain.NewMoney(2490, billingdomain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	price, err := billingdomain.ParseStripePriceID(providersim.StripePriceID)
	if err != nil {
		t.Fatalf("ParseStripePriceID: %v", err)
	}
	product, err := billingdomain.NewProduct(billingdomain.MarketBrazil, productID, amount, grant, price)
	if err != nil {
		t.Fatalf("NewProduct: %v", err)
	}
	catalog, err := billingdomain.NewCatalog(1, []billingdomain.Product{product})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

// serveJourneys monta TODA a composição real sobre o mundo e a serve. Cada
// chamada refaz os handlers do zero (memória nova, mesmo banco): chamar de
// novo depois de fechar o servidor é o restart do processo.
func (world *journeyWorld) serveJourneys(t *testing.T) *httptest.Server {
	t.Helper()

	identity, sender, validator := world.mountJourneyIdentity(t)
	world.sender = sender
	positions := mountPositions(t, world.pool, world.clock, world.secMgr)
	arguments := mountArguments(t, world.pool, world.clock, world.secMgr)
	persuasion := mountPersuasion(t, world.pool, world.clock, world.secMgr)
	wallet := mountWallet(t, world.pool, world.secMgr)
	profiles := mountProfiles(t, world.pool, world.secMgr)
	moderation := mountJourneyModeration(t, world)
	search := mountSearch(t, world.pool, world.secMgr)
	arenas := mountArenas(t, world.pool, world.clock, world.secMgr, arenasbilling.New(billingpg.NewRepository(world.pool), world.clock))
	passes, _ := mountBillingPasses(t, world.pool, world.clock, world.secMgr)
	checkout := world.mountJourneyCheckout(t)
	metrics, export := mountTransparencyExport(t, world.pool, world.clock, world.secMgr)
	profileExport, profileDeletion := mountJourneyPrivacy(t, world)

	ids := clockseed.NewIDGenerator("req", world.random, world.clock)
	handler, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{
		surfaceOf(identityRoutes(), identity.RegisterRoutes),
		surfaceOf(arenasRoutes(), arenas.RegisterRoutes),
		surfaceOf(argumentsRoutes(), arguments.RegisterRoutes),
		surfaceOf(positionsRoutes(), positions.RegisterRoutes),
		surfaceOf(persuasionRoutes(), persuasion.RegisterRoutes),
		surfaceOf(walletRoutes(), wallet.RegisterRoutes),
		surfaceOf(profilesRoutes(), profiles.RegisterRoutes),
		surfaceOf(billingPassesRoutes(), passes.RegisterRoutes),
		surfaceOf(billingCheckoutRoutes(), checkout.RegisterBillingRoutes),
		surfaceOf(moderationRoutes(), moderation.RegisterRoutes),
		surfaceOf(searchRoutes(), search.RegisterRoutes),
		surfaceOf(transparencyRoutes(), metrics.RegisterRoutes),
		surfaceOf(transparencyExportRoutes(), export.RegisterRoutes),
		surfaceOf(journeyPrivacyRoutes(), profileExport.RegisterRoutes),
		surfaceOf(journeyDeletionRoutes(), profileDeletion.RegisterRoutes),
	})
	if err != nil {
		t.Fatalf("compose journeys router: %v", err)
	}
	// O webhook de teste não pertence ao registro de rotas do produto (a
	// rota ainda não existe na composição): é servido num mux externo que
	// delega todo o resto ao mux da plataforma, sob o mesmo middleware de
	// autenticação.
	webhookMux := http.NewServeMux()
	world.webhookEndpoint(t)(webhookMux)
	outer := http.NewServeMux()
	outer.Handle("/api/v1/j09/", webhookMux)
	outer.Handle("/", handler)
	server := httptest.NewServer(world.secMgr.AuthenticateMiddleware(validator)(outer))
	t.Cleanup(server.Close)
	return server
}

// journeyCall faz uma chamada HTTP contra o servidor de jornadas com cookie
// de sessão manual: sem jar, sem cliente mágico, o token viaja no header
// como um browser o guardaria.
func journeyCall(t *testing.T, server *httptest.Server, method, path, cookie, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: cookie})
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	assertNoSecrets(t, method+" "+path, responseBody)
	return response.StatusCode, response.Header, responseBody
}

// journeyCookie extrai o token bruto de sessão do Set-Cookie do login.
func journeyCookie(t *testing.T, header http.Header) string {
	t.Helper()
	for _, setCookie := range header.Values("Set-Cookie") {
		parts := strings.Split(setCookie, ";")
		if len(parts) == 0 {
			continue
		}
		name, value, found := strings.Cut(strings.TrimSpace(parts[0]), "=")
		if found && name == "arena_session" && value != "" {
			return value
		}
	}
	t.Fatal("login issued no arena_session cookie")
	return ""
}

// journeyAccountID resolve o id da conta por email (preparação de fixture,
// como o seed direto do conformance).
func journeyAccountID(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM app.accounts WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	return id
}

// journeyFund credita INK comprado na wallet (preparação; a jornada em si é
// o débito atômico da publicação sobre HTTP).
func journeyFund(t *testing.T, pool *pgxpool.Pool, email string, keySuffix string, amount int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accountID := journeyAccountID(t, pool, email)
	key, err := walletdomain.ParseIdempotencyKey("j09-fund-" + keySuffix)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	reference, err := walletdomain.ParseReference("model:fund:j09")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	repo := walletpg.NewRepository(pool)
	if _, err := repo.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID: walletdomain.AccountID(accountID), Bucket: walletdomain.BucketPurchased,
		OperationType: walletdomain.OperationCreditPurchase, IdempotencyKey: key,
		Reference: reference, Delta: amount, ChangedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("fund wallet: %v", err)
	}
}

// journeyArena publica uma arena por SQL (preparação; a jornada de
// participação de ponta a ponta vive no primeiro teste).
func journeyArena(t *testing.T, pool *pgxpool.Pool, email, statement, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var arenaID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		SELECT id, $2, 'technology', 'pt-BR', 'published', $3, now()
		FROM app.accounts WHERE email = $1
		RETURNING id::text`, email, statement, slug).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}
	return arenaID
}

// journeyGrantPass concede um lote de passes (preparação para publicar).
func journeyGrantPass(t *testing.T, pool *pgxpool.Pool, email string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accountID := journeyAccountID(t, pool, email)
	reference, err := billingdomain.ParseReference("j09:grant:draft")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	repo := billingpg.NewRepository(pool)
	if _, err := repo.GrantPassLot(ctx, billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID), Origin: billingdomain.OriginAdmin,
		Quantity: quantity, Reference: reference, GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("grant pass lot: %v", err)
	}
}

// journeySession semeia uma sessão real (hash do token bruto conhecido) para
// as jornadas que começam já autenticadas; com mfa o segundo fator é fresco,
// o que abre o portão administrativo da moderação.
func journeySession(t *testing.T, pool *pgxpool.Pool, email, rawToken string, mfa bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accountID := journeyAccountID(t, pool, email)
	digest := sha256.Sum256([]byte(rawToken))
	now := time.Now().UTC()
	if mfa {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at, mfa_verified_at)
			VALUES ($1::uuid, $2, $3, $4, $5)`, accountID, digest[:], now, now.Add(time.Hour), now); err != nil {
			t.Fatalf("seed mfa session: %v", err)
		}
		return
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at)
		VALUES ($1::uuid, $2, $3, $4)`, accountID, digest[:], now, now.Add(time.Hour)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// journeyGrantAdmin concede o papel administrativo (preparação).
func journeyGrantAdmin(t *testing.T, pool *pgxpool.Pool, email string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accountID := journeyAccountID(t, pool, email)
	if _, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by)
		SELECT $1::uuid, 'admin', $1::uuid`, accountID); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
}

// journeyQueryInt conta uma consulta escalar (verificação de estado no banco).
func journeyQueryInt(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var count int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return count
}

// TestJourneysSignupToParticipation encadeia registro→verificação→login→
// rascunho→publicação→posição sobre HTTP com sessão real, atravessa um
// restart do processo no meio e prova que nada é parcial nem duplicado.
func TestJourneysSignupToParticipation(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "j09-alice@arena.example.com"
	const password = "j09-correct-horse-1"

	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+token, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token=not-a-real-token", "", "", nil)
	if status != 400 && status != 404 {
		t.Fatalf("verify with bad token = %d, want a 4xx refusal", status)
	}

	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, header)
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, nil)
	if status != 401 {
		t.Fatalf("login with wrong password = %d, want 401", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", "", `{"statement":"A arena j09 debate a tese com clareza","category":"technology","language":"pt-BR"}`, nil)
	if status != 401 {
		t.Fatalf("draft without session = %d, want 401", status)
	}

	status, _, body := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookie, `{"statement":"A arena j09 debate a tese com clareza","category":"technology","language":"pt-BR"}`, nil)
	if status != 201 {
		t.Fatalf("draft = %d, want 201 (%s)", status, string(body))
	}
	var draft struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &draft); err != nil || draft.ID == "" {
		t.Fatalf("draft carries no id: %s", string(body))
	}

	// Publicar sem passe é recusado; com passe, publica. Sem passe não há
	// estado parcial: o rascunho continua rascunho.
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arena-drafts/"+draft.ID+"/publish", cookie, `{}`, nil)
	if status != 402 && status != 409 {
		t.Fatalf("publish without pass = %d, want a documented refusal (402 or 409)", status)
	}
	journeyGrantPass(t, world.pool, email)
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arena-drafts/"+draft.ID+"/publish", cookie, `{}`, nil)
	if status != 200 {
		t.Fatalf("publish with pass = %d, want 200", status)
	}
	// Republicar é estável e idempotente: 200 sem segunda arena.
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arena-drafts/"+draft.ID+"/publish", cookie, `{}`, nil)
	if status != 200 {
		t.Fatalf("publish replay = %d, want 200", status)
	}

	var arenaID string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := world.pool.QueryRow(ctx, `SELECT id::text FROM app.arenas WHERE id::text = $1 AND status = 'published'`, draft.ID).Scan(&arenaID); err != nil {
		t.Fatalf("published arena missing in db: %v", err)
	}

	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arenas/"+arenaID+"/position", cookie, `{"position":"agree"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("position = %d, want 200 or 201", status)
	}

	// O processo cai aqui: fecha o listener e recompõe tudo sobre o mesmo
	// banco. A sessão viaja no cookie e resolve no banco, então a jornada
	// continua exatamente onde parou.
	server.Close()
	server = world.serveJourneys(t)

	status, _, positionBody := journeyCall(t, server, "GET", "/api/v1/me/arenas/"+arenaID+"/position", cookie, "", nil)
	if status != 200 {
		t.Fatalf("position after restart = %d, want 200", status)
	}
	if !strings.Contains(string(positionBody), "agree") {
		t.Fatalf("position after restart lost the value: %s", string(positionBody))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arenas/"+arenaID+"/position/changes", cookie, `{"position":"disagree"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("position change after restart = %d, want 200 or 201", status)
	}

	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.arenas WHERE id::text = $1`, draft.ID); got != 1 {
		t.Fatalf("arenas for draft = %d, want exactly 1 (no duplicate on replay)", got)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.accounts WHERE email = $1 AND status = 'active'`, email); got != 1 {
		t.Fatalf("active accounts = %d, want 1", got)
	}
}

// mountJourneyCheckout compõe o checkout real: catálogo de teste, gateway
// Stripe contra o simulador, elegibilidade/contas/intents no Postgres.
func (world *journeyWorld) mountJourneyCheckout(t *testing.T) *billinghttp.BillingHandler {
	t.Helper()
	repo := billingpg.NewRepository(world.pool)
	gateway, err := stripeadapter.NewGateway(stripeadapter.Config{
		SecretKey: providersim.StripeSecretKey,
		Timeout:   5 * time.Second,
		BaseURL:   world.stripe.URL(),
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	createCheckout, err := billingapp.NewCreateCheckoutUseCase(billingapp.CheckoutDependencies{
		Catalog:    journeyCatalog(t),
		Gateway:    gateway,
		Purchasers: repo, Customers: repo, Intents: repo,
		Clock: world.clock,
		Returns: billingapp.CheckoutReturnURLs{
			Success: "https://arena.invalid/checkout/success",
			Cancel:  "https://arena.invalid/checkout/cancel",
		},
	})
	if err != nil {
		t.Fatalf("NewCreateCheckoutUseCase: %v", err)
	}
	statusUC := billingapp.NewGetSubscriptionStatusUseCase(repo)
	portalUC, err := billingapp.NewGetBillingPortalUseCase(billingapp.PortalDependencies{
		Customers: repo,
		Gateway:   gateway,
		ReturnURL: "https://arena.invalid/billing/return",
	})
	if err != nil {
		t.Fatalf("NewGetBillingPortalUseCase: %v", err)
	}
	return billinghttp.NewBillingHandler(billinghttp.BillingHandlerConfig{
		CreateCheckout:        createCheckout,
		GetSubscriptionStatus: statusUC,
		GetBillingPortal:      portalUC,
		SecurityManager:       world.secMgr,
	})
}

// journeyMFAAudit descarta a trilha de MFA do servidor de jornadas: a
// auditoria com contagem vive no servidor da T01/T08, que é quem prova o
// rastro administrativo.
type journeyMFAAudit struct{}

func (journeyMFAAudit) RecordMFAEnrolled(context.Context, string, time.Time) error       { return nil }
func (journeyMFAAudit) RecordMFABackupCodeUsed(context.Context, string, time.Time) error { return nil }

// webhookEndpoint é o embrulho fino de teste do ProcessWebhookUseCase real:
// lê corpo e assinatura do HTTP, executa verificação→persistência→
// liquidação e responde 200/400/500. É exatamente o que a composição futura
// da rota deve servir; até lá, a jornada o exerce por HTTP sem chamar o caso
// de uso direto.
func (world *journeyWorld) webhookEndpoint(t *testing.T) func(*http.ServeMux) {
	t.Helper()
	repo := billingpg.NewRepositoryWithClock(world.pool, world.clock)
	verifier, err := stripeadapter.NewWebhookVerifier(providersim.StripeWebhookSecret, 0, testsource.NewClock(journeyFixedInstant))
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	inker := billingwallet.NewInker(walletpg.NewRepository(world.pool), world.clock)
	settler, err := billingapp.NewSettleCheckoutUseCase(billingapp.SettleCheckoutDependencies{
		Catalog: journeyCatalog(t), Intents: repo, Inker: inker, Clock: world.clock,
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
	}
	process, err := billingapp.NewProcessWebhookUseCase(billingapp.ProcessWebhookDependencies{
		Verifier: verifier, Events: repo, Settler: settler, Clock: world.clock,
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase: %v", err)
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/j09/billing/webhook", func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "unreadable body", http.StatusBadRequest)
				return
			}
			err = process.Execute(r.Context(), billingapp.ProcessWebhookCommand{
				RawBody:         raw,
				SignatureHeader: r.Header.Get("Stripe-Signature"),
				TimestampHeader: r.Header.Get("Stripe-Timestamp"),
			})
			if err != nil {
				// O contrato que a futura rota de produção deve servir:
				// assinatura/integridade 400, corpo grande 413,
				// entrega em processamento 429 (transitório, o provider
				// retenta) e falha de processamento 500.
				switch {
				case errors.Is(err, billingapp.ErrWebhookSignatureInvalid),
					errors.Is(err, billingapp.ErrWebhookPayloadMalformed),
					errors.Is(err, billingapp.ErrCheckoutIntentNotFound):
					http.Error(w, "invalid webhook", http.StatusBadRequest)
				case errors.Is(err, billingapp.ErrWebhookPayloadTooLarge):
					http.Error(w, "webhook too large", http.StatusRequestEntityTooLarge)
				case errors.Is(err, billingapp.ErrWebhookEventInProcessing):
					http.Error(w, "webhook in processing", http.StatusTooManyRequests)
				default:
					http.Error(w, "processing failure", http.StatusInternalServerError)
				}
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}
}

// mountJourneyModeration compõe a moderação completa: denúncia, fila,
// claim, decisão e recurso, com o repositório Postgres em todos os ports.
func mountJourneyModeration(t *testing.T, world *journeyWorld) *moderationhttp.Handler {
	t.Helper()
	repo := moderationpg.NewRepository(world.pool)
	authorizer, err := moderationapp.NewAuthorizer(repo, world.clock)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	fileReport, err := moderationapp.NewFileReportUseCase(moderationapp.FileReportDependencies{
		Targets: repo, Reports: repo, Clock: world.clock,
	})
	if err != nil {
		t.Fatalf("NewFileReportUseCase: %v", err)
	}
	fileAppeal, err := moderationapp.NewFileAppealUseCase(moderationapp.AppealDependencies{
		Appeals: repo, Authorizer: authorizer, Clock: world.clock,
	})
	if err != nil {
		t.Fatalf("NewFileAppealUseCase: %v", err)
	}
	getQueue, err := moderationapp.NewGetCaseQueueUseCase(repo, repo)
	if err != nil {
		t.Fatalf("NewGetCaseQueueUseCase: %v", err)
	}
	claimCase, err := moderationapp.NewClaimCaseUseCase(moderationapp.ReviewDependencies{
		Cases: repo, Authorizer: authorizer, Clock: world.clock,
	})
	if err != nil {
		t.Fatalf("NewClaimCaseUseCase: %v", err)
	}
	decideCase, err := moderationapp.NewDecideCaseUseCase(moderationapp.ReviewDependencies{
		Cases: repo, Authorizer: authorizer, Clock: world.clock,
	})
	if err != nil {
		t.Fatalf("NewDecideCaseUseCase: %v", err)
	}
	codec, err := moderationapp.NewQueueCursorCodec([]byte("j09-mod-queue-cursor-secret-0123456"))
	if err != nil {
		t.Fatalf("NewQueueCursorCodec: %v", err)
	}
	return moderationhttp.NewHandler(moderationhttp.HandlerConfig{
		FileReport: fileReport, FileAppeal: fileAppeal, GetQueue: getQueue,
		ClaimCase: claimCase, DecideCase: decideCase,
		Roles: repo, Sessions: repo, QueueCodec: codec,
		SecurityManager: world.secMgr, Clock: world.clock,
	})
}

// mountJourneyPrivacy compõe export e deleção reais sobre o Postgres, com
// auditoria real de deleção: o pedido, o estado e o cancelamento viajam por
// HTTP e cada transição deixa linha no banco.
func mountJourneyPrivacy(t *testing.T, world *journeyWorld) (*profileshttp.ExportHandler, *profileshttp.DeletionHandler) {
	t.Helper()
	repo := profilespg.NewRepository(world.pool)
	requestExport, err := profilesapp.NewRequestPersonalExportUseCase(repo, world.random, world.clock)
	if err != nil {
		t.Fatalf("NewRequestPersonalExportUseCase: %v", err)
	}
	downloadExport, err := profilesapp.NewDownloadPersonalExportUseCase(repo, world.clock)
	if err != nil {
		t.Fatalf("NewDownloadPersonalExportUseCase: %v", err)
	}
	exportHandler := profileshttp.NewExportHandler(profileshttp.ExportHandlerConfig{
		RequestUseCase: requestExport, DownloadUseCase: downloadExport,
		Sessions: repo, SecurityManager: world.secMgr, Clock: world.clock,
	})

	auditRepo := auditpg.NewRepository(world.pool)
	uow := platformpg.NewTxManager(world.pool)
	requestDeletion, err := profilesapp.NewRequestDeletionUseCase(repo, auditRepo, uow, world.clock)
	if err != nil {
		t.Fatalf("NewRequestDeletionUseCase: %v", err)
	}
	deletionStatus, err := profilesapp.NewGetDeletionStatusUseCase(repo)
	if err != nil {
		t.Fatalf("NewGetDeletionStatusUseCase: %v", err)
	}
	cancelDeletion, err := profilesapp.NewCancelDeletionUseCase(repo, auditRepo, uow, world.clock)
	if err != nil {
		t.Fatalf("NewCancelDeletionUseCase: %v", err)
	}
	deletionHandler := profileshttp.NewDeletionHandler(profileshttp.DeletionHandlerConfig{
		RequestUseCase: requestDeletion, StatusUseCase: deletionStatus,
		CancelUseCase: cancelDeletion, SecurityManager: world.secMgr,
	})
	return exportHandler, deletionHandler
}

func journeyPrivacyRoutes() []httpserver.Route {
	return []httpserver.Route{
		{Method: "POST", Path: "/api/v1/me/exports"},
		{Method: "GET", Path: "/api/v1/me/exports/{id}/download"},
	}
}

func journeyDeletionRoutes() []httpserver.Route {
	return []httpserver.Route{
		{Method: "POST", Path: "/api/v1/me/deletion"},
		{Method: "GET", Path: "/api/v1/me/deletion"},
		{Method: "POST", Path: "/api/v1/me/deletion/cancel"},
	}
}

// TestJourneysWalletToArgument publica um argumento debitando INK: positivo,
// replay idempotente sem segundo débito, recusa por saldo e conservação do
// ledger conferida no banco.
func TestJourneysWalletToArgument(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const owner = "j09-carol@arena.example.com"
	const broke = "j09-dave@arena.example.com"
	for _, email := range []string{owner, broke} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active')`, email); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	journeyFund(t, world.pool, owner, "carol", 100000)
	arenaID := journeyArena(t, world.pool, owner, "A arena j09 debate a tese com clareza", "j09-wallet-arena")
	journeySession(t, world.pool, owner, "j09-carol-token", false)
	journeySession(t, world.pool, broke, "j09-dave-token", false)

	publish := func(cookie, key, body string) (int, http.Header, []byte) {
		return journeyCall(t, server, "POST", "/api/v1/me/arenas/"+arenaID+"/arguments", cookie, body, map[string]string{"Idempotency-Key": key})
	}

	status, _, raw := publish("j09-carol-token", "j09-arg-1", `{"relation":"support","content":"Afirmação j09 para a jornada"}`)
	if status != 200 && status != 201 {
		t.Fatalf("publish = %d, want 200 or 201 (%s)", status, string(raw))
	}
	var published struct {
		Argument struct {
			ID string `json:"id"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(raw, &published); err != nil || published.Argument.ID == "" {
		t.Fatalf("publish carries no argument id: %s", string(raw))
	}

	status, replayHeader, replayBody := publish("j09-carol-token", "j09-arg-1", `{"relation":"support","content":"Afirmação j09 para a jornada"}`)
	if status != 200 && status != 201 {
		t.Fatalf("publish replay = %d, want 200 or 201", status)
	}
	if got := replayHeader.Get("Idempotency-Replayed"); got != "true" {
		t.Fatalf("replay header = %q, want true", got)
	}
	var replayed struct {
		Argument struct {
			ID string `json:"id"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(replayBody, &replayed); err != nil || replayed.Argument.ID != published.Argument.ID {
		t.Fatalf("replay resolved another argument: %s", string(replayBody))
	}

	status, _, _ = publish("j09-carol-token", "j09-arg-bad", `{"relation":"maybe","content":"x"}`)
	if status != 400 {
		t.Fatalf("publish invalid = %d, want 400", status)
	}
	status, _, _ = publish("j09-dave-token", "j09-arg-broke", `{"relation":"support","content":"Sem saldo para publicar"}`)
	if status != 409 {
		t.Fatalf("publish without funds = %d, want 409 insufficient_ink", status)
	}

	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.arguments WHERE id::text = $1`, published.Argument.ID); got != 1 {
		t.Fatalf("arguments = %d, want exactly 1 (replay duplicates nothing)", got)
	}
	// O ledger conserva: o saldo exibido é a soma dos deltas persistidos e o
	// replay não escreveu segunda linha de débito.
	balance := journeyWalletBalance(t, server, "j09-carol-token")
	ledgerSum := journeyLedgerSum(t, world.pool, owner)
	if balance != ledgerSum {
		t.Fatalf("wallet balance %d != ledger sum %d", balance, ledgerSum)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.wallet_transactions t
		JOIN app.wallet_operations o ON o.id = t.operation_id
		WHERE o.account_id::text = $1 AND t.amount < 0`,
		journeyAccountID(t, world.pool, owner)); got != 1 {
		t.Fatalf("debit lines = %d, want exactly 1", got)
	}
}

// journeyWalletBalance lê o saldo total (livre + comprado) pela API.
func journeyWalletBalance(t *testing.T, server *httptest.Server, cookie string) int64 {
	t.Helper()
	status, _, body := journeyCall(t, server, "GET", "/api/v1/me/wallet", cookie, "", nil)
	if status != 200 {
		t.Fatalf("wallet = %d, want 200 (%s)", status, string(body))
	}
	var document struct {
		BalanceFree      int64 `json:"balance_free"`
		BalancePurchased int64 `json:"balance_purchased"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode wallet: %v (%s)", err, string(body))
	}
	return document.BalanceFree + document.BalancePurchased
}

// journeyLedgerSum soma os deltas persistidos do ledger.
func journeyLedgerSum(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var sum int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(t.amount),0) FROM app.wallet_transactions t
		JOIN app.wallet_operations o ON o.id = t.operation_id
		WHERE o.account_id::text = $1`,
		journeyAccountID(t, pool, email)).Scan(&sum); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	return sum
}

// TestJourneysPurchaseToBenefit compra ink_10000, entrega o webhook assinado
// e lê o benefício: retry resolve o mesmo intent, replay do evento liquida
// uma vez, assinatura ruim é 400.
func TestJourneysPurchaseToBenefit(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "j09-erin@arena.example.com"
	const password = "j09-correct-horse-2"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, _ := identitydomain.ParseEmail(email)
	verifyToken, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+verifyToken, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, header)

	status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie, `{"market":"BR","product":"ink_10000","idempotency_key":"j09-buy-1"}`, nil)
	if status != 200 {
		t.Fatalf("checkout = %d, want 200 (%s)", status, string(raw))
	}
	var order struct {
		IntentID    string `json:"intent_id"`
		RedirectURL string `json:"redirect_url"`
		Replayed    bool   `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &order); err != nil || order.IntentID == "" || order.RedirectURL == "" || order.Replayed {
		t.Fatalf("checkout answer is not a fresh intent: %s", string(raw))
	}

	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie, `{"market":"BR","product":"no_such_product","idempotency_key":"j09-buy-bad"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("checkout unknown product = %d, want a 4xx refusal", status)
	}

	status, _, retryRaw := journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie, `{"market":"BR","product":"ink_10000","idempotency_key":"j09-buy-1"}`, nil)
	if status != 200 {
		t.Fatalf("checkout retry = %d, want 200", status)
	}
	var retryOrder struct {
		IntentID string `json:"intent_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(retryRaw, &retryOrder); err != nil || retryOrder.IntentID != order.IntentID || !retryOrder.Replayed {
		t.Fatalf("checkout retry did not resolve the same intent: %s", string(retryRaw))
	}
	// O retry resolve o mesmo intent localmente; no provider as duas
	// tentativas viajam com a mesma chave de idempotência (o simulador não
	// deduplica por chave, então o que se prova é a chave estável: no
	// provider real ela resolve o mesmo objeto em vez de provisionar outro).
	calls := world.stripe.CallsTo("POST", "/v1/checkout/sessions")
	if len(calls) != 2 {
		t.Fatalf("provider sessions = %d, want exactly 2 attempts (create + retry)", len(calls))
	}
	if calls[0].Header("Idempotency-Key") == "" || calls[0].Header("Idempotency-Key") != calls[1].Header("Idempotency-Key") {
		t.Fatalf("retry changed the provider key: %q vs %q", calls[0].Header("Idempotency-Key"), calls[1].Header("Idempotency-Key"))
	}

	deliver := func(payload, signature, timestamp string) (int, []byte) {
		request, err := http.NewRequest("POST", server.URL+"/api/v1/j09/billing/webhook", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("build webhook request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Stripe-Signature", signature)
		request.Header.Set("Stripe-Timestamp", timestamp)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("deliver webhook: %v", err)
		}
		defer response.Body.Close()
		answer, _ := io.ReadAll(response.Body)
		return response.StatusCode, answer
	}
	// O parser do caso de uso lê por casamento de string na ordem que o
	// provider declara (id, type e livemode do nível superior antes de
	// data): o documento é montado nessa ordem, como o Stripe responde.
	sessionJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	eventPayload := fmt.Sprintf(`{"id":"evt_J09purchase1","object":"event","type":"checkout.session.completed","created":%d,"livemode":false,"data":{"object":%s}}`,
		journeyFixedInstant.Unix(), sessionJSON)
	signature, timestamp := providersim.SignStripeWebhook(providersim.StripeWebhookSecret, journeyFixedInstant, eventPayload)

	if status, _ := deliver(eventPayload, "t=bogus,v1=deadbeef", timestamp); status != 400 {
		t.Fatalf("webhook with bad signature = %d, want 400", status)
	}
	if status, answer := deliver(eventPayload, signature, timestamp); status != 200 {
		t.Fatalf("webhook = %d, want 200 (%s)", status, string(answer))
	}
	if status, _ := deliver(eventPayload, signature, timestamp); status != 200 {
		t.Fatalf("webhook replay = %d, want 200 (idempotent claim)", status)
	}

	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.checkout_intents WHERE id::text = $1 AND status = 'paid'`, order.IntentID); got != 1 {
		t.Fatalf("paid intents = %d, want 1", got)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.stripe_events WHERE stripe_event_id = 'evt_J09purchase1' AND status = 'processed'`); got != 1 {
		t.Fatalf("processed events = %d, want 1", got)
	}
	// O benefício aparece na leitura HTTP: o crédito de 10000 INK.
	if balance := journeyWalletBalance(t, server, cookie); balance != 10000 {
		t.Fatalf("wallet after settlement = %d, want 10000", balance)
	}
}

// TestJourneysReportToAppeal encadeia denúncia→fila→claim→decisão→recurso:
// a fila e o recurso têm replay estável, claim duplo é 409 e estranho é 403.
func TestJourneysReportToAppeal(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const reporter = "j09-grace@arena.example.com"
	const owner = "j09-henry@arena.example.com"
	const moderator = "j09-moder@arena.example.com"
	const moderator2 = "j09-moder2@arena.example.com"
	for _, email := range []string{reporter, owner, moderator, moderator2} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active')`, email); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	arenaID := journeyArena(t, world.pool, owner, "A arena j09 sob revisão da moderação", "j09-moderation-arena")
	journeySession(t, world.pool, reporter, "j09-grace-token", false)
	journeySession(t, world.pool, owner, "j09-henry-token", false)
	journeySession(t, world.pool, moderator, "j09-moder-token", true)
	journeySession(t, world.pool, moderator2, "j09-moder2-token", true)
	journeyGrantAdmin(t, world.pool, moderator)
	journeyGrantAdmin(t, world.pool, moderator2)

	reportBody := `{"target_type":"arena","target_id":"` + arenaID + `","reason":"spam","context":"Conteúdo j09 sob revisão"}`
	status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", "j09-grace-token", reportBody, nil)
	if status != 200 {
		t.Fatalf("report = %d, want 200 (%s)", status, string(raw))
	}
	var report struct {
		ReportID string `json:"report_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &report); err != nil || report.ReportID == "" || report.Replayed {
		t.Fatalf("report is not a fresh filing: %s", string(raw))
	}
	status, _, replayRaw := journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", "j09-grace-token", reportBody, nil)
	if status != 200 {
		t.Fatalf("report replay = %d, want 200", status)
	}
	var reportReplay struct {
		ReportID string `json:"report_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(replayRaw, &reportReplay); err != nil || reportReplay.ReportID != report.ReportID || !reportReplay.Replayed {
		t.Fatalf("report replay did not resolve the same filing: %s", string(replayRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", "j09-grace-token", `{"target_type":"arena","target_id":"00000000-0000-0000-0000-000000000000","reason":"spam"}`, nil)
	if status != 404 {
		t.Fatalf("report unknown target = %d, want 404", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", "j09-grace-token", `{"target_type":"arena","target_id":"`+arenaID+`","reason":"bogus"}`, nil)
	if status != 400 {
		t.Fatalf("report bogus reason = %d, want 400", status)
	}

	// A abertura do caso é triagem (sem caminho automatizado no produto):
	// a fixture a registra como a triagem faria, e daqui em diante tudo é HTTP.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var caseID string
	if err := world.pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id) VALUES ('arena', $1::uuid) RETURNING id::text`, arenaID).Scan(&caseID); err != nil {
		t.Fatalf("open case: %v", err)
	}

	status, _, queueBody := journeyCall(t, server, "GET", "/api/v1/moderation/cases", "j09-moder-token", "", nil)
	if status != 200 {
		t.Fatalf("queue = %d, want 200", status)
	}
	if !strings.Contains(string(queueBody), caseID) {
		t.Fatalf("queue hides the open case: %s", string(queueBody))
	}

	status, _, claimBody := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", "j09-moder-token", "", nil)
	if status != 200 {
		t.Fatalf("claim = %d, want 200 (%s)", status, string(claimBody))
	}
	if !strings.Contains(string(claimBody), caseID) {
		t.Fatalf("claim answer carries no case: %s", string(claimBody))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", "j09-grace-token", "", nil)
	if status != 403 {
		t.Fatalf("stranger claim = %d, want 403", status)
	}
	// Reclaim pelo dono do claim é idempotente; outro moderador encontra 409.
	status, _, reclaimBody := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", "j09-moder-token", "", nil)
	if status != 200 {
		t.Fatalf("claim replay = %d, want 200", status)
	}
	if !strings.Contains(string(reclaimBody), caseID) {
		t.Fatalf("claim replay lost the case: %s", string(reclaimBody))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", "j09-moder2-token", "", nil)
	if status != 409 {
		t.Fatalf("second moderator claim = %d, want 409", status)
	}

	decideBody := `{"action":"warning","rule":"MOD-2:warning","justification":"Advertência j09 medida com escopo"}`
	status, _, decisionRaw := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/decisions", "j09-moder-token", decideBody, nil)
	if status != 200 {
		t.Fatalf("decide = %d, want 200 (%s)", status, string(decisionRaw))
	}
	var decision struct {
		ActionID string `json:"action_id"`
		CaseID   string `json:"case_id"`
	}
	if err := json.Unmarshal(decisionRaw, &decision); err != nil || decision.ActionID == "" || decision.CaseID != caseID {
		t.Fatalf("decision carries no action: %s", string(decisionRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/decisions", "j09-moder-token", `{"action":"ban_everything","rule":"x","justification":"y"}`, nil)
	if status != 400 {
		t.Fatalf("decide invalid action = %d, want 400", status)
	}

	appealBody := `{"action_id":"` + decision.ActionID + `","context":"Recurso j09 do dono sancionado"}`
	status, _, appealRaw := journeyCall(t, server, "POST", "/api/v1/me/moderation/appeals", "j09-henry-token", appealBody, nil)
	if status != 200 {
		t.Fatalf("appeal = %d, want 200 (%s)", status, string(appealRaw))
	}
	var appeal struct {
		AppealID string `json:"appeal_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(appealRaw, &appeal); err != nil || appeal.AppealID == "" || appeal.Replayed {
		t.Fatalf("appeal is not a fresh filing: %s", string(appealRaw))
	}
	status, _, appealReplayRaw := journeyCall(t, server, "POST", "/api/v1/me/moderation/appeals", "j09-henry-token", appealBody, nil)
	if status != 200 {
		t.Fatalf("appeal replay = %d, want 200", status)
	}
	var appealReplay struct {
		AppealID string `json:"appeal_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(appealReplayRaw, &appealReplay); err != nil || appealReplay.AppealID != appeal.AppealID || !appealReplay.Replayed {
		t.Fatalf("appeal replay did not resolve the same filing: %s", string(appealReplayRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/moderation/appeals", "j09-henry-token", `{"action_id":"00000000-0000-0000-0000-000000000000","context":"Recurso j09 órfão"}`, nil)
	if status != 404 {
		t.Fatalf("appeal unknown action = %d, want 404", status)
	}

	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.moderation_reports WHERE id::text = $1`, report.ReportID); got != 1 {
		t.Fatalf("reports = %d, want 1 (replay duplicates nothing)", got)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.moderation_actions WHERE case_id::text = $1`, caseID); got != 1 {
		t.Fatalf("actions = %d, want 1", got)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.moderation_appeals WHERE id::text = $1`, appeal.AppealID); got != 1 {
		t.Fatalf("appeals = %d, want 1", got)
	}
}

// TestJourneysExportToDelete pede o export, exerce o replay do pedido e as
// recusas do download pendente, e completa o ciclo com pedido→estado→
// cancelamento da deleção, tudo com linha no banco (a geração do documento é
// workload de worker ainda não registrada, como worker.go documenta).
func TestJourneysExportToDelete(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "j09-ivan@arena.example.com"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active')`, email); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	journeySession(t, world.pool, email, "j09-ivan-token", false)

	status, _, exportRaw := journeyCall(t, server, "POST", "/api/v1/me/exports", "j09-ivan-token", "", nil)
	if status != 202 {
		t.Fatalf("export request = %d, want 202 (%s)", status, string(exportRaw))
	}
	var exportOrder struct {
		ExportID      string `json:"export_id"`
		DownloadToken string `json:"download_token"`
	}
	if err := json.Unmarshal(exportRaw, &exportOrder); err != nil || exportOrder.ExportID == "" || exportOrder.DownloadToken == "" {
		t.Fatalf("export request carries no single-use capability: %s", string(exportRaw))
	}
	status, _, exportReplayRaw := journeyCall(t, server, "POST", "/api/v1/me/exports", "j09-ivan-token", "", nil)
	if status != 202 {
		t.Fatalf("export rerequest = %d, want 202", status)
	}
	var exportReplay struct {
		ExportID      string `json:"export_id"`
		DownloadToken string `json:"download_token"`
	}
	if err := json.Unmarshal(exportReplayRaw, &exportReplay); err != nil || exportReplay.ExportID != exportOrder.ExportID {
		t.Fatalf("export rerequest did not resolve the same record: %s", string(exportReplayRaw))
	}
	if exportReplay.DownloadToken == exportOrder.DownloadToken {
		t.Fatal("export rerequest reused the single-use capability")
	}

	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/exports/"+exportOrder.ExportID+"/download?token=bogus", "j09-ivan-token", "", nil)
	if status != 403 {
		t.Fatalf("download with bad token = %d, want 403", status)
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/exports/"+exportOrder.ExportID+"/download?token="+exportReplay.DownloadToken, "j09-ivan-token", "", nil)
	if status != 404 {
		t.Fatalf("download before generation = %d, want 404 export_not_found", status)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.data_exports WHERE id::text = $1`, exportOrder.ExportID); got != 1 {
		t.Fatalf("exports = %d, want 1", got)
	}

	status, _, deletionRaw := journeyCall(t, server, "POST", "/api/v1/me/deletion", "j09-ivan-token", "", nil)
	if status != 202 {
		t.Fatalf("deletion request = %d, want 202 (%s)", status, string(deletionRaw))
	}
	var deletion struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(deletionRaw, &deletion); err != nil || deletion.Status == "" {
		t.Fatalf("deletion request carries no status: %s", string(deletionRaw))
	}
	status, _, deletionReplayRaw := journeyCall(t, server, "POST", "/api/v1/me/deletion", "j09-ivan-token", "", nil)
	if status != 202 {
		t.Fatalf("deletion rerequest = %d, want 202", status)
	}
	var deletionReplay struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(deletionReplayRaw, &deletionReplay); err != nil || deletionReplay.Status != deletion.Status {
		t.Fatalf("deletion rerequest changed the state: %s", string(deletionReplayRaw))
	}
	status, _, statusRaw := journeyCall(t, server, "GET", "/api/v1/me/deletion", "j09-ivan-token", "", nil)
	if status != 200 {
		t.Fatalf("deletion status = %d, want 200", status)
	}
	if !strings.Contains(string(statusRaw), deletion.Status) {
		t.Fatalf("deletion status diverges: %s", string(statusRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/deletion/cancel", "j09-ivan-token", `{"reason":"Desisti da deleção j09"}`, nil)
	if status != 200 {
		t.Fatalf("deletion cancel = %d, want 200", status)
	}
	status, _, canceledRaw := journeyCall(t, server, "GET", "/api/v1/me/deletion", "j09-ivan-token", "", nil)
	if status != 200 || !strings.Contains(string(canceledRaw), "canceled") {
		t.Fatalf("deletion after cancel = %d (%s), want canceled", status, string(canceledRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/deletion/cancel", "j09-ivan-token", `{"reason":"Segundo cancelamento j09"}`, nil)
	if status != 409 {
		t.Fatalf("cancel after cancel = %d, want 409", status)
	}

	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.account_deletion_requests WHERE account_id::text = $1`,
		journeyAccountID(t, world.pool, email)); got != 1 {
		t.Fatalf("deletion requests = %d, want 1", got)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.audit_events`); got < 1 {
		t.Fatalf("audit events = %d, want the deletion trail recorded", got)
	}
}

// TestJourneysWorkerDrainsJobs roda o Worker real com o handler real de
// limpeza de sessões: a fila esvazia, a sessão expirada é revogada (401 no
// HTTP) e a fresca sobrevive; re-enqueue com a mesma chave resolve o mesmo
// job e tipo desconhecido é recusado no enqueue.
func TestJourneysWorkerDrainsJobs(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "j09-judy@arena.example.com"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active')`, email); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	accountID := journeyAccountID(t, world.pool, email)
	journeySession(t, world.pool, email, "j09-judy-fresh", false)
	expiredDigest := sha256.Sum256([]byte("j09-judy-expired"))
	past := time.Now().UTC()
	if _, err := world.pool.Exec(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at)
		VALUES ($1::uuid, $2, $3, $4)`, accountID, expiredDigest[:], past.Add(-2*time.Hour), past.Add(-time.Hour)); err != nil {
		t.Fatalf("seed expired session: %v", err)
	}

	jobsRepo := jobspg.NewRepository(world.pool)
	enqueue, err := jobsapp.NewEnqueueUseCase(jobsRepo, world.clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}
	enqueueCtx, enqueueCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer enqueueCancel()
	queued, err := enqueue.Enqueue(enqueueCtx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeSessionCleanup, Version: identityjobs.CleanupPayloadVersion,
		Payload: []byte(`{}`), IdempotencyKey: "j09-cleanup-1",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := enqueue.Enqueue(enqueueCtx, jobsapp.EnqueueCommand{
		Type: "no_such_workload", Version: 1, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("enqueue of unknown type was accepted")
	}
	requeued, err := enqueue.Enqueue(enqueueCtx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeSessionCleanup, Version: identityjobs.CleanupPayloadVersion,
		Payload: []byte(`{}`), IdempotencyKey: "j09-cleanup-1",
	})
	if err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}
	if !requeued.Replayed || requeued.Job.ID != queued.Job.ID {
		t.Fatalf("enqueue retry did not resolve the same job (replayed=%v)", requeued.Replayed)
	}

	lease, err := jobsapp.NewLeaseUseCase(jobsRepo, world.clock)
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}
	complete, err := jobsapp.NewCompleteUseCase(jobsRepo, world.clock)
	if err != nil {
		t.Fatalf("NewCompleteUseCase: %v", err)
	}
	fail, err := jobsapp.NewFailUseCase(jobsRepo, world.clock)
	if err != nil {
		t.Fatalf("NewFailUseCase: %v", err)
	}
	recoverLeases, err := jobsapp.NewRecoverExpiredLeasesUseCase(jobsRepo, world.clock)
	if err != nil {
		t.Fatalf("NewRecoverExpiredLeasesUseCase: %v", err)
	}
	cleanupHandler, err := identityjobs.NewCleanupHandler(
		identityapp.NewCleanupSessionsUseCase(identitypg.NewRepository(world.pool), world.clock, identitydomain.DefaultSessionPolicy()))
	if err != nil {
		t.Fatalf("NewCleanupHandler: %v", err)
	}
	registry := jobsapp.NewHandlerMap()
	if err := cleanupHandler.Register(registry); err != nil {
		t.Fatalf("register cleanup handler: %v", err)
	}
	worker, err := jobsapp.NewWorker(jobsapp.WorkerDeps{
		Lease: lease, Complete: complete, Fail: fail, Recover: recoverLeases,
		Registry: registry, Clock: world.clock, Random: world.random,
		Config: jobsapp.WorkerConfig{
			Concurrency: 1, LeaseDuration: 30 * time.Second,
			HandlerTimeout: 5 * time.Second, PollInterval: 50 * time.Millisecond,
			Backoff: jobsapp.DefaultBackoffPolicy(),
		},
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	t.Cleanup(func() { stopWorker(); <-workerDone })

	deadline := time.Now().Add(15 * time.Second)
	for {
		if journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.jobs WHERE id::text = $1 AND state = 'succeeded'`, queued.Job.ID) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker stats: %+v (job never succeeded)", worker.Stats())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.sessions WHERE revoked_at IS NOT NULL`); got < 1 {
		t.Fatal("the expired session was not revoked by the worker")
	}
	status, _, _ := journeyCall(t, server, "GET", "/api/v1/me/profile", "j09-judy-expired", "", nil)
	if status != 401 {
		t.Fatalf("revoked session = %d, want 401", status)
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/sessions", "j09-judy-fresh", "", nil)
	if status != 200 {
		t.Fatalf("fresh session after cleanup = %d, want 200", status)
	}
	if stats := worker.Stats(); stats.Succeeded < 1 {
		t.Fatalf("worker stats succeeded = %d, want at least 1", stats.Succeeded)
	}
}

//J09-MORE
