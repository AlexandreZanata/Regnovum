package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// TestWorkerUsageAndArgumentValidation covers the argument surface of the
// worker subcommand without touching a database.
func TestWorkerUsageAndArgumentValidation(t *testing.T) {
	t.Parallel()

	stdout, _, err := runForTest(t, "worker", "-h")
	if err != nil {
		t.Fatalf("worker -h error = %v", err)
	}
	for _, want := range []string{"arena worker", "ARENA_DATABASE_URL", "SIGTERM"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("worker -h output missing %q:\n%s", want, stdout)
		}
	}

	if _, _, err := runForTest(t, "worker", "extra"); err == nil {
		t.Error("worker with arguments must fail")
	}

	help, _, err := runForTest(t, "help")
	if err != nil {
		t.Fatalf("help error = %v", err)
	}
	if !strings.Contains(help, "worker") {
		t.Errorf("usage does not list the worker command:\n%s", help)
	}
}

// TestWorkerFailsFastWithoutDatabaseURL pins the fail-fast behavior: without
// ARENA_DATABASE_URL the worker stops before opening anything, and it stops
// promptly rather than starting to consume jobs.
//
// The test establishes its own precondition instead of inheriting it. The CI
// job exports ARENA_DATABASE_URL for every package, so the assertion was only
// ever exercised on machines that happened to have no database configured:
// with the variable set, run("worker") booted a real worker and blocked until
// SIGTERM, which the suite reported as a ten-minute package timeout rather
// than as a failure. Clearing the variable makes the precondition true by
// construction, and the bound below keeps a future regression from hanging
// the suite — a fail-fast test that can hang is not testing fail-fast.
func TestWorkerFailsFastWithoutDatabaseURL(t *testing.T) {
	// t.Setenv restores the previous value on cleanup, which is what keeps the
	// rest of the package hermetic; it cannot be combined with t.Parallel.
	t.Setenv("ARENA_DATABASE_URL", "")

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer stdoutFile.Close()

	// run is called on its own goroutine so the wait can be bounded, and every
	// t.Fatal stays on the test goroutine.
	errs := make(chan error, 1)
	go func() { errs <- run([]string{"worker"}, stdoutFile) }()

	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("worker without ARENA_DATABASE_URL must not start")
		}
		if !strings.Contains(err.Error(), "ARENA_") {
			t.Errorf("error should name the missing ARENA_* configuration, got: %v", err)
		}
	case <-time.After(failFastDeadline):
		t.Fatalf("worker did not stop within %s without ARENA_DATABASE_URL: it must fail fast, not start consuming jobs", failFastDeadline)
	}
}

// failFastDeadline bounds the no-configuration path. It does no I/O — the
// configuration is refused before any connection is attempted — so the real
// cost is microseconds.
const failFastDeadline = 10 * time.Second

// TestWorkerRegistersTheTransactionalEmailHandlerInProduction is the
// process-level validation of P19-T02A: `arena worker` composes the email
// delivery, so the process an operator runs in production is the one that
// executes the `email_delivery` jobs the account journey queues. The claim is
// asserted against the binary, not against the composition in isolation,
// because the wiring — configuration in, handler registered — is exactly what
// a unit test with a hand-built Options would let drift.
//
// The registration is read from two records: the explicit one, and the started
// record's handler count, which is 2 — the email delivery and the scheduled
// session cleanup — instead of the 1 of a process that delivers nothing.
func TestWorkerRegistersTheTransactionalEmailHandlerInProduction(t *testing.T) {
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

	logPath := filepath.Join(t.TempDir(), "worker.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	defer logFile.Close()

	command := exec.Command(binary, "worker")
	command.Env = []string{
		"ARENA_ENV=production",
		"ARENA_DATABASE_URL=" + db.DSN,
		"ARENA_DB_MAX_CONNS=4",
		"ARENA_DB_MIN_CONNS=1",
		// The production requirements the configuration validates: the
		// payment credential, the provider credential and the sender
		// address. Only the last two belong to this task, and the first is
		// here because production refuses to boot without it.
		"ARENA_STRIPE_SECRET_KEY=sk_live_boot_test",
		"ARENA_RESEND_API_KEY=re_boot_test_key",
		"ARENA_EMAIL_FROM=Arena <no-reply@arena.invalid>",
		"PATH=" + os.Getenv("PATH"),
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	waitForLog := func(fragment string, deadline time.Duration) string {
		limit := time.Now().Add(deadline)
		for time.Now().Before(limit) {
			content, _ := os.ReadFile(logPath)
			if strings.Contains(string(content), fragment) {
				return string(content)
			}
			time.Sleep(50 * time.Millisecond)
		}
		content, _ := os.ReadFile(logPath)
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("worker log never contained %q; log:\n%s", fragment, content)
		return ""
	}

	startedLog := waitForLog("job worker: started", 20*time.Second)
	if !strings.Contains(startedLog, "job worker: transactional email handler registered") {
		t.Errorf("production did not register the email delivery handler:\n%s", startedLog)
	}
	if strings.Contains(startedLog, "no email handler to register") {
		t.Errorf("production delivered through the local sink:\n%s", startedLog)
	}
	if !strings.Contains(startedLog, `"handlers":2`) {
		t.Errorf("the started record should count the email delivery and the session cleanup:\n%s", startedLog)
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal worker: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			content, _ := os.ReadFile(logPath)
			t.Fatalf("worker exited with %v after SIGTERM; log:\n%s", waitErr, content)
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		content, _ := os.ReadFile(logPath)
		t.Fatalf("worker did not stop within the deadline after SIGTERM; log:\n%s", content)
	}
}

// TestWorkerBootsAndStopsOnSIGTERM is the process-level lifecycle validation
// of P15-T02: the binary boots against a real database, logs the started
// record, and terminates cleanly (exit 0) well within the deadline after
// SIGTERM, logging the stopped record.
//
// The complementary property — a job already leased is finished and recorded
// instead of being abandoned — is proven at the runtime level in
// internal/jobs/application (TestWorkerGracefulShutdownCompletesInFlightJob),
// because no workload handler is registered yet: they arrive with P15-T03/T04.
func TestWorkerBootsAndStopsOnSIGTERM(t *testing.T) {
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

	logPath := filepath.Join(t.TempDir(), "worker.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	defer logFile.Close()

	command := exec.Command(binary, "worker")
	command.Env = []string{
		"ARENA_ENV=development",
		"ARENA_DATABASE_URL=" + db.DSN,
		"ARENA_DB_MAX_CONNS=4",
		"ARENA_DB_MIN_CONNS=1",
		"PATH=" + os.Getenv("PATH"),
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	// Keep the log file readable while the process writes to it.
	waitForLog := func(fragment string, deadline time.Duration) string {
		limit := time.Now().Add(deadline)
		for time.Now().Before(limit) {
			content, _ := os.ReadFile(logPath)
			if strings.Contains(string(content), fragment) {
				return string(content)
			}
			time.Sleep(50 * time.Millisecond)
		}
		content, _ := os.ReadFile(logPath)
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("worker log never contained %q; log:\n%s", fragment, content)
		return ""
	}

	startedLog := waitForLog("job worker: started", 20*time.Second)
	if !strings.Contains(startedLog, `"concurrency"`) {
		t.Errorf("started record should carry the configured concurrency:\n%s", startedLog)
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal worker: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	select {
	case waitErr := <-done:
		if waitErr != nil {
			content, _ := os.ReadFile(logPath)
			t.Fatalf("worker exited with %v after SIGTERM; log:\n%s", waitErr, content)
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		content, _ := os.ReadFile(logPath)
		t.Fatalf("worker did not stop within the deadline after SIGTERM; log:\n%s", content)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read worker log: %v", err)
	}
	if !strings.Contains(string(content), "job worker: stopped") {
		t.Errorf("worker did not log the stopped record:\n%s", content)
	}
}
