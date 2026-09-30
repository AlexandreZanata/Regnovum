// Package testguard refuses what outlives a test (P22-T06).
//
// Every resource this file hands out — a directory, a listener, a connection, a
// goroutine, a timer, a process, an environment variable — is registered with
// the test that created it, and when that test ends the guard asks the two
// questions a green suite otherwise never asks: is it gone, and did the test
// close it or did the guard find it open? A resource the guard has to clean up
// itself is a leak the test is told about, by name and by the line that created
// it, because a fixture that leaves a listener open passes today and fails six
// months later inside somebody else's suite.
//
// Why the guard and not a linter: a linter sees `httptest.NewServer` and
// `time.NewTicker`, and it cannot see whether the test stopped them. Only the
// process knows when the test ended, and only then can the answer be measured.
//
// Three rules shape the package:
//
//   - **Ownership is explicit.** The guard hands out the resource, so it knows
//     the test had it and knows when it should be gone. Nothing here inspects
//     somebody else's value and guesses.
//   - **A leak is reported where it was created.** Each registration captures
//     the caller's file and line, and the report names it: the reader of a
//     failure is sent to the fixture that leaked, not to the guard.
//   - **The guard never cleans up in silence.** A directory it cannot remove, a
//     process it has to kill, a variable still set at the end — every one of
//     them is a report. Silence is what turns a leak into a permanent one.
//
// Nothing the product ships may import this package: it is listed in the
// test-only gate of internal/architecture_test.go exactly as the scenario
// builders and the provider simulators are.
//
// The typical fixture is one line at the top and ownership thereafter:
//
//	guard := testguard.New(t)
//	directory := guard.TempDir()
//	listener, err := guard.Listen("tcp", "127.0.0.1:0")
//	guard.Goroutine("delivery worker", worker.Run)
//
// A fixture that asks the guard for nothing pays almost nothing: the snapshot
// of the environment is one call, and the guard's cleanup does no work beyond
// comparing it.
package testguard

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The rules of the guard, one per kind of resource. They are the vocabulary of
// a failure and of the audit that reads the same names: a report that says
// `goroutine-not-joined` can be counted, and a fixture can refuse to lose one.
const (
	// RuleGoroutine is a goroutine the test started and did not let finish.
	RuleGoroutine = "goroutine-not-joined"
	// RuleProcess is a process the test started and left running.
	RuleProcess = "process-left-running"
	// RuleListener is a listener the test left accepting.
	RuleListener = "listener-left-open"
	// RuleConnection is a connection the test left open.
	RuleConnection = "connection-left-open"
	// RuleTempDirectory is a directory the guard could not remove.
	RuleTempDirectory = "temp-directory-left"
	// RuleTimer is a ticker or timer the test left armed.
	RuleTimer = "timer-left-armed"
	// RuleEnvironment is an environment variable the test changed and did not
	// restore.
	RuleEnvironment = "environment-not-restored"
)

// Rules answers every rule this guard can report, in a fixed order. It is what
// the audit and the fixtures of the package read instead of a list written
// twice in prose: a rule declared here without a fixture that proves it fires
// is a rule nobody has seen work.
func Rules() []string {
	return []string{
		RuleGoroutine,
		RuleProcess,
		RuleListener,
		RuleConnection,
		RuleTempDirectory,
		RuleTimer,
		RuleEnvironment,
	}
}

// tempDirectoryPrefix marks the directories the guard owns, so an audit can
// find what a killed run left behind and a developer can see at a glance where
// a stray directory came from.
const tempDirectoryPrefix = "testguard-"

// TempDirectoryPrefix answers the prefix of the directories the guard owns.
func TempDirectoryPrefix() string { return tempDirectoryPrefix }

// defaultGrace is how long the guard waits for a goroutine or a process to
// finish before calling it leaked. Something that ends because the test ended
// ends promptly; something that never ends is not made true by waiting longer,
// and the grace is what keeps a worker that is mid-flight when the test body
// returns from being reported as a leak.
const defaultGrace = 250 * time.Millisecond

// Reporter is the part of testing.TB the guard needs: where a leak is
// announced, and where the guard registers its own cleanup. It is an interface
// and not *testing.T so that a fixture can hold a recorder and prove every rule
// fires without failing itself — the same shape the provider simulators use for
// the unexpected-call rule.
type Reporter interface {
	Helper()
	Cleanup(func())
	Errorf(format string, args ...any)
}

// Option configures a guard.
type Option func(*Guard)

// WithReporter attaches the reporter the leaks are announced to.
func WithReporter(reporter Reporter) Option {
	return func(guard *Guard) {
		if reporter != nil {
			guard.reporter = reporter
		}
	}
}

// WithGrace changes how long the guard waits for a goroutine or a process to
// finish before calling it leaked.
func WithGrace(grace time.Duration) Option {
	return func(guard *Guard) {
		if grace > 0 {
			guard.grace = grace
		}
	}
}

// Guard is the lifecycle of one test: the resources it owns, and the refusal of
// what outlives it.
type Guard struct {
	reporter Reporter
	grace    time.Duration

	mu          sync.Mutex
	tasks       []*ownedTask
	processes   []*ownedProcess
	listeners   []*ownedSocket
	connections []*ownedSocket
	directories []*ownedDirectory
	timers      []*ownedTimer
	variables   []*ownedVariable
	environment []string

	released sync.Once
}

// New builds the guard of the current test, snapshots the environment and
// registers the refusal to run when the test ends.
//
// Call it before the fixture creates anything: the snapshot is what the end
// compares against, so a variable that was already set is part of the baseline
// the guard is not asked to judge.
func New(t testing.TB, options ...Option) *Guard {
	t.Helper()
	guard := newGuard(append([]Option{WithReporter(t)}, options...)...)
	t.Cleanup(guard.Release)
	return guard
}

// NewWithReporter builds a guard whose reports go to a reporter the caller
// owns, which is how the fixtures of this package observe each rule. It
// registers nothing: the caller decides when Release runs.
func NewWithReporter(reporter Reporter, options ...Option) *Guard {
	return newGuard(append([]Option{WithReporter(reporter)}, options...)...)
}

// newGuard is the shared constructor.
func newGuard(options ...Option) *Guard {
	guard := &Guard{grace: defaultGrace, environment: os.Environ()}
	for _, option := range options {
		option(guard)
	}
	return guard
}

// TempDir creates a directory the test owns. It is removed when the test ends,
// and a directory that survives its own removal is reported: a guard that
// removed in silence would turn a file left behind into a file nobody hears
// about.
func (g *Guard) TempDir() string {
	g.helper()
	path, err := os.MkdirTemp("", tempDirectoryPrefix)
	if err != nil {
		g.report(RuleTempDirectory, "a temporary directory could not be created: %v", err)
		return ""
	}
	directory := &ownedDirectory{path: path, site: callerSite()}
	g.mu.Lock()
	g.directories = append(g.directories, directory)
	g.mu.Unlock()
	return path
}

// Listen opens a loopback listener the test owns. A listener the test closes
// itself is not a leak; a listener the guard finds open when the test ends is.
//
// Only loopback addresses are accepted. A suite that binds a routable address
// depends on the network of the machine it runs on, which is the dependency the
// hermetic environment of this phase exists to remove; an empty host, which
// binds every interface, is refused for the same reason.
func (g *Guard) Listen(network, address string) (net.Listener, error) {
	g.helper()
	if err := requireLoopback(address); err != nil {
		return nil, err
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	socket := &ownedSocket{name: listener.Addr().String(), site: callerSite(), listener: listener}
	g.mu.Lock()
	g.listeners = append(g.listeners, socket)
	g.mu.Unlock()
	return listener, nil
}

// Dial connects to a loopback address and hands the connection to the test. A
// connection the test closes itself is not a leak; one the guard finds open is.
func (g *Guard) Dial(network, address string) (net.Conn, error) {
	g.helper()
	if err := requireLoopback(address); err != nil {
		return nil, err
	}
	connection, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	socket := &ownedSocket{name: connection.RemoteAddr().String(), site: callerSite(), connection: connection}
	g.mu.Lock()
	g.connections = append(g.connections, socket)
	g.mu.Unlock()
	return connection, nil
}

// Goroutine starts f in a goroutine the test owns and requires it to be
// finished when the test ends. The name is what the report says, so it is the
// name of the thing that kept running — "delivery worker", not "worker 3".
func (g *Guard) Goroutine(name string, f func()) {
	g.helper()
	finished := make(chan struct{})
	entry := &ownedTask{name: name, site: callerSite(), finished: finished}
	g.mu.Lock()
	g.tasks = append(g.tasks, entry)
	g.mu.Unlock()

	go func() {
		defer close(finished)
		f()
	}()
}

// Command starts a process the test owns and requires it to be over when the
// test ends. A process the guard has to kill is reported: the fixture started
// something it did not stop, and the port it held would stay taken until
// somebody noticed.
func (g *Guard) Command(name string, command *exec.Cmd) error {
	g.helper()
	if command == nil {
		return errors.New("testguard: a process needs a command to run")
	}
	if err := command.Start(); err != nil {
		return err
	}
	finished := make(chan struct{})
	entry := &ownedProcess{name: name, site: callerSite(), command: command, finished: finished}
	g.mu.Lock()
	g.processes = append(g.processes, entry)
	g.mu.Unlock()

	go func() {
		defer close(finished)
		_ = command.Wait()
	}()
	return nil
}

// Ticker starts a ticker the test owns. A ticker the test stops itself is not a
// leak; a ticker the guard finds armed is, because an armed ticker holds a
// runtime timer and a channel that keeps receiving.
func (g *Guard) Ticker(d time.Duration) *Ticker {
	g.helper()
	entry := &ownedTimer{name: "ticker " + d.String(), site: callerSite(), ticker: time.NewTicker(d)}
	g.mu.Lock()
	g.timers = append(g.timers, entry)
	g.mu.Unlock()
	return &Ticker{Ticker: entry.ticker, entry: entry}
}

// AfterFunc schedules f in a timer the test owns. A timer the test stops itself
// is not a leak; a timer the guard finds armed is, and a function still running
// when the test ends is a goroutine leak reported with the same site.
func (g *Guard) AfterFunc(d time.Duration, f func()) *Timer {
	g.helper()
	entry := &ownedTimer{name: "timer " + d.String(), site: callerSite()}
	fired := make(chan struct{})
	entry.onFire = func() {
		// The function of a timer runs in a goroutine of the runtime: it is
		// registered the moment it starts, which is the only moment the guard
		// can know it exists.
		task := &ownedTask{name: entry.name, site: entry.site, finished: fired}
		g.mu.Lock()
		g.tasks = append(g.tasks, task)
		g.mu.Unlock()
		defer close(fired)
		f()
	}
	entry.timer = time.AfterFunc(d, entry.onFire)
	g.mu.Lock()
	g.timers = append(g.timers, entry)
	g.mu.Unlock()
	return &Timer{Timer: entry.timer, entry: entry}
}

// Setenv sets an environment variable for the test and restores it when the
// test ends. It exists so that a fixture which needs a variable states the
// intention — "this test owns this value" — instead of mutating the process it
// shares with every other test of the binary.
//
// The guard compares the whole environment at the end regardless: a variable
// set with os.Setenv directly is reported, because it is that mutation that
// makes a parallel suite depend on the order its tests ran in.
func (g *Guard) Setenv(key, value string) {
	g.helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		g.report(RuleEnvironment, "the variable %s could not be set: %v", key, err)
		return
	}
	variable := &ownedVariable{key: key, site: callerSite(), previous: previous, existed: existed}
	g.mu.Lock()
	g.variables = append(g.variables, variable)
	g.mu.Unlock()
}

// Release asks every question of the guard and reports what outlived the test.
// It is idempotent, so a fixture may call it to observe the refusal while the
// registered cleanup still does the right thing once.
func (g *Guard) Release() {
	g.released.Do(g.release)
}

// release is the once-guarded body of Release: restore what the guard owns,
// then ask about everything it does not.
func (g *Guard) release() {
	g.mu.Lock()
	tasks := append([]*ownedTask(nil), g.tasks...)
	processes := append([]*ownedProcess(nil), g.processes...)
	listeners := append([]*ownedSocket(nil), g.listeners...)
	connections := append([]*ownedSocket(nil), g.connections...)
	directories := append([]*ownedDirectory(nil), g.directories...)
	timers := append([]*ownedTimer(nil), g.timers...)
	variables := append([]*ownedVariable(nil), g.variables...)
	baseline := append([]string(nil), g.environment...)
	g.mu.Unlock()

	// The guard's own variables are restored first, so that the environment
	// comparison judges the fixture and not the guard.
	for _, variable := range variables {
		variable.restore(g)
	}
	for _, entry := range timers {
		entry.refuse(g)
	}
	for _, directory := range directories {
		directory.refuse(g)
	}
	for _, socket := range listeners {
		socket.refuse(RuleListener, "listener", g)
	}
	for _, socket := range connections {
		socket.refuse(RuleConnection, "connection", g)
	}
	// One budget for the whole release, and not one per resource: a run of
	// leaks must not turn a cleanup of a quarter of a second into a minute.
	// Reading the clock is somebody else's job (P22-T02): the budget is a timer,
	// and waiting is not an effect.
	budget := newBudget(g.grace)
	for _, entry := range processes {
		entry.refuse(budget, g)
	}
	for _, entry := range tasks {
		entry.refuse(budget, g)
	}
	budget.stop()
	g.refuseEnvironment(baseline)
}

// report announces one leak through the reporter.
func (g *Guard) report(rule, format string, args ...any) {
	if g.reporter == nil {
		return
	}
	g.reporter.Errorf("testguard: %s: %s", rule, fmt.Sprintf(format, args...))
}

func (g *Guard) helper() {
	if g.reporter == nil {
		return
	}
	g.reporter.Helper()
}

// ownedDirectory is a directory the test owns.
type ownedDirectory struct {
	path string
	site string
}

// refuse removes the directory and refuses to remove in silence.
func (d *ownedDirectory) refuse(g *Guard) {
	if err := os.RemoveAll(d.path); err != nil {
		g.report(RuleTempDirectory, "the directory %s created at %s could not be removed: %v", d.path, d.site, err)
		return
	}
	if _, err := os.Stat(d.path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		g.report(RuleTempDirectory, "the directory %s created at %s survived its removal", d.path, d.site)
	}
}

// ownedSocket is a listener or a connection the test owns.
type ownedSocket struct {
	name       string
	site       string
	listener   net.Listener
	connection net.Conn
}

// refuse refuses a socket the test left open. A socket the test closed itself
// answers net.ErrClosed, which is the answer that says the fixture did its job.
func (s *ownedSocket) refuse(rule, kind string, g *Guard) {
	var err error
	if s.listener != nil {
		err = s.listener.Close()
	} else {
		err = s.connection.Close()
	}
	if err == nil {
		g.report(rule, "the %s %s opened at %s was still open", kind, s.name, s.site)
		return
	}
	if !errors.Is(err, net.ErrClosed) {
		g.report(rule, "the %s %s opened at %s could not be closed: %v", kind, s.name, s.site, err)
	}
}

// ownedTask is a goroutine the test owns.
type ownedTask struct {
	name     string
	site     string
	finished chan struct{}
}

// refuse refuses a goroutine that has not finished within the budget. Nothing
// is waited for afterwards: a goroutine cannot be stopped, and a cleanup that
// waits for one that will never end hides the leak it just found behind a suite
// that never ends.
func (t *ownedTask) refuse(budget *budget, g *Guard) {
	if budget.wait(t.finished) {
		return
	}
	g.report(RuleGoroutine, "the goroutine %q started at %s is still running", t.name, t.site)
}

// ownedProcess is a process the test owns.
type ownedProcess struct {
	name     string
	site     string
	command  *exec.Cmd
	finished chan struct{}
}

// refuse refuses a process the test left running, and ends it: a leaked process
// holds a port and a database connection until somebody kills it.
func (p *ownedProcess) refuse(budget *budget, g *Guard) {
	if budget.wait(p.finished) {
		return
	}
	g.report(RuleProcess, "the process %q started at %s was still running", p.name, p.site)
	if p.command.Process != nil {
		if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			g.report(RuleProcess, "the process %q started at %s could not be killed: %v", p.name, p.site, err)
		}
	}
	// A fresh, bounded wait for the kill to be reaped: the budget above is
	// spent, and a process that ignores the signal must not hang the suite.
	budget.reap(p.finished, killGrace)
}

// ownedTimer is a ticker or a timer the test owns.
type ownedTimer struct {
	name   string
	site   string
	ticker *time.Ticker
	timer  *time.Timer
	onFire func()
	ended  atomic.Bool
}

// markEnded records that the test stopped the timer itself.
func (t *ownedTimer) markEnded() { t.ended.Store(true) }

// refuse refuses a timer the test left armed.
func (t *ownedTimer) refuse(g *Guard) {
	if t.ended.Load() {
		return
	}
	if t.ticker != nil {
		t.ticker.Stop()
		g.report(RuleTimer, "the %s started at %s was still armed", t.name, t.site)
		return
	}
	if t.timer != nil && t.timer.Stop() {
		g.report(RuleTimer, "the %s started at %s was still armed", t.name, t.site)
	}
}

// Ticker is a ticker the guard owns. Stop tells the guard the test stopped it.
type Ticker struct {
	*time.Ticker
	entry *ownedTimer
}

// Stop stops the ticker and records that the test did.
func (t *Ticker) Stop() {
	if t.entry != nil {
		t.entry.markEnded()
	}
	t.Ticker.Stop()
}

// Timer is a timer the guard owns. Stop tells the guard the test stopped it.
type Timer struct {
	*time.Timer
	entry *ownedTimer
}

// Stop stops the timer and records that the test did.
func (t *Timer) Stop() bool {
	if t.entry != nil {
		t.entry.markEnded()
	}
	return t.Timer.Stop()
}

// ownedVariable is an environment variable the test owns.
type ownedVariable struct {
	key      string
	site     string
	previous string
	existed  bool
}

// restore puts back what Setenv replaced.
func (v *ownedVariable) restore(g *Guard) {
	if v.existed {
		if err := os.Setenv(v.key, v.previous); err != nil {
			g.report(RuleEnvironment, "the variable %s set at %s could not be restored: %v", v.key, v.site, err)
		}
		return
	}
	if err := os.Unsetenv(v.key); err != nil {
		g.report(RuleEnvironment, "the variable %s set at %s could not be removed: %v", v.key, v.site, err)
	}
}

// refuseEnvironment refuses a variable the test changed and did not restore.
func (g *Guard) refuseEnvironment(baseline []string) {
	before := environmentMap(baseline)
	after := environmentMap(os.Environ())
	for key, value := range before {
		changed, exists := after[key]
		if !exists || changed != value {
			g.report(RuleEnvironment, "the variable %s was changed (%s → %s) and not restored", key, redact(key, value), redact(key, after[key]))
		}
	}
	for key := range after {
		if _, exists := before[key]; !exists {
			g.report(RuleEnvironment, "the variable %s was set and not removed", key)
		}
	}
}

// environmentMap renders an environment as the pairs it is.
func environmentMap(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

// redact hides the value of a variable whose name looks like a credential: a
// report is read in a CI log, and a log is not a place for a key.
func redact(key, value string) string {
	lower := strings.ToLower(key)
	for _, marker := range []string{"secret", "token", "password", "passwd", "key", "credential", "dsn", "url"} {
		if strings.Contains(lower, marker) {
			return "[REDACTED]"
		}
	}
	return value
}

// killGrace is how long a killed process is given to be reaped before the
// guard moves on. A process that ignores SIGKILL does not exist, so this is a
// bound on the operating system, not on the process.
const killGrace = 2 * time.Second

// budget is the time one release spends waiting for the resources to end. It is
// a timer rather than a clock reading on purpose: the test platform checks that
// nothing reads the machine's time (P22-T02, ADR-012), and waiting is not
// reading.
type budget struct {
	timer *time.Timer
	spent bool
}

// newBudget arms the budget.
func newBudget(d time.Duration) *budget {
	return &budget{timer: time.NewTimer(d)}
}

// wait answers whether the resource ended before the budget expired. A spent
// budget answers immediately for every later resource, which is what makes the
// whole release bounded by one grace and not by one per leak — and the spent
// flag is not a detail: a timer channel delivers once, so a second wait on a
// spent budget would block forever and turn a leak report into a suite that
// never ends (it did, until the mixed fixture of this package had one).
func (b *budget) wait(finished <-chan struct{}) bool {
	if b.spent {
		return false
	}
	select {
	case <-finished:
		return true
	case <-b.timer.C:
		b.spent = true
		return false
	}
}

// reap waits out the grace given to a killed process, after the budget is gone.
func (b *budget) reap(finished <-chan struct{}, grace time.Duration) {
	select {
	case <-finished:
	case <-time.After(grace):
	}
}

// stop releases the timer of the budget.
func (b *budget) stop() {
	b.timer.Stop()
}

// requireLoopback refuses an address this machine shares with the network.
func requireLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("testguard: %q is not an address with a port: %w", address, err)
	}
	if host == "" {
		return fmt.Errorf("testguard: %q binds every interface; a suite binds loopback", address)
	}
	if host == "localhost" {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return fmt.Errorf("testguard: %q is not a loopback address; a suite must not depend on the network", address)
	}
	return nil
}

// callerSite renders the file and line of the fixture that owns a resource: the
// reader of a leak is sent to the fixture, never to the guard.
func callerSite() string {
	for skip := 2; skip < 12; skip++ {
		_, file, line, ok := runtime.Caller(skip)
		if !ok {
			break
		}
		if strings.HasSuffix(file, "testguard.go") {
			continue
		}
		return fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}
	return "an unknown site"
}
