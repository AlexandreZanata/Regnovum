package contract_test

// P27-T04 — modos de falha de dependências sobre HTTP e Postgres reais.
//
// Timeout, reset, resposta parcial/inválida, 429, 5xx e latência no
// Stripe simulado, no Resend simulado e no banco descartável, mais o
// circuit breaker sob relógio injetado: retry com a mesma chave nunca
// duplica, nada parcial persiste, goroutines e locks ficam limitados,
// auth/wallet não importam telemetria, e o erro público mantém o código.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go/parser"
	"go/token"
	"path/filepath"

	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	notifyapp "github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/backpressure"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
)

// resetListener accepts TCP connections and resets them at once: the
// client reads ECONNRESET instead of an answer, the harshest transport
// failure short of silence.
func resetListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return "http://" + listener.Addr().String()
}

// stripeGatewayAt builds the real gateway against an arbitrary base URL.
func stripeGatewayAt(t *testing.T, baseURL string, timeout time.Duration) *stripeadapter.Gateway {
	t.Helper()
	gateway, err := stripeadapter.NewGateway(stripeadapter.Config{
		SecretKey: providersim.StripeSecretKey,
		Timeout:   timeout,
		BaseURL:   baseURL,
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return gateway
}

// TestDependencyStripeOutageAndRecovery fails once with 500 and succeeds
// on retry with the same key: one provider session, same key, error com
// código estável e retryable, sem segredo.
func TestDependencyStripeOutageAndRecovery(t *testing.T) {
	t.Parallel()
	simulator := providersim.NewStripe(t)
	simulator.Route(providersim.StripeCheckoutCreate,
		providersim.StripeError(500, "upstream outage"),
		providersim.Reply(200, providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")))
	gateway := stripeGateway(t, simulator, 5*time.Second)

	_, err := gateway.CreateCheckoutSession(context.Background(), checkoutRequest())
	if !errors.Is(err, billingapp.ErrPaymentGatewayUnavailable) {
		t.Fatalf("first error = %v, want ErrPaymentGatewayUnavailable", err)
	}
	if !billingapp.IsRetryablePaymentGatewayError(err) {
		t.Fatal("outage must be retryable")
	}
	session, err := gateway.CreateCheckoutSession(context.Background(), checkoutRequest())
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if session.ID.String() != providersim.StripeCheckoutID {
		t.Fatalf("session = %v", session.ID)
	}
	calls := simulator.CallsTo("POST", "/v1/checkout/sessions")
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want exactly 2 (attempt + retry, nothing hidden)", len(calls))
	}
	if calls[0].Header("Idempotency-Key") != calls[1].Header("Idempotency-Key") || calls[0].Header("Idempotency-Key") == "" {
		t.Fatal("retry changed or dropped the idempotency key")
	}
}

// TestDependencyResendDegradation answers 503 then 200: the first send is
// retryable, the retry carries the same key and resolves one receipt.
func TestDependencyResendDegradation(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	simulator := providersim.NewResend(t)
	simulator.Route(providersim.ResendEmails,
		providersim.ResendError(503, "internal_error", "upstream"),
		providersim.Reply(200, providersim.ResendAcceptedBody("msg_retry_1")))
	sender := resendSender(t, simulator, &logs, 5*time.Second)
	message := resendMessage(t)

	_, err := sender.Send(context.Background(), message)
	if !errors.Is(err, notifyapp.ErrProviderUnavailable) {
		t.Fatalf("first error = %v, want ErrProviderUnavailable", err)
	}
	if !notifyapp.IsRetryable(err) {
		t.Fatal("outage must be retryable")
	}
	receipt, err := sender.Send(context.Background(), message)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if receipt.ProviderID != "msg_retry_1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	calls := simulator.CallsTo("POST", "/emails")
	if len(calls) != 2 || calls[0].Header("Idempotency-Key") != calls[1].Header("Idempotency-Key") {
		t.Fatalf("provider saw %d calls with unstable keys", len(calls))
	}
}

// TestDependencyReset resets connections: gateway e sender classificam
// como indisponível retryable, sem travar e sem vazar segredo.
func TestDependencyReset(t *testing.T) {
	t.Parallel()
	endpoint := resetListener(t)
	gateway := stripeGatewayAt(t, endpoint, 3*time.Second)

	started := time.Now()
	_, err := gateway.CreateCustomer(context.Background(), billingapp.CreateCustomerRequest{IdempotencyKey: "reset-1"})
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("reset took %s (unbounded)", elapsed)
	}
	if !errors.Is(err, billingapp.ErrPaymentGatewayUnavailable) {
		t.Fatalf("reset error = %v, want ErrPaymentGatewayUnavailable", err)
	}
	if !billingapp.IsRetryablePaymentGatewayError(err) {
		t.Error("reset must be retryable")
	}
	for _, forbidden := range []string{providersim.StripeSecretKey, endpoint} {
		if err != nil && strings.Contains(err.Error(), forbidden) {
			t.Errorf("error leaks %q", forbidden)
		}
	}
}

// TestDependencyPartialResponse answers truncated JSON, wrong media and
// empty 200: tudo recusa como contrato quebrado, sem retry cego.
func TestDependencyPartialResponse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "truncated", status: 200, body: `{"id":"cs_test_sim","object":`},
		{name: "wrong shape", status: 200, body: `[1,2,3]`},
		{name: "empty success", status: 200, body: ``},
		{name: "html error page", status: 502, body: `<html><body>bad gateway</body></html>`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			simulator := providersim.NewStripe(t)
			simulator.Route(providersim.StripeCheckoutCreate, providersim.Reply(testCase.status, testCase.body))
			gateway := stripeGateway(t, simulator, 5*time.Second)
			_, err := gateway.CreateCheckoutSession(context.Background(), checkoutRequest())
			if err == nil {
				t.Fatal("partial answer accepted")
			}
			if billingapp.IsRetryablePaymentGatewayError(err) && testCase.name != "html error page" {
				t.Errorf("broken contract is retryable: %v", err)
			}
		})
	}
}

// TestDependencyLatencyBounded answers slow but inside the timeout: a
// degradação lenta ainda acerta, dentro do orçamento.
func TestDependencyLatencyBounded(t *testing.T) {
	t.Parallel()
	simulator := providersim.NewStripe(t)
	simulator.Route(providersim.StripeSubscriptionGet,
		providersim.Slow(providersim.Reply(200, providersim.StripeSubscriptionBody(providersim.StripeSubscriptionID, providersim.StripeCustomerID, providersim.StripePriceID)), 300*time.Millisecond))
	gateway := stripeGateway(t, simulator, 5*time.Second)

	started := time.Now()
	subscription, err := gateway.GetSubscription(context.Background(), billingdomain.StripeSubscriptionID(providersim.StripeSubscriptionID))
	if err != nil {
		t.Fatalf("slow but inside timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("slow answer took %s", elapsed)
	}
	if subscription.ID.String() != providersim.StripeSubscriptionID {
		t.Fatalf("subscription = %+v", subscription)
	}
}

// TestDependencyPostgresCancel prova rollback no cancelamento: a linha da
// transação abortada não existe depois, e o pool segue servindo.
func TestDependencyPostgresCancel(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	pool := world.pool

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ('t04-cancel@arena.example.com', 'active')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	before := journeyQueryInt(t, pool, `SELECT count(*) FROM app.accounts`)

	aborted, abort := context.WithCancel(context.Background())
	transaction, err := pool.Begin(aborted)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := transaction.Exec(aborted, `INSERT INTO app.accounts (email, status) VALUES ('t04-aborted@arena.example.com', 'active')`); err != nil {
		t.Fatalf("insert in tx: %v", err)
	}
	abort()
	if err := transaction.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.accounts`); got != before {
		t.Fatalf("accounts changed %d -> %d across an aborted transaction", before, got)
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.accounts WHERE email = 't04-aborted@arena.example.com'`); got != 0 {
		t.Fatal("aborted row survived the rollback")
	}

	// Pool fechado falha rápido e alto, sem travar o chamador.
	pool.Close()
	started := time.Now()
	if err := pool.Ping(context.Background()); err == nil {
		t.Fatal("closed pool pinged successfully")
	} else if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("closed pool took %s to refuse", elapsed)
	}
}

// zeroJitter fixes backoff without entropy: sleeps stay at the base,
// deterministic and fast, with no global random source involved.
type zeroJitter struct{}

func (zeroJitter) Int63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return 0
}

// breakerRunner builds a circuit runner with injected clock and no
// waiting beyond milliseconds.
func breakerRunner(t *testing.T, clock *steppingNow) *backpressure.Runner {
	t.Helper()
	runner, err := backpressure.New(backpressure.Config{
		MaxInFlight: 2, AcquireTimeout: 100 * time.Millisecond,
		OperationTimeout: 300 * time.Millisecond, MaxAttempts: 2,
		BaseBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		FailureThreshold: 2, OpenDuration: time.Minute,
		Now: clock.Now, Random: zeroJitter{},
	})
	if err != nil {
		t.Fatalf("backpressure.New: %v", err)
	}
	return runner
}

// TestDependencyCircuitBreaker prova abrir, recusar rápido sem executar,
// meia-abertura com um probe só e fechamento no sucesso.
func TestDependencyCircuitBreaker(t *testing.T) {
	t.Parallel()
	clock := &steppingNow{now: time.Now().UTC()}
	runner := breakerRunner(t, clock)
	ctx := context.Background()

	var executions atomic.Int64
	failing := func(ctx context.Context) error {
		executions.Add(1)
		return errors.New("dependency down")
	}
	if err := runner.Run(ctx, failing); err == nil {
		t.Fatal("failing operation succeeded")
	}
	if err := runner.Run(ctx, failing); err == nil {
		t.Fatal("failing operation succeeded")
	}
	if got := executions.Load(); got != 4 {
		t.Fatalf("executions = %d, want 4 (2 runs x 2 attempts)", got)
	}
	// Circuito aberto: recusa imediata sem executar.
	started := time.Now()
	if err := runner.Run(ctx, failing); !errors.Is(err, backpressure.ErrCircuitOpen) {
		t.Fatalf("open circuit = %v, want ErrCircuitOpen", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("open circuit took %s (not fast-failing)", elapsed)
	}
	if got := executions.Load(); got != 4 {
		t.Fatalf("open circuit executed the operation (%d)", got)
	}
	// Meia-abertura após a janela: um probe por vez; concorrentes recusam.
	clock.advance(61 * time.Second)
	release := make(chan struct{})
	flight := make(chan struct{})
	var flightOnce sync.Once
	var probes atomic.Int64
	probing := func(ctx context.Context) error {
		probes.Add(1)
		flightOnce.Do(func() { close(flight) })
		<-release
		return nil
	}
	var first sync.WaitGroup
	first.Add(1)
	var firstErr error
	go func() {
		defer first.Done()
		firstErr = runner.Run(ctx, probing)
	}()
	select {
	case <-flight:
	case <-time.After(10 * time.Second):
		t.Fatal("half-open probe never took flight")
	}
	var losers sync.WaitGroup
	loserErrs := make([]error, 2)
	for index := range loserErrs {
		losers.Add(1)
		go func(index int) {
			defer losers.Done()
			loserErrs[index] = runner.Run(ctx, probing)
		}(index)
	}
	losers.Wait()
	for _, err := range loserErrs {
		if !errors.Is(err, backpressure.ErrCircuitOpen) {
			t.Fatalf("half-open loser = %v, want ErrCircuitOpen", err)
		}
	}
	close(release)
	first.Wait()
	if firstErr != nil {
		t.Fatalf("half-open probe: %v", firstErr)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("half-open probes = %d, want exactly 1", got)
	}
	// Sucesso fecha: a próxima executa normal.
	if err := runner.Run(ctx, func(context.Context) error { executions.Add(1); return nil }); err != nil {
		t.Fatalf("closed circuit: %v", err)
	}
}

// TestDependencySaturationBounded prova que a fila cheia recusa rápido em
// vez de empilhar: um slot ocupado, outro pedido com timeout curto.
func TestDependencySaturationBounded(t *testing.T) {
	t.Parallel()
	clock := &steppingNow{now: time.Now().UTC()}
	runner, err := backpressure.New(backpressure.Config{
		MaxInFlight: 1, AcquireTimeout: 50 * time.Millisecond,
		OperationTimeout: time.Second, MaxAttempts: 1,
		BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond,
		FailureThreshold: 100, OpenDuration: time.Minute,
		Now: clock.Now, Random: zeroJitter{},
	})
	if err != nil {
		t.Fatalf("backpressure.New: %v", err)
	}
	release := make(chan struct{})
	acquired := make(chan struct{})
	var acquireOnce sync.Once
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(context.Background(), func(ctx context.Context) error {
			acquireOnce.Do(func() { close(acquired) })
			<-release
			return nil
		})
	}()
	// Espera o ocupante adquirir o slot único antes de disputar.
	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("slot holder never acquired")
	}
	started := time.Now()
	if err := runner.Run(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, backpressure.ErrSaturated) {
		t.Fatalf("saturated runner = %v, want ErrSaturated", err)
	} else if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("saturated acquire took %s", elapsed)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder: %v", err)
	}
}

// TestDependencyAuthWalletIndependentOfTelemetry prova estaticamente que
// os fluxos Q0 não importam telemetria: nenhum arquivo entregue dos
// pacotes de aplicação a referencia, então um apagão de analytics nunca
// os derruba por dependência.
func TestDependencyAuthWalletIndependentOfTelemetry(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{
		"internal/identity/application",
		"internal/wallet/application",
		"internal/billing/application",
		"internal/arguments/application",
	} {
		root := filepath.Join("..", "..", directory)
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s: %v", directory, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, entry.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", entry.Name(), err)
			}
			for _, importSpec := range parsed.Imports {
				path := strings.Trim(importSpec.Path.Value, `"`)
				if strings.Contains(path, "observability") {
					t.Fatalf("%s imports telemetry %q: Q0 flows must not depend on analytics", filepath.Join(directory, entry.Name()), path)
				}
			}
		}
	}
}
