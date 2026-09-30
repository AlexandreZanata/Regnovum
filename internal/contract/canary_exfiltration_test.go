package contract_test

// P26-T07 — canaries contra exfiltração em todos os sinks.
//
// Tokens e dados sintéticos atravessam requests, respostas, erros, logs,
// analytics, pânico e artifacts: nenhum sink proibido pode conter um
// canary, logs preservam correlação (IDs) sem conteúdo, e fixtures de
// credencial são sintéticas por construção. A allowlist de campos vive em
// observability (eventos e tags) e a redação central em logging;
// este teste é o scanner que cobra as duas.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/resend"
	notifyapp "github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	notifydomain "github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// canaries são os segredos sintéticos que nenhum sink pode vazar. Todos
// têm forma válida (email passa em validadores) e são distintos por
// categoria, para que o achado nomeie o vazamento.
var canaries = []string{
	"canary-t07-alice@canary.invalid",
	"canary-t07-bob@canary.invalid",
	"canary-token-t07-9f8e7d6c5b4a",
	"canary-password-t07-correct-horse",
	"4242424242424242",
	"sk_test_canary_t07_value",
	"canary-code-t07-K7QP",
}

// assertNoCanaries falha se qualquer canary aparecer no documento, nomeando
// o sink para o relatório não depender de adivinhação.
func assertNoCanaries(t *testing.T, sink, document string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(document, canary) {
			t.Fatalf("sink %s exfiltrates canary %q", sink, canary)
		}
	}
}

// TestCanaryHTTPResponses dirige jornadas com canaries em cada campo e
// varre toda resposta: corpo de sucesso ou Problem nunca carrega o valor.
func TestCanaryHTTPResponses(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	email := "canary-t07-alice@canary.invalid"
	password := "canary-password-t07-correct-horse"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	// Login errado com canary: a recusa 401 também é varrida.
	status, _, loginBody := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"canary-password-t07-WRONG-horse"}`, nil)
	if status != 401 {
		t.Fatalf("wrong login = %d, want 401", status)
	}
	assertNoCanaries(t, "login refusal", string(loginBody))

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
	status, loginHeader, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, loginHeader)
	assertNoCanaries(t, "login Set-Cookie", loginHeader.Get("Set-Cookie"))

	statement := "Tese canary com sk_test_canary_t07_value e token canary-token-t07-9f8e7d6c5b4a embutidos"
	body, _ := json.Marshal(map[string]string{"statement": statement, "category": "technology", "language": "pt-BR"})
	status, _, draftRaw := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookie, string(body), nil)
	if status != 201 {
		t.Fatalf("draft = %d, want 201 (%s)", status, string(draftRaw))
	}
	// O conteúdo do titular volta para o titular (armazenamento, não
	// vazamento): o que se prova é que erros e terceiros não o veem.
	status, _, otherRaw := journeyCall(t, server, "GET", "/api/v1/me/arena-drafts", "", "", nil)
	if status != 401 {
		t.Fatalf("anonymous drafts = %d, want 401", status)
	}
	assertNoCanaries(t, "anonymous refusal", string(otherRaw))

	status, _, reportRaw := journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", cookie,
		`{"target_type":"arena","target_id":"00000000-0000-0000-0000-000000000000","reason":"spam","context":"canary-token-t07-9f8e7d6c5b4a"}`, nil)
	if status < 400 || status >= 500 {
		t.Fatalf("report unknown target = %d, want 4xx", status)
	}
	assertNoCanaries(t, "report refusal", string(reportRaw))
}

// TestCanaryLogs prova redação no caminho de log: email mascarado, valor
// redigido, e o sender com falha registra sem canary em nenhum campo.
func TestCanaryLogs(t *testing.T) {
	t.Parallel()
	if got := logging.RedactEmail("canary-t07-alice@canary.invalid"); strings.Contains(got, "canary-t07-alice") {
		t.Fatalf("RedactEmail keeps the local part: %q", got)
	}
	// RedactValue é por-marcador por desenho (corpos brutos nunca são
	// logados; campos seguros são extraídos): marcador vira [REDACTED].
	if got := logging.RedactValue("Authorization: Bearer sk_test_canary_t07_value"); got != "[REDACTED]" {
		t.Fatalf("RedactValue missed the bearer marker: %q", got)
	}
	if got := logging.RedactValue("password=canary-password-t07-x"); got != "[REDACTED]" {
		t.Fatalf("RedactValue missed the password marker: %q", got)
	}

	var logs bytes.Buffer
	simulator := providersim.NewResend(t)
	simulator.Route(providersim.ResendEmails, providersim.ResendError(422, "validation_error", "invalid to: canary-t07-alice@canary.invalid"))
	sender, err := resend.NewSender(resend.Config{
		APIToken: providersim.ResendAPIKey,
		From:     providersim.ResendSenderAddress,
		BaseURL:  simulator.URL(),
		Timeout:  5 * time.Second,
		Logger:   slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	message := canaryMessage(t)
	if _, err := sender.Send(context.Background(), message); !isRetryableOrRejected(err) {
		t.Fatalf("Send error = %v, want a classified failure", err)
	}
	assertNoCanaries(t, "resend logs", logs.String())
	if !strings.Contains(logs.String(), "a***@") && !strings.Contains(logs.String(), "c***@") {
		t.Fatalf("logs carry no masked recipient: %s", logs.String())
	}
}

// TestCanaryAnalytics prova a allowlist: propriedade proibida com canary
// é recusada antes do fio, e o lote entregue nunca contém o valor.
func TestCanaryAnalytics(t *testing.T) {
	t.Parallel()
	sentry := providersim.NewSentry(t, providersim.CapturingBodies())
	posthog := providersim.NewPostHog(t, providersim.CapturingBodies())
	telemetry, err := observability.New(observability.Config{
		Logger:            slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Clock:             testsource.NewClock(time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)),
		Environment:       "test",
		SentryDSN:         "https://simkey@o1.ingest.sentry.io/42",
		SentryBaseURL:     sentry.URL(),
		PostHogAPIKey:     providersim.PostHogAPIKey,
		PostHogHost:       posthog.URL(),
		SampleRatePercent: 100,
	})
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	t.Cleanup(telemetry.Close)

	telemetry.Events.Capture(observability.Event{
		Name:       observability.EventAccountSignedIn,
		AccountID:  "acc-canary-t07",
		Properties: map[string]any{"email": "canary-t07-alice@canary.invalid"},
		RequestID:  "req-canary-t07",
	})
	// Lote cheio descarrega sem esperar o intervalo (100 é o default
	// do sink, como na T08): sem dormir 5s por evento.
	for index := 0; index < providersim.PostHogBatchSize; index++ {
		telemetry.Events.Capture(observability.Event{
			Name:       observability.EventAccountSignedIn,
			AccountID:  "acc-canary-t07",
			Properties: map[string]any{"locale": "pt-BR"},
			RequestID:  "req-canary-t07",
		})
	}
	// Kind estável viaja (correlação); forma hostil cai no filtro de
	// forma e nunca chega ao provider.
	telemetry.Errors.Report(observability.ErrorReport{
		Message:   "job email_delivery failed",
		Kind:      "handler",
		Operation: "email_delivery",
		RequestID: "req-canary-t07",
	})
	telemetry.Errors.Report(observability.ErrorReport{
		Message:   "job email_delivery failed",
		Kind:      "canary\nkind-t07-\u202einject" + strings.Repeat("x", 100),
		Operation: "email_delivery",
		RequestID: "req-canary-t07",
	})

	calls := waitForCalls(t, posthog, "POST", "/batch/", 1)
	for _, call := range calls {
		assertNoCanaries(t, "posthog batch", string(call.Body))
	}
	calls = waitForCalls(t, sentry, "POST", "/api/42/envelope/", 2)
	for _, call := range calls {
		assertNoCanaries(t, "sentry envelope", string(call.Body))
	}
}

// recordingReporter guarda relatórios para inspeção.
type recordingReporter struct {
	reports []observability.ErrorReport
}

func (reporter *recordingReporter) Report(report observability.ErrorReport) {
	reporter.reports = append(reporter.reports, report)
}

// TestCanaryPanic prova que o pânico reporta padrão de rota e IDs, nunca a
// URL crua com o canary da query.
func TestCanaryPanic(t *testing.T) {
	t.Parallel()
	recorder := &recordingReporter{}
	telemetry := &observability.Telemetry{
		Errors:  recorder,
		Metrics: observability.NewMetrics(testsource.NewClock(time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC))),
	}
	defer telemetry.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/arenas/{id}/arguments", func(http.ResponseWriter, *http.Request) {
		panic("canary-token-t07-9f8e7d6c5b4a in handler")
	})
	server := httptest.NewServer(telemetry.HTTPMiddleware(mux))
	defer server.Close()

	func() {
		defer func() { _ = recover() }()
		// O middleware força 500 e re-panica: a conexão morre com EOF em
		// vez de entregar corpo — sem corpo, sem exfiltração na resposta.
		response, err := http.Get(server.URL + "/api/v1/arenas/123/arguments?token=canary-token-t07-9f8e7d6c5b4a&relation=support")
		if err != nil {
			if !strings.Contains(err.Error(), "EOF") {
				t.Fatalf("get: %v", err)
			}
			return
		}
		defer response.Body.Close()
		answer, _ := io.ReadAll(response.Body)
		if response.StatusCode != 500 {
			t.Fatalf("panicking route = %d, want 500", response.StatusCode)
		}
		assertNoCanaries(t, "panic response", string(answer))
	}()

	if len(recorder.reports) != 1 {
		t.Fatalf("reports = %d, want exactly 1", len(recorder.reports))
	}
	report := recorder.reports[0]
	if report.Message != "http handler panic" || report.Kind != "panic" {
		t.Fatalf("report = %+v, want the stable panic shape", report)
	}
	assertNoCanaries(t, "panic operation", report.Operation)
	assertNoCanaries(t, "panic request id", report.RequestID)
	if !strings.Contains(report.Operation, "arguments") {
		t.Fatalf("operation lost the route pattern: %q", report.Operation)
	}
}

// TestCanaryFixtures proíbe credencial real em fixture: todo segredo dos
// testes é sintético por marcador, sem prefixo live nem email real.
func TestCanaryFixtures(t *testing.T) {
	t.Parallel()
	synthetic := []string{
		providersim.StripeSecretKey,
		providersim.StripeWebhookSecret,
		providersim.ResendAPIKey,
		providersim.TurnstileSecretKey,
		providersim.PostHogAPIKey,
		"sk_test_canary_t07_value",
	}
	for _, secret := range synthetic {
		if strings.Contains(secret, "sk_live") || strings.Contains(secret, "whsec_live") || strings.Contains(secret, "re_live") {
			t.Fatalf("fixture carries a live prefix: %q", secret)
		}
	}
	for _, canary := range canaries {
		if strings.Contains(canary, "@gmail.com") || strings.Contains(canary, "@arena.example.com") && !strings.Contains(canary, "canary") {
			t.Fatalf("canary looks like a real address: %q", canary)
		}
	}
	assertNoCanaries(t, "canary self-check", "nothing here but the scanner")
}

// canaryMessage monta a mensagem com o destinatário canary.
func canaryMessage(t *testing.T) notifydomain.Message {
	t.Helper()
	body, err := notifydomain.NewBody("Confirme seu email", "Use o código canary.", "<p>canary</p>")
	if err != nil {
		t.Fatalf("NewBody: %v", err)
	}
	message, err := notifydomain.NewMessage(
		"canary-t07-alice@canary.invalid",
		notifydomain.LocaleBrazilianPortuguese,
		notifydomain.TemplateVerification,
		body,
		"email:verification:canary-t07",
	)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return message
}

// isRetryableOrRejected aceita as duas classes de falha do provider.
func isRetryableOrRejected(err error) bool {
	return err != nil && (notifyapp.IsRetryable(err) || isRejected(err))
}

func isRejected(err error) bool {
	return err != nil && strings.Contains(err.Error(), "rejected")
}
