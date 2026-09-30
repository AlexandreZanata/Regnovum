package main

// P27-T07 — shutdown, deploy e rollback sobre o binário e o transporte reais.
//
// Hipótese: SIGTERM no meio de request/transação/webhook/job drena o aceito
// ou o devolve a estado seguro; deploy N→N+1 com schema expand mantém a
// release antiga servindo até a nova ficar ready; rollback é do processo e
// nunca exige down migration.
// Steady state: todo confirmado antes do sinal continua válido depois;
// nenhuma linha parcial; ready 503 com dependência caída e 200 com ela sã;
// drain medido dentro do budget de 10s (+margem de supervisor 20s).
// Comando: go test ./cmd/arena -run TestDeploy -count=1 -v.
// Estado esperado antes: servidor live+ready, DB migrado, fila vazia.
// Estado esperado depois: mesmo, com schema expand intacto e sem órfãos.
// Limpeza: t.Cleanup em startChaosServer (kill+wait) e dbtest (DROP DATABASE).

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// deployWaitReady polls /health/ready until 200 or the budget ends.
func deployWaitReady(t *testing.T, server *chaosServer, budget time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(budget)
	for {
		response, err := client.Get(server.baseURL + "/health/ready")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became ready in %s", budget)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// deployReadyCode probes /health/ready once.
func deployReadyCode(t *testing.T, server *chaosServer) int {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(server.baseURL + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

// TestDeployGracefulRequestDrain sends SIGTERM mid-burst: o aceito termina ou
// volta a estado seguro, o drain é medido dentro do budget e o confirmado
// continua logando depois do restart.
func TestDeployGracefulRequestDrain(t *testing.T) {
	database := dbtest.New(t)
	server := startChaosServer(t, database)
	if got := deployReadyCode(t, server); got != http.StatusOK {
		t.Fatalf("ready before deploy = %d, want 200", got)
	}

	const burst = 3
	type credential struct{ email, password string }
	var acknowledged sync.Map
	var ackedAtomic atomic.Int64
	var group sync.WaitGroup
	for index := 0; index < burst; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			client := chaosBrowser(t)
			email := fmt.Sprintf("deploy-drain-%d@arena.example.com", index)
			password := fmt.Sprintf("deploy-drain-horse-%d", index)
			if chaosVerifiedLogin(t, client, server, nil, email, password) {
				acknowledged.Store(credential{email, password}, true)
				ackedAtomic.Add(1)
			}
		}(index)
	}
	chaosWaitAcked(t, &ackedAtomic, 1, 30*time.Second)
	started := time.Now()
	if err := server.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- server.command.Wait() }()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("server did not drain within 20s after SIGTERM (budget 10s + margem)")
	}
	drained := time.Since(started)
	if drained > 20*time.Second {
		t.Fatalf("drain took %s, want within 20s", drained)
	}
	t.Logf("drain measured at %s", drained)
	group.Wait()

	restarted := startChaosServer(t, database)
	deployWaitReady(t, restarted, 20*time.Second)
	acked, missing := 0, 0
	acknowledged.Range(func(key, _ any) bool {
		credential := key.(credential)
		acked++
		if !chaosLoginOnly(t, chaosBrowser(t), restarted, credential.email, credential.password) {
			missing++
		}
		return true
	})
	if acked == 0 {
		t.Fatal("no request acknowledged before SIGTERM; the experiment proved nothing")
	}
	if missing != 0 {
		t.Fatalf("%d of %d accepted requests lost after drain+restart", missing, acked)
	}
}

// TestDeployTransactionLeavesNoOrphan cancels a transaction mid-flight: a
// rollback explícito (o que o shutdown gracioso faz) não deixa linha nem
// transação aberta, e o commit vizinho sobrevive ao restart.
func TestDeployTransactionLeavesNoOrphan(t *testing.T) {
	database := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := database.Exec(ctx, `CREATE TABLE deploy_tx_probe (id text PRIMARY KEY, note text)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	committed := "deploy-tx-committed"
	if _, err := database.Exec(ctx, `INSERT INTO deploy_tx_probe (id) VALUES ($1)`, committed); err != nil {
		t.Fatalf("seed committed row: %v", err)
	}

	abandoned, err := database.Pool.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := abandoned.Exec(ctx, `INSERT INTO deploy_tx_probe (id) VALUES ($1)`, "deploy-tx-abandoned"); err != nil {
		_ = abandoned.Rollback(ctx)
		t.Fatalf("insert in tx: %v", err)
	}
	// SIGTERM no meio da transação = rollback, nunca commit parcial.
	if err := abandoned.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var abandonedCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM deploy_tx_probe WHERE id = 'deploy-tx-abandoned'`).Scan(&abandonedCount); err != nil {
		t.Fatalf("count abandoned: %v", err)
	}
	if abandonedCount != 0 {
		t.Fatalf("abandoned row survived rollback: count = %d, want 0", abandonedCount)
	}
	var idleTx int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'idle in transaction'`).Scan(&idleTx); err != nil {
		t.Fatalf("count idle in transaction: %v", err)
	}
	if idleTx != 0 {
		t.Fatalf("idle in transaction = %d, want 0 (transação órfã)", idleTx)
	}
	var kept int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM deploy_tx_probe WHERE id = $1`, committed).Scan(&kept); err != nil {
		t.Fatalf("count committed: %v", err)
	}
	if kept != 1 {
		t.Fatalf("committed row = %d, want 1", kept)
	}
}

// TestDeployWebhookCompletesOrRollsBack proves the webhook-shaped POST either
// finishes under SIGTERM or leaves no partial row: cada 200 tem exatamente
// uma linha, e erro de transporte nunca escreve.
func TestDeployWebhookCompletesOrRollsBack(t *testing.T) {
	database := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := database.Exec(ctx, `CREATE TABLE deploy_webhook_probe (key text PRIMARY KEY)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}

	release := make(chan struct{})
	started := make(chan string, 8)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		key := request.URL.Query().Get("key")
		if key == "" {
			http.Error(writer, "missing key", http.StatusBadRequest)
			return
		}
		select {
		case started <- key:
		default:
		}
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		if _, err := database.Exec(request.Context(), `INSERT INTO deploy_webhook_probe (key) VALUES ($1) ON CONFLICT DO NOTHING`, key); err != nil {
			http.Error(writer, "store", http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.Handle("POST /hooks/deploy", handler)
	mux.Handle("GET /health/live", httpserver.LiveHandler())
	server, err := httpserver.New(httpserver.Options{
		Addr:    "127.0.0.1:0",
		Handler: mux,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := server.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	runCtx, stop := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(runCtx) }()
	base := "http://" + server.Addr()

	const inflight = 2
	results := make([]int, inflight)
	var group sync.WaitGroup
	for index := 0; index < inflight; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			key := fmt.Sprintf("deploy-hook-%d", index)
			response, err := http.Post(base+"/hooks/deploy?key="+url.QueryEscape(key), "text/plain", strings.NewReader("payload"))
			if err != nil {
				results[index] = -1
				return
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			results[index] = response.StatusCode
			if response.StatusCode >= 500 {
				t.Errorf("POST webhook = %d: never 5xx", response.StatusCode)
			}
		}(index)
	}
	// Wait until both handlers are inside the critical section, then SIGTERM.
	deadline := time.Now().Add(10 * time.Second)
	seen := 0
	for seen < inflight {
		select {
		case <-started:
			seen++
		default:
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("webhook handlers never reached the critical section")
		}
		time.Sleep(20 * time.Millisecond)
	}
	drainStarted := time.Now()
	stop()
	close(release)
	group.Wait()
	if elapsed := time.Since(drainStarted); elapsed > 20*time.Second {
		t.Fatalf("webhook drain took %s, want within 20s", elapsed)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after cancel")
	}
	succeeded := 0
	for _, code := range results {
		if code == http.StatusOK {
			succeeded++
		} else if code != -1 {
			t.Fatalf("webhook status = %d, want 200 or transport error only", code)
		}
	}
	var rows int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM deploy_webhook_probe`).Scan(&rows); err != nil {
		t.Fatalf("count probe: %v", err)
	}
	if rows != succeeded {
		t.Fatalf("probe rows = %d, want exactly the %d succeeded webhooks (sem parcial)", rows, succeeded)
	}
}

// TestDeployJobLeaseSurvivesRestart abandons a lease like a SIGKILLed worker:
// o reclaim devolve o job, o próximo worker completa e o efeito existe uma vez.
func TestDeployJobLeaseSurvivesRestart(t *testing.T) {
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	repo := postgres.NewRepository(pool)
	clock := clockseed.NewClock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS deploy_marks (job_id text PRIMARY KEY)`); err != nil {
		t.Fatalf("create marks: %v", err)
	}
	enqueue, err := jobsapp.NewEnqueueUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}
	lease, err := jobsapp.NewLeaseUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}
	recover, err := jobsapp.NewRecoverExpiredLeasesUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewRecoverExpiredLeasesUseCase: %v", err)
	}
	complete, err := jobsapp.NewCompleteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewCompleteUseCase: %v", err)
	}
	result, err := enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeSessionCleanup, Version: 1, Payload: []byte(`{}`),
		IdempotencyKey: "deploy-job-1", MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	jobID := result.Job.ID
	// Worker morto segura o lease e morre sem completar (o SIGTERM do deploy).
	if _, err := lease.Lease(ctx, "deploy-worker-dead", 2*time.Second); err != nil {
		t.Fatalf("abandon lease: %v", err)
	}
	reclaimDeadline := time.Now().Add(15 * time.Second)
	reclaimed := 0
	for reclaimed == 0 {
		var err error
		reclaimed, err = recover.Recover(ctx)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if reclaimed == 0 {
			if time.Now().After(reclaimDeadline) {
				t.Fatal("abandoned lease never expired for reclaim")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	claimed, err := lease.Lease(ctx, "deploy-worker-next", 30*time.Second)
	if err != nil {
		t.Fatalf("re-lease: %v", err)
	}
	if claimed == nil || claimed.ID != jobID {
		t.Fatalf("re-leased job = %+v, want %s", claimed, jobID)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deploy_marks (job_id) VALUES ($1) ON CONFLICT DO NOTHING`, jobID); err != nil {
		t.Fatalf("mark effect: %v", err)
	}
	if _, err := complete.Complete(ctx, jobID, "deploy-worker-next"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var marks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM deploy_marks WHERE job_id = $1`, jobID).Scan(&marks); err != nil {
		t.Fatalf("count marks: %v", err)
	}
	if marks != 1 {
		t.Fatalf("business effect rows = %d, want exactly 1", marks)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state::text FROM app.jobs WHERE id::text = $1`, jobID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "succeeded" {
		t.Fatalf("state = %q, want succeeded", state)
	}
}

// TestDeployExpandRollbackWithoutDown simulates N→N+1 with an expand-only
// column: a escrita antiga (sem a coluna) continua válida, a nova usa a
// coluna, e o rollback (binário antigo) serve sem down migration.
func TestDeployExpandRollbackWithoutDown(t *testing.T) {
	database := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := database.Exec(ctx, `CREATE TABLE deploy_expand_probe (id text PRIMARY KEY, payload text NOT NULL)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	// Release N escreve sem a coluna nova.
	if _, err := database.Exec(ctx, `INSERT INTO deploy_expand_probe (id, payload) VALUES ('n-old', 'vN')`); err != nil {
		t.Fatalf("vN insert: %v", err)
	}
	// Passo expand da N+1: coluna anulável, compatível com a N.
	if _, err := database.Exec(ctx, `ALTER TABLE deploy_expand_probe ADD COLUMN IF NOT EXISTS note text`); err != nil {
		t.Fatalf("expand: %v", err)
	}
	// A N antiga continua servindo sobre o schema expandido.
	if _, err := database.Exec(ctx, `INSERT INTO deploy_expand_probe (id, payload) VALUES ('n-old-2', 'vN pós-expand')`); err != nil {
		t.Fatalf("vN insert after expand: %v", err)
	}
	// A N+1 usa a coluna nova.
	if _, err := database.Exec(ctx, `INSERT INTO deploy_expand_probe (id, payload, note) VALUES ('n-new', 'vN+1', 'expand')`); err != nil {
		t.Fatalf("vN+1 insert: %v", err)
	}
	// Deploy: a nova instância só fica ready com dependência sã.
	// Rollback: o binário antigo serve sobre o mesmo schema, sem down.
	server := startChaosServer(t, database)
	deployWaitReady(t, server, 20*time.Second)
	if !chaosLoginOnly(t, chaosBrowser(t), server, deploySeedLogin(t, server), deploySeedPassword()) {
		t.Fatal("old binary does not serve after expand (rollback quebraria)")
	}
	var expandCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'deploy_expand_probe' AND column_name = 'note'`).Scan(&expandCount); err != nil {
		t.Fatalf("count expand column: %v", err)
	}
	if expandCount != 1 {
		t.Fatalf("expand column missing after rollback: count = %d, want 1 (schema segue em frente)", expandCount)
	}
	var rows int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM deploy_expand_probe`).Scan(&rows); err != nil {
		t.Fatalf("count probe: %v", err)
	}
	if rows != 3 {
		t.Fatalf("probe rows = %d, want 3 (nenhuma perda no deploy/rollback)", rows)
	}
	// O runner não tem caminho de volta: migrate down é recusa, não operação.
	binary := chaosBinary(t)
	command := exec.Command(binary, "migrate", "down")
	command.Env = append([]string{"PATH=" + os.Getenv("PATH"), "ARENA_DATABASE_URL=" + database.DSN}, os.Environ()...)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("migrate down succeeded, want refusal (output: %s)", output)
	}
	if !strings.Contains(string(output), "unknown migrate subcommand") {
		t.Fatalf("migrate down error = %q, want it to report unknown migrate subcommand", output)
	}
}

var (
	deploySeedOnce     sync.Once
	deploySeedEmail    string
	deploySeedPassword = func() string { return "deploy-expand-horse" }
)

// deploySeedLogin registers one account for the rollback probe.
func deploySeedLogin(t *testing.T, server *chaosServer) string {
	t.Helper()
	deploySeedOnce.Do(func() {
		deploySeedEmail = "deploy-expand@arena.example.com"
	})
	client := chaosBrowser(t)
	if chaosVerifiedLogin(t, client, server, nil, deploySeedEmail, deploySeedPassword()) {
		return deploySeedEmail
	}
	return deploySeedEmail
}
