// Command qualityinvariants is the backend invariant matrix (P24-T01).
//
// It reads the versioned matrix in quality/invariants.json and judges every
// claim it makes against the checkout: the catalog rule each row cites, the
// packages it names, and the tests it says prove the valid, invalid, limit
// and replay cases. It then asks the question a matrix has to answer — is
// this the whole matrix? — by requiring every Q0/Q1 catalog rule to be
// linked from at least one row, every one of the twelve business modules to
// appear, and every persisted transition to carry all four cases with an
// unreachable proof per module. Removing a row therefore fails coverage
// instead of passing in silence. It never writes and never runs a test.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

// SchemaVersion is the only matrix version the loader understands.
const SchemaVersion = 1

// InvariantsPath is where the matrix lives, relative to the repository root.
const InvariantsPath = "quality/invariants.json"

// CatalogPath is where the rule catalog lives, relative to the root.
const CatalogPath = "quality/catalog.json"

// RequiredModules are the twelve business modules the phase names. The file
// may name adjacent platform modules for full Q0/Q1 linkage, but it must
// name every one of these.
var RequiredModules = []string{
	"identity", "profiles", "wallet", "entitlements",
	"arenas", "positions", "arguments", "persuasion",
	"billing", "moderation", "transparency", "jobs",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with its status as a value: the contract of a
// gate is about exit statuses, and a contract that only lives inside main is
// one no test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("qualityinvariants", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the matrix is judged against")
	matrixPath := flags.String("matrix", InvariantsPath, "matrix to judge, relative to the root")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	matrix, violations := ReadMatrix(*root, *matrixPath)
	if len(violations) == 0 {
		violations = Check(*root, matrix)
		if len(violations) == 0 {
			fmt.Fprintf(stdout, "qualityinvariants: %d invariant(s) in %d module(s) linked to the catalog, %d persisted with four cases\n",
				len(matrix.Invariants), len(matrix.Modules), countPersisted(matrix))
			return exitOK
		}
	}
	for _, violation := range violations {
		fmt.Fprintf(stderr, "qualityinvariants: %s\n", violation)
	}
	fmt.Fprintf(stderr, "qualityinvariants: %d violation(s) — a transition without four cases is a promise the tests do not keep\n", len(violations))
	return exitViolation
}

func countPersisted(matrix Matrix) int {
	count := 0
	for _, inv := range matrix.Invariants {
		if inv.Persisted {
			count++
		}
	}
	return count
}
