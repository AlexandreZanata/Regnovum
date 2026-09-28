package regression

// P27-T09 — soak e leaks sobre PostgreSQL descartável e HTTP em loopback.
//
// Hipótese: uma jornada mista prolongada (jobs com worker real, créditos INK
// reais, HTTP ok/lento/cancelado) não vaza heap, goroutines, FDs nem conexões,
// drena a fila e mantém a latência no budget; o comparador de budgets acusa
// crescimento sintético nas duas direções.
// Steady state: fila drenada (queued==leased==0, dead==0), efeito de negócio
// 1x por job, saldo derivado == soma creditada, zero conclusões tardias.
// Comando: go test ./internal/regression/ -run TestSoak -count=1 -v.
// Estado esperado antes: banco migrado vazio, worker e servidores no ar.
// Estado esperado depois: mesmo, com relatório no log e teardown total.
// Limpeza: t.Cleanup fecha worker/simulator/servidores e dbtest derruba o banco.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Soak budgets, ratificados nesta tarefa: o soak detecta vazamento
// monotônico grosseiro (recurso por iteração), não micro-variação de
// alocador — a certificação fina de heap é gate de release (P45).
const (
	soakWarmupIterations = 30
	soakWindowIterations = 300
	soakWindowDuration   = 60 * time.Second
	soakDrainBudget      = 60 * time.Second

	soakMaxHeapDeltaBytes = 8 << 20
	soakMaxGoroutineDelta = 8
	soakMaxFDDelta        = 8
	soakMaxP95Latency     = 2 * time.Second
)

// soakSnapshot is one reading of the process resources under load.
type soakSnapshot struct {
	HeapBytes     uint64 `json:"heap_bytes"`
	Goroutines    int    `json:"goroutines"`
	FDs           int    `json:"fds"`
	FDsMeasured   bool   `json:"fds_measured"`
	TotalConns    int32  `json:"total_conns"`
	AcquiredConns int32  `json:"acquired_conns"`
	Queued        int64  `json:"queued"`
	Leased        int64  `json:"leased"`
}

// soakBudgets bounds the growth the window may show over the baseline.
type soakBudgets struct {
	MaxHeapDelta uint64
	MaxRoutine   int64
	MaxFD        int64
}

// soakReport is the execution report the task requires: hardware, seed,
// duration and commit travel with every measurement.
type soakReport struct {
	OS         string       `json:"os"`
	Arch       string       `json:"arch"`
	CPUs       int          `json:"cpus"`
	GoVersion  string       `json:"go_version"`
	Seed       int64        `json:"seed"`
	DurationS  float64      `json:"duration_s"`
	Commit     string       `json:"commit"`
	Iterations int          `json:"iterations"`
	P50MS      float64      `json:"p50_ms"`
	P95MS      float64      `json:"p95_ms"`
	MaxMS      float64      `json:"max_ms"`
	Before     soakSnapshot `json:"before"`
	After      soakSnapshot `json:"after"`
	Jobs       int64        `json:"jobs_completed"`
	Late       int64        `json:"late_effects"`
	Violations []string     `json:"violations"`
}

func soakSeed(t *testing.T) int64 {
	t.Helper()
	return testsource.SeedFor(t)
}

func soakCommit() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func soakHeapBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

func soakFDs() (int, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

// soakCompareBudgets judges growth against the budgets. Connection counts
// are not compared here: pgx keeps idle connections up to MaxConns by
// design, so growth there is pooling, not leaking — the leak signal for
// connections is AcquiredConns != 0 with nobody working, asserted directly.
func soakCompareBudgets(budgets soakBudgets, before, after soakSnapshot) []string {
	var violations []string
	if after.HeapBytes > before.HeapBytes {
		if delta := after.HeapBytes - before.HeapBytes; delta > budgets.MaxHeapDelta {
			violations = append(violations, fmt.Sprintf("heap grew by %d bytes (budget %d)", delta, budgets.MaxHeapDelta))
		}
	}
	if delta := int64(after.Goroutines) - int64(before.Goroutines); delta > budgets.MaxRoutine {
		violations = append(violations, fmt.Sprintf("goroutines grew by %d (budget %d)", delta, budgets.MaxRoutine))
	}
	if before.FDsMeasured && after.FDsMeasured {
		if delta := int64(after.FDs) - int64(before.FDs); delta > budgets.MaxFD {
			violations = append(violations, fmt.Sprintf("fds grew by %d (budget %d)", delta, budgets.MaxFD))
		}
	}
	return violations
}

func soakPercentile(durations []time.Duration, fraction float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), durations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(fraction * float64(len(ordered)-1))
	return ordered[index]
}

func soakUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// soakWorld is the mixed journey: real queue with a real worker, real INK
// credits, real HTTP (fast, slow-provider, cancellable) over one database.
type soakWorld struct {
	pool      *pgxpool.Pool
	enqueue   *jobsapp.EnqueueUseCase
	health    *jobsapp.GetQueueHealthUseCase
	worker    *jobsapp.Worker
	workerEnd context.CancelFunc
	workerErr chan error
	wallet    *walletpg.Repository
	account   walletdomain.AccountID
	mux       *http.ServeMux
	server    *httptest.Server
	simulator *providersim.Simulator
	client    *http.Client
	late      atomic.Int64
	enqueued  atomic.Int64
	credited  atomic.Int64
}

func soakNewWorld(t *testing.T) *soakWorld {
	t.Helper()
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS soak_marks (job_id text PRIMARY KEY)`); err != nil {
		t.Fatalf("create soak marks: %v", err)
	}

	clock := clockseed.NewClock()
	jobsRepository := jobsrepo.NewRepository(pool)
	enqueue, err := jobsapp.NewEnqueueUseCase(jobsRepository, clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}
	lease, err := jobsapp.NewLeaseUseCase(jobsRepository, clock)
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}
	complete, err := jobsapp.NewCompleteUseCase(jobsRepository, clock)
	if err != nil {
		t.Fatalf("NewCompleteUseCase: %v", err)
	}
	fail, err := jobsapp.NewFailUseCase(jobsRepository, clock)
	if err != nil {
		t.Fatalf("NewFailUseCase: %v", err)
	}
	recover, err := jobsapp.NewRecoverExpiredLeasesUseCase(jobsRepository, clock)
	if err != nil {
		t.Fatalf("NewRecoverExpiredLeasesUseCase: %v", err)
	}
	health, err := jobsapp.NewGetQueueHealthUseCase(jobsRepository, clock)
	if err != nil {
		t.Fatalf("NewGetQueueHealthUseCase: %v", err)
	}
	registry := jobsapp.NewHandlerMap()
	world := &soakWorld{pool: pool, enqueue: enqueue, health: health}
	if err := registry.Register(jobsdomain.TypeSessionCleanup, 1, func(ctx context.Context, job *jobsdomain.Job) error {
		if _, err := pool.Exec(ctx, `INSERT INTO soak_marks (job_id) VALUES ($1) ON CONFLICT DO NOTHING`, job.ID); err != nil {
			return fmt.Errorf("mark soak job: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("register soak handler: %v", err)
	}
	worker, err := jobsapp.NewWorker(jobsapp.WorkerDeps{
		Lease: lease, Complete: complete, Fail: fail, Recover: recover,
		Registry: registry, Clock: clock, Random: clockseed.NewRandom(),
		Config: jobsapp.WorkerConfig{
			Concurrency: 2, LeaseDuration: 30 * time.Second, HandlerTimeout: 10 * time.Second,
			PollInterval: 10 * time.Millisecond,
			Backoff:      jobsapp.BackoffPolicy{Base: 10 * time.Millisecond, Max: 100 * time.Millisecond},
		},
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	world.worker = worker
	workerCtx, workerCancel := context.WithCancel(context.Background())
	world.workerEnd = workerCancel
	world.workerErr = make(chan error, 1)
	go func() { world.workerErr <- worker.Run(workerCtx) }()
	t.Cleanup(func() {
		workerCancel()
		<-world.workerErr
	})

	queries := platformpg.New(pool)
	created, err := queries.CreateAccount(ctx, platformpg.CreateAccountParams{Email: "soak@arena.example.com", Status: "active"})
	if err != nil {
		t.Fatalf("create soak account: %v", err)
	}
	world.account = walletdomain.AccountID(soakUUID(created.ID))
	world.wallet = walletpg.NewRepository(pool)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /soak/ok", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /soak/cancel", func(writer http.ResponseWriter, request *http.Request) {
		// The safety timer is deliberately far above the client timeout
		// below (20ms): the test proves the handler leaves through the
		// cancelled context, and a tight timer would race the TCP
		// propagation of the cancel instead of proving anything — under
		// -race that race is lost often enough to flake. If the timer
		// ever fires, the handler genuinely outlived the cancel.
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			world.late.Add(1)
			writer.WriteHeader(http.StatusOK)
		case <-request.Context().Done():
			return
		}
	})
	world.mux = mux
	world.server = httptest.NewServer(mux)
	t.Cleanup(world.server.Close)
	world.client = &http.Client{Timeout: 5 * time.Second}

	simulator := providersim.New(t, "soak-provider")
	simulator.Route("GET /slow", providersim.Slow(providersim.Reply(http.StatusOK, `{"status":"ok"}`), 100*time.Millisecond))
	world.simulator = simulator
	t.Cleanup(simulator.Stop)
	return world
}

func soakCredit(t *testing.T, ctx context.Context, world *soakWorld, index int) {
	t.Helper()
	ink, err := walletdomain.NewInk(10)
	if err != nil {
		t.Fatalf("NewInk: %v", err)
	}
	reference, err := walletdomain.ParseReference(fmt.Sprintf("soak:%06d", index))
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	key, err := walletdomain.ParseIdempotencyKey(fmt.Sprintf("soak-key-%06d", index))
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	operationType := walletdomain.OperationCreditFree
	delta, err := operationType.Direction()
	if err != nil {
		t.Fatalf("Direction: %v", err)
	}
	amount, err := delta.Apply(ink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	result, err := world.wallet.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID: world.account, Bucket: walletdomain.BucketFree,
		OperationType: operationType, IdempotencyKey: key,
		Reference: reference, Delta: amount, ChangedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ApplyCredit %d: %v", index, err)
	}
	if result.Replayed {
		t.Fatalf("credit %d replayed on first use", index)
	}
	world.credited.Add(amount)
}

// soakIteration runs one mixed step and reports its latency.
func soakIteration(t *testing.T, ctx context.Context, world *soakWorld, index int) time.Duration {
	t.Helper()
	started := time.Now()
	result, err := world.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeSessionCleanup, Version: 1, Payload: []byte(`{}`),
		IdempotencyKey: fmt.Sprintf("soak-%06d", index), MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("enqueue %d: %v", index, err)
	}
	if result.Replayed {
		t.Fatalf("enqueue %d replayed on first use", index)
	}
	world.enqueued.Add(1)
	if index%2 == 0 {
		soakCredit(t, ctx, world, index)
	}
	response, err := world.client.Get(world.server.URL + "/soak/ok")
	if err != nil {
		t.Fatalf("GET /soak/ok %d: %v", index, err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /soak/ok %d = %d, want 200", index, response.StatusCode)
	}
	if index%5 == 0 {
		slow, err := world.client.Get(world.simulator.URL() + "/slow")
		if err != nil {
			t.Fatalf("GET slow provider %d: %v", index, err)
		}
		_, _ = io.Copy(io.Discard, slow.Body)
		_ = slow.Body.Close()
		if slow.StatusCode != http.StatusOK {
			t.Fatalf("GET slow provider %d = %d, want 200", index, slow.StatusCode)
		}
	}
	if index%10 == 0 {
		cancelCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		request, err := http.NewRequestWithContext(cancelCtx, http.MethodPost, world.server.URL+"/soak/cancel", nil)
		if err != nil {
			t.Fatalf("build cancel request %d: %v", index, err)
		}
		if _, err := world.client.Do(request); err == nil {
			t.Fatalf("POST /soak/cancel %d succeeded, want client cancellation", index)
		}
	}
	return time.Since(started)
}

func soakQueueDepth(t *testing.T, ctx context.Context, world *soakWorld) (queued, leased, dead int64) {
	t.Helper()
	report, err := world.health.Execute(ctx)
	if err != nil {
		t.Fatalf("queue health: %v", err)
	}
	return report.Queue.Queued, report.Queue.Leased, report.Queue.Dead
}

func soakDrain(t *testing.T, ctx context.Context, world *soakWorld, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		queued, leased, _ := soakQueueDepth(t, ctx, world)
		if queued == 0 && leased == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not drain in %s (queued=%d leased=%d)", budget, queued, leased)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func soakCapture(t *testing.T, ctx context.Context, world *soakWorld) soakSnapshot {
	t.Helper()
	fds, ok := soakFDs()
	stat := world.pool.Stat()
	queued, leased, _ := soakQueueDepth(t, ctx, world)
	return soakSnapshot{
		HeapBytes:     soakHeapBytes(),
		Goroutines:    soakGoroutines(),
		FDs:           fds,
		FDsMeasured:   ok,
		TotalConns:    stat.TotalConns(),
		AcquiredConns: stat.AcquiredConns(),
		Queued:        queued,
		Leased:        leased,
	}
}

func soakGoroutines() int {
	return runtime.NumGoroutine()
}

// TestSoakMixedJourneyDetectsLeaks runs the prolonged mixed journey and
// proves resources stay within budget after warmup and GC.
func TestSoakMixedJourneyDetectsLeaks(t *testing.T) {
	seed := soakSeed(t)
	world := soakNewWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for index := 0; index < soakWarmupIterations; index++ {
		soakIteration(t, ctx, world, index)
	}
	soakDrain(t, ctx, world, soakDrainBudget)
	before := soakCapture(t, ctx, world)

	var durations []time.Duration
	started := time.Now()
	iterations := 0
	for iterations < soakWindowIterations || time.Since(started) < soakWindowDuration {
		durations = append(durations, soakIteration(t, ctx, world, soakWarmupIterations+iterations))
		iterations++
	}
	soakDrain(t, ctx, world, soakDrainBudget)
	after := soakCapture(t, ctx, world)
	elapsed := time.Since(started)

	budgets := soakBudgets{MaxHeapDelta: soakMaxHeapDeltaBytes, MaxRoutine: soakMaxGoroutineDelta, MaxFD: soakMaxFDDelta}
	violations := soakCompareBudgets(budgets, before, after)
	if after.AcquiredConns != 0 {
		violations = append(violations, fmt.Sprintf("%d connections still acquired with nobody working", after.AcquiredConns))
	}
	if after.Queued != 0 || after.Leased != 0 {
		violations = append(violations, fmt.Sprintf("queue not drained (queued=%d leased=%d)", after.Queued, after.Leased))
	}
	p95 := soakPercentile(durations, 0.95)
	if p95 > soakMaxP95Latency {
		violations = append(violations, fmt.Sprintf("p95 latency %s exceeds %s", p95, soakMaxP95Latency))
	}

	var marks, completed int64
	if err := world.pool.QueryRow(ctx, `SELECT count(*) FROM soak_marks`).Scan(&marks); err != nil {
		t.Fatalf("count soak marks: %v", err)
	}
	stats := world.worker.Stats()
	completed = stats.Succeeded
	if marks != world.enqueued.Load() {
		violations = append(violations, fmt.Sprintf("business effect rows = %d, want exactly %d enqueued (1x)", marks, world.enqueued.Load()))
	}
	balance, err := world.wallet.DerivedBalance(ctx, world.account)
	if err != nil {
		t.Fatalf("DerivedBalance: %v", err)
	}
	if got := balance.Free.Int64() + balance.Purchased.Int64(); got != world.credited.Load() {
		violations = append(violations, fmt.Sprintf("derived balance = %d, want conserved %d", got, world.credited.Load()))
	}
	if late := world.late.Load(); late != 0 {
		violations = append(violations, fmt.Sprintf("%d cancelled requests completed late (timer survived cancel)", late))
	}
	if len(world.simulator.CallsTo("GET", "/slow")) == 0 {
		t.Fatal("slow provider received no calls; the journey proved nothing about it")
	}

	report := soakReport{
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		GoVersion: runtime.Version(), Seed: seed, DurationS: elapsed.Seconds(),
		Commit: soakCommit(), Iterations: iterations,
		P50MS:  float64(soakPercentile(durations, 0.50).Microseconds()) / 1000,
		P95MS:  float64(p95.Microseconds()) / 1000,
		MaxMS:  float64(soakPercentile(durations, 1).Microseconds()) / 1000,
		Before: before, After: after, Jobs: completed,
		Late: world.late.Load(), Violations: violations,
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("encode soak report: %v", err)
	}
	t.Logf("soak report:\n%s", encoded)
	if len(violations) != 0 {
		t.Fatalf("soak budgets refused %d violation(s): %s", len(violations), strings.Join(violations, "; "))
	}
}

// TestSoakBudgetsRefuseSyntheticGrowth proves the comparator alive in both
// directions: synthetic growth is refused, identical readings pass.
func TestSoakBudgetsRefuseSyntheticGrowth(t *testing.T) {
	budgets := soakBudgets{MaxHeapDelta: soakMaxHeapDeltaBytes, MaxRoutine: soakMaxGoroutineDelta, MaxFD: soakMaxFDDelta}
	before := soakSnapshot{HeapBytes: 10 << 20, Goroutines: 20, FDs: 30, FDsMeasured: true}
	leaked := soakSnapshot{HeapBytes: (10 << 20) + soakMaxHeapDeltaBytes + 1, Goroutines: 20, FDs: 30, FDsMeasured: true}
	if violations := soakCompareBudgets(budgets, before, leaked); len(violations) == 0 {
		t.Fatal("heap growth far above budget was accepted")
	}
	routineLeak := soakSnapshot{HeapBytes: 10 << 20, Goroutines: 20 + soakMaxGoroutineDelta + 1, FDs: 30, FDsMeasured: true}
	if violations := soakCompareBudgets(budgets, before, routineLeak); len(violations) == 0 {
		t.Fatal("goroutine growth above budget was accepted")
	}
	fdLeak := soakSnapshot{HeapBytes: 10 << 20, Goroutines: 20, FDs: 30 + soakMaxFDDelta + 1, FDsMeasured: true}
	if violations := soakCompareBudgets(budgets, before, fdLeak); len(violations) == 0 {
		t.Fatal("fd growth above budget was accepted")
	}
	if violations := soakCompareBudgets(budgets, before, before); len(violations) != 0 {
		t.Fatalf("identical readings refused: %s", strings.Join(violations, "; "))
	}
	unmeasured := soakSnapshot{HeapBytes: 10 << 20, Goroutines: 20, FDsMeasured: false}
	if violations := soakCompareBudgets(budgets, before, unmeasured); len(violations) != 0 {
		t.Fatalf("unavailable fd reading refused: %s", strings.Join(violations, "; "))
	}
}
