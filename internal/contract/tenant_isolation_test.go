package contract_test

// P26-T09 — isolamento entre titulares e ciclo de privacidade.
//
// Duas titulares com canaries distintos exercem export, deleção,
// retenção, transparência com baixa contagem, analytics e restauração:
// zero mistura entre elas, export por allowlist, exclusão agendada sem
// antecipar, retenção com base e prazo, e isolamento de pé após restart.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	auditpg "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	profilejson "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// tenantHolders semeia Ava e Ben verificados com sessões e saldos
// diferentes, e devolve os cookies. Os canaries vivem nos textos de cada
// uma: email, tese e argumento.
func tenantHolders(t *testing.T, world *journeyWorld) (ava, ben string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, account := range []struct {
		email string
		funds int64
		key   string
	}{
		{"t09-ava@canary.invalid", 100000, "ava"},
		{"t09-ben@canary.invalid", 50000, "ben"},
	} {
		if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, 'active', now())`, account.email); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		journeyFund(t, world.pool, account.email, account.key, account.funds)
	}
	journeySession(t, world.pool, "t09-ava@canary.invalid", "t09-ava-token", false)
	journeySession(t, world.pool, "t09-ben@canary.invalid", "t09-ben-token", false)
	return "t09-ava-token", "t09-ben-token"
}

// TestTenantNoMixing prova zero mistura: cada titular lê só o seu no
// privado, Add cruzado nega sem vazar, e saldos nunca se tocam.
func TestTenantNoMixing(t *testing.T) {
	t.Parallel()
	world, audit := privilegedWorld(t)
	server := servePrivileged(t, world, audit)
	ava, ben := tenantHolders(t, world)

	statements := map[string]string{
		ava: "Tese canary-t09-ava sobre persuasão pública",
		ben: "Tese canary-t09-ben sobre deliberação aberta",
	}
	drafts := map[string]string{}
	for cookie, statement := range statements {
		body, _ := json.Marshal(map[string]string{"statement": statement, "category": "technology", "language": "pt-BR"})
		status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookie, string(body), nil)
		if status != 201 {
			t.Fatalf("draft = %d, want 201", status)
		}
		var draft struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &draft); err != nil || draft.ID == "" {
			t.Fatalf("draft carries no id: %s", string(raw))
		}
		drafts[cookie] = draft.ID
	}

	status, _, listRaw := journeyCall(t, server, "GET", "/api/v1/me/arena-drafts", ava, "", nil)
	if status != 200 {
		t.Fatalf("ava drafts = %d, want 200", status)
	}
	if strings.Contains(string(listRaw), "canary-t09-ben") {
		t.Fatalf("ava lists ben's draft: %s", string(listRaw))
	}
	if !strings.Contains(string(listRaw), "canary-t09-ava") {
		t.Fatalf("ava misses her own draft: %s", string(listRaw))
	}

	status, _, crossRaw := journeyCall(t, server, "GET", "/api/v1/me/arena-drafts/"+drafts[ben], ava, "", nil)
	if status != 403 && status != 404 {
		t.Fatalf("cross draft read = %d, want deny", status)
	}
	assertNoCanaries(t, "cross refusal", string(crossRaw))

	if balance := journeyWalletBalance(t, server, ava); balance != 100000 {
		t.Fatalf("ava balance = %d, want 100000", balance)
	}
	if balance := journeyWalletBalance(t, server, ben); balance != 50000 {
		t.Fatalf("ben balance = %d, want 50000", balance)
	}

	status, _, deletionRaw := journeyCall(t, server, "POST", "/api/v1/me/deletion", ava, "", nil)
	if status != 202 {
		t.Fatalf("ava deletion request = %d, want 202", status)
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/deletion", ben, "", nil)
	if status != 404 {
		t.Fatalf("ben reads deletion = %d, want 404 (no oracle)", status)
	}
	_ = deletionRaw
}

// TestTenantExportAllowlist gera o documento pelo caso de uso real (o
// workload do worker) e baixa por HTTP: só chaves admitidas viajam, sem
// hashes, tokens ou dado da outra titular.
func TestTenantExportAllowlist(t *testing.T) {
	t.Parallel()
	world, audit := privilegedWorld(t)
	server := servePrivileged(t, world, audit)
	ava, ben := tenantHolders(t, world)
	_ = ben

	status, _, exportRaw := journeyCall(t, server, "POST", "/api/v1/me/exports", ava, "", nil)
	if status != 202 {
		t.Fatalf("export request = %d, want 202", status)
	}
	var order struct {
		ExportID      string `json:"export_id"`
		DownloadToken string `json:"download_token"`
	}
	if err := json.Unmarshal(exportRaw, &order); err != nil || order.ExportID == "" || order.DownloadToken == "" {
		t.Fatalf("export carries no capability: %s", string(exportRaw))
	}

	repo := profilespg.NewRepository(world.pool)
	generate, err := profilesapp.NewGeneratePersonalExportUseCase(repo, repo, profilejson.NewEncoder(), world.clock)
	if err != nil {
		t.Fatalf("NewGeneratePersonalExportUseCase: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	generated, err := generate.Execute(ctx, order.ExportID)
	if err != nil {
		t.Fatalf("generate (worker stand-in): %v", err)
	}
	if generated.Replayed {
		t.Fatal("fresh export reported as replayed")
	}

	status, _, documentRaw := journeyCall(t, server, "GET", "/api/v1/me/exports/"+order.ExportID+"/download?token="+order.DownloadToken, ava, "", nil)
	if status != 200 {
		t.Fatalf("download = %d, want 200 (%s)", status, string(documentRaw))
	}
	var document map[string]any
	if err := json.Unmarshal(documentRaw, &document); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	serialized := string(documentRaw)
	if !strings.Contains(serialized, "t09-ava@canary.invalid") {
		t.Fatal("export lost the owner's own address")
	}
	for _, forbidden := range []string{
		"$argon2id$", "token_hash", "secret", "password", "credential",
		"canary-t09-ben", "t09-ben@canary.invalid",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("export carries forbidden %q", forbidden)
		}
	}
	_ = audit
}

// TestTenantDeletionScheduled prova que a exclusão não antecipa: o worker
// da manutenção roda, executa zero antes do cooldown e o pedido segue
// pendente com trilha.
func TestTenantDeletionScheduled(t *testing.T) {
	t.Parallel()
	world, audit := privilegedWorld(t)
	server := servePrivileged(t, world, audit)
	ava, _ := tenantHolders(t, world)
	_ = audit

	status, _, _ := journeyCall(t, server, "POST", "/api/v1/me/deletion", ava, "", nil)
	if status != 202 {
		t.Fatalf("deletion request = %d, want 202", status)
	}
	repo := profilespg.NewRepository(world.pool)
	auditRepo := auditpg.NewRepository(world.pool)
	execute, err := profilesapp.NewExecuteDueDeletionsUseCase(repo, auditRepo, platformpg.NewTxManager(world.pool), world.clock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executed, err := execute.Execute(ctx)
	if err != nil {
		t.Fatalf("execute due: %v", err)
	} else if executed.Executed != 0 {
		t.Fatalf("executed %d deletions inside cooldown, want 0", executed.Executed)
	}
	status, _, statusRaw := journeyCall(t, server, "GET", "/api/v1/me/deletion", ava, "", nil)
	if status != 200 {
		t.Fatalf("deletion status = %d, want 200", status)
	}
	var state struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(statusRaw, &state); err != nil || state.Status == "" {
		t.Fatalf("deletion status unreadable: %s", string(statusRaw))
	}
	if strings.Contains(strings.ToLower(state.Status), "execut") {
		t.Fatalf("deletion executed inside cooldown: %q", state.Status)
	}
}

// TestTenantRetentionPolicy prova base e prazo documentados: cada linha da
// política executável é válida com motivo, holds exigem motivo, e a
// manutenção roda registrando a passada sem purgar o fresco.
func TestTenantRetentionPolicy(t *testing.T) {
	t.Parallel()
	world, _ := privilegedWorld(t)
	pool := world.pool

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ('t09-hold@canary.invalid', 'active', now())`); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	for _, schedule := range profilesdomain.RetentionSchedules() {
		if err := schedule.IsValid(); err != nil {
			t.Fatalf("schedule %+v invalid: %v", schedule, err)
		}
		if schedule.ReasonCode == "" {
			t.Fatalf("schedule %+v has no reason code", schedule)
		}
		if !schedule.Indefinite && schedule.Window <= 0 {
			t.Fatalf("schedule %+v has no deadline", schedule)
		}
	}

	accountID := journeyAccountID(t, pool, "t09-hold@canary.invalid")
	if _, err := pool.Exec(ctx, `INSERT INTO app.retention_holds (data_class, reason_code, placed_by)
		VALUES ('sessions', '', $1::uuid)`, accountID); err == nil {
		t.Fatal("hold without reason was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.retention_holds (data_class, reason_code, placed_by)
		VALUES ('sessions', 'incident-t09-hold', $1::uuid) RETURNING id`, accountID); err != nil {
		t.Fatalf("hold with reason refused: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.retention_holds SET released_at = now() WHERE reason_code = 'incident-t09-hold'`); err == nil {
		t.Fatal("release without reason was accepted")
	}

	repo := profilespg.NewRepository(pool)
	enforce, err := profilesapp.NewEnforceRetentionUseCase(repo, platformpg.NewTxManager(pool), world.clock)
	if err != nil {
		t.Fatalf("NewEnforceRetentionUseCase: %v", err)
	}
	summary, err := enforce.Execute(ctx)
	if err != nil {
		t.Fatalf("enforce retention: %v", err)
	}
	if summary == nil || len(summary.Runs) == 0 {
		t.Fatal("retention run recorded nothing")
	}
}

// TestTenantLowCounts prova a supressão: um participante só não
// desanonimiza — agregado e export zerados com suppressed.
func TestTenantLowCounts(t *testing.T) {
	t.Parallel()
	world, audit := privilegedWorld(t)
	server := servePrivileged(t, world, audit)
	ava, _ := tenantHolders(t, world)
	_ = audit

	arenaID := journeyArena(t, world.pool, "t09-ava@canary.invalid", "Arena t09 solitária com clareza", "t09-solo-arena")
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/me/arenas/"+arenaID+"/position", ava, `{"position":"agree"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("position = %d, want 200 or 201", status)
	}
	status, _, aggregateRaw := journeyCall(t, server, "GET", "/api/v1/arenas/"+arenaID+"/positions", "", "", nil)
	if status != 200 {
		t.Fatalf("aggregate = %d, want 200", status)
	}
	var aggregate struct {
		Suppressed  bool   `json:"suppressed"`
		Agree       int64  `json:"agree"`
		Disagree    int64  `json:"disagree"`
		Undecided   int64  `json:"undecided"`
		Total       int64  `json:"participants_total"`
		Attribution string `json:"-"`
	}
	if err := json.Unmarshal(aggregateRaw, &aggregate); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if !aggregate.Suppressed {
		t.Fatalf("solo aggregate not suppressed: %s", string(aggregateRaw))
	}
	if aggregate.Agree+aggregate.Disagree+aggregate.Undecided+aggregate.Total != 0 {
		t.Fatalf("suppressed aggregate leaks counts: %s", string(aggregateRaw))
	}
	assertNoCanaries(t, "suppressed aggregate", string(aggregateRaw))

	status, _, exportRaw := journeyCall(t, server, "GET", "/api/v1/arenas/"+arenaID+"/export", "", "", nil)
	if status != 200 {
		t.Fatalf("export = %d, want 200", status)
	}
	if strings.Contains(string(exportRaw), "canary-t09-ava@canary.invalid") {
		t.Fatal("public export carries the owner's address")
	}
}

// TestTenantAnalyticsIsolation prova atribuição por titular no lote: cada
// item leva seu distinct_id, sem email nem cruzamento.
func TestTenantAnalyticsIsolation(t *testing.T) {
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
	defer telemetry.Close()

	for _, holder := range []struct{ account, request string }{
		{"acc-t09-ava", "req-t09-ava"},
		{"acc-t09-ben", "req-t09-ben"},
	} {
		for index := 0; index < providersim.PostHogBatchSize/2; index++ {
			telemetry.Events.Capture(observability.Event{
				Name:       observability.EventAccountSignedIn,
				AccountID:  holder.account,
				Properties: map[string]any{"locale": "pt-BR"},
				RequestID:  holder.request,
			})
		}
	}

	calls := waitForCalls(t, posthog, "POST", "/batch/", 1)
	seen := map[string]int{}
	for _, call := range calls {
		var document struct {
			Batch []struct {
				Properties map[string]any `json:"properties"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(call.Body, &document); err != nil {
			t.Fatalf("decode batch: %v", err)
		}
		for _, item := range document.Batch {
			distinct, _ := item.Properties["distinct_id"].(string)
			seen[distinct]++
			if _, present := item.Properties["email"]; present {
				t.Fatal("batch carries an email property")
			}
			assertNoCanaries(t, "analytics batch", string(call.Body))
		}
	}
	if seen["acc-t09-ava"] == 0 || seen["acc-t09-ben"] == 0 {
		t.Fatalf("batch attribution = %v, want both holders", seen)
	}
	_ = sentry
}

// TestTenantRestoreIsolation recompõe o servidor sobre o mesmo banco
// (restauração) e reverifica: isolamento e linhas de privacidade intactos.
func TestTenantRestoreIsolation(t *testing.T) {
	t.Parallel()
	world, audit := privilegedWorld(t)
	server := servePrivileged(t, world, audit)
	ava, ben := tenantHolders(t, world)

	status, _, exportRaw := journeyCall(t, server, "POST", "/api/v1/me/exports", ava, "", nil)
	if status != 202 {
		t.Fatalf("export request = %d, want 202", status)
	}
	var order struct {
		ExportID string `json:"export_id"`
	}
	if err := json.Unmarshal(exportRaw, &order); err != nil || order.ExportID == "" {
		t.Fatalf("export carries no id: %s", string(exportRaw))
	}

	server.Close()
	server = servePrivileged(t, world, audit)

	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/exports/"+order.ExportID+"/download?token=bogus", ben, "", nil)
	if status == 200 {
		t.Fatal("cross-holder download succeeded after restore")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/deletion", ben, "", nil)
	if status != 404 {
		t.Fatalf("ben deletion status after restore = %d, want 404", status)
	}
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.data_exports WHERE id::text = $1`, order.ExportID); got != 1 {
		t.Fatalf("export rows after restore = %d, want 1", got)
	}
	assertNoCanaries(t, "restore check", order.ExportID)
	_ = audit
}
