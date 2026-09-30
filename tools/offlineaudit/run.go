package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RunOptions is what one offline run is given.
type RunOptions struct {
	// Root is the checkout being measured.
	Root string
	// Name is what the run is called in the artifacts: the declared name of the
	// suite, not the command line, because two machines run it differently.
	Name string
	// Kind is the kind of evidence the run produces, in the vocabulary of the
	// evidence format (P22-T07). Empty means the run files no declaration: it
	// still files its manifest.
	Kind string
	// Rules are the identifiers of the rules this run proves, which the quality
	// registry owns. The evidence format refuses a suite that cites none.
	Rules []string
	// Out is the directory the artifacts are written to.
	Out string
	// Measurer answers the installed versions; nil means the machine's tools.
	Measurer Measurer
	// Now answers the instant of the run; nil means the wall clock.
	Now func() time.Time
	// Stdout and Stderr are where the command's output goes, beside the copy
	// the declaration keeps.
	Stdout, Stderr io.Writer
}

// RunResult is what one offline run answers.
type RunResult struct {
	// Status is the exit status of the command. A red gate is a result.
	Status int
	// ManifestPath and DeclarationPath are the artifacts that were written.
	ManifestPath    string
	DeclarationPath string
	// Attempts is what the sensor refused.
	Attempts []Attempt
	// Duration is how long the command took, and At is when it started.
	Duration time.Duration
	At       time.Time
	// Output is the tail of what the command printed.
	Output []string
}

// Run executes a gate with egress denied.
//
// The order matters and it is the whole method: the manifest is built before
// anything runs (a run whose tools drifted never starts), the sensor goes up
// before the command (a process cannot reach out and have it noticed
// afterwards), and the artifacts are written after the command whether it was
// green or red — while a run that reached out files nothing at all, because a
// measurement taken through a hole is not a measurement of the wall.
func Run(ctx context.Context, options RunOptions, argv []string) (RunResult, error) {
	if len(argv) == 0 {
		return RunResult{}, fmt.Errorf("%w: run needs a command after --", errUsage)
	}
	if options.Out == "" {
		return RunResult{}, fmt.Errorf("%w: run needs -out: an offline run files what it measured", errUsage)
	}
	measurer := options.Measurer
	if measurer == nil {
		measurer = hostTools{}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	manifest, err := Build(options.Root, measurer)
	if err != nil {
		return RunResult{}, err
	}
	if violations := Violations(manifest); len(violations) != 0 {
		return RunResult{}, violations[0]
	}

	sensor, err := StartSensor()
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = sensor.Close() }()

	environment := DeniedEnvironment(os.Environ(), sensor.URL())
	if err := preflight(ctx, options.Root, environment, sensor, options.Stderr); err != nil {
		return RunResult{}, err
	}

	recorder := newRecorder(options.Stdout, options.Stderr)
	started := now()
	status, err := execute(ctx, options.Root, environment, argv, recorder)
	if err != nil {
		return RunResult{}, err
	}
	duration := now().Sub(started)

	result := RunResult{Status: status, Attempts: sensor.Attempts(), Duration: duration, At: started, Output: recorder.tail()}
	if len(result.Attempts) != 0 {
		return result, refuse(RuleEgressAttempt,
			"the run of %s reached out %d time(s), first to %s: it is not the offline gate this task declares",
			strings.Join(argv, " "), sensor.Count(), result.Attempts[0])
	}

	manifestPath, declarationPath, err := writeArtifacts(options, manifest, result)
	if err != nil {
		return result, err
	}
	result.ManifestPath = manifestPath
	result.DeclarationPath = declarationPath
	return result, nil
}

// preflight runs, inside the denied environment, the two commands that prove
// the caches satisfy the lockfiles without touching the network. It is what
// turns "the gate ran offline" into "the gate could not have done anything
// else": the module cache is verified against go.sum before the suite starts.
func preflight(ctx context.Context, root string, environment []string, sensor *Sensor, stderr io.Writer) error {
	names := []string{"go"}
	for _, name := range names {
		command := exec.CommandContext(ctx, name, "mod", "verify")
		command.Dir = root
		command.Env = environment
		output, err := command.CombinedOutput()
		if err == nil {
			continue
		}
		if attempts := sensor.Attempts(); len(attempts) != 0 {
			return refuse(RuleEgressAttempt, "the preflight of %s reached out to %s: the caches do not satisfy the lockfiles", name, attempts[0])
		}
		fmt.Fprintf(nonNil(stderr), "%s\n", strings.TrimSpace(string(output)))
		return fmt.Errorf("the preflight `%s mod verify` failed: run `make offline-preload` first", name)
	}
	return nil
}

// execute runs the command and answers its exit status. A non-zero status is
// not an error: a red gate is a result, and the artifacts say so.
func execute(ctx context.Context, root string, environment []string, argv []string, recorder *recorder) (int, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = root
	command.Env = environment
	command.Stdout = recorder
	command.Stderr = recorder

	err := command.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, fmt.Errorf("cannot run %s: %w", strings.Join(argv, " "), err)
}

// writeArtifacts puts the manifest and, when the run declares one, the
// declaration of the evidence format on disk.
func writeArtifacts(options RunOptions, manifest Manifest, result RunResult) (string, string, error) {
	if err := os.MkdirAll(options.Out, 0o755); err != nil {
		return "", "", fmt.Errorf("cannot create %s: %w", options.Out, err)
	}
	slug := Slug(options.Name)

	encoded, err := manifest.JSON()
	if err != nil {
		return "", "", fmt.Errorf("cannot render the manifest: %w", err)
	}
	manifestPath := filepath.Join(options.Out, slug+".manifest.json")
	if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
		return "", "", fmt.Errorf("cannot write the manifest: %w", err)
	}

	if options.Kind == "" {
		return manifestPath, "", nil
	}
	declaration := Declaration{
		Kind:            options.Kind,
		Name:            options.Name,
		Rules:           options.Rules,
		Status:          statusOfExit(result.Status),
		DurationSeconds: result.Duration.Seconds(),
		At:              result.At.UTC().Format(time.RFC3339),
		Output:          result.Output,
	}
	rendered, err := json.MarshalIndent(declaration, "", "  ")
	if err != nil {
		return manifestPath, "", fmt.Errorf("cannot render the declaration: %w", err)
	}
	declarationPath := filepath.Join(options.Out, slug+".declaration.json")
	if err := os.WriteFile(declarationPath, append(rendered, '\n'), 0o644); err != nil {
		return manifestPath, "", fmt.Errorf("cannot write the declaration: %w", err)
	}
	return manifestPath, declarationPath, nil
}

// Declaration is the shape `tools/evidence declare` reads (P22-T07). It is
// declared here instead of imported because the tool that *produces* evidence
// must not depend on the format that judges it: a producer that shares the
// judge's code cannot be refused by it for the wrong shape.
type Declaration struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Rules           []string `json:"rules"`
	Status          string   `json:"status"`
	DurationSeconds float64  `json:"duration_seconds"`
	At              string   `json:"at"`
	Output          []string `json:"output,omitempty"`
}

// statusOfExit translates an exit status into the status of the evidence.
func statusOfExit(status int) string {
	if status == 0 {
		return "pass"
	}
	return "fail"
}

// Slug reduces a suite name to something a file name can carry.
func Slug(name string) string {
	var builder strings.Builder
	previousDash := false
	for _, character := range strings.ToLower(name) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			previousDash = false
		case !previousDash:
			builder.WriteRune('-')
			previousDash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "run"
	}
	return slug
}

// recorder writes what a command prints to the console and keeps the tail of it
// for the declaration. The tail is bounded because evidence is a record, not a
// transcript: a suite that prints a hundred thousand lines should not turn the
// artifact into the log it already has.
type recorder struct {
	mutex sync.Mutex
	lines []string
	out   io.Writer
	err   io.Writer
	// partial holds the last line printed without its newline yet.
	partial string
}

// tailLimit is how many lines of a command's output the declaration keeps.
const tailLimit = 200

func newRecorder(stdout, stderr io.Writer) *recorder {
	return &recorder{out: nonNil(stdout), err: nonNil(stderr)}
}

// Write implements io.Writer: both streams of the command go to the console and
// to the same tail, in the order the process printed them.
func (r *recorder) Write(data []byte) (int, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	_, errOut := r.err.Write(data)
	_ = errOut

	text := r.partial + string(data)
	lines := strings.Split(text, "\n")
	r.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > tailLimit {
		r.lines = r.lines[len(r.lines)-tailLimit:]
	}
	return len(data), errOut
}

// tail answers what the command printed, the last line included even when it
// arrived without a newline.
func (r *recorder) tail() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	lines := make([]string, 0, len(r.lines)+1)
	lines = append(lines, r.lines...)
	if strings.TrimSpace(r.partial) != "" {
		lines = append(lines, r.partial)
	}
	return lines
}

// nonNil answers a writer that swallows what a nil one would panic on.
func nonNil(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}
