package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func runForTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	t.Cleanup(func() { os.Remove(stdoutFile.Name()) })

	err = run(args, stdoutFile)

	if _, seekErr := stdoutFile.Seek(0, 0); seekErr != nil {
		t.Fatalf("seek temp file: %v", seekErr)
	}
	if _, copyErr := stdout.ReadFrom(stdoutFile); copyErr != nil {
		t.Fatalf("read temp file: %v", copyErr)
	}
	if closeErr := stdoutFile.Close(); closeErr != nil {
		t.Fatalf("close temp file: %v", closeErr)
	}

	return stdout.String(), stderr.String(), err
}

func assertError(t *testing.T, err error, wantSubstring string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected error containing %q, got %q", wantSubstring, err.Error())
	}
}

func assertStdout(t *testing.T, stdout, want string) {
	t.Helper()

	if stdout != want {
		t.Fatalf("stdout mismatch\n got: %q\nwant: %q", stdout, want)
	}
}

func TestRunVersionReportsDevelopmentVersion(t *testing.T) {
	stdout, _, err := runForTest(t, "version")
	if err != nil {
		t.Fatalf("run version: %v", err)
	}
	assertStdout(t, stdout, "arena version dev\n")
}

func TestRunVersionRejectsUnknownFlags(t *testing.T) {
	_, _, err := runForTest(t, "version", "extra")
	assertError(t, err, `unknown flag "extra"`)
	assertError(t, err, `Usage: arena version [--json]`)
}

func TestRunVersionJSONEmitsSingleValidObject(t *testing.T) {
	stdout, _, err := runForTest(t, "version", "--json")
	if err != nil {
		t.Fatalf("run version --json: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("version --json output is not valid JSON: %v\n%s", err, stdout)
	}
	for _, key := range []string{"version", "commit", "date"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("JSON missing key %q: %s", key, stdout)
		}
	}
	if payload["version"] != "dev" {
		t.Fatalf("version = %v, want dev", payload["version"])
	}
	if payload["commit"] != "unknown" {
		t.Fatalf("commit = %v, want unknown", payload["commit"])
	}
	if payload["date"] != "unknown" {
		t.Fatalf("date = %v, want unknown", payload["date"])
	}
}

func TestRunWithoutSubcommandPrintsHelp(t *testing.T) {
	stdout, _, err := runForTest(t)
	if err != nil {
		t.Fatalf("run without arguments: %v", err)
	}
	if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "version") {
		t.Fatalf("expected usage help, got %q", stdout)
	}
}

func TestRunHelpAndFlagsPrintHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "-help", "--help"} {
		stdout, _, err := runForTest(t, arg)
		if err != nil {
			t.Fatalf("run %q: %v", arg, err)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Fatalf("run %q: expected usage help, got %q", arg, stdout)
		}
	}
}

func TestRunHelpRejectsArguments(t *testing.T) {
	_, _, err := runForTest(t, "help", "extra")
	assertError(t, err, `help takes no arguments`)
}

// TestRunUnknownCommandFailsWithSuggestion holds the shape the usage error has:
// the failure is the sentinel of the bad invocation wrapped with the command it
// could not place, and the help text is printed where usage belongs — on the
// output of the process — instead of travelling inside the error string.
func TestRunUnknownCommandFailsWithSuggestion(t *testing.T) {
	stdout, _, err := runForTest(t, "serve")
	assertError(t, err, `unknown command "serve"`)
	if !errors.Is(err, errUsage) {
		t.Fatalf("an unknown command must wrap errUsage, got %v", err)
	}
	for _, want := range []string{"Usage:", "help       show this help"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the usage text did not reach the output (%q missing): %q", want, stdout)
		}
	}
}

func TestModulePathMatchesMasterPlan(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	const wantModule = "module github.com/AlexandreZanata/Regnovum"
	if !strings.Contains(string(data), wantModule) {
		t.Fatalf("go.mod does not declare %q:\n%s", wantModule, data)
	}
}

func TestRunHelpListsServerCommand(t *testing.T) {
	stdout, _, err := runForTest(t)
	if err != nil {
		t.Fatalf("run without arguments: %v", err)
	}
	if !strings.Contains(stdout, "server") {
		t.Fatalf("expected help to list the server command, got %q", stdout)
	}
}

// TestRunServerRejectsArguments guards the guard: argument validation must
// run before configuration loading, so a typo never reaches the loader.
func TestRunServerRejectsArguments(t *testing.T) {
	_, _, err := runForTest(t, "server", "extra")
	assertError(t, err, "server takes no arguments")
	assertError(t, err, "Usage: arena server")
}

// TestServerBootsServesAndStopsOnSIGTERM is the subprocess validation
// required by P02-T05: the binary boots from a clean environment, answers
// /health/live and /health/ready with 200, logs the listening record as a
// single JSON object, and terminates within the deadline after SIGTERM,
// logging the graceful shutdown record.
func TestServerBootsServesAndStopsOnSIGTERM(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "arena")
	build := exec.Command("go", "build", "-o", binary, "./cmd/arena")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // Reserve the port number only; the server binds it.

	logPath := filepath.Join(t.TempDir(), "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	defer logFile.Close()

	command := exec.Command(binary, "server")
	command.Env = []string{
		"ARENA_ADDR=" + address,
		"ARENA_ENV=development",
		"PATH=" + os.Getenv("PATH"), // go build may need the toolchain.
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	baseURL := "http://" + address
	client := &http.Client{Timeout: 2 * time.Second}

	deadline := time.Now().Add(10 * time.Second)
	var liveResponse *http.Response
	for liveResponse == nil {
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			log, _ := os.ReadFile(logPath)
			t.Fatalf("server did not answer /health/live in time; log:\n%s", log)
		}
		response, err := client.Get(baseURL + "/health/live")
		if err == nil {
			liveResponse = response
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	body, err := io.ReadAll(liveResponse.Body)
	_ = liveResponse.Body.Close()
	if err != nil {
		t.Fatalf("read /health/live body: %v", err)
	}
	if liveResponse.StatusCode != http.StatusOK {
		t.Fatalf("/health/live status = %d, want 200", liveResponse.StatusCode)
	}
	var livePayload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &livePayload); err != nil || livePayload.Status != "live" {
		t.Fatalf("/health/live body = %q (parse error: %v), want {\"status\":\"live\"}", body, err)
	}
	if got := liveResponse.Header.Get("X-Request-Id"); got == "" {
		t.Error("/health/live response is missing the X-Request-Id correlation header")
	}

	// P02-T08: the negotiated interface locale is echoed to clients. A
	// Brazilian Portuguese Accept-Language wins over the default; an
	// unmapped locale falls back to the configured default.
	if got := liveResponse.Header.Get("X-Interface-Locale"); got != "pt-BR" {
		t.Errorf("/health/live X-Interface-Locale default = %q, want pt-BR", got)
	}

	acceptRequest, err := http.NewRequest(http.MethodGet, baseURL+"/health/live", nil)
	if err != nil {
		t.Fatalf("build accept-language request: %v", err)
	}
	acceptRequest.Header.Set("Accept-Language", "en-US")
	acceptResponse, err := client.Do(acceptRequest)
	if err != nil {
		t.Fatalf("GET /health/live with Accept-Language: %v", err)
	}
	_, _ = io.Copy(io.Discard, acceptResponse.Body)
	_ = acceptResponse.Body.Close()
	if got := acceptResponse.Header.Get("X-Interface-Locale"); got != "en-US" {
		t.Errorf("X-Interface-Locale with Accept-Language en-US = %q, want en-US", got)
	}

	esRequest, err := http.NewRequest(http.MethodGet, baseURL+"/health/live", nil)
	if err != nil {
		t.Fatalf("build es-ES request: %v", err)
	}
	esRequest.Header.Set("Accept-Language", "es-ES")
	esResponse, err := client.Do(esRequest)
	if err != nil {
		t.Fatalf("GET /health/live with es-ES: %v", err)
	}
	_, _ = io.Copy(io.Discard, esResponse.Body)
	_ = esResponse.Body.Close()
	if got := esResponse.Header.Get("X-Interface-Locale"); got != "pt-BR" {
		t.Errorf("X-Interface-Locale with unsupported es-ES = %q, want pt-BR fallback", got)
	}

	readyResponse, err := client.Get(baseURL + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	}
	readyBody, err := io.ReadAll(readyResponse.Body)
	_ = readyResponse.Body.Close()
	if err != nil {
		t.Fatalf("read /health/ready body: %v", err)
	}
	if readyResponse.StatusCode != http.StatusOK {
		t.Fatalf("/health/ready status = %d, want 200", readyResponse.StatusCode)
	}
	var readyPayload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(readyBody, &readyPayload); err != nil || readyPayload.Status != "ready" {
		t.Fatalf("/health/ready body = %q (parse error: %v), want {\"status\":\"ready\"}", readyBody, err)
	}

	// SIGTERM must terminate the process within the deadline, gracefully.
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with error after SIGTERM: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatal("server did not terminate within 5s of SIGTERM")
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read server log: %v", err)
	}
	records := parseJSONLogRecords(t, string(log))
	if !containsRecord(records, "msg", "http server: listening") {
		t.Errorf("log is missing the listening record:\n%s", log)
	}
	if !containsRecord(records, "msg", "http server: graceful shutdown complete") {
		t.Errorf("log is missing the graceful shutdown record:\n%s", log)
	}
}

// parseJSONLogRecords decodes every line as a JSON object, failing on any
// non-JSON line: the whole server log must stay machine-readable.
func parseJSONLogRecords(t *testing.T, log string) []map[string]any {
	t.Helper()

	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not a JSON object: %q (%v)\nfull log:\n%s", line, err, log)
		}
		records = append(records, record)
	}
	return records
}

func containsRecord(records []map[string]any, key, want string) bool {
	for _, record := range records {
		if value, ok := record[key].(string); ok && value == want {
			return true
		}
	}
	return false
}

// TestServerReadinessWithDatabaseURL verifies that the server initializes the database
// pool and reports ready when the database is up, and unavailable (503) when down.
func TestServerReadinessWithDatabaseURL(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "arena")
	build := exec.Command("go", "build", "-o", binary, "./cmd/arena")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}

	dsn := "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable"
	if envDSN := os.Getenv("ARENA_DATABASE_URL"); envDSN != "" {
		dsn = envDSN
	}

	// 1. Subprocess with healthy database connection
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	// The account journey is composed from the database and the frontend build
	// (P18-T07A), so the boot needs both. The manifest is the fixture the
	// composition tests use, pointed at explicitly: a deployment names its own
	// directory the same way.
	assetsDir := filepath.Join(repoRoot, "internal", "bootstrap", "testdata", "assets")

	cmdHealthy := exec.Command(binary, "server")
	cmdHealthy.Env = []string{
		"ARENA_ADDR=" + address,
		"ARENA_ENV=development",
		"ARENA_DATABASE_URL=" + dsn,
		"ARENA_ASSETS_DIR=" + assetsDir,
		// The participation journey (P18-T07B) signs its pagination
		// cursors; without this key it is deliberately not mounted.
		"ARENA_CURSOR_SECRET=arena-boot-test-cursor-secret-32b",
		"PATH=" + os.Getenv("PATH"),
	}
	if err := cmdHealthy.Start(); err != nil {
		t.Fatalf("start healthy server: %v", err)
	}
	defer func() {
		_ = cmdHealthy.Process.Kill()
		_ = cmdHealthy.Wait()
	}()

	baseURL := "http://" + address
	client := &http.Client{Timeout: 2 * time.Second}

	deadline := time.Now().Add(10 * time.Second)
	var readyResp *http.Response
	for readyResp == nil {
		if time.Now().After(deadline) {
			t.Fatal("server with database did not answer /health/ready in time")
		}
		resp, err := client.Get(baseURL + "/health/ready")
		if err == nil {
			readyResp = resp
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	body, err := io.ReadAll(readyResp.Body)
	_ = readyResp.Body.Close()
	if err != nil {
		t.Fatalf("read ready body: %v", err)
	}
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("/health/ready status with active DB = %d, want 200 (body: %s)", readyResp.StatusCode, body)
	}

	var readyPayload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &readyPayload); err != nil || readyPayload.Status != "ready" {
		t.Fatalf("unexpected payload: %s", body)
	}

	// The pages of the account journey are served by the composed binary, not
	// only by the adapter in its own test: this is the claim P18-T07A exists to
	// make, checked against the process an operator runs.
	for _, page := range []string{"/register", "/verify", "/login", "/logout", "/reset", "/reset/confirm"} {
		pageResponse, err := client.Get(baseURL + page)
		if err != nil {
			t.Fatalf("GET %s: %v", page, err)
		}
		body, err := io.ReadAll(pageResponse.Body)
		_ = pageResponse.Body.Close()
		if err != nil {
			t.Fatalf("read GET %s body: %v", page, err)
		}
		if pageResponse.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (body: %.200s)", page, pageResponse.StatusCode, body)
		}
		if contentType := pageResponse.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", page, contentType)
		}
	}

	// The transitions of the Arena participation journey are mounted by the
	// composed binary too (P18-T07B). They are asked without a session, so the
	// answer is a refusal — what matters is that it is not the 404 of a route
	// nobody mounted: a declared route whose module is not composed answers
	// exactly that, and it is indistinguishable from a working journey until
	// someone tries to use it.
	for _, transition := range []string{"position", "position/change", "arguments", "attributions"} {
		path := "/arenas/qualquer-arena/" + transition
		request, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(""))
		if err != nil {
			t.Fatalf("build POST %s: %v", path, err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			t.Errorf("POST %s status = 404: the participation transition is not mounted (body: %.200s)", path, body)
		}
	}
	// The page is the same claim for the read: it is answered by the composed
	// surface instead of by the placeholder of an uncomposed module. What the
	// surface answers depends on the database this process was pointed at — the
	// refusal page when the schema is there, an RFC 9457 problem document when
	// it is not — and both are documents of the journey, while the placeholder
	// answers net/http's plain-text 404. The rendered page over a migrated
	// database is asserted where it can be set up: internal/bootstrap composes
	// the journey against a disposable one.
	pageResponse, err := client.Get(baseURL + "/arenas/qualquer-arena")
	if err != nil {
		t.Fatalf("GET /arenas/qualquer-arena: %v", err)
	}
	body, _ = io.ReadAll(pageResponse.Body)
	_ = pageResponse.Body.Close()
	if contentType := pageResponse.Header.Get("Content-Type"); strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("GET /arenas/qualquer-arena answered the placeholder of an uncomposed route (Content-Type %q, body %.200s)", contentType, body)
	}

	// The build the pages reference is served by the same process (P18-T07C):
	// the published address of the manifest is immutable for a year, the stable
	// address of the module graph is revalidated every time, and an address the
	// manifest never published is not reachable — which is what keeps a broken
	// stylesheet from being a page that loads nothing.
	frontend, err := assets.LoadFile(assetsDir)
	if err != nil {
		t.Fatalf("read the build manifest: %v", err)
	}
	publishedStylesheet, err := frontend.URL("styles/reset.css")
	if err != nil {
		t.Fatalf("resolve the published stylesheet: %v", err)
	}
	publishedModule, err := frontend.URL("pages/auth.js")
	if err != nil {
		t.Fatalf("resolve the published module: %v", err)
	}

	for _, want := range []struct {
		target      string
		contentType string
		cache       string
	}{
		{target: publishedStylesheet, contentType: "text/css; charset=utf-8", cache: assets.CacheHashed},
		{target: "/assets/styles/reset.css", contentType: "text/css; charset=utf-8", cache: assets.CacheStable},
		{target: publishedModule, contentType: "text/javascript; charset=utf-8", cache: assets.CacheHashed},
	} {
		response, err := client.Get(baseURL + want.target)
		if err != nil {
			t.Fatalf("GET %s: %v", want.target, err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (body: %.200s)", want.target, response.StatusCode, body)
			continue
		}
		if contentType := response.Header.Get("Content-Type"); contentType != want.contentType {
			t.Errorf("GET %s Content-Type = %q, want %q", want.target, contentType, want.contentType)
		}
		if cache := response.Header.Get("Cache-Control"); cache != want.cache {
			t.Errorf("GET %s Cache-Control = %q, want %q", want.target, cache, want.cache)
		}
	}

	undeclared, err := client.Get(baseURL + "/assets/styles/private.css")
	if err != nil {
		t.Fatalf("GET an undeclared asset: %v", err)
	}
	_ = undeclared.Body.Close()
	if undeclared.StatusCode != http.StatusNotFound {
		t.Errorf("GET an address the manifest does not declare = %d, want 404", undeclared.StatusCode)
	}

	// Terminate healthy server
	_ = cmdHealthy.Process.Signal(syscall.SIGTERM)
	_ = cmdHealthy.Wait()

	// 2. Subprocess with unreachable database
	downListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve down port: %v", err)
	}
	downAddress := downListener.Addr().String()
	_ = downListener.Close()

	const unreachableDSN = "postgres://arena:arena-local-dev@127.0.0.1:54399/arena?sslmode=disable&connect_timeout=1"
	cmdDown := exec.Command(binary, "server")
	cmdDown.Env = []string{
		"ARENA_ADDR=" + downAddress,
		"ARENA_ENV=development",
		"ARENA_DATABASE_URL=" + unreachableDSN,
		"ARENA_ASSETS_DIR=" + assetsDir,
		"PATH=" + os.Getenv("PATH"),
	}
	if err := cmdDown.Start(); err != nil {
		t.Fatalf("start down server: %v", err)
	}
	defer func() {
		_ = cmdDown.Process.Kill()
		_ = cmdDown.Wait()
	}()

	downBaseURL := "http://" + downAddress
	downDeadline := time.Now().Add(10 * time.Second)
	var downResp *http.Response
	for downResp == nil {
		if time.Now().After(downDeadline) {
			t.Fatal("down server did not answer /health/ready in time")
		}
		resp, err := client.Get(downBaseURL + "/health/ready")
		if err == nil {
			downResp = resp
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	downBodyBytes, err := io.ReadAll(downResp.Body)
	_ = downResp.Body.Close()
	if err != nil {
		t.Fatalf("read down body: %v", err)
	}
	if downResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/health/ready status with down DB = %d, want 503 (body: %s)", downResp.StatusCode, downBodyBytes)
	}

	var downPayload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(downBodyBytes, &downPayload); err != nil || downPayload.Status != "unavailable" {
		t.Fatalf("unexpected down payload: %s", downBodyBytes)
	}

	// Verify no sensitive info leaked
	downBodyStr := string(downBodyBytes)
	if strings.Contains(downBodyStr, "arena-local-dev") || strings.Contains(downBodyStr, "54399") {
		t.Fatalf("down body leaked credentials or DSN: %s", downBodyStr)
	}

	_ = cmdDown.Process.Signal(syscall.SIGTERM)
	_ = cmdDown.Wait()
}

// TestMigrateUsageAndArgumentValidation covers the argument surface of the
// migrate subcommand without touching a database: help variants print the
// usage, unknown subcommands and stray arguments fail with the usage text.
func TestMigrateUsageAndArgumentValidation(t *testing.T) {
	t.Parallel()

	stdout, _, err := runForTest(t, "migrate", "-h")
	if err != nil {
		t.Fatalf("migrate -h error = %v", err)
	}
	for _, want := range []string{"arena migrate status", "arena migrate up", "ARENA_DATABASE_URL"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("migrate -h output missing %q:\n%s", want, stdout)
		}
	}

	if _, _, err := runForTest(t, "migrate", "bogus"); err == nil {
		t.Error("migrate with unknown subcommand must fail")
	}
	if _, _, err := runForTest(t, "migrate"); err == nil {
		t.Error("migrate without subcommand must fail")
	}
	if _, _, err := runForTest(t, "migrate", "status", "extra"); err == nil {
		t.Error("migrate status with arguments must fail")
	}
	if _, _, err := runForTest(t, "migrate", "up", "extra"); err == nil {
		t.Error("migrate up with arguments must fail")
	}
}

// TestMigrateCommandsFailWithoutDatabaseURL pins the fail-fast behavior:
// without ARENA_DATABASE_URL the commands stop before opening anything.
func TestMigrateCommandsFailWithoutDatabaseURL(t *testing.T) {
	// config.MustLoad reads the process environment, so the test owns the
	// precondition it asserts instead of depending on the environment it happens
	// to be run in: the ARENA_* variables of this process are emptied for the
	// duration of the test, and the assertion below then holds everywhere. The
	// precondition and `t.Parallel` are incompatible, and the precondition wins
	// (the decision the P19-T04A records for the same conflict).
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ARENA_") {
			t.Setenv(name, "")
		}
	}

	_, _, err := runForTest(t, "migrate", "status")
	if err == nil {
		t.Fatal("without ARENA_* configuration the command must stop before opening anything, and it returned no error")
	}
	if !strings.Contains(err.Error(), "ARENA_") {
		t.Errorf("error should name the missing ARENA_* configuration, got: %v", err)
	}
}

// TestServerBootsInProductionAndServesTheJourney is the validation of P19-T02A
// at the process edge: the boot that used to be refused — production, because no
// email delivery adapter was composed — now serves the account journey, and the
// log says which pipeline it installed: messages are queued as durable work,
// the local sink is not installed, and the provider credential is nowhere in it.
//
// The database is a disposable migrated one, so the pages are answered against
// the real schema rather than against an empty server. It is a subprocess test
// because the claim is about the binary an operator runs, not about a
// composition assembled by the test.
func TestServerBootsInProductionAndServesTheJourney(t *testing.T) {
	const credential = "re_live_never_print_me"
	db := dbtest.New(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "arena")
	build := exec.Command("go", "build", "-o", binary, "./cmd/arena")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	logPath := filepath.Join(t.TempDir(), "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	defer logFile.Close()

	command := exec.Command(binary, "server")
	command.Env = []string{
		"ARENA_ADDR=" + address,
		"ARENA_ENV=production",
		"ARENA_DATABASE_URL=" + db.DSN,
		"ARENA_ASSETS_DIR=" + filepath.Join(repoRoot, "internal", "bootstrap", "testdata", "assets"),
		// The three production requirements the configuration validates: the
		// payment credential, the provider credential and the sender address
		// (P19-T02A).
		"ARENA_STRIPE_SECRET_KEY=sk_live_boot_test",
		"ARENA_RESEND_API_KEY=" + credential,
		"ARENA_EMAIL_FROM=Arena <no-reply@arena.invalid>",
		// The participation journey signs its pagination cursors, and a
		// server that mounts no such journey refuses the boot: production
		// composes both journeys from one process (P18-T07B).
		"ARENA_CURSOR_SECRET=arena-boot-test-cursor-secret-32b",
		"PATH=" + os.Getenv("PATH"),
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	baseURL := "http://" + address
	client := &http.Client{Timeout: 2 * time.Second}

	deadline := time.Now().Add(15 * time.Second)
	var liveResponse *http.Response
	for liveResponse == nil {
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			log, _ := os.ReadFile(logPath)
			t.Fatalf("the production server never answered /health/live; log:\n%s", log)
		}
		response, err := client.Get(baseURL + "/health/live")
		if err == nil {
			liveResponse = response
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, _ = io.Copy(io.Discard, liveResponse.Body)
	_ = liveResponse.Body.Close()
	if liveResponse.StatusCode != http.StatusOK {
		t.Fatalf("/health/live status = %d, want 200", liveResponse.StatusCode)
	}

	// The page is what proves the journey is mounted rather than merely
	// routed: an unmounted surface answers the placeholder, which is not a
	// document.
	register, err := client.Get(baseURL + "/register")
	if err != nil {
		t.Fatalf("GET /register: %v", err)
	}
	body, err := io.ReadAll(register.Body)
	_ = register.Body.Close()
	if err != nil {
		t.Fatalf("read GET /register body: %v", err)
	}
	if register.StatusCode != http.StatusOK {
		t.Fatalf("GET /register status = %d, want 200 (body: %.200s)", register.StatusCode, body)
	}
	if contentType := register.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("GET /register Content-Type = %q, want text/html", contentType)
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("the production server exited with %v after SIGTERM; log:\n%s", waitErr, log)
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		log, _ := os.ReadFile(logPath)
		t.Fatalf("the production server did not stop within the deadline; log:\n%s", log)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read server log: %v", err)
	}
	for _, record := range parseJSONLogRecords(t, string(log)) {
		if message, ok := record["msg"].(string); ok && strings.Contains(message, "local email sink") {
			t.Errorf("production installed the local email sink: %q", message)
		}
	}
	if !containsRecord(parseJSONLogRecords(t, string(log)), "msg", "transactional email: messages are queued as durable jobs and delivered by the worker through the provider") {
		t.Errorf("the log does not name the delivery pipeline production installed:\n%s", log)
	}
	if strings.Contains(string(log), credential) {
		t.Errorf("the server log carries the provider credential:\n%s", log)
	}
}
