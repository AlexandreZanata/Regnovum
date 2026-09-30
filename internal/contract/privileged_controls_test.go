package contract_test

// P26-T08 — controles de operação privilegiada sobre HTTP, CLI e banco
// reais.
//
// Bootstrap só-host (segunda promoção recusa, revogação reversível com
// trilha), nenhuma rota HTTP que concede papel, step-up contra sessão
// velha, backup codes de uso único, insider revogado e em conflito
// recusados, e trilha de auditoria que o próprio banco congela: UPDATE,
// DELETE e CHECK-violação falham com a trilha intacta.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	arenasbilling "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/contract"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	platformmfa "github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// privilegedServer monta a composição total com identity de MFA/throttle
// reais: a mesma que a T01 usa para auth, mais jornadas, moderação
// completa, cobrança, privacidade e webhook de teste.
func privilegedServer(t *testing.T) (*journeyWorld, *httptest.Server, *countingMFAAudit) {
	t.Helper()
	world, audit := privilegedWorld(t)
	return world, servePrivileged(t, world, audit), audit
}

// privilegedWorld compõe as bordas sem servir: o servidor pode ser
// reconstruído sobre o mesmo banco (restart/restauração) sem perder nada.
func privilegedWorld(t *testing.T) (*journeyWorld, *countingMFAAudit) {
	t.Helper()
	world := newJourneyWorld(t)
	return world, &countingMFAAudit{}
}

// servePrivileged serve o mundo e fecha com o teste.
func servePrivileged(t *testing.T, world *journeyWorld, audit *countingMFAAudit) *httptest.Server {
	t.Helper()
	identity, sender, validator := mountAuthIdentity(t, world, audit)
	world.sender = sender
	positions := mountPositions(t, world.pool, world.clock, world.secMgr)
	arguments := mountArguments(t, world.pool, world.clock, world.secMgr)
	persuasion := mountPersuasion(t, world.pool, world.clock, world.secMgr)
	wallet := mountWallet(t, world.pool, world.secMgr)
	profiles := mountProfiles(t, world.pool, world.secMgr)
	moderation := mountJourneyModeration(t, world)
	search := mountSearch(t, world.pool, world.secMgr)
	arenas := mountArenas(t, world.pool, world.clock, world.secMgr, arenasbilling.New(postgres.NewRepository(world.pool), world.clock))
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
		t.Fatalf("compose privileged router: %v", err)
	}
	webhookMux := http.NewServeMux()
	world.webhookEndpoint(t)(webhookMux)
	outer := http.NewServeMux()
	outer.Handle("/api/v1/j09/", webhookMux)
	outer.Handle("/", handler)
	server := httptest.NewServer(world.secMgr.AuthenticateMiddleware(validator)(outer))
	t.Cleanup(server.Close)
	return server
}

// privilegedEnrolledLogin executa registro, verificação, login e MFA
// (enroll+confirm) e devolve o cookie de uma conta com segundo fator
// confirmado — o estado que a promoção só-host exige.
func privilegedEnrolledLogin(t *testing.T, world *journeyWorld, server *httptest.Server, email, password string) string {
	t.Helper()
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
	status, loginHeader, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, loginHeader)
	status, _, enrollRaw := authRawCall(t, server, "POST", "/api/v1/me/mfa/enrollment", cookie, "", nil)
	if status != 200 {
		t.Fatalf("enroll = %d, want 200", status)
	}
	var enrollment struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enrollRaw, &enrollment); err != nil || enrollment.Secret == "" {
		t.Fatalf("enrollment shows no secret: %s", string(enrollRaw))
	}
	secret, err := platformmfa.DecodeSecret(enrollment.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	code, err := platformmfa.Config{}.Code(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("totp code: %v", err)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/mfa/enrollment/confirm", cookie, `{"code":"`+code+`"}`, nil)
	if status != 200 {
		t.Fatalf("confirm = %d, want 200", status)
	}
	return cookie
}

// TestPrivilegedBootstrap ataca a promoção só-host: a primeira com MFA
// passa com trilha, a segunda recusa (instalação já tem dono), revogar
// passa com trilha, e sem admin a base volta a aceitar a primeira.
func TestPrivilegedBootstrap(t *testing.T) {
	t.Parallel()
	world, server, _ := privilegedServer(t)
	pool := world.pool
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	privilegedEnrolledLogin(t, world, server, "t08-boss@arena.example.com", "t08-boss-horse-1")
	privilegedEnrolledLogin(t, world, server, "t08-second@arena.example.com", "t08-second-horse-1")
	privilegedEnrolledLogin(t, world, server, "t08-third@arena.example.com", "t08-third-horse-1")

	administration, err := bootstrap.ComposeAdministration(bootstrap.Options{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:   pool,
		Clock:  world.clock,
	})
	if err != nil {
		t.Fatalf("ComposeAdministration: %v", err)
	}
	if _, err := administration.GrantFirstAdministrator(ctx, "t08-boss@arena.example.com"); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if _, err := administration.GrantFirstAdministrator(ctx, "t08-second@arena.example.com"); err == nil {
		t.Fatal("second bootstrap grant was accepted")
	}
	if _, err := administration.RevokeAdministrator(ctx, "t08-boss@arena.example.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := administration.RevokeAdministrator(ctx, "t08-boss@arena.example.com"); err == nil {
		t.Fatal("double revoke was accepted")
	}
	if _, err := administration.GrantFirstAdministrator(ctx, "t08-third@arena.example.com"); err != nil {
		t.Fatalf("bootstrap after full revoke: %v", err)
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.admin_roles WHERE role = 'admin' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("active admins = %d, want exactly the reversible one", got)
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.audit_events WHERE action LIKE 'administration.%'`); got < 2 {
		t.Fatalf("admin audit rows = %d, want grant+revoke recorded", got)
	}
}

// TestPrivilegedNoHttpPromotion prova a ausência da superfície: o contrato
// não declara rota que concede papel e os palpites voltam 404.
func TestPrivilegedNoHttpPromotion(t *testing.T) {
	t.Parallel()
	world, server, _ := privilegedServer(t)
	_ = world

	document, err := contract.Load("../../api/openapi.json")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	for path := range document.Paths {
		lowered := strings.ToLower(path)
		if strings.HasPrefix(lowered, "/api/v1/admin") || strings.Contains(lowered, "role") && strings.Contains(lowered, "grant") {
			t.Fatalf("contract declares a role-granting route: %s", path)
		}
	}
	for _, path := range []string{"/api/v1/admin/promote", "/api/v1/admin/roles", "/api/v1/moderation/roles", "/api/v1/moderation/grant"} {
		status, _, _ := journeyCall(t, server, "POST", path, "", `{}`, nil)
		if status != 404 && status != 405 {
			t.Fatalf("POST %s = %d, want 404 (no such surface)", path, status)
		}
	}
}

// privilegedActors semeia denunciante e moderador (papel + MFA fresco)
// e devolve os cookies e a arena do denunciante.
func privilegedActors(t *testing.T, world *journeyWorld, reporter, moderator, slug string) (string, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, email := range []string{reporter, moderator} {
		if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, 'active', now())`, email); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	journeyGrantRole(t, world.pool, moderator, "moderator")
	reporterToken := "t08-" + strings.ReplaceAll(strings.Split(reporter, "@")[0], ".", "-") + "-token"
	moderatorToken := "t08-" + strings.ReplaceAll(strings.Split(moderator, "@")[0], ".", "-") + "-token"
	journeySession(t, world.pool, reporter, reporterToken, false)
	journeySession(t, world.pool, moderator, moderatorToken, true)
	arenaID := journeyArena(t, world.pool, reporter, "Arena t08 sob revisão privilegiada", slug)
	return reporterToken, moderatorToken, arenaID
}

// privilegedModerationChain denuncia, abre, reclama e decide, devolvendo o
// caso e a ação para os testes de insider.
func privilegedModerationChain(t *testing.T, world *journeyWorld, server *httptest.Server, reporterCookie, moderatorCookie, arenaID string) (string, string) {
	t.Helper()
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", reporterCookie,
		`{"target_type":"arena","target_id":"`+arenaID+`","reason":"spam"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("report = %d, want 200 or 201", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var caseID string
	if err := world.pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id) VALUES ('arena', $1::uuid) RETURNING id::text`,
		arenaID).Scan(&caseID); err != nil {
		t.Fatalf("open case: %v", err)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", moderatorCookie, "", nil)
	if status != 200 {
		t.Fatalf("claim = %d, want 200", status)
	}
	status, _, decisionRaw := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/decisions", moderatorCookie,
		`{"action":"warning","rule":"MOD-2:warning","justification":"Privilégio t08 com escopo"}`, nil)
	if status != 200 {
		t.Fatalf("decide = %d, want 200 (%s)", status, string(decisionRaw))
	}
	var decision struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(decisionRaw, &decision); err != nil || decision.ActionID == "" {
		t.Fatalf("decision carries no action: %s", string(decisionRaw))
	}
	return caseID, decision.ActionID
}

//L08-MORE

// TestPrivilegedStepUpAgedSession prova que a idade da sessão decide o
// alto impacto: sessão velha com fator fresco reclama, mas suspender
// exige step-up e cai no 401.
func TestPrivilegedStepUpAgedSession(t *testing.T) {
	t.Parallel()
	world, server, _ := privilegedServer(t)
	reporterCookie, _, arenaID := privilegedActors(t, world, "t08-stale-reporter@arena.example.com", "t08-stale-mod@arena.example.com", "t08-stale-arena")
	ctx0, cancel0 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel0()
	if _, err := world.pool.Exec(ctx0, `UPDATE app.admin_roles SET role = 'admin' WHERE account_id = $1::uuid`,
		journeyAccountID(t, world.pool, "t08-stale-mod@arena.example.com")); err != nil {
		t.Fatalf("promote stale mod: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	staleDigest := sha256.Sum256([]byte("t08-stale-session"))
	accountID := journeyAccountID(t, world.pool, "t08-stale-mod@arena.example.com")
	if _, err := world.pool.Exec(ctx, `INSERT INTO app.sessions (account_id, token_hash, created_at, last_seen_at, expires_at, mfa_verified_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)`, accountID, staleDigest[:], now.Add(-time.Hour), now.Add(-time.Hour), now.Add(time.Hour), now); err != nil {
		t.Fatalf("seed stale session: %v", err)
	}

	var caseID string
	if err := world.pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id) VALUES ('arena', $1::uuid) RETURNING id::text`,
		arenaID).Scan(&caseID); err != nil {
		t.Fatalf("open case: %v", err)
	}
	_ = reporterCookie
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", "t08-stale-session", "", nil)
	if status != 200 {
		t.Fatalf("stale claim = %d, want 200 (low impact, fresh factor)", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/decisions", "t08-stale-session",
		`{"action":"warning","rule":"MOD-2:warning","justification":"Privilégio t08 velho"}`, nil)
	if status != 200 {
		t.Fatalf("stale warning = %d, want 200 (low impact)", status)
	}

	var profileCase string
	if err := world.pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_account_id)
		SELECT 'profile', id FROM app.accounts WHERE email = 't08-stale-reporter@arena.example.com' RETURNING id::text`).Scan(&profileCase); err != nil {
		t.Fatalf("open profile case: %v", err)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+profileCase+"/claim", "t08-stale-session", "", nil)
	if status != 200 {
		t.Fatalf("stale profile claim = %d, want 200", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+profileCase+"/decisions", "t08-stale-session",
		`{"action":"suspension","rule":"MOD-10:suspension","justification":"Privilégio t08 velho","expires_at":"2026-10-18T12:00:00Z"}`, nil)
	if status != 401 {
		t.Fatalf("stale suspension = %d, want 401 step_up_required", status)
	}
}

// TestPrivilegedBackupCodes gasta um recovery code uma vez: o primeiro
// eleva, o replay cai, o errado cai, e a auditoria conta um uso.
func TestPrivilegedBackupCodes(t *testing.T) {
	t.Parallel()
	world, server, audit := privilegedServer(t)

	const email = "t08-backup@arena.example.com"
	const password = "t08-backup-horse-1"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, _ := identitydomain.ParseEmail(email)
	verifyToken, _ := world.sender.LastTokenForEmail(address)
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+verifyToken, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, loginHeader, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, loginHeader)

	status, _, enrollRaw := authRawCall(t, server, "POST", "/api/v1/me/mfa/enrollment", cookie, "", nil)
	if status != 200 {
		t.Fatalf("enroll = %d, want 200", status)
	}
	var enrollment struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enrollRaw, &enrollment); err != nil || enrollment.Secret == "" {
		t.Fatalf("enrollment shows no secret: %s", string(enrollRaw))
	}
	secret, err := platformmfa.DecodeSecret(enrollment.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	code, err := platformmfa.Config{}.Code(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("totp code: %v", err)
	}
	status, _, confirmRaw := journeyCall(t, server, "POST", "/api/v1/me/mfa/enrollment/confirm", cookie, `{"code":"`+code+`"}`, nil)
	if status != 200 {
		t.Fatalf("confirm = %d, want 200 (%s)", status, string(confirmRaw))
	}
	var confirmed struct {
		Codes []string `json:"backup_codes"`
	}
	if err := json.Unmarshal(confirmRaw, &confirmed); err != nil || len(confirmed.Codes) == 0 {
		t.Fatalf("confirm shows no backup codes: %s", string(confirmRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/mfa/recovery", cookie, `{"code":"`+confirmed.Codes[0]+`"}`, nil)
	if status != 200 {
		t.Fatalf("recovery = %d, want 200", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/mfa/recovery", cookie, `{"code":"`+confirmed.Codes[0]+`"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("recovery replay = %d, want 4xx (single use)", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/mfa/recovery", cookie, `{"code":"000000-000000"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("recovery wrong code = %d, want 4xx", status)
	}
	if _, recovery := audit.counts(); recovery != 1 {
		t.Fatalf("backup uses audited = %d, want exactly 1", recovery)
	}
}

// TestPrivilegedInsiderRevokedAndConflict recusa o insider sem poder: papel
// revogado não reclama, e reclamar o próprio alvo é conflito, não moderação.
func TestPrivilegedInsiderRevokedAndConflict(t *testing.T) {
	t.Parallel()
	world, server, _ := privilegedServer(t)
	pool := world.pool

	reporterCookie, moderatorCookie, _ := privilegedActors(t, world, "t08-self-reporter@arena.example.com", "t08-self-mod@arena.example.com", "t08-self-arena")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `UPDATE app.admin_roles SET revoked_at = now() WHERE account_id = $1::uuid`,
		journeyAccountID(t, pool, "t08-self-mod@arena.example.com")); err != nil {
		t.Fatalf("revoke role: %v", err)
	}
	var caseID string
	if err := pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id)
		SELECT 'arena', a.id FROM app.arenas a JOIN app.accounts c ON c.id = a.creator_id
		WHERE c.email = 't08-self-reporter@arena.example.com' RETURNING app.moderation_cases.id::text`).Scan(&caseID); err != nil {
		// Sem arena do denunciante ainda: abre sobre qualquer alvo próprio.
		_ = reporterCookie
		if err := pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_account_id)
			SELECT 'profile', id FROM app.accounts WHERE email = 't08-self-mod@arena.example.com' RETURNING id::text`).Scan(&caseID); err != nil {
			t.Fatalf("open case: %v", err)
		}
	}
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", moderatorCookie, "", nil)
	if status != 403 {
		t.Fatalf("revoked claim = %d, want 403", status)
	}

	// Conflito de interesse: o moderador denuncia a própria arena e tenta
	// reclamar o caso do próprio conteúdo.
	ownArena := journeyArena(t, pool, "t08-self-mod@arena.example.com", "Arena t08 do próprio moderador", "t08-self-mod-arena")
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", moderatorCookie,
		`{"target_type":"arena","target_id":"`+ownArena+`","reason":"spam"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("self report = %d, want 200 or 201", status)
	}
	var selfCase string
	if err := pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id) VALUES ('arena', $1::uuid) RETURNING id::text`,
		ownArena).Scan(&selfCase); err != nil {
		t.Fatalf("open self case: %v", err)
	}
	// Reativa para isolar o conflito da revogação.
	if _, err := pool.Exec(ctx, `UPDATE app.admin_roles SET revoked_at = NULL WHERE account_id = $1::uuid`,
		journeyAccountID(t, pool, "t08-self-mod@arena.example.com")); err != nil {
		t.Fatalf("unrevoke role: %v", err)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+selfCase+"/claim", moderatorCookie, "", nil)
	if status != 409 {
		t.Fatalf("self claim = %d, want 409 conflict of interest", status)
	}
}

// TestPrivilegedAuditTamperEvident prova a trilha congelada no banco: cada
// UPDATE e DELETE morre no trigger, CHECK barra ação e metadata fora da
// allowlist, e as linhas dos fluxos reais seguem intactas.
func TestPrivilegedAuditTamperEvident(t *testing.T) {
	t.Parallel()
	world, server, _ := privilegedServer(t)
	pool := world.pool

	// A promoção real planta as primeiras linhas da trilha.
	privilegedEnrolledLogin(t, world, server, "t08-audit-mod@arena.example.com", "t08-audit-horse-1")
	administration, err := bootstrap.ComposeAdministration(bootstrap.Options{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:   pool,
		Clock:  world.clock,
	})
	if err != nil {
		t.Fatalf("ComposeAdministration: %v", err)
	}
	if _, err := administration.GrantFirstAdministrator(context.Background(), "t08-audit-mod@arena.example.com"); err != nil {
		t.Fatalf("bootstrap grant: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ('t08-audit-reporter@arena.example.com', 'active', now())`); err != nil {
		t.Fatalf("seed reporter: %v", err)
	}
	journeySession(t, pool, "t08-audit-reporter@arena.example.com", "t08-audit-reporter-token", false)
	journeySession(t, pool, "t08-audit-mod@arena.example.com", "t08-audit-mod-token", true)
	_, _ = privilegedModerationChain(t, world, server, "t08-audit-reporter-token", "t08-audit-mod-token",
		journeyArena(t, pool, "t08-audit-reporter@arena.example.com", "Arena t08 auditada", "t08-audit-arena-seed"))
	before := journeyQueryInt(t, pool, `SELECT count(*) FROM app.audit_events`)
	if before < 1 {
		t.Fatal("no audit rows from the real flows")
	}

	attackCtx, attackCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer attackCancel()
	if _, err := pool.Exec(attackCtx, `UPDATE app.audit_events SET reason_code = 'tampered' WHERE id IN (SELECT id FROM app.audit_events LIMIT 1)`); err == nil {
		t.Fatal("UPDATE on audit_events was accepted (append-only trigger missing?)")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE failed without the immutability signal: %v", err)
	}
	if _, err := pool.Exec(attackCtx, `DELETE FROM app.audit_events WHERE id IN (SELECT id FROM app.audit_events LIMIT 1)`); err == nil {
		t.Fatal("DELETE on audit_events was accepted (append-only trigger missing?)")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE failed without the immutability signal: %v", err)
	}
	accountID := journeyAccountID(t, pool, "t08-audit-mod@arena.example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code)
		VALUES ($1::uuid, 'not a code', 'case', 'x', 'y')`, accountID); err == nil {
		t.Fatal("INSERT with bad action was accepted (CHECK missing?)")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code, metadata)
		VALUES ($1::uuid, 'moderation.decide', 'case', 'x', 'y', '{"email":"evil@arena.example.com"}')`, accountID); err == nil {
		t.Fatal("INSERT with forbidden metadata was accepted (allowlist CHECK missing?)")
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.audit_events`); got != before {
		t.Fatalf("audit rows changed %d -> %d under attack", before, got)
	}
}
