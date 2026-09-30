package main

// P27-T05 — experimentos de caos contra o binário real.
//
// Cada experimento declara hipótese e steady state, executa o `arena
// server` de verdade (jornada HTML de conta + health) sobre PostgreSQL
// descartável em loopback, injeta a falha (SIGKILL no meio da rajada,
// SIGTERM gracioso, pool exaurido) e confere recuperação dentro do
// budget com oráculos: credencial confirmada faz login depois, sem linha
// parcial. Nada toca ambiente externo (o PostgreSQL compartilhado nunca
// reinicia) e o teardown (kill, wait) roda sempre via t.Cleanup.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// chaosBinary builds the delivery binary once per test binary run: three
// experiments share one build instead of paying it three times.
var (
	chaosBinaryOnce sync.Once
	chaosBinaryPath string
	chaosBinaryErr  error
)

func chaosBinary(t *testing.T) string {
	t.Helper()
	chaosBinaryOnce.Do(func() {
		repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			chaosBinaryErr = err
			return
		}
		directory, err := os.MkdirTemp("", "arena-chaos-bin")
		if err != nil {
			chaosBinaryErr = err
			return
		}
		chaosBinaryPath = filepath.Join(directory, "arena")
		build := exec.Command("go", "build", "-o", chaosBinaryPath, "./cmd/arena")
		build.Dir = repoRoot
		if output, err := build.CombinedOutput(); err != nil {
			chaosBinaryErr = fmt.Errorf("go build: %v\n%s", err, output)
			return
		}
	})
	if chaosBinaryErr != nil {
		t.Fatalf("build arena: %v", chaosBinaryErr)
	}
	return chaosBinaryPath
}

// chaosServer is one running delivery process over a disposable database.
type chaosServer struct {
	command *exec.Cmd
	baseURL string
	logPath string
	sinkDir string
}

// startChaosServer boots the real server with the given extra environment
// and waits for liveness. The caller owns nothing: kill, wait and log
// collection all happen in t.Cleanup, so teardown always occurs. The
// child runs with the repository root as its working directory, like a
// developer boot: relative asset paths resolve the same way.
func startChaosServer(t *testing.T, database *dbtest.TestDB, extraEnv ...string) *chaosServer {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	logFile, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	sinkDir := filepath.Join(t.TempDir(), "email-sink")
	if err := os.MkdirAll(sinkDir, 0o755); err != nil {
		t.Fatalf("create sink dir: %v", err)
	}
	server := &chaosServer{baseURL: "http://" + address, logPath: logFile.Name(), sinkDir: sinkDir}
	command := exec.Command(chaosBinary(t), "server")
	command.Dir = repoRoot
	command.Env = append([]string{
		"ARENA_ADDR=" + address,
		"ARENA_ENV=development",
		"ARENA_DATABASE_URL=" + database.DSN,
		"ARENA_EMAIL_SINK_DIR=" + sinkDir,
		"PATH=" + os.Getenv("PATH"),
	}, extraEnv...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	server.command = command
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = logFile.Close()
	})
	server.waitLive(t, 20*time.Second)
	return server
}

// waitLive polls readiness until the budget runs out.
func (server *chaosServer) waitLive(t *testing.T, budget time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(budget)
	for {
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(server.logPath)
			t.Fatalf("server did not answer /health/live in %s; log:\n%s", budget, log)
		}
		response, err := client.Get(server.baseURL + "/health/live")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// chaosBrowser is a cookie-keeping client that stops at redirects: the
// journey asserts on statuses and cookies, not on landing pages.
func chaosBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

var chaosCSRFField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// chaosCSRFToken reads one page and returns its double-submit token.
// A transport error means the process is already gone: it reports
// failure, never fatal, because a dead server is the experiment.
func chaosCSRFToken(t *testing.T, client *http.Client, server *chaosServer, path string) (string, bool) {
	t.Helper()
	response, err := client.Get(server.baseURL + path)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, response.StatusCode)
	}
	match := chaosCSRFField.FindStringSubmatch(string(body))
	if match == nil {
		return "", false
	}
	return match[1], true
}

// chaosSinkToken polls the directory sink for the verification token of
// one address, the way a harness completes a journey owned by another
// process.
func chaosSinkToken(t *testing.T, server *chaosServer, stop <-chan struct{}, email string) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if stop != nil {
			select {
			case <-stop:
				// The process died mid-journey: unacknowledged work may
				// be lost, which is the correct semantic, not a failure.
				return "", false
			default:
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no verification email for %s in %s", email, server.sinkDir)
		}
		entries, err := os.ReadDir(server.sinkDir)
		if err != nil {
			t.Fatalf("read sink dir: %v", err)
		}
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(server.sinkDir, entry.Name()))
			if err != nil {
				continue
			}
			var message struct {
				Kind  string `json:"kind"`
				Email string `json:"email"`
				Token string `json:"token"`
			}
			if err := json.Unmarshal(raw, &message); err != nil {
				continue
			}
			if message.Kind == "verification" && message.Email == email && message.Token != "" {
				return message.Token, true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// chaosPostForm submits one HTML form with a fresh CSRF token and reports
// the status. Transport errors mean the process is gone and report -1:
// during chaos, a dead server is an outcome, not a test bug.
func chaosPostForm(t *testing.T, client *http.Client, server *chaosServer, page, path string, values url.Values) int {
	t.Helper()
	token, ok := chaosCSRFToken(t, client, server, page)
	if !ok {
		return -1
	}
	values.Set("csrf_token", token)
	response, err := client.PostForm(server.baseURL+path, values)
	if err != nil {
		return -1
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode >= 500 {
		t.Fatalf("POST %s = %d: never 5xx", path, response.StatusCode)
	}
	return response.StatusCode
}

// chaosVerifiedLogin registers, verifies and logs in through the browser
// journey, reporting whether a session cookie arrived.
func chaosVerifiedLogin(t *testing.T, client *http.Client, server *chaosServer, stop <-chan struct{}, email, password string) bool {
	t.Helper()
	if stop != nil {
		select {
		case <-stop:
			return false
		default:
		}
	}
	if status := chaosPostForm(t, client, server, "/register", "/register", url.Values{"email": {email}, "password": {password}}); status >= 400 {
		return false
	}
	token, ok := chaosSinkToken(t, server, stop, email)
	if !ok {
		return false
	}
	if status := chaosPostForm(t, client, server, "/verify", "/verify", url.Values{"token": {token}}); status >= 400 {
		return false
	}
	if status := chaosPostForm(t, client, server, "/login", "/login", url.Values{"email": {email}, "password": {password}}); status >= 400 {
		return false
	}
	parsed, err := url.Parse(server.baseURL)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == "arena_session" && cookie.Value != "" {
			return true
		}
	}
	return false
}

// chaosWaitAcked waits until at least want journeys are acknowledged,
// proving the burst is really in flight instead of sleeping a guess.
func chaosWaitAcked(t *testing.T, acked *atomic.Int64, want int64, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for acked.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("only %d journeys acknowledged in %s", acked.Load(), budget)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestChaosKillRestartsJourney kills -9 mid-burst and restarts on the same
// database. Hipótese: credenciais confirmadas (2xx/3xx) fazem login depois
// e nenhuma linha parcial existe. Steady state: todo confirmado autentica
// no processo novo em segundos.
func TestChaosKillRestartsJourney(t *testing.T) {
	database := dbtest.New(t)
	server := startChaosServer(t, database)

	const burst = 4
	type credential struct{ email, password string }
	var acknowledged sync.Map
	var ackedAtomic atomic.Int64
	var group sync.WaitGroup
	stop := make(chan struct{})
	for index := 0; index < burst; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			select {
			case <-stop:
				return
			default:
			}
			client := chaosBrowser(t)
			email := fmt.Sprintf("chaos-kill-%d@arena.example.com", index)
			password := fmt.Sprintf("chaos-kill-horse-%d", index)
			if chaosVerifiedLogin(t, client, server, stop, email, password) {
				acknowledged.Store(credential{email, password}, true)
				ackedAtomic.Add(1)
			}
		}(index)
	}
	// Wait until part of the burst is acknowledged instead of sleeping a
	// fixed duration: argon hashing sets the pace, and the kill must land
	// with work both done and in flight.
	chaosWaitAcked(t, &ackedAtomic, 1, 30*time.Second)
	close(stop)
	if err := server.command.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	_ = server.command.Wait()
	group.Wait()

	restarted := startChaosServer(t, database)
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
		t.Fatal("no registration acknowledged before SIGKILL; the experiment proved nothing")
	}
	if missing != 0 {
		t.Fatalf("%d of %d acknowledged credentials fail login after SIGKILL+restart", missing, acked)
	}
}

// TestChaosGracefulDrain sends SIGTERM mid-burst: o processo sai dentro do
// budget de drain, o confirmado continua logando e nada parcial aparece.
func TestChaosGracefulDrain(t *testing.T) {
	database := dbtest.New(t)
	server := startChaosServer(t, database)

	const burst = 4
	type credential struct{ email, password string }
	var acknowledged sync.Map
	var ackedAtomic atomic.Int64
	var group sync.WaitGroup
	for index := 0; index < burst; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			client := chaosBrowser(t)
			email := fmt.Sprintf("chaos-drain-%d@arena.example.com", index)
			password := fmt.Sprintf("chaos-drain-horse-%d", index)
			if chaosVerifiedLogin(t, client, server, nil, email, password) {
				acknowledged.Store(credential{email, password}, true)
				ackedAtomic.Add(1)
			}
		}(index)
	}
	chaosWaitAcked(t, &ackedAtomic, 1, 30*time.Second)
	if err := server.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- server.command.Wait() }()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("server did not exit within the drain budget after SIGTERM")
	}
	group.Wait()

	restarted := startChaosServer(t, database)
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
		t.Fatal("no registration acknowledged before SIGTERM; the experiment proved nothing")
	}
	if missing != 0 {
		t.Fatalf("%d of %d acknowledged credentials fail login after graceful drain", missing, acked)
	}
}

// TestChaosPoolExhaustion bounds the pool to 2 and fires concurrent
// logins: hipótese é fila limitada, não deadlock — tudo completa rápido,
// sem 500, e o tráfego seguinte passa.
func TestChaosPoolExhaustion(t *testing.T) {
	database := dbtest.New(t)
	server := startChaosServer(t, database, "ARENA_DB_MAX_CONNS=2")

	const accounts = 2
	for index := 0; index < accounts; index++ {
		if !chaosVerifiedLogin(t, chaosBrowser(t), server, nil,
			fmt.Sprintf("chaos-pool-%d@arena.example.com", index),
			fmt.Sprintf("chaos-pool-horse-%d", index)) {
			t.Fatalf("seed journey %d refused", index)
		}
	}

	const concurrent = 6
	statuses := make([]bool, concurrent)
	var group sync.WaitGroup
	started := time.Now()
	for index := range statuses {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			statuses[index] = chaosLoginOnly(t, chaosBrowser(t), server,
				fmt.Sprintf("chaos-pool-%d@arena.example.com", index%accounts),
				fmt.Sprintf("chaos-pool-horse-%d", index%accounts))
		}(index)
	}
	group.Wait()
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("pool of 2 took %s for %d logins (unbounded)", elapsed, concurrent)
	}
	for index, ok := range statuses {
		if !ok {
			t.Fatalf("login %d failed under pool pressure", index)
		}
	}
	if !chaosLoginOnly(t, chaosBrowser(t), server, "chaos-pool-0@arena.example.com", "chaos-pool-horse-0") {
		t.Fatal("traffic after pressure fails")
	}
}

// chaosLoginOnly submits only the login form and reports whether a session
// cookie arrived: for accounts the seed phase already verified.
func chaosLoginOnly(t *testing.T, client *http.Client, server *chaosServer, email, password string) bool {
	t.Helper()
	if status := chaosPostForm(t, client, server, "/login", "/login", url.Values{"email": {email}, "password": {password}}); status >= 400 {
		return false
	}
	parsed, err := url.Parse(server.baseURL)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == "arena_session" && cookie.Value != "" {
			return true
		}
	}
	return false
}
