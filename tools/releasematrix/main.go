// Command releasematrix is the release evidence matrix gate
// (P45-T02).
//
// It judges the versioned manifest quality/release-matrix.json
// against the Makefile at the root, and optionally one directory of
// per-gate result artifacts against a frozen candidate SHA. It reads
// and judges; it never writes.
//
// Usage:
//
//	go run ./tools/releasematrix -root . [-manifest quality/release-matrix.json]
//	go run ./tools/releasematrix -root . -results release-evidence/<sha> -commit <sha>
//
// Exit 0 holds every rule, 1 lists each violated rule, 2 is a usage
// error.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	exitOK    = 0
	exitAudit = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("releasematrix", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root holding the Makefile and the manifest")
	manifest := flags.String("manifest", filepath.Join("quality", "release-matrix.json"), "matrix manifest path relative to root")
	results := flags.String("results", "", "directory of per-gate result artifacts to judge")
	commit := flags.String("commit", "", "frozen candidate SHA the results must carry")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "releasematrix takes no positional arguments")
		return exitUsage
	}
	if *results != "" && *commit == "" {
		fmt.Fprintln(stderr, "releasematrix needs -commit with -results")
		return exitUsage
	}
	loaded, findings := LoadManifest(filepath.Join(*root, *manifest))
	if len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintln(stdout, FormatError(finding))
		}
		return exitAudit
	}
	findings = JudgeManifest(*root, loaded)
	if *results != "" {
		findings = append(findings, JudgeResults(loaded, filepath.Join(*root, *results), *commit)...)
	}
	if len(findings) != 0 {
		for _, finding := range sortedUnique(findings) {
			fmt.Fprintln(stdout, FormatError(finding))
		}
		return exitAudit
	}
	fmt.Fprintln(stdout, "releasematrix: matrix holds")
	return exitOK
}
