package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// The preload is the one moment the machine is allowed to reach the network,
// and it is allowed to reach exactly one thing: the lockfiles. It installs what
// `go.sum`, `web/package-lock.json` and `tools/e2e/package-lock.json` declare
// and nothing else — `go mod download` follows the module graph and `npm ci`
// installs the locked tree or refuses, which is why neither may be replaced by
// an install that resolves versions by itself.
//
// Then it proves the point of the exercise: it repeats the installation inside
// the denied environment, where the only thing that could satisfy a missing
// dependency is the sensor, and the sensor refuses and records. A preload that
// ends with "the caches answer the lockfiles" is a preload that makes the next
// run hermetic; one that ends with "the install succeeded" is a machine that
// happens to have a network.

// preloadStep is one installation of the preload.
type preloadStep struct {
	// Name is how the step is reported: the tool and the tree, not the whole
	// command.
	Name string
	// Argv is the command line of the installation.
	Argv []string
	// Offline is whether the step runs with egress denied. Filling a cache
	// cannot: `go mod download` and `npm ci` exist to fetch. The steps that
	// prove the caches — `go mod verify` and the npm dry run over the locked
	// tree — read and must never reach out.
	Offline bool
}

// preloadPlan answers the installations of the preload, in the order that makes
// each one's failure attributable.
func preloadPlan() []preloadStep {
	return []preloadStep{
		{Name: "go mod download", Argv: []string{"go", "mod", "download"}},
		{Name: "npm ci in web", Argv: []string{"npm", "ci", "--prefix", "web", "--no-audit", "--no-fund"}},
		{Name: "npm ci in tools/e2e", Argv: []string{"npm", "ci", "--prefix", "tools/e2e", "--no-audit", "--no-fund"}},
		{Name: "go mod verify", Argv: []string{"go", "mod", "verify"}, Offline: true},
		{
			// The dry run is the offline proof of the npm cache: it resolves the
			// locked tree and fails if one tarball is missing, writing nothing.
			Name:    "npm ci --dry-run in web",
			Argv:    []string{"npm", "ci", "--prefix", "web", "--dry-run"},
			Offline: true,
		},
	}
}

// Preload fills the approved caches from the lockfiles and then verifies, with
// egress denied, that they answer the lockfiles. The manifest of the tree is
// written where the caller asked for it, so that the preload and the run that
// follows it carry the same record.
func Preload(ctx context.Context, root, manifestPath string, stdout, stderr io.Writer) error {
	manifest, err := Build(root, hostTools{})
	if err != nil {
		return err
	}

	sensor, err := StartSensor()
	if err != nil {
		return err
	}
	defer func() { _ = sensor.Close() }()
	denied := DeniedEnvironment(os.Environ(), sensor.URL())

	for _, step := range preloadPlan() {
		mode := "with the network this task allows"
		environment := os.Environ()
		if step.Offline {
			mode = "with egress denied"
			environment = denied
		}
		fmt.Fprintf(stdout, "offlineaudit: preload — %s, %s\n", step.Name, mode)

		command := exec.CommandContext(ctx, step.Argv[0], step.Argv[1:]...)
		command.Dir = root
		command.Env = environment
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("the preload step %q failed: %w", step.Name, err)
		}
		if attempts := sensor.Attempts(); len(attempts) != 0 {
			// A step that the caches should have answered reached out: the
			// cache does not satisfy the lockfile, and the offline run would
			// fail later with a message nobody could act on.
			return refuse(RuleEgressAttempt,
				"the offline step %q asked for %s: the caches do not answer the lockfiles", step.Name, attempts[0])
		}
	}

	if manifestPath != "" {
		encoded, err := manifest.JSON()
		if err != nil {
			return fmt.Errorf("cannot render the manifest: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", filepath.Dir(manifestPath), err)
		}
		if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
			return fmt.Errorf("cannot write the manifest: %w", err)
		}
	}
	fmt.Fprintf(stdout, "offlineaudit: preload is done — %d source(s) digested, %d image(s), %d tool(s)\n",
		len(manifest.Sources), len(manifest.Images), len(manifest.Tools))
	return nil
}
