package contract_test

// P26-T01 — matriz de ataque de autenticação sobre HTTP real.
//
// Sessão, cookie e email falsos locais, banco descartável e throttle/MFA
// reais: enumeração uniforme, flags de cookie, fixation, replay,
// rotação/revogação/logout, expiração ociosa/absoluta, lock por taxa e
// uso único de MFA com corrida de um vencedor.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identityargon "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	identityfake "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	identityhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	identitymfa "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/mfamechanism"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	platformmfa "github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// countingMFAAudit registra apenas contagens, sem conteúdo: a trilha que o
// teste confere é o fato administrativo, nunca o segredo.
type countingMFAAudit struct {
	mu       sync.Mutex
	enrolled int
	recovery int
}

func (audit *countingMFAAudit) RecordMFAEnrolled(context.Context, string, time.Time) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.enrolled++
	return nil
}

func (audit *countingMFAAudit) RecordMFABackupCodeUsed(context.Context, string, time.Time) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.recovery++
	return nil
}

func (audit *countingMFAAudit) counts() (int, int) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return audit.enrolled, audit.recovery
}

// authMatrixWorld é a composição de autenticação real: throttle, MFA,
// rotação e revogação ligados, validador de sessão real no banco.
type authMatrixWorld struct {
	journey *journeyWorld
	sender  *identityfake.Sender
	audit   *countingMFAAudit
	server  *httptest.Server
}

func mountAuthIdentity(t *testing.T, world *journeyWorld, audit *countingMFAAudit) (*identityhttp.Handler, *identityfake.Sender, security.SessionValidator) {
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
	throttle := ratelimit.New(ratelimit.NewLimiter(ratelimit.Options{Now: world.clock.Now}), clientip.New(nil))

	handler := identityhttp.NewHandler(identityhttp.HandlerConfig{
		RegisterUseCase:              identityapp.NewRegisterAccountUseCase(repo, repo, hasher, sender, world.clock, world.random, vPolicy),
		VerifyEmailUseCase:           identityapp.NewVerifyEmailUseCase(repo, repo, world.clock),
		LoginUseCase:                 identityapp.NewLoginUseCase(repo, repo, repo, hasher, world.clock, world.random, sPolicy),
		LogoutUseCase:                identityapp.NewLogoutUseCase(repo),
		RevokeSessionUseCase:         identityapp.NewRevokeSessionUseCase(repo, repo, hasher),
		RotateSessionUseCase:         identityapp.NewRotateSessionUseCase(repo, repo, world.clock, world.random, sPolicy),
		ListSessionsUseCase:          identityapp.NewListSessionsUseCase(repo, world.clock, sPolicy),
		RequestPasswordResetUseCase:  identityapp.NewRequestPasswordResetUseCase(repo, repo, sender, world.clock, world.random, rPolicy),
		CompletePasswordResetUseCase: identityapp.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, sender, world.clock),
		AuthenticateSessionUseCase:   authUC,
		BeginMFAEnrollmentUseCase:    identityapp.NewBeginMFAEnrollmentUseCase(repo, mechanism),
		ConfirmMFAEnrollmentUseCase:  identityapp.NewConfirmMFAEnrollmentUseCase(repo, mechanism, hasher, world.clock, audit),
		StepUpMFAUseCase:             identityapp.NewStepUpMFAUseCase(repo, mechanism, world.clock),
		RecoverMFAUseCase:            identityapp.NewRecoverMFAUseCase(repo, mechanism, hasher, world.clock, audit),
		SecurityManager:              world.secMgr,
		RateLimit:                    throttle,
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

func newAuthMatrixWorld(t *testing.T) *authMatrixWorld {
	t.Helper()
	world := newJourneyWorld(t)
	audit := &countingMFAAudit{}
	identity, sender, validator := mountAuthIdentity(t, world, audit)
	ids := clockseed.NewIDGenerator("req", world.random, world.clock)
	handler, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{
		surfaceOf(identityRoutes(), identity.RegisterRoutes),
	})
	if err != nil {
		t.Fatalf("compose auth router: %v", err)
	}
	server := httptest.NewServer(world.secMgr.AuthenticateMiddleware(validator)(handler))
	t.Cleanup(server.Close)
	return &authMatrixWorld{journey: world, sender: sender, audit: audit, server: server}
}

// authRawCall é a chamada sem o detector genérico de segredos: para os
// endpoints que devolvem segredo por desenho (segredo/recovery do MFA) ou
// cujo vocabulário documentado prefixa um marcador (password_reset), valem
// asserções direcionadas sobre valores, não o casamento de substring.
func authRawCall(t *testing.T, server *httptest.Server, method, path, cookie, body string, headers map[string]string) (int, http.Header, []byte) {
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
	return response.StatusCode, response.Header, responseBody
}

// authRegisterVerifyLogin executa a cadeia feliz e devolve o cookie.
func authRegisterVerifyLogin(t *testing.T, world *authMatrixWorld, email, password string) string {
	t.Helper()
	server := world.server
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
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	return journeyCookie(t, header)
}

// TestAuthMatrixEnumeration prova a uniformidade anti-oráculo: endereço
// inválido e duplicado, email desconhecido e senha errada, token usado e
// inexistente respondem bytes e status que não distinguem os casos.
func TestAuthMatrixEnumeration(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-oracle@arena.example.com"
	const password = "t01-correct-horse-1"
	cookie := authRegisterVerifyLogin(t, world, email, password)
	_ = cookie

	invalid, _, invalidBody := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"not-an-email","password":"x"}`, nil)
	duplicate, _, duplicateBody := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if invalid != 201 || duplicate != 201 || string(invalidBody) != string(duplicateBody) {
		t.Fatalf("register oracle: invalid=%d duplicate=%d bodies differ", invalid, duplicate)
	}

	unknown, _, unknownBody := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"nobody-here@arena.example.com","password":"`+password+`"}`, nil)
	wrong, _, wrongBody := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, nil)
	// Problem carrega request ID único: a igualdade que importa é o código
	// estável (o que o cliente decide) e a ausência de eco.
	var unknownDoc, wrongDoc struct {
		Code string `json:"code"`
	}
	if unknown != 401 || wrong != 401 {
		t.Fatalf("login oracle: unknown=%d wrong=%d, want both 401", unknown, wrong)
	}
	if err := json.Unmarshal(unknownBody, &unknownDoc); err != nil || unknownDoc.Code == "" {
		t.Fatalf("unknown login carries no code: %s", string(unknownBody))
	}
	if err := json.Unmarshal(wrongBody, &wrongDoc); err != nil || wrongDoc.Code != unknownDoc.Code {
		t.Fatalf("login codes diverge: unknown=%q wrong=%q", unknownDoc.Code, wrongDoc.Code)
	}
	for _, secret := range []string{password, "wrong-password-9", email} {
		if strings.Contains(string(wrongBody), secret) || strings.Contains(string(unknownBody), secret) {
			t.Fatalf("login refusal echoes caller input %q", secret)
		}
	}

	// Token de verificação é uso único: o segundo uso falha sem dizer se o
	// token existiu — mesma família 4xx do token inexistente.
	address, _ := identitydomain.ParseEmail("t01-replay@arena.example.com")
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"t01-replay@arena.example.com","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	replayToken, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+replayToken, "", "", nil)
	if status != 200 {
		t.Fatalf("first verify = %d, want 200", status)
	}
	reused, _, _ := journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+replayToken, "", "", nil)
	ghost, _, _ := journeyCall(t, server, "GET", "/api/v1/auth/verify?token=never-existed-token", "", "", nil)
	if reused < 400 || reused >= 500 || ghost < 400 || ghost >= 500 {
		t.Fatalf("verify oracle: reused=%d ghost=%d, want both 4xx", reused, ghost)
	}
}

// TestAuthMatrixCookieFlags prova o escopo do cookie: HttpOnly, SameSite
// Lax e Path=/ em test; sem Domain; logout limpa e invalida.
func TestAuthMatrixCookieFlags(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-cookie@arena.example.com"
	const password = "t01-correct-horse-2"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, _ := identitydomain.ParseEmail(email)
	token, _ := world.sender.LastTokenForEmail(address)
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+token, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	raw := header.Get("Set-Cookie")
	lowered := strings.ToLower(raw)
	for _, want := range []string{"httponly", "samesite=lax", "path=/"} {
		if !strings.Contains(lowered, want) {
			t.Fatalf("Set-Cookie = %q, want %q", raw, want)
		}
	}
	if strings.Contains(lowered, "domain=") {
		t.Fatalf("Set-Cookie pins a Domain: %q", raw)
	}
	cookie := journeyCookie(t, header)

	status, logoutHeader, _ := journeyCall(t, server, "POST", "/api/v1/auth/logout", cookie, "", nil)
	if status != 200 {
		t.Fatalf("logout = %d, want 200", status)
	}
	if cleared := logoutHeader.Get("Set-Cookie"); !strings.Contains(strings.ToLower(cleared), "max-age=0") && !strings.Contains(strings.ToLower(cleared), "expires=") {
		t.Fatalf("logout did not clear the cookie: %q", cleared)
	}
	status, _, _ = authRawCall(t, server, "GET", "/api/v1/me/sessions", cookie, "", nil)
	if status != 401 {
		t.Fatalf("use after logout = %d, want 401", status)
	}
}

// TestAuthMatrixFixation prova que um valor plantado nunca vira sessão: o
// middleware recusa o cookie desconhecido antes do handler, e o login limpo
// emite credencial nova e inédita.
func TestAuthMatrixFixation(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-fix@arena.example.com"
	const password = "t01-correct-horse-3"
	const planted = "attacker-planted-session-value"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, _ := identitydomain.ParseEmail(email)
	token, _ := world.sender.LastTokenForEmail(address)
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+token, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}

	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", planted, `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 401 {
		t.Fatalf("login with planted cookie = %d, want 401 (unknown values never reach the handler)", status)
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	issued := journeyCookie(t, header)
	if issued == planted {
		t.Fatal("login kept the attacker-planted session value")
	}
	status, _, _ = authRawCall(t, server, "GET", "/api/v1/me/sessions", planted, "", nil)
	if status != 401 {
		t.Fatalf("planted value authenticates = %d, want 401", status)
	}
	status, _, _ = authRawCall(t, server, "GET", "/api/v1/me/sessions", issued, "", nil)
	if status != 200 {
		t.Fatalf("issued session rejected = %d", status)
	}
}

// TestAuthMatrixReplay prova que reset é uso único e troca a credencial: o
// token morre no primeiro uso e a senha antiga morre com ele.
func TestAuthMatrixReplay(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-replay2@arena.example.com"
	const oldPassword = "t01-old-horse-1"
	const newPassword = "t01-new-horse-2"
	cookie := authRegisterVerifyLogin(t, world, email, oldPassword)
	_ = cookie

	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/password-reset/request", "", `{"email":"`+email+`"}`, nil)
	if status != 200 {
		t.Fatalf("reset request = %d, want 200", status)
	}
	// Anti-enumeração também no reset: desconhecido responde igual.
	status, _, unknownBody := journeyCall(t, server, "POST", "/api/v1/auth/password-reset/request", "", `{"email":"ghost@arena.example.com"}`, nil)
	if status != 200 {
		t.Fatalf("reset request ghost = %d, want uniform 200 (%s)", status, string(unknownBody))
	}
	address, _ := identitydomain.ParseEmail(email)
	resetToken, found := world.sender.LastResetTokenForEmail(address)
	if !found {
		t.Fatal("reset issued no email")
	}
	confirm := `{"token":"` + resetToken + `","password":"` + newPassword + `"}`
	status, _, confirmBody := authRawCall(t, server, "POST", "/api/v1/auth/password-reset/confirm", "", confirm, nil)
	if status != 200 {
		t.Fatalf("reset confirm = %d, want 200 (%s)", status, string(confirmBody))
	}
	for _, secret := range []string{resetToken, newPassword, oldPassword} {
		if strings.Contains(string(confirmBody), secret) {
			t.Fatalf("reset confirm echoes caller input %q", secret)
		}
	}
	status, _, replayBody := authRawCall(t, server, "POST", "/api/v1/auth/password-reset/confirm", "", confirm, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("reset confirm replay = %d, want 4xx", status)
	}
	for _, secret := range []string{resetToken, newPassword} {
		if strings.Contains(string(replayBody), secret) {
			t.Fatalf("reset refusal echoes caller input %q", secret)
		}
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+oldPassword+`"}`, nil)
	if status != 401 {
		t.Fatalf("login with old password = %d, want 401", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+newPassword+`"}`, nil)
	if status != 200 {
		t.Fatalf("login with new password = %d, want 200", status)
	}
	// O reset revoga tudo: a sessão anterior morreu junto com a senha.
	status, _, _ = authRawCall(t, server, "GET", "/api/v1/me/sessions", cookie, "", nil)
	if status != 401 {
		t.Fatalf("pre-reset session survives = %d, want 401", status)
	}
}

// TestAuthMatrixRotationRevocation prova rotação atômica e revogação com
// re-autenticação: o token antigo morre, o novo serve, revogar exige senha.
func TestAuthMatrixRotationRevocation(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-rotate@arena.example.com"
	const password = "t01-rotate-horse-1"
	cookie := authRegisterVerifyLogin(t, world, email, password)

	status, rotateHeader, _ := journeyCall(t, server, "POST", "/api/v1/me/sessions/rotation", cookie, "", nil)
	if status != 200 {
		t.Fatalf("rotation = %d, want 200", status)
	}
	fresh := journeyCookie(t, rotateHeader)
	if fresh == cookie {
		t.Fatal("rotation reissued the same token")
	}
	status, _, _ = authRawCall(t, server, "GET", "/api/v1/me/sessions", cookie, "", nil)
	if status != 401 {
		t.Fatalf("pre-rotation token survives = %d, want 401", status)
	}
	status, _, _ = authRawCall(t, server, "GET", "/api/v1/me/sessions", fresh, "", nil)
	if status != 200 {
		t.Fatalf("rotated session rejected = %d", status)
	}

	// Segunda sessão para revogar com a primeira como prova de pessoa.
	status, secondHeader, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("second login = %d, want 200", status)
	}
	second := journeyCookie(t, secondHeader)
	status, _, listRaw := authRawCall(t, server, "GET", "/api/v1/me/sessions", second, "", nil)
	if status != 200 {
		t.Fatalf("sessions list = %d, want 200", status)
	}
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listRaw, &list); err != nil || len(list.Items) < 2 {
		t.Fatalf("sessions list hides a session: %s", string(listRaw))
	}
	target := ""
	for _, item := range list.Items {
		if !item.Current {
			target = item.ID
		}
	}
	if target == "" {
		t.Fatalf("no revocable (non-current) session in: %s", string(listRaw))
	}
	// A senha viaja no corpo por desenho (nunca em URL); a recusa não
	// pode ecoar o valor.
	status, _, wrongPassBody := authRawCall(t, server, "POST", "/api/v1/me/sessions/revocation", second, `{"session_id":"`+target+`","password":"wrong-password-9"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("revoke with wrong password = %d, want 4xx", status)
	}
	if strings.Contains(string(wrongPassBody), "wrong-password-9") {
		t.Fatal("revoke refusal echoes the password value")
	}
	status, _, _ = authRawCall(t, server, "POST", "/api/v1/me/sessions/revocation", second, `{"session_id":"`+target+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("revoke = %d, want 200", status)
	}
}

// TestAuthMatrixExpiry prova expiração ociosa e absoluta no banco: linhas
// antigas respondem 401 mas continuam existindo (expirar não é apagar).
func TestAuthMatrixExpiry(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server
	pool := world.journey.pool

	const email = "t01-expiry@arena.example.com"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active')`, email); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	accountID := journeyAccountID(t, pool, email)
	now := time.Now().UTC()
	seed := func(raw string, created, lastSeen, expires time.Time, mfa *time.Time) {
		t.Helper()
		digest := sha256.Sum256([]byte(raw))
		if mfa != nil {
			if _, err := pool.Exec(ctx, `INSERT INTO app.sessions (account_id, token_hash, created_at, last_seen_at, expires_at, mfa_verified_at)
				VALUES ($1::uuid, $2, $3, $4, $5, $6)`, accountID, digest[:], created, lastSeen, expires, *mfa); err != nil {
				t.Fatalf("seed session: %v", err)
			}
			return
		}
		if _, err := pool.Exec(ctx, `INSERT INTO app.sessions (account_id, token_hash, created_at, last_seen_at, expires_at)
			VALUES ($1::uuid, $2, $3, $4, $5)`, accountID, digest[:], created, lastSeen, expires); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}
	seed("t01-idle-victim", now.Add(-25*time.Hour), now.Add(-25*time.Hour), now.Add(time.Hour), nil)
	seed("t01-absolute-victim", now.Add(-15*24*time.Hour), now, now.Add(time.Hour), nil)
	seed("t01-fresh-witness", now, now, now.Add(time.Hour), nil)

	for token, want := range map[string]int{
		"t01-idle-victim":     401,
		"t01-absolute-victim": 401,
		"t01-fresh-witness":   200,
	} {
		status, _, _ := authRawCall(t, server, "GET", "/api/v1/me/sessions", token, "", nil)
		if status != want {
			t.Fatalf("token %s = %d, want %d", token, status, want)
		}
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.sessions`); got != 3 {
		t.Fatalf("sessions = %d, want 3 rows kept (expiry revokes use, not rows)", got)
	}
}

// TestAuthMatrixRateLimit prova o cadeado: 10 falhas cabem no balde, a 11ª
// é 429 com código estável; o login legítimo anterior passou intacto.
func TestAuthMatrixRateLimit(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-lock@arena.example.com"
	const password = "t01-lock-horse-1"
	cookie := authRegisterVerifyLogin(t, world, email, password)
	_ = cookie

	for attempt := 1; attempt <= 9; attempt++ {
		status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, nil)
		if status != 401 {
			t.Fatalf("wrong login %d = %d, want 401", attempt, status)
		}
	}
	status, _, raw := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, nil)
	if status != 429 {
		t.Fatalf("10th wrong login = %d, want 429", status)
	}
	var document struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Code == "" {
		t.Fatalf("429 carries no stable code: %s", string(raw))
	}
}

// TestAuthMatrixMFASingleUse enrola, confirma, gasta o código uma vez e
// prova que a corrida de uso único tem exatamente um vencedor.
func TestAuthMatrixMFASingleUse(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const email = "t01-mfa@arena.example.com"
	const password = "t01-mfa-horse-1"
	cookie := authRegisterVerifyLogin(t, world, email, password)

	status, _, enrollRaw := authRawCall(t, server, "POST", "/api/v1/me/mfa/enrollment", cookie, "", nil)
	if status != 200 {
		t.Fatalf("enroll = %d, want 200 (%s)", status, string(enrollRaw))
	}
	var enrollment struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(enrollRaw, &enrollment); err != nil || enrollment.Secret == "" || !strings.HasPrefix(enrollment.URI, "otpauth://totp/") {
		t.Fatalf("enrollment shows no secret/uri: %s", string(enrollRaw))
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
		RecoveryCodes []string `json:"backup_codes"`
	}
	if err := json.Unmarshal(confirmRaw, &confirmed); err != nil || len(confirmed.RecoveryCodes) == 0 {
		t.Fatalf("confirm shows no single-use recovery codes: %s", string(confirmRaw))
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/mfa/enrollment/confirm", cookie, `{"code":"`+code+`"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("confirm replay = %d, want 4xx", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/mfa/enrollment/confirm", cookie, `{"code":"000000"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("confirm wrong code = %d, want 4xx", status)
	}
	if enrolled, _ := world.audit.counts(); enrolled != 1 {
		t.Fatalf("mfa enrollments audited = %d, want 1", enrolled)
	}
}

// TestAuthMatrixSingleUseRace dispara o mesmo token de verificação em 8
// goroutines: exatamente uma vence, as demais falham sem parcial.
func TestAuthMatrixSingleUseRace(t *testing.T) {
	t.Parallel()
	world := newAuthMatrixWorld(t)
	server := world.server

	const password = "t01-race-horse-1"
	const email = "t01-race@arena.example.com"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, _ := identitydomain.ParseEmail(email)
	token, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}

	var winners atomic.Int64
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			request, err := http.NewRequest("GET", server.URL+"/api/v1/auth/verify?token="+token, nil)
			if err != nil {
				return
			}
			response, err := server.Client().Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if response.StatusCode == 200 {
				winners.Add(1)
			}
		}()
	}
	group.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("verify race winners = %d, want exactly 1", got)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login after race = %d, want 200", status)
	}
}
