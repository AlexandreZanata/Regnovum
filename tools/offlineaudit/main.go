// Package main is the offline mode of the test platform (P22-T08).
//
// The phase asks for a run that installs from approved caches and lockfiles and
// executes the gates without an external network, with the toolchains pinned and
// the image digests recorded. The three claims are the three parts of this tool:
//
//   - **The tree says which tools and images it needs.** `manifest` reads the
//     pins the tree states (`go.mod` for the toolchain, the workflows for Node,
//     the `Dockerfile` and the two Compose files for the images), measures the
//     machine, and answers one document. It refuses a tool whose installed
//     version disagrees with its pin, an image that builds or deploys without a
//     digest, an image the development compose runs that no file pins by
//     digest, and a package manifest without its lockfile. The document carries
//     no instant, no host name and no absolute path, so two runs of the same
//     tree answer the same bytes — which is what "two runs produce the same
//     manifest" means, and the only form of it anybody can check.
//
//   - **The caches come from the lockfiles, and they are proved.** `preload`
//     fills the Go module cache and the npm caches from `go.sum` and the two
//     `package-lock.json`, and then repeats the *reading* half — `go mod verify`
//     and the npm dry run over the locked tree — inside the denied environment.
//     A preload that ends with "the install worked" is a machine with a network;
//     one that ends with "the caches answer the lockfiles" is a machine that can
//     run the gate offline.
//
//   - **The run denies egress and observes it.** `run` starts a loopback HTTP
//     proxy, points every client of the run at it, executes the gate, and
//     refuses the run if anything reached out — naming the host and the path.
//     Denial alone turns an unexpected download into "connection refused" and a
//     reader who cannot act; observation turns it into the name of what was
//     wanted. The loopback addresses stay outside the proxy, because the gates
//     of this repository talk to PostgreSQL and to fake providers on 127.0.0.1:
//     what the sensor sees is what leaves the machine.
//
// The declaration a run writes is the shape `tools/evidence declare` reads
// (P22-T07), declared here and not imported: the producer of evidence must not
// share the judge's code, or the judge cannot refuse it for the wrong shape.
//
// It is a tool and never part of the delivered application, and it needs no
// dependency beyond the standard library.
//
// Usage. The artifacts go where the caller says and never into the tree: a run
// that dirtied the checkout it measures would make the next gate red.
//
//	offlineaudit manifest -root . -out "$WORK"
//	offlineaudit check    -in "$WORK/manifest.json"
//	offlineaudit preload  -root . -manifest "$WORK/preload.manifest.json"
//	offlineaudit run      -root . -out "$WORK" -name "make test-unit" -kind go-test \
//	    -rules "$(the rules the quality registry declares)" -- make test-unit
//	offlineaudit probe    -url https://registry.npmjs.org/
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	exitOK = 0
	// exitViolation is the answer of a run the audit refuses: an attempt that
	// left the machine, a manifest that disagrees with the tree.
	exitViolation = 1
	// exitUsage is the answer of a command that cannot do its job.
	exitUsage = 2
)

// errUsage marks a misuse, which is not a measurement.
var errUsage = errors.New("offlineaudit: usage")

const usage = `offlineaudit — execução offline e reprodutível dos gates (P22-T08)

  offlineaudit manifest -root <dir> [-out <dir>]        escreve o manifesto de ferramentas, imagens e fontes
  offlineaudit check    -in <manifest.json>             julga um manifesto arquivado
  offlineaudit preload  -root <dir> [-manifest <path>]  instala dos lockfiles e prova os caches offline
  offlineaudit run      -root <dir> -out <dir> [flags] -- <cmd> [args...]
                                                        executa o gate com egress negado e observado
  offlineaudit probe    -url <url>                      tenta alcançar um endereço com o ambiente do run
  offlineaudit rules                                    imprime o vocabulário das recusas

flags de run:
  -name <suite>     nome declarado da suíte (padrão: a linha de comando)
  -kind <tipo>      tipo de evidência no vocabulário da P22-T07; vazio não arquiva declaração
  -rules <lista>    identificadores das regras que o gate prova, separados por vírgula
`

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI is the whole command with its status as a value: a contract that only
// lives inside main is one no test can hold.
func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "manifest":
		return runManifest(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "preload":
		return runPreload(ctx, args[1:], stdout, stderr)
	case "run":
		return runOffline(ctx, args[1:], stdout, stderr)
	case "probe":
		return runProbe(args[1:], stdout, stderr)
	case "rules":
		return runRules(stdout)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "offlineaudit: unknown subcommand %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// report translates an error into an exit status, naming the rule when there is
// one: a refusal is data, a crash is not.
func report(err error, stderr io.Writer) int {
	if err == nil {
		return exitOK
	}
	var refusal Refusal
	if errors.As(err, &refusal) {
		encoded, marshalErr := json.Marshal(refusal)
		if marshalErr == nil {
			fmt.Fprintln(stderr, string(encoded))
			return exitViolation
		}
	}
	if errors.Is(err, errUsage) {
		fmt.Fprintf(stderr, "offlineaudit: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stderr, "offlineaudit: %v\n", err)
	return exitViolation
}

// runManifest writes the manifest of the tree.
func runManifest(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("manifest", flag.ContinueOnError)
	set.SetOutput(stderr)
	root := set.String("root", ".", "the checkout to measure")
	out := set.String("out", "", "the directory to write the manifest to")
	if err := set.Parse(args); err != nil {
		return exitUsage
	}
	manifest, err := Build(*root, hostTools{})
	if err != nil {
		return report(err, stderr)
	}
	if violations := Violations(manifest); len(violations) != 0 {
		return report(violations[0], stderr)
	}
	encoded, err := manifest.JSON()
	if err != nil {
		return report(fmt.Errorf("cannot render the manifest: %w", err), stderr)
	}
	if *out == "" {
		fmt.Fprint(stdout, string(encoded))
		return exitOK
	}
	path := filepath.Join(*out, "manifest.json")
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return report(fmt.Errorf("cannot create %s: %w", *out, err), stderr)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return report(fmt.Errorf("cannot write %s: %w", path, err), stderr)
	}
	fmt.Fprintf(stdout, "offlineaudit: manifest written to %s — %d tool(s), %d image(s), %d source(s)\n",
		path, len(manifest.Tools), len(manifest.Images), len(manifest.Sources))
	return exitOK
}

// runCheck judges a filed manifest.
func runCheck(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("check", flag.ContinueOnError)
	set.SetOutput(stderr)
	in := set.String("in", "", "the manifest to judge")
	if err := set.Parse(args); err != nil {
		return exitUsage
	}
	if *in == "" {
		return report(fmt.Errorf("%w: check needs -in", errUsage), stderr)
	}
	content, err := os.ReadFile(*in)
	if err != nil {
		return report(fmt.Errorf("cannot read %s: %w", *in, err), stderr)
	}
	manifest, err := ParseManifest(content)
	if err != nil {
		return report(err, stderr)
	}
	if violations := Violations(manifest); len(violations) != 0 {
		encoded, marshalErr := json.Marshal(violations)
		if marshalErr != nil {
			return report(fmt.Errorf("cannot render the refusals: %w", marshalErr), stderr)
		}
		fmt.Fprintln(stderr, string(encoded))
		fmt.Fprintf(stderr, "offlineaudit: the manifest is refused by %d rule(s)\n", len(violations))
		return exitViolation
	}
	fmt.Fprintf(stdout, "offlineaudit: %s is readable against schema %d — %d tool(s), %d image(s), %d source(s)\n",
		*in, manifest.Schema, len(manifest.Tools), len(manifest.Images), len(manifest.Sources))
	return exitOK
}

// runPreload fills the caches from the lockfiles.
func runPreload(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("preload", flag.ContinueOnError)
	set.SetOutput(stderr)
	root := set.String("root", ".", "the checkout to preload")
	manifest := set.String("manifest", "", "where to write the manifest of the preload")
	if err := set.Parse(args); err != nil {
		return exitUsage
	}
	if err := Preload(ctx, *root, *manifest, stdout, stderr); err != nil {
		return report(err, stderr)
	}
	return exitOK
}

// runOffline executes a gate with egress denied.
func runOffline(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	set.SetOutput(stderr)
	root := set.String("root", ".", "the checkout to run in")
	out := set.String("out", "", "the directory to write the artifacts to")
	name := set.String("name", "", "the declared name of the suite")
	kind := set.String("kind", "", "the kind of evidence to declare, or empty for none")
	rules := set.String("rules", "", "the rules the gate proves, comma separated")
	if err := set.Parse(args); err != nil {
		return exitUsage
	}
	argv := set.Args()
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return report(fmt.Errorf("%w: run needs a command after --", errUsage), stderr)
	}
	suite := *name
	if suite == "" {
		suite = strings.Join(argv, " ")
	}

	result, err := Run(ctx, RunOptions{
		Root:   *root,
		Name:   suite,
		Kind:   *kind,
		Rules:  splitRules(*rules),
		Out:    *out,
		Stdout: stdout,
		Stderr: stderr,
	}, argv)

	if len(result.Attempts) != 0 {
		// The refusal is written after the command's output, which is where a
		// reader looks for it.
		return report(err, stderr)
	}
	if err != nil {
		return report(err, stderr)
	}
	fmt.Fprintf(stdout, "offlineaudit: %s finished with status %d in %.3fs, with egress denied and nothing attempted\n",
		suite, result.Status, result.Duration.Seconds())
	if result.ManifestPath != "" {
		fmt.Fprintf(stdout, "offlineaudit: manifest %s\n", result.ManifestPath)
	}
	if result.DeclarationPath != "" {
		fmt.Fprintf(stdout, "offlineaudit: declaration %s\n", result.DeclarationPath)
	}
	// The gate's own status is the run's status: a red gate is a red gate.
	return result.Status
}

// runProbe is the control client of the exercise.
func runProbe(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("probe", flag.ContinueOnError)
	set.SetOutput(stderr)
	target := set.String("url", "", "the address to try to reach")
	if err := set.Parse(args); err != nil {
		return exitUsage
	}
	if *target == "" {
		return report(fmt.Errorf("%w: probe needs -url", errUsage), stderr)
	}
	return report(Probe(*target, stdout, stderr), stderr)
}

// runRules prints the vocabulary of the refusals.
func runRules(stdout io.Writer) int {
	payload := struct {
		Rules []string `json:"rules"`
	}{Rules()}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "offlineaudit: %v\n", err)
		return exitViolation
	}
	fmt.Fprintln(stdout, string(encoded))
	return exitOK
}

// splitRules reads the comma separated rules of a run.
func splitRules(value string) []string {
	rules := []string{}
	for _, rule := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(rule)
		if trimmed == "" {
			continue
		}
		rules = append(rules, trimmed)
	}
	return rules
}
