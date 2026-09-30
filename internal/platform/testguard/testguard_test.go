// Fixtures of the lifecycle guard (P22-T06).
//
// The guard reports through an interface, so these fixtures hold a recorder
// instead of failing themselves: a rule is proved by making the guard announce
// it, not by making this package red. `TestEveryDeclaredRuleIsProvedByAFixture`
// closes the loop — a rule declared in testguard.Rules() that no fixture
// exercises is a rule nobody has seen work, and it fails the suite.
//
// Two fixtures reproduce failures that only a POSIX filesystem and a POSIX
// process model have — a directory whose contents cannot be unlinked, and a
// child process that ignores a polite end. The product ships as a Linux
// container and every gate of this repository is a bash script, so the fixtures
// state the platform instead of guessing about it.
package testguard_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testguard"
)

// recorder is the reporter a fixture observes the guard through. It keeps what
// it was told, and registers nothing: the fixture decides when the guard is
// released.
type recorder struct {
	mu       sync.Mutex
	messages []string
}

func (r *recorder) Helper() {}

// Cleanup answers the half of the Reporter contract the fixtures do not use: a
// guard built with a recorder registers nothing, so the recorder would be
// storing a cleanup nobody calls.
func (r *recorder) Cleanup(func()) {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

// told answers everything the guard reported.
func (r *recorder) told() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.messages...)
}

// rules answers the rules the guard reported, counted, and fails when a report
// does not carry a rule at all — an announcement nobody can classify is a
// message a reader has to guess about.
func (r *recorder) rules(t *testing.T) map[string]int {
	t.Helper()
	counted := map[string]int{}
	for _, message := range r.told() {
		rule, found := strings.CutPrefix(message, "testguard: ")
		if !found {
			t.Fatalf("the guard reported %q without naming a rule", message)
		}
		name, _, found := strings.Cut(rule, ":")
		if !found {
			t.Fatalf("the guard reported %q without a message after the rule", message)
		}
		counted[name]++
	}
	return counted
}

// TestTheGuardStaysQuietWhenTheFixtureReleasesEverything is the control of the
// whole package: a fixture that owns every kind of resource and gives each one
// back must hear nothing. Without it, "the guard reported a leak" could be the
// answer of a guard that reports everything.
func TestTheGuardStaysQuietWhenTheFixtureReleasesEverything(t *testing.T) {
	t.Parallel()

	announcements := &recorder{}
	guard := testguard.NewWithReporter(announcements, testguard.WithGrace(50*time.Millisecond))

	directory := guard.TempDir()
	if directory == "" {
		t.Fatal("the guard handed out no temporary directory")
	}
	if err := os.WriteFile(filepath.Join(directory, "kept.txt"), []byte("synthetic"), 0o600); err != nil {
		t.Fatalf("write inside the directory the guard owns: %v", err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatalf("the fixture could not remove its own directory: %v", err)
	}

	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	connection, err := guard.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = connection.Close()
	_ = listener.Close()

	done := make(chan struct{})
	guard.Goroutine("a worker that ends", func() { close(done) })
	<-done

	command := exec.Command("go", "env", "GOOS")
	if err := guard.Command("a process that ends", command); err != nil {
		t.Fatalf("Command: %v", err)
	}

	ticker := guard.Ticker(time.Hour)
	ticker.Stop()
	after := guard.AfterFunc(time.Hour, func() {})
	after.Stop()

	guard.Setenv("TESTGUARD_FIXTURE_VALUE", "synthetic")

	guard.Release()

	if told := announcements.told(); len(told) != 0 {
		t.Fatalf("a fixture that released everything was told %v", told)
	}
}

// TestEveryDeclaredRuleIsProvedByAFixture refuses a rule with no fixture. It
// runs the leak fixtures below through a recorder, collects the rules they
// produced and compares the two sets: a new rule is proved, or it fails here.
// The fixtures below run in sequence and not in parallel, and that is the rule
// they exercise: the environment fixture mutates a variable of the process, the
// one thing every test of the binary shares, so running it beside another
// fixture makes that fixture's guard report somebody else's variable. The
// failure would be real and the culprit would be the parallelism — which is
// exactly what the phase asks this task to remove.
func TestEveryDeclaredRuleIsProvedByAFixture(t *testing.T) {
	proved := map[string]bool{}
	for _, leak := range leaks {
		announcements := &recorder{}
		leak.leak(announcements)
		for rule := range announcements.rules(t) {
			proved[rule] = true
		}
	}

	missing := make([]string, 0, len(testguard.Rules()))
	for _, rule := range testguard.Rules() {
		if !proved[rule] {
			missing = append(missing, rule)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("the guard declares rules no fixture exercises: %v", missing)
	}
}

// leaks are the fixtures that reproduce one leak each. Every entry has to make
// the guard name its own rule, and the union of them has to cover
// testguard.Rules().
var leaks = []struct {
	rule string
	leak func(*recorder)
}{
	{rule: testguard.RuleGoroutine, leak: leakGoroutine},
	{rule: testguard.RuleProcess, leak: leakProcess},
	{rule: testguard.RuleListener, leak: leakListener},
	{rule: testguard.RuleConnection, leak: leakConnection},
	{rule: testguard.RuleTempDirectory, leak: leakDirectory},
	{rule: testguard.RuleTimer, leak: leakTimer},
	{rule: testguard.RuleEnvironment, leak: leakEnvironment},
}

// TestEachLeakIsReportedByItsOwnRule walks the table: every fixture must
// produce the rule it exists for, and nothing else — a leak that reports two
// rules would prove one of them for the wrong reason.
func TestEachLeakIsReportedByItsOwnRule(t *testing.T) {
	for _, leak := range leaks {
		t.Run(leak.rule, func(t *testing.T) {
			announcements := &recorder{}
			leak.leak(announcements)
			counted := announcements.rules(t)
			if counted[leak.rule] == 0 {
				t.Fatalf("the fixture reported %v, want at least one %s", counted, leak.rule)
			}
			for rule := range counted {
				if rule != leak.rule {
					t.Fatalf("the fixture reported %s as well, which is another fixture's failure", rule)
				}
			}
		})
	}
}

// TestTheReportNamesTheFixtureThatLeaked holds the third rule of the package:
// the reader of a failure is sent to the fixture, not to the guard. The report
// must name this file.
func TestTheReportNamesTheFixtureThatLeaked(t *testing.T) {
	t.Parallel()

	announcements := &recorder{}
	guard := testguard.NewWithReporter(announcements, testguard.WithGrace(20*time.Millisecond))
	blocked := make(chan struct{})
	defer close(blocked)
	guard.Goroutine("a worker that never ends", func() { <-blocked })
	guard.Release()

	told := announcements.told()
	if len(told) != 1 {
		t.Fatalf("the guard reported %v, want exactly one leak", told)
	}
	if !strings.Contains(told[0], "testguard_test.go:") {
		t.Fatalf("the report does not name the fixture that leaked: %s", told[0])
	}
	if !strings.Contains(told[0], "a worker that never ends") {
		t.Fatalf("the report does not name the resource that leaked: %s", told[0])
	}
}

// TestTheGuardRefusesAnAddressTheMachineShares is the positive control of the
// loopback rule: a suite that binds a routable address depends on the network
// of the machine it runs on, and the guard refuses it before it listens.
func TestTheGuardRefusesAnAddressTheMachineShares(t *testing.T) {
	t.Parallel()

	guard := testguard.NewWithReporter(&recorder{})
	for _, address := range []string{"0.0.0.0:0", "[::]:0", ":0", "192.0.2.10:0"} {
		listener, err := guard.Listen("tcp", address)
		if err == nil {
			_ = listener.Close()
			t.Fatalf("the guard listened on %q", address)
		}
		if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "binds every interface") {
			t.Fatalf("the refusal of %q does not say why: %v", address, err)
		}
	}
	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("a loopback listener was refused: %v", err)
	}
	_ = listener.Close()
	guard.Release()
}

// unique answers the set of a list, in the order the list first mentions it.
func unique(values []string) []string {
	seen := map[string]bool{}
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		kept = append(kept, value)
	}
	return kept
}

// equalStrings answers whether two lists are the same list.
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// leakGoroutine starts a worker that never ends.
func leakGoroutine(announcements *recorder) {
	guard := testguard.NewWithReporter(announcements, testguard.WithGrace(20*time.Millisecond))
	blocked := make(chan struct{})
	guard.Goroutine("a worker that never ends", func() { <-blocked })
	guard.Release()
	close(blocked)
}

// leakProcess starts a process and never ends it: the guard has to kill it,
// and killing something is the report.
//
// The child is the POSIX sleep, which ignores nothing and ends when it is
// killed — a process the test forgot is exactly this process.
func leakProcess(announcements *recorder) {
	if runtime.GOOS == "windows" {
		panic("the process fixture reproduces a POSIX child; run the suite on the platform the product ships on")
	}
	guard := testguard.NewWithReporter(announcements, testguard.WithGrace(20*time.Millisecond))
	command := exec.Command("sleep", "30")
	if err := guard.Command("a server nobody stopped", command); err != nil {
		panic(fmt.Sprintf("the fixture could not start its process: %v", err))
	}
	guard.Release()
}

// leakListener opens a listener and leaves it accepting.
func leakListener(announcements *recorder) {
	guard := testguard.NewWithReporter(announcements)
	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("the fixture could not listen: %v", err))
	}
	_ = listener
	guard.Release()
}

// leakConnection opens a connection and leaves it open. The listener is closed
// by the fixture, so the only thing the guard finds is the connection.
func leakConnection(announcements *recorder) {
	guard := testguard.NewWithReporter(announcements)
	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("the fixture could not listen: %v", err))
	}
	connection, err := guard.Dial("tcp", listener.Addr().String())
	if err != nil {
		panic(fmt.Sprintf("the fixture could not connect: %v", err))
	}
	// The fixture accepts and closes its own end: the socket the guard is
	// asked about is the one the test opened, and the acceptance is only what
	// makes it a connection rather than a half-open attempt.
	accepted, err := listener.Accept()
	if err != nil {
		panic(fmt.Sprintf("the fixture could not accept: %v", err))
	}
	if err := accepted.Close(); err != nil {
		panic(fmt.Sprintf("the fixture could not close the accepted end: %v", err))
	}
	if err := listener.Close(); err != nil {
		panic(fmt.Sprintf("the fixture could not close its listener: %v", err))
	}
	guard.Release()
	_ = connection.Close()
}

// leakDirectory makes a directory the guard cannot remove: it holds a file and
// its own permissions no longer allow the unlink. This is what a removal that
// fails really looks like — a file the process is no longer allowed to delete —
// and the guard must say so instead of returning in silence.
func leakDirectory(announcements *recorder) {
	if runtime.GOOS == "windows" {
		panic("the directory fixture reproduces a POSIX permission failure; run the suite on the platform the product ships on")
	}
	guard := testguard.NewWithReporter(announcements)
	directory := guard.TempDir()
	if directory == "" {
		panic("the fixture got no directory")
	}
	if err := os.WriteFile(filepath.Join(directory, "kept.txt"), []byte("synthetic"), 0o600); err != nil {
		panic(fmt.Sprintf("the fixture could not write: %v", err))
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		panic(fmt.Sprintf("the fixture could not drop the permission: %v", err))
	}
	guard.Release()
	// The fixture still has to leave the machine as it found it: the guard
	// refused to remove in silence, and the fixture restores the permission so
	// that the operating system can.
	if err := os.Chmod(directory, 0o700); err != nil {
		panic(fmt.Sprintf("the fixture could not restore the permission: %v", err))
	}
	if err := os.RemoveAll(directory); err != nil {
		panic(fmt.Sprintf("the fixture could not remove what it made: %v", err))
	}
}

// leakTimer arms a ticker and never stops it.
func leakTimer(announcements *recorder) {
	guard := testguard.NewWithReporter(announcements)
	_ = guard.Ticker(time.Hour)
	_ = guard.AfterFunc(time.Hour, func() {})
	guard.Release()
}

// leakEnvironment sets a variable the way a test that forgot about the process
// it shares with every other test does: directly.
func leakEnvironment(announcements *recorder) {
	const key = "TESTGUARD_FIXTURE_LEAK"
	previous, existed := os.LookupEnv(key)
	guard := testguard.NewWithReporter(announcements)
	if err := os.Setenv(key, "synthetic"); err != nil {
		panic(fmt.Sprintf("the fixture could not set its variable: %v", err))
	}
	guard.Release()
	if existed {
		if err := os.Setenv(key, previous); err != nil {
			panic(fmt.Sprintf("the fixture could not restore its variable: %v", err))
		}
		return
	}
	if err := os.Unsetenv(key); err != nil {
		panic(fmt.Sprintf("the fixture could not remove its variable: %v", err))
	}
}

// TestTheGuardReportsEveryKindOfLeakOfOneFixture is the integration of the
// rules: a fixture that forgets everything at once must hear one report per
// kind, each with its own rule, and the release must end. It is the shape the
// audit's leak fixture uses, and it is the fixture that caught the budget of the
// release being spent by the first leak: a timer channel delivers once, so the
// goroutine below — checked after the process — left the release waiting for a
// signal that was never coming again.
func TestTheGuardReportsEveryKindOfLeakOfOneFixture(t *testing.T) {
	announcements := &recorder{}
	guard := testguard.NewWithReporter(announcements, testguard.WithGrace(50*time.Millisecond))
	directory := guard.TempDir()
	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_ = listener
	_ = directory
	if err := guard.Command("a process nobody stopped", exec.Command("sleep", "30")); err != nil {
		t.Fatalf("Command: %v", err)
	}
	blocked := make(chan struct{})
	guard.Goroutine("a worker that never ends", func() { <-blocked })
	_ = guard.Ticker(time.Hour)
	guard.Setenv("TESTGUARD_FIXTURE_MIXED", "synthetic")

	// The release is bounded: a guard that waits per resource instead of within
	// one budget would make this fixture take as long as it has leaks.
	released := make(chan struct{})
	go func() {
		defer close(released)
		guard.Release()
	}()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the release never ended: a leak report must not hang the suite")
	}
	close(blocked)

	counted := announcements.rules(t)
	want := []string{
		testguard.RuleGoroutine,
		testguard.RuleProcess,
		testguard.RuleListener,
		testguard.RuleTimer,
	}
	for _, rule := range want {
		if counted[rule] == 0 {
			t.Fatalf("the fixture leaked %v, want a report for %s", counted, rule)
		}
	}
	if counted[testguard.RuleEnvironment] != 0 {
		t.Fatalf("a variable the guard owns was reported as a leak: %v", counted)
	}
	if counted[testguard.RuleTempDirectory] != 0 {
		t.Fatalf("a directory the guard removed itself was reported as a leak: %v", counted)
	}

	// The rules are also the vocabulary of the audit, so the list is asserted
	// to be stable and free of duplicates rather than a bag of names: an
	// audit that counts a rule twice would report a leak that is one leak.
	declared := testguard.Rules()
	if len(declared) != len(unique(declared)) {
		t.Fatalf("the rules carry a duplicate: %v", declared)
	}
	if !equalStrings(declared, testguard.Rules()) {
		t.Fatal("the rules change between two calls: a reader cannot rely on them")
	}
}
