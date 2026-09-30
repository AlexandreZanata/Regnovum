package contract_test

// P26-T05 — limites de abuso sobre HTTP real e no limiter.
//
// Bursts por IP/conta/ação com 429 e Retry-After corretos, janela que
// expira (relógio injetado, sem dormir uma hora), memória do limiter
// limitada por capacidade e sweep, slowloris contido pelo ReadTimeout do
// servidor hardened, corpo grande e alta cardinalidade recusados cedo,
// cancelamento e flood concorrente sem esgotar o servidor, e spoof de IP
// sem efeito. Tráfego legítimo volta a passar quando a janela expira:
// bloqueio é temporário por construção, nunca permanente.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// abuseAuthWorld serve só o identity com throttle real (composição da T01).
func abuseAuthWorld(t *testing.T) *authMatrixWorld {
	t.Helper()
	return newAuthMatrixWorld(t)
}

// TestAbuseLoginBurst prova o cadeado do login: 10 erros cabem, o 11º é
// 429 com Retry-After em segundos e código estável.
func TestAbuseLoginBurst(t *testing.T) {
	t.Parallel()
	world := abuseAuthWorld(t)
	server := world.server

	const email = "t05-lock@arena.example.com"
	const password = "t05-lock-horse-1"
	cookie := authRegisterVerifyLogin(t, world, email, password)
	_ = cookie

	// O login legítimo acima já consumiu 1 do balde de 10: restam 9.
	for attempt := 1; attempt <= 9; attempt++ {
		status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, nil)
		if status != 401 {
			t.Fatalf("wrong login %d = %d, want 401", attempt, status)
		}
	}
	status, header, raw := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, nil)
	if status != 429 {
		t.Fatalf("10th wrong login = %d, want 429", status)
	}
	retryAfter := header.Get("Retry-After")
	seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter))
	if err != nil || seconds < 1 || seconds > 60 {
		t.Fatalf("Retry-After = %q, want delta-seconds within the minute window", retryAfter)
	}
	var document struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Code == "" {
		t.Fatalf("429 carries no stable code: %s", string(raw))
	}
	// O mesmo par, mesma resposta: o cadeado não é oráculo.
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"ghost@arena.example.com","password":"wrong-password-9"}`, nil)
	if status != 429 {
		t.Fatalf("ghost login under lock = %d, want the same 429", status)
	}
}

// TestAbuseRegisterBurst prova o balde de registro: 5 criações passam, a
// 6ª é 429 com Retry-After.
func TestAbuseRegisterBurst(t *testing.T) {
	t.Parallel()
	world := abuseAuthWorld(t)
	server := world.server

	for index := 1; index <= 5; index++ {
		status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "",
			fmt.Sprintf(`{"email":"t05-burst-%d@arena.example.com","password":"t05-correct-horse-1"}`, index), nil)
		if status != 201 {
			t.Fatalf("register %d = %d, want 201", index, status)
		}
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "",
		`{"email":"t05-burst-6@arena.example.com","password":"t05-correct-horse-1"}`, nil)
	if status != 429 {
		t.Fatalf("6th register = %d, want 429", status)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
}

// steppingNow é o relógio injetado que viaja no tempo sem dormir.
type steppingNow struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *steppingNow) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *steppingNow) advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

// TestAbuseWindowExpires prova que o bloqueio é temporário e isolado por
// dimensão: esgota um IP, outro passa; viaja além da janela, o primeiro
// passa de novo; IPv6 tem balde próprio.
func TestAbuseWindowExpires(t *testing.T) {
	t.Parallel()
	clock := &steppingNow{now: time.Now().UTC()}
	limiter := ratelimit.NewLimiter(ratelimit.Options{Now: clock.Now})
	ctx := context.Background()
	allow := func(ip string) (bool, time.Duration) {
		decision, err := limiter.Allow(ctx, ratelimit.ActionAuthLogin, ratelimit.Subject{Kind: ratelimit.SubjectAddress, Value: ip})
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		return decision.Allowed, decision.RetryAfter
	}

	for index := 0; index < 10; index++ {
		if allowed, _ := allow("198.51.100.7"); !allowed {
			t.Fatalf("attempt %d refused inside the burst", index)
		}
	}
	if allowed, _ := allow("198.51.100.7"); allowed {
		t.Fatal("11th attempt allowed past the burst")
	}
	if allowed, _ := allow("198.51.100.8"); !allowed {
		t.Fatal("another address blocked by someone else's burst")
	}
	if allowed, _ := allow("2001:db8::7"); !allowed {
		t.Fatal("IPv6 shares the IPv4 bucket")
	}
	clock.advance(61 * time.Second)
	if allowed, _ := allow("198.51.100.7"); !allowed {
		t.Fatal("address still blocked past the minute window (permanent block)")
	}
}

// TestAbuseMemoryBounded prova os dois tetos do limiter: capacidade evicta
// LRU e o sweep recolhe o ocioso — 20 chaves distintas nunca viram 20
// entradas permanentes.
func TestAbuseMemoryBounded(t *testing.T) {
	t.Parallel()
	clock := &steppingNow{now: time.Now().UTC()}
	limiter := ratelimit.NewLimiter(ratelimit.Options{Capacity: 8, Now: clock.Now})
	ctx := context.Background()
	for index := 0; index < 20; index++ {
		_, _ = limiter.Allow(ctx, ratelimit.ActionAuthLogin,
			ratelimit.Subject{Kind: ratelimit.SubjectAddress, Value: fmt.Sprintf("198.51.100.%d", index)})
	}
	if got := limiter.Len(); got > 8 {
		t.Fatalf("limiter holds %d keys past capacity 8", got)
	}
	clock.advance(2 * time.Hour)
	limiter.Sweep()
	if got := limiter.Len(); got != 0 {
		t.Fatalf("sweep kept %d idle keys", got)
	}
}

// TestAbuseSlowloris serve o mux real no servidor hardened com ReadTimeout
// curto: o gotejamento lento aborta no timeout e o tráfego rápido segue.
func TestAbuseSlowloris(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	audit := &countingMFAAudit{}
	identity, _, validator := mountAuthIdentity(t, world, audit)
	ids := clockseed.NewIDGenerator("req", world.random, world.clock)
	handler, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{
		surfaceOf(identityRoutes(), identity.RegisterRoutes),
	})
	if err != nil {
		t.Fatalf("compose mux: %v", err)
	}
	secured := world.secMgr.AuthenticateMiddleware(validator)(handler)
	server, err := httpserver.New(httpserver.Options{
		Addr:        "127.0.0.1:0",
		Handler:     secured,
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		ReadTimeout: 600 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("httpserver.New: %v", err)
	}
	if err := server.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	url := "http://" + server.Addr() + "/api/v1/auth/register"

	pr, pw := io.Pipe()
	request, err := http.NewRequest("POST", url, pr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	type result struct {
		status int
		err    error
	}
	slow := make(chan result, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			slow <- result{err: err}
			return
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		slow <- result{status: response.StatusCode}
	}()
	// Goteja mais devagar que o ReadTimeout: o servidor deve abortar, não
	// esperar para sempre.
	for index := 0; index < 10; index++ {
		time.Sleep(200 * time.Millisecond)
		if _, err := pw.Write([]byte("x")); err != nil {
			break
		}
	}
	_ = pw.Close()
	select {
	case outcome := <-slow:
		if outcome.err == nil && outcome.status == 201 {
			t.Fatal("slow drip completed as a registration (no read bound enforced)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("slow connection never resolved (unbounded)")
	}

	fast, err := http.Post("http://"+server.Addr()+"/api/v1/auth/register", "application/json",
		strings.NewReader(`{"email":"t05-after-slow@arena.example.com","password":"t05-correct-horse-1"}`))
	if err != nil {
		t.Fatalf("fast request after slowloris: %v", err)
	}
	defer fast.Body.Close()
	_, _ = io.Copy(io.Discard, fast.Body)
	if fast.StatusCode != 201 {
		t.Fatalf("fast request after slowloris = %d, want 201", fast.StatusCode)
	}
}

// TestAbuseBigBody prova recusa rápida de corpo grande com conexão reusável.
func TestAbuseBigBody(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	big := `{"email":"` + strings.Repeat("b", 5<<20) + `@arena.example.com","password":"x"}`
	started := time.Now()
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", big, nil)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("5MiB body took %s", elapsed)
	}
	if status < 400 || status >= 500 {
		t.Fatalf("5MiB body = %d, want 4xx", status)
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/arenas", "", "", nil)
	if status != 200 {
		t.Fatalf("connection not reusable after big body = %d", status)
	}
}

// TestAbuseHighCardinality prova que dicionário gigante não trava o parse.
func TestAbuseHighCardinality(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	var document strings.Builder
	document.WriteString("{")
	for index := 0; index < 20000; index++ {
		if index > 0 {
			document.WriteString(",")
		}
		fmt.Fprintf(&document, `"k%d":"v%d"`, index, index)
	}
	document.WriteString("}")
	started := time.Now()
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", document.String(), nil)
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("20k-key body took %s", elapsed)
	}
	if status < 400 || status >= 500 {
		t.Fatalf("20k-key body = %d, want 4xx", status)
	}
}

// TestAbuseClientCancel prova que o cancelamento do cliente não encrava o
// servidor: a próxima chamada serve normal.
func TestAbuseClientCancel(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/auth/register", strings.NewReader(`{"email":"t05-cancel@arena.example.com","password":"x"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := server.Client().Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled request never returned")
	}
	status, _, _ := journeyCall(t, server, "GET", "/api/v1/arenas", "", "", nil)
	if status != 200 {
		t.Fatalf("server wedged after cancel = %d", status)
	}
}

// TestAbuseConcurrentFlood prova que 25 lentas não matam 1 rápida: o pool
// de conexões é limitado e a rápida completa no prazo.
func TestAbuseConcurrentFlood(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	var slowGroup sync.WaitGroup
	var slowErrors atomic.Int64
	for index := 0; index < 25; index++ {
		slowGroup.Add(1)
		go func(index int) {
			defer slowGroup.Done()
			pr, pw := io.Pipe()
			go func() {
				for count := 0; count < 20; count++ {
					time.Sleep(50 * time.Millisecond)
					if _, err := pw.Write([]byte("x")); err != nil {
						return
					}
				}
				_ = pw.Close()
			}()
			request, err := http.NewRequest("POST", server.URL+"/api/v1/auth/register", pr)
			if err != nil {
				slowErrors.Add(1)
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(request)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}(index)
	}
	started := time.Now()
	status, _, _ := journeyCall(t, server, "GET", "/api/v1/arenas", "", "", nil)
	fastElapsed := time.Since(started)
	if status != 200 {
		t.Fatalf("fast request under flood = %d", status)
	}
	if fastElapsed > 10*time.Second {
		t.Fatalf("fast request took %s under 25 slow connections", fastElapsed)
	}
	slowGroup.Wait()
	if slowErrors.Load() != 0 {
		t.Fatalf("%d slow requests failed to build", slowErrors.Load())
	}
}

// TestAbuseSpoofSharesPeerBucket prova que XFF forjado cai no balde do
// par: esgota com XFF e sem XFF continua 429 (mesma conta, sem bypass).
func TestAbuseSpoofSharesPeerBucket(t *testing.T) {
	t.Parallel()
	world := abuseAuthWorld(t)
	server := world.server

	const email = "t05-spoof@arena.example.com"
	const password = "t05-spoof-horse-1"
	cookie := authRegisterVerifyLogin(t, world, email, password)
	_ = cookie

	spoof := map[string]string{"X-Forwarded-For": "9.9.9.9"}
	for attempt := 1; attempt <= 9; attempt++ {
		status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"wrong-password-9"}`, spoof)
		if status != 401 {
			t.Fatalf("spoofed wrong login %d = %d, want 401", attempt, status)
		}
	}
	// Sem header, mesmo par: o balde é o mesmo, então segue 429.
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 429 {
		t.Fatalf("legit login after spoofed burst = %d, want 429 (peer bucket, window-bounded)", status)
	}
}
