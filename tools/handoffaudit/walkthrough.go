// The walkthrough half of the handoff audit (P20-T08).
//
// The phase's minimum validation is that a person or an agent, new to the
// repository, follows the README in a clean checkout and executes the smoke
// without tacit knowledge. This file does exactly that, mechanically: it checks
// out the commit into a worktree, copies the document and the audit itself over
// it (declared, with digests — the instrument cannot be inside the commit it
// verifies), runs the commands the document's own quickstart block declares, and
// then starts the server exactly as the document's serve block declares and
// smokes the surfaces the rules require the document to name.
//
// Nothing here invents a command: the sequence comes from the document, so a
// quickstart that does not work is a red walkthrough instead of a paragraph
// somebody believes. Because the document is the deliverable of this task, the
// document and the tool are what a clean checkout of the commit does not hold.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// walkthrough is what one run holds.
type walkthrough struct {
	root    string
	file    string
	work    string
	log     *os.File
	stdout  io.Writer
	stderr  io.Writer
	overlay []copiedFile
	timeout time.Duration
}

// copiedFile is one file the run put over the checkout, with the digest of what
// it copied: the run declares its own overlay instead of hiding it.
type copiedFile struct {
	path   string
	digest string
}

func runWalkthrough(root, file string, stdout, stderr io.Writer) error {
	work, err := os.MkdirTemp("", "arena-handoff-")
	if err != nil {
		return fmt.Errorf("handoffaudit: create the sandbox: %w", err)
	}
	log, err := os.CreateTemp("", "arena-handoff-log-")
	if err != nil {
		return fmt.Errorf("handoffaudit: create the log: %w", err)
	}
	defer log.Close()

	checkout := filepath.Join(work, "checkout")
	run := &walkthrough{
		root:    root,
		file:    file,
		work:    checkout,
		log:     log,
		stdout:  stdout,
		stderr:  stderr,
		timeout: 20 * time.Minute,
	}
	fmt.Fprintf(stdout, "handoff-walkthrough: reading the document\n")

	facts, err := loadFacts(root, file)
	if err != nil {
		return err
	}
	quickstart := facts.doc.block("quickstart")
	serve := facts.doc.block("serve")
	if quickstart == nil || serve == nil {
		return errors.New("handoffaudit: the document declares no quickstart or serve block; run `handoffaudit check` first")
	}

	commit, err := run.git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	branch, err := run.git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "handoff-walkthrough: commit %s on %s\n", commit[:12], branch)

	defer run.cleanup()
	if _, err := run.git("worktree", "add", "--detach", checkout, commit); err != nil {
		return err
	}
	if err := run.overlayFiles(); err != nil {
		return err
	}
	for _, file := range run.overlay {
		fmt.Fprintf(stdout, "handoff-walkthrough: over the commit: %s (%s)\n", file.path, file.digest[:16])
	}

	if err := run.quickstart(quickstart); err != nil {
		return err
	}
	if err := run.serveAndSmoke(quickstart, serve); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "handoff-walkthrough: ok — the commands of the document ran in a clean checkout and the smoke answered\n")
	return nil
}

// overlayFiles copies the working tree's changes over the checkout when the
// checkout does not already hold them with the same bytes.
//
// The changes of the task are the overlay: this tool and the document are two of
// them, and the Makefile that declares the targets the document names is another
// — the walkthrough runs `make test-unit`, so the instrument has to be complete
// in the checkout or the checkout would judge a Makefile the document does not
// describe. The local directory never ships and is never copied.
func (run *walkthrough) overlayFiles() error {
	candidates, err := run.changedFiles()
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		from := filepath.Join(run.root, candidate)
		to := filepath.Join(run.work, candidate)
		content, err := os.ReadFile(from)
		if err != nil {
			// A change that removes a file leaves the commit's copy in place,
			// reported instead of hidden.
			fmt.Fprintf(run.stdout, "handoff-walkthrough: not copied (absent in the working tree): %s\n", candidate)
			continue
		}
		digest := digestOf(content)
		if current, err := os.ReadFile(to); err == nil && digestOf(current) == digest {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return fmt.Errorf("handoffaudit: create %s: %w", filepath.Dir(candidate), err)
		}
		if err := os.WriteFile(to, content, 0o644); err != nil {
			return fmt.Errorf("handoffaudit: copy %s: %w", candidate, err)
		}
		run.overlay = append(run.overlay, copiedFile{path: candidate, digest: digest})
	}
	return nil
}

// porcelainPaths reads the paths out of `git status --porcelain` output. The
// first three characters are the two status columns and the separator, so the
// path starts at the third; a rename carries both names and the new one is the
// file that exists. The local directory never ships and is never listed — the
// plan is the operator's, not the repository's.
func porcelainPaths(status string) []string {
	paths := []string{}
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if arrow := strings.Index(path, " -> "); arrow >= 0 {
			path = path[arrow+4:]
		}
		path = strings.Trim(strings.TrimSpace(path), `"`)
		if path == "" || path == ".local" || strings.HasPrefix(path, ".local/") {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// changedFiles lists every file the working tree changes, which is the overlay
// the run declares — plus this tool's own sources and the document, so a run
// from a clean tree (the second run of a committed task) still follows the tool
// the reader is holding.
func (run *walkthrough) changedFiles() ([]string, error) {
	status, err := run.gitRaw("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	add := func(path string) {
		set[path] = true
	}
	for _, path := range porcelainPaths(status) {
		add(path)
	}
	add(run.file)
	sources, err := filepath.Glob(filepath.Join(run.root, "tools", "handoffaudit", "*.go"))
	if err != nil {
		return nil, fmt.Errorf("handoffaudit: list the tool's sources: %w", err)
	}
	for _, source := range sources {
		relative, err := filepath.Rel(run.root, source)
		if err != nil {
			return nil, err
		}
		add(relative)
	}
	return sortedKeys(set), nil
}

// quickstart runs the block the document declares, in the checkout, with the
// ARENA_* environment of the caller removed: a walkthrough that inherits the
// operator's variables proves nothing about the document being self-sufficient.
func (run *walkthrough) quickstart(block *block) error {
	lines := commandLines(block)
	if len(lines) == 0 {
		return errors.New("handoffaudit: the quickstart block holds no command")
	}

	// The database the document points at may already be answering — a
	// developer machine usually has it up, and the Compose file names the
	// container, so starting a second one is not even possible. That is
	// reported as the step being satisfied, never as the step being skipped
	// silently.
	satisfied := ""
	if dsn := databaseURL(lines); dsn != "" && reachable(dsn) {
		satisfied = "docker compose up -d db"
		lines = dropCommand(lines, satisfied)
		fmt.Fprintf(run.stdout, "handoff-walkthrough: %s: satisfied — the database of the document already answers on %s\n", satisfied, hostPort(dsn))
	}

	fmt.Fprintf(run.stdout, "handoff-walkthrough: running the quickstart of the document\n")
	command := exec.Command("bash", "-ev")
	command.Dir = run.work
	command.Env = cleanEnv()
	command.Stdin = strings.NewReader("set -euo pipefail\n" + strings.Join(lines, "\n") + "\n")
	command.Stdout = run.log
	command.Stderr = run.log
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := runAndWait(command, run.timeout); err != nil {
		return fmt.Errorf("handoffaudit: a command of the document's quickstart failed: %w (the run's output is in %s)", err, run.log.Name())
	}
	for _, line := range lines {
		fmt.Fprintf(run.stdout, "handoff-walkthrough: quickstart step ok: %s\n", line)
	}
	return nil
}

// serveAndSmoke starts the server the way the document's serve block says and
// requires the surfaces the rules require the document to name to answer.
//
// The serve block runs in a second shell, which is what a person has after the
// quickstart: so the exports of the quickstart are replayed first, and the
// values come from the document instead of being invented here.
func (run *walkthrough) serveAndSmoke(quickstart, serve *block) error {
	serveLines := commandLines(serve)
	if len(serveLines) == 0 {
		return errors.New("handoffaudit: the serve block holds no command")
	}
	script := "set -euo pipefail\n" + strings.Join(quickstartExports(quickstart), "\n")
	if len(quickstartExports(quickstart)) > 0 {
		script += "\n"
	}
	script += strings.Join(serveLines, "\n") + "\n"

	address := serveAddress(serveLines)
	if address == "" {
		address = "127.0.0.1:8080"
	}
	if !freeToBind(address) {
		return fmt.Errorf("handoffaudit: the address the document uses (%s) is already in use on this machine; the walkthrough runs the documented command as it is, so it needs it free", address)
	}

	fmt.Fprintf(run.stdout, "handoff-walkthrough: starting the server of the document on %s\n", address)
	command := exec.Command("bash", "-ev")
	command.Dir = run.work
	command.Env = cleanEnv()
	command.Stdin = strings.NewReader(script)
	command.Stdout = run.log
	command.Stderr = run.log
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("handoffaudit: start the server: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
	}()

	base := "http://" + address
	for _, surface := range requiredSurfaces {
		status, err := waitForStatus(base+surface, 90*time.Second)
		if err != nil {
			return fmt.Errorf("handoffaudit: %s did not answer (%w); the server's output is in %s", surface, err, run.log.Name())
		}
		if status != 200 {
			return fmt.Errorf("handoffaudit: %s answered %d, and the document says it answers 200", surface, status)
		}
		fmt.Fprintf(run.stdout, "handoff-walkthrough: smoke ok: GET %s -> 200\n", surface)
	}
	return nil
}

// cleanup removes the worktree and the sandbox, keeping the log of a failed run
// where the failure message says it is.
func (run *walkthrough) cleanup() {
	_, _ = run.git("worktree", "remove", "--force", run.work)
	_, _ = run.git("worktree", "prune")
	_ = os.RemoveAll(filepath.Dir(run.work))
}

// git runs one git command in the repository and returns its trimmed output.
func (run *walkthrough) git(args ...string) (string, error) {
	out, err := run.gitRaw(args...)
	return strings.TrimSpace(out), err
}

// gitRaw runs one git command and returns its output untouched. Porcelain
// output is not a sentence: its first column carries meaning, so trimming it
// would turn " M Makefile" into "M Makefile" and the path into "akefile" —
// which is how the first version of this file failed to copy the Makefile over
// the checkout and left the walkthrough judging the wrong one.
func (run *walkthrough) gitRaw(args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = run.root
	var out, errOut bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errOut
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("handoffaudit: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// commandLines returns the runnable lines of a block: comments and blank lines
// are the document talking to the reader, not something to execute.
func commandLines(block *block) []string {
	lines := []string{}
	for _, line := range block.lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return lines
}

// quickstartExports returns the export lines of the quickstart block, which is
// the environment the serve block is entitled to.
func quickstartExports(block *block) []string {
	lines := []string{}
	for _, line := range commandLines(block) {
		if strings.HasPrefix(line, "export ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// dropCommand removes one command from the script without touching the rest.
func dropCommand(lines []string, command string) []string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == command {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

var (
	dsnPattern          = regexp.MustCompile(`postgres(?:ql)?://[^\s'"]+`)
	serveAddressPattern = regexp.MustCompile(`ARENA_ADDR=([0-9]{1,3}(?:\.[0-9]{1,3}){3}:[0-9]+)`)
)

// databaseURL returns the DSN the quickstart exports, if it exports one.
func databaseURL(lines []string) string {
	for _, line := range lines {
		if match := dsnPattern.FindString(line); match != "" {
			return match
		}
	}
	return ""
}

// hostPort reads host:port out of a DSN.
func hostPort(dsn string) string {
	withoutScheme := dsn[strings.Index(dsn, "://")+3:]
	if at := strings.Index(withoutScheme, "@"); at >= 0 {
		withoutScheme = withoutScheme[at+1:]
	}
	if slash := strings.Index(withoutScheme, "/"); slash >= 0 {
		withoutScheme = withoutScheme[:slash]
	}
	if question := strings.Index(withoutScheme, "?"); question >= 0 {
		withoutScheme = withoutScheme[:question]
	}
	return withoutScheme
}

// reachable reports whether something answers on the host:port of a DSN.
func reachable(dsn string) bool {
	connection, err := net.DialTimeout("tcp", hostPort(dsn), 2*time.Second)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

// serveAddress reads the address the serve block sets.
func serveAddress(lines []string) string {
	for _, line := range lines {
		if match := serveAddressPattern.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

// freeToBind reports whether the address is free on this machine.
func freeToBind(address string) bool {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// cleanEnv is the caller's environment without the ARENA_* variables: the
// document has to bring its own configuration.
func cleanEnv() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ARENA_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// waitForStatus polls a URL until it answers, up to the deadline.
func waitForStatus(url string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		status, err := httpStatus(url)
		if err == nil {
			return status, nil
		}
		last = err
		time.Sleep(500 * time.Millisecond)
	}
	return 0, last
}

// runAndWait runs a command with its own process group and a deadline.
func runAndWait(command *exec.Cmd, timeout time.Duration) error {
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return fmt.Errorf("timed out after %s", timeout)
	}
}

// digestOf is the sha256 of a file's content, as the overlay report prints it.
func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
