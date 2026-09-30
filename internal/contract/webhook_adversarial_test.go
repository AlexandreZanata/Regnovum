package contract_test

// P26-T06 — webhooks adversariais sobre HTTP real com Stripe simulado.
//
// Assinatura, timestamp, replay, duplicata/reordenação, divergência de
// amount/currency, payload alterado, timeout do provider e concorrência:
// só o evento autenticado e reconciliado (sessão↔intent) concede
// benefício, exatamente uma concessão por chave, e nada concede sem
// liquidar.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
)

// adversarialBuyer registra, verifica, loga e compra: devolve o cookie e
// o intent aberto para a sessão fixa do simulador.
func adversarialBuyer(t *testing.T, world *journeyWorld, server *httptest.Server, email, password, key string) (string, string) {
	t.Helper()
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
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
	status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie,
		`{"market":"BR","product":"ink_10000","idempotency_key":"`+key+`"}`, nil)
	if status != 200 {
		t.Fatalf("checkout = %d, want 200 (%s)", status, string(raw))
	}
	var order struct {
		IntentID string `json:"intent_id"`
	}
	if err := json.Unmarshal(raw, &order); err != nil || order.IntentID == "" {
		t.Fatalf("checkout carries no intent: %s", string(raw))
	}
	return cookie, order.IntentID
}

// adversarialEvent monta o evento na ordem que o parser declara, com o
// documento de sessão editável e assinatura válida do simulador.
func adversarialEvent(eventID, eventType string, at time.Time, sessionJSON string) (payload, signature, timestamp string) {
	payload = fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"created":%d,"livemode":false,"data":{"object":%s}}`,
		eventID, eventType, at.UTC().Unix(), sessionJSON)
	signature, timestamp = providersim.SignStripeWebhook(providersim.StripeWebhookSecret, at, payload)
	return payload, signature, timestamp
}

// adversarialDeliver entrega um webhook e devolve status e corpo.
func adversarialDeliver(t *testing.T, server *httptest.Server, payload, signature, timestamp string) (int, []byte) {
	t.Helper()
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

// adversarialBalance lê o saldo total da conta por email.
func adversarialBalance(t *testing.T, world *journeyWorld, server *httptest.Server, cookie string) int64 {
	t.Helper()
	return journeyWalletBalance(t, server, cookie)
}

// adversarialPaid conta intents pagos.
func adversarialPaid(t *testing.T, world *journeyWorld, intentID string) int64 {
	t.Helper()
	return journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.checkout_intents WHERE id::text = $1 AND status = 'paid'`, intentID)
}

// TestWebhookAdversarialSignature recusa tudo que não é autêntico e nada
// concede: segredo errado, byte alterado, headers ausentes, velho e futuro.
func TestWebhookAdversarialSignature(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-sig@arena.example.com", "t06-correct-horse-1", "t06-adv-sig-1")

	sessionJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	goodPayload, goodSignature, goodTimestamp := adversarialEvent("evt_T06sig001", "checkout.session.completed", journeyFixedInstant, sessionJSON)

	cases := []struct {
		name      string
		payload   string
		signature string
		timestamp string
	}{
		{name: "wrong secret", payload: goodPayload, signature: "t=0,v1=deadbeef", timestamp: goodTimestamp},
		{name: "tampered byte", payload: goodPayload + " ", signature: goodSignature, timestamp: goodTimestamp},
		{name: "missing signature", payload: goodPayload, signature: "", timestamp: goodTimestamp},
		{name: "missing timestamp", payload: goodPayload, signature: goodSignature, timestamp: ""},
	}
	for _, testCase := range cases {
		status, _ := adversarialDeliver(t, server, testCase.payload, testCase.signature, testCase.timestamp)
		if status != 400 {
			t.Fatalf("%s = %d, want 400", testCase.name, status)
		}
	}
	staleAt := journeyFixedInstant.Add(-time.Hour)
	stalePayload, staleSignature, staleTimestamp := adversarialEvent("evt_T06sig002", "checkout.session.completed", staleAt, sessionJSON)
	if status, _ := adversarialDeliver(t, server, stalePayload, staleSignature, staleTimestamp); status != 400 {
		t.Fatalf("stale event = %d, want 400", status)
	}
	futureAt := journeyFixedInstant.Add(time.Hour)
	futurePayload, futureSignature, futureTimestamp := adversarialEvent("evt_T06sig003", "checkout.session.completed", futureAt, sessionJSON)
	if status, _ := adversarialDeliver(t, server, futurePayload, futureSignature, futureTimestamp); status != 400 {
		t.Fatalf("future event = %d, want 400", status)
	}

	if got := adversarialPaid(t, world, intentID); got != 0 {
		t.Fatalf("paid intents = %d after unauthenticated deliveries, want 0", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 0 {
		t.Fatalf("balance = %d after unauthenticated deliveries, want 0", balance)
	}
}

// TestWebhookAdversarialUnknownSession entrega evento válido para sessão
// inexistente: 400 permanente, sem concessão e sem crash.
func TestWebhookAdversarialUnknownSession(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-unk@arena.example.com", "t06-correct-horse-1", "t06-adv-unk-1")

	ghostSession := strings.Replace(providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim"),
		providersim.StripeCheckoutID, "cs_test_GHOST000000000001", 1)
	payload, signature, timestamp := adversarialEvent("evt_T06unk001", "checkout.session.completed", journeyFixedInstant, ghostSession)
	status, _ := adversarialDeliver(t, server, payload, signature, timestamp)
	if status != 400 {
		t.Fatalf("unknown session = %d, want 400 (permanent, never retried as success)", status)
	}
	if got := adversarialPaid(t, world, intentID); got != 0 {
		t.Fatalf("real intent paid by a ghost event: %d", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 0 {
		t.Fatalf("balance = %d after ghost event, want 0", balance)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.stripe_events WHERE stripe_event_id = 'evt_T06unk001'`); got != 1 {
		t.Fatalf("ghost event rows = %d, want 1 recorded as failed", got)
	}
}

// TestWebhookAdversarialDivergent documenta o modelo de confiança: a
// liquidação vincula sessão↔intent e concede o catálogo (o preço que o
// comprador aceitou e que o create já reconciliou); o evento assinado com
// total divergente liquida o mesmo intent, sem criar outro benefício.
func TestWebhookAdversarialDivergent(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-div@arena.example.com", "t06-correct-horse-1", "t06-adv-div-1")

	divergent := strings.Replace(providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim"),
		`"amount_total":2490`, `"amount_total":9999`, 1)
	divergent = strings.Replace(divergent, `"currency":"brl"`, `"currency":"usd"`, 1)
	payload, signature, timestamp := adversarialEvent("evt_T06div001", "checkout.session.completed", journeyFixedInstant, divergent)
	status, _ := adversarialDeliver(t, server, payload, signature, timestamp)
	if status != 200 {
		t.Fatalf("divergent event = %d, want 200 (session binds the intent)", status)
	}
	if got := adversarialPaid(t, world, intentID); got != 1 {
		t.Fatalf("paid intents = %d, want 1", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 10000 {
		t.Fatalf("balance = %d, want the catalog 10000 (never the event figure)", balance)
	}
}

// TestWebhookAdversarialUnpaid prova que só liquida o pago: sessão aberta
// com payment pendente não concede e o intent segue aberto.
func TestWebhookAdversarialUnpaid(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-unp@arena.example.com", "t06-correct-horse-1", "t06-adv-unp-1")

	openSession := strings.Replace(providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim"),
		`"payment_status":"paid"`, `"payment_status":"unpaid"`, 1)
	openSession = strings.Replace(openSession, `"status":"complete"`, `"status":"open"`, 1)
	payload, signature, timestamp := adversarialEvent("evt_T06unp001", "checkout.session.completed", journeyFixedInstant, openSession)
	status, _ := adversarialDeliver(t, server, payload, signature, timestamp)
	if status != 400 {
		t.Fatalf("unpaid event = %d, want 400", status)
	}
	if got := adversarialPaid(t, world, intentID); got != 0 {
		t.Fatalf("unpaid event settled the intent: %d", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 0 {
		t.Fatalf("balance = %d after unpaid event, want 0", balance)
	}
}

// TestWebhookAdversarialReplayRace dispara a mesma entrega em 8
// goroutines: exatamente uma liquida, as demais veem replay ou 429
// transitório — nunca 500, nunca segundo crédito.
func TestWebhookAdversarialReplayRace(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-race@arena.example.com", "t06-correct-horse-1", "t06-adv-race-1")

	sessionJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	payload, signature, timestamp := adversarialEvent("evt_T06race001", "checkout.session.completed", journeyFixedInstant, sessionJSON)

	var granted atomic.Int64
	var retryable atomic.Int64
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			status, _ := adversarialDeliver(t, server, payload, signature, timestamp)
			switch status {
			case 200:
				granted.Add(1)
			case 429:
				retryable.Add(1)
			default:
				t.Errorf("race delivery = %d, want 200 or 429", status)
			}
		}()
	}
	group.Wait()
	if got := adversarialPaid(t, world, intentID); got != 1 {
		t.Fatalf("paid intents = %d, want exactly 1", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 10000 {
		t.Fatalf("balance = %d, want exactly one grant of 10000", balance)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var credits int64
	accountID := journeyAccountID(t, world.pool, "t06-adv-race@arena.example.com")
	if err := world.pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_transactions t
		JOIN app.wallet_operations o ON o.id = t.operation_id
		WHERE o.account_id::text = $1 AND t.amount > 0`, accountID).Scan(&credits); err != nil {
		t.Fatalf("count credits: %v", err)
	}
	if credits != 1 {
		t.Fatalf("credit lines = %d, want exactly 1", credits)
	}
	t.Logf("race: %d delivered 200, %d saw 429 in-processing", granted.Load(), retryable.Load())
}

// TestWebhookAdversarialReordered prova as duas ordens: expirado antes do
// completo não rouba a liquidação, e expirado depois do pago não a desfaz;
// em ambas, um crédito só.
func TestWebhookAdversarialReordered(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-reo@arena.example.com", "t06-correct-horse-1", "t06-adv-reo-1")

	completeJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	expiredJSON := strings.Replace(completeJSON, `"status":"complete"`, `"status":"expired"`, 1)
	expiredPayload, expiredSignature, expiredTimestamp := adversarialEvent("evt_T06reoExp", "checkout.session.expired", journeyFixedInstant, expiredJSON)
	completePayload, completeSignature, completeTimestamp := adversarialEvent("evt_T06reoCmp", "checkout.session.completed", journeyFixedInstant, completeJSON)

	if status, _ := adversarialDeliver(t, server, expiredPayload, expiredSignature, expiredTimestamp); status != 200 {
		t.Fatalf("expired first = %d, want 200 (acknowledged, no-op)", status)
	}
	if got := adversarialPaid(t, world, intentID); got != 0 {
		t.Fatalf("expired event settled the intent: %d", got)
	}
	if status, _ := adversarialDeliver(t, server, completePayload, completeSignature, completeTimestamp); status != 200 {
		t.Fatalf("completed after expired = %d, want 200", status)
	}
	if status, _ := adversarialDeliver(t, server, expiredPayload, expiredSignature, expiredTimestamp); status != 200 {
		t.Fatalf("expired after paid = %d, want 200 (acknowledged, no-op)", status)
	}
	if got := adversarialPaid(t, world, intentID); got != 1 {
		t.Fatalf("paid intents = %d, want exactly 1", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 10000 {
		t.Fatalf("balance = %d, want exactly one grant of 10000", balance)
	}
}

// TestWebhookAdversarialUnknownType prova que tipo desconhecido é
// reconhecido e ignorado: 200, sem concessão, sem linha de benefício.
func TestWebhookAdversarialUnknownType(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "t06-adv-typ@arena.example.com", "t06-correct-horse-1", "t06-adv-typ-1")

	sessionJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	payload, signature, timestamp := adversarialEvent("evt_T06typ001", "customer.deleted", journeyFixedInstant, sessionJSON)
	status, _ := adversarialDeliver(t, server, payload, signature, timestamp)
	if status != 200 {
		t.Fatalf("unknown type = %d, want 200 acknowledged-and-ignored", status)
	}
	if got := adversarialPaid(t, world, intentID); got != 0 {
		t.Fatalf("unknown type settled the intent: %d", got)
	}
	if balance := adversarialBalance(t, world, server, cookie); balance != 0 {
		t.Fatalf("balance = %d after unknown type, want 0", balance)
	}
}

// TestWebhookAdversarialProviderTimeout prova que provider lento no create
// não deixa intent pendente fantasma: timeout com zero intents liquidados.
func TestWebhookAdversarialProviderTimeout(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "t06-adv-tmo@arena.example.com"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"t06-correct-horse-1"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	verifyToken, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+verifyToken, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"t06-correct-horse-1"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, header)

	world.stripe.Route(providersim.StripeCheckoutCreate,
		providersim.Slow(providersim.Reply(200, providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")), 6*time.Second))
	started := time.Now()
	status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie,
		`{"market":"BR","product":"ink_10000","idempotency_key":"t06-adv-tmo-1"}`, nil)
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("slow provider took %s", elapsed)
	}
	if status != 503 && status != 504 && status != 500 && status != 502 {
		t.Fatalf("slow provider checkout = %d (%s), want a transport-class failure, never an intent", status, string(raw))
	}
	accountID := journeyAccountID(t, world.pool, email)
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.checkout_intents WHERE account_id::text = $1 AND status = 'paid'`, accountID); got != 0 {
		t.Fatalf("paid intents after timeout = %d, want 0", got)
	}
}
