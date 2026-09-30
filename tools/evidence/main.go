// Package main is the evidence format of the test platform (P22-T07).
//
// Every gate of this repository produces output, and until now every one of
// them produced it in its own shape: `go test -json` for the Go suites, a
// scanner's text for the audits, a summary for the load run, the runner's
// reporter for the journeys. A human reads them and a machine cannot: nothing
// carries the commit, the version of the tool, the seed of the run, how long it
// took, or which rule fired. This command turns a run into one document with
// exactly those fields, in a format that does not depend on who produced it —
// and into JUnit XML for the readers that only know how to read that.
//
// Four rules shape it, and each one has a fixture in evidence_test.go:
//
//   - **Nothing incomplete is published.** A stream that ends in the middle of
//     a package or a test, a line that is not an event, an action the format
//     does not know, a declaration that says "ok" without saying how: all of
//     them refuse the conversion. A truncated result that converts is worse
//     than a missing one — it is a report somebody will read as the whole
//     truth.
//   - **The order is the format's, not the input's.** Suites and cases are
//     written in canonical order, so the line of one artifact lines up with the
//     line of another and a diff between two runs means a change in what
//     happened. The output of a case keeps the order it was printed in, because
//     that is the log.
//   - **A document says what it measured.** The commit, the seed, the instant,
//     the duration, the version of every tool and the rules each suite cites are
//     required, and their absence is the rule `missing-metadata`.
//   - **Sensitive content never leaves in the artifact.** The values the
//     platform's own detector knows (the seeded dataset rules of P22-T04) are
//     replaced by a marker that names the rule before the document is written
//     and counted by rule, and `check` refuses a document that still carries
//     one. Evidence is filed, copied, uploaded and read in the future: a token
//     in it is a token published.
//
// The command knows two producers. `convert` reads `go test -json`, which is
// what the Go suites already emit, and `declare` reads the small JSON document
// an audit, the load run or the browser journeys write about themselves —
// because those three produce text, and a format that only understood `go test
// -json` would leave them with no artifact at all.
//
// It is a tool and never part of the delivered application.
//
// Usage:
//
//	go test -json ./... > run.jsonl
//	evidence convert -in run.jsonl -out evidence.json -junit evidence.xml \
//	    -commit "$(git rev-parse HEAD)" -seed "$ARENA_TEST_SEED" \
//	    -tool "go=$(go version)" -cite "./internal/wallet=QUAL-WALLET-LEDGER"
//	evidence declare -in declaration.json -out evidence.json -junit evidence.xml \
//	    -commit "$(git rev-parse HEAD)" -seed "$ARENA_TEST_SEED" -tool "k6=1.2.3"
//	evidence check -in evidence.json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// errUsage is the exit path of a call the command cannot make sense of. It is
// separate from a refusal because a misuse is not a measurement.
var errUsage = errors.New("evidence: usage")

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		var refusal Refusal
		if errors.As(err, &refusal) {
			// A refusal is data: the caller reads a rule identifier and the
			// reason, which is what a gate counts and a human acts on.
			encoded, _ := json.Marshal(refusal)
			fmt.Fprintln(os.Stderr, string(encoded))
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "evidence: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches one call. It is a function with its own streams so the tests
// drive the command the way a caller does, and not through the process.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "convert":
		return runConvert(args[1:], stdout, stderr)
	case "declare":
		return runDeclare(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "rules":
		return runRules(stdout)
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}

const usage = `evidence convert -in <jsonl> -out <json> [-junit <xml>] -commit <rev> -seed <seed> [-tool name=version]... [-cite <name>=<rule>,...]
evidence declare -in <json> -out <json> [-junit <xml>] -commit <rev> -seed <seed> [-tool name=version]...
evidence check -in <json>
evidence rules
`

// metadataFlags are the flags every producing call shares: what a document must
// know about the run itself.
type metadataFlags struct {
	commit string
	seed   string
	tools  listFlag
	cites  listFlag
}

// bind declares the shared flags.
func (m *metadataFlags) bind(set *flag.FlagSet) {
	set.StringVar(&m.commit, "commit", "", "the revision the run measured")
	set.StringVar(&m.seed, "seed", "", "the seed of the run")
	set.Var(&m.tools, "tool", "the version of a tool, as name=version; repeatable")
	set.Var(&m.cites, "cite", "the rules a suite proves, as suite=rule[,rule]; repeatable")
}

// metadata reads the flags into the metadata of the document.
func (m *metadataFlags) metadata() (Metadata, error) {
	tools := map[string]string{}
	for _, entry := range m.tools {
		name, version, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(name) == "" {
			return Metadata{}, fmt.Errorf("%w: -tool wants name=version, got %q", errUsage, entry)
		}
		tools[strings.TrimSpace(name)] = strings.TrimSpace(version)
	}
	return Metadata{Commit: m.commit, Seed: m.seed, Tools: tools}, nil
}

// citations reads the -cite flags, refusing a malformed one instead of
// dropping it: a citation nobody can parse is a rule the artifact does not
// carry.
func (m *metadataFlags) citations() (Citations, error) {
	citations := Citations{}
	for _, entry := range m.cites {
		name, rules, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%w: -cite wants suite=rule[,rule], got %q", errUsage, entry)
		}
		for _, rule := range strings.Split(rules, ",") {
			if strings.TrimSpace(rule) == "" {
				return nil, fmt.Errorf("%w: -cite names an empty rule in %q", errUsage, entry)
			}
			citations[strings.TrimSpace(name)] = append(citations[strings.TrimSpace(name)], strings.TrimSpace(rule))
		}
	}
	return citations, nil
}

// listFlag collects a repeated flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// runConvert reads a `go test -json` stream and writes the artifact.
func runConvert(args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("convert", flag.ContinueOnError)
	set.SetOutput(stderr)
	var metadata metadataFlags
	metadata.bind(set)
	in := set.String("in", "", "the stream to read, or - for the standard input")
	out := set.String("out", "", "the document to write, or - for the standard output")
	junitPath := set.String("junit", "", "the JUnit artifact to write, when asked")
	if err := set.Parse(args); err != nil {
		return errUsage
	}
	if set.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("%w: convert takes no positional argument, got %q", errUsage, set.Arg(0))
	}

	source, closeSource, err := openInput(*in)
	if err != nil {
		return err
	}
	defer closeSource()

	values, err := metadata.metadata()
	if err != nil {
		return err
	}
	citations, err := metadata.citations()
	if err != nil {
		return err
	}

	document, err := Convert(source, values, citations)
	if err != nil {
		return err
	}
	if err := document.write(*out, *junitPath); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "evidence: %d suites, %d cases, %d failed, %d redactions\n",
		document.Totals.Suites, document.Totals.Cases, document.Totals.Failed, redacted(document))
	return nil
}

// runDeclare reads what a producer that is not `go test` says about itself.
func runDeclare(args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("declare", flag.ContinueOnError)
	set.SetOutput(stderr)
	var metadata metadataFlags
	metadata.bind(set)
	in := set.String("in", "", "the declaration to read, or - for the standard input")
	out := set.String("out", "", "the document to write, or - for the standard output")
	junitPath := set.String("junit", "", "the JUnit artifact to write, when asked")
	if err := set.Parse(args); err != nil {
		return errUsage
	}
	if set.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("%w: declare takes no positional argument, got %q", errUsage, set.Arg(0))
	}

	source, closeSource, err := openInput(*in)
	if err != nil {
		return err
	}
	defer closeSource()
	raw, err := io.ReadAll(source)
	if err != nil {
		return fmt.Errorf("read the declaration: %w", err)
	}

	declaration, err := ParseDeclaration(raw)
	if err != nil {
		return err
	}
	values, err := metadata.metadata()
	if err != nil {
		return err
	}
	document, err := Declare(declaration, values)
	if err != nil {
		return err
	}
	if err := document.write(*out, *junitPath); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "evidence: %d suites, %d cases, %d failed, %d redactions\n",
		document.Totals.Suites, document.Totals.Cases, document.Totals.Failed, redacted(document))
	return nil
}

// runCheck is the gate over a filed artifact.
func runCheck(args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("check", flag.ContinueOnError)
	set.SetOutput(stderr)
	in := set.String("in", "", "the document to check, or - for the standard input")
	if err := set.Parse(args); err != nil {
		return errUsage
	}
	if set.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("%w: check takes no positional argument, got %q", errUsage, set.Arg(0))
	}

	source, closeSource, err := openInput(*in)
	if err != nil {
		return err
	}
	defer closeSource()
	raw, err := io.ReadAll(source)
	if err != nil {
		return fmt.Errorf("read the document: %w", err)
	}

	document, err := ParseDocument(raw)
	if err != nil {
		return err
	}
	violations := Violations(document)
	if len(violations) != 0 {
		encoded, marshalErr := json.Marshal(violations)
		if marshalErr != nil {
			return fmt.Errorf("render the refusals: %w", marshalErr)
		}
		fmt.Fprintln(stderr, string(encoded))
		return fmt.Errorf("the document is refused by %d rule(s)", len(violations))
	}
	fmt.Fprintf(stdout, "evidence: %s is readable against schema %d: %d suites, %d cases, %d failed, %d redactions\n",
		document.Commit, document.Schema, document.Totals.Suites,
		document.Totals.Cases, document.Totals.Failed, redacted(document))
	return nil
}

// runRules prints both vocabularies: the rules of the format and the rules of
// the detector the redaction uses. It is what a reader of a refusal looks up,
// and what a test of this package compares a fixture against.
func runRules(stdout io.Writer) error {
	payload := struct {
		Rules    []string `json:"rules"`
		Detector []string `json:"detector_rules"`
		Kinds    []string `json:"kinds"`
		Statuses []string `json:"statuses"`
	}{FormatRules(), DetectorRules(), Kinds(), Statuses()}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}

// write puts the document where the caller asked for it, and the JUnit artifact
// when one was asked for.
func (d Document) write(outPath, junitPath string) error {
	encoded, err := d.JSON()
	if err != nil {
		return fmt.Errorf("render the document: %w", err)
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
			return fmt.Errorf("write the document: %w", err)
		}
	}
	if junitPath == "" {
		return nil
	}
	report, err := d.JUnit()
	if err != nil {
		return fmt.Errorf("render the JUnit artifact: %w", err)
	}
	if err := os.WriteFile(junitPath, report, 0o644); err != nil {
		return fmt.Errorf("write the JUnit artifact: %w", err)
	}
	return nil
}

// openInput answers a reader for a path, with `-` meaning the standard input.
func openInput(path string) (io.Reader, func(), error) {
	if path == "" {
		return nil, func() {}, fmt.Errorf("%w: -in is required", errUsage)
	}
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open %s: %w", path, err)
	}
	return file, func() { _ = file.Close() }, nil
}

// redacted answers how many values the redaction replaced.
func redacted(document Document) int {
	total := 0
	for _, entry := range document.Redactions {
		total += entry.Count
	}
	return total
}
