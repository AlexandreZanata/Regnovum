// Tests of the contractgen command line (P18-T02): -check must detect drift,
// writing must be idempotent, and invalid invocations must fail loudly.
package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tinyContract = `{"components":{"schemas":{"A":{"type":"object","properties":{"x":{"type":"string"}}}}}}`

func TestRunWritesChecksAndDetectsDrift(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	contractPath := filepath.Join(directory, "openapi.json")
	targetPath := filepath.Join(directory, "contracts", "generated.ts")
	if err := os.WriteFile(contractPath, []byte(tinyContract), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}

	// A missing artifact is drift: -check must fail without writing.
	if err := run([]string{"-check", "-contract", contractPath, "-out", targetPath}, io.Discard); err == nil {
		t.Fatal("run -check on a missing artifact succeeded, want failure")
	}
	if _, err := os.Stat(targetPath); err == nil {
		t.Fatal("-check wrote the artifact; it must never write")
	}

	// Writing creates the file, including its directory.
	var stdout bytes.Buffer
	if err := run([]string{"-contract", contractPath, "-out", targetPath}, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "wrote "+targetPath) {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	written, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}

	// The artifact is now up to date.
	stdout.Reset()
	if err := run([]string{"-check", "-contract", contractPath, "-out", targetPath}, &stdout); err != nil {
		t.Fatalf("run -check: %v", err)
	}
	if !strings.Contains(stdout.String(), "is up to date") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}

	// A second write is a no-op, so `make generate` never rewrites the file.
	stdout.Reset()
	if err := run([]string{"-contract", contractPath, "-out", targetPath}, &stdout); err != nil {
		t.Fatalf("run again: %v", err)
	}
	if !strings.Contains(stdout.String(), "already up to date") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	reloaded, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !bytes.Equal(written, reloaded) {
		t.Fatal("the second run changed the artifact")
	}

	// Drift in the artifact is reported with its path.
	if err := os.WriteFile(targetPath, []byte("// tampered\n"), 0o644); err != nil {
		t.Fatalf("tamper with artifact: %v", err)
	}
	err = run([]string{"-check", "-contract", contractPath, "-out", targetPath}, io.Discard)
	if err == nil {
		t.Fatal("run -check on a drifted artifact succeeded, want failure")
	}
	if !strings.Contains(err.Error(), targetPath) || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestStagedFragmentNamesItsOwnContract(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	contractPath := filepath.Join(directory, "openapi.fragment.json")
	targetPath := filepath.Join(directory, "staged", "module.ts")
	if err := os.WriteFile(contractPath, []byte(tinyContract), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	if err := run([]string{"-contract", contractPath, "-out", targetPath}, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	written, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.Contains(string(written), "from "+contractPath) {
		t.Fatalf("staged artifact does not name its fragment contract:\n%s", written)
	}
	if strings.Contains(string(written), "from api/openapi.json") {
		t.Fatal("staged artifact claims the published contract as its source")
	}
	if err := run([]string{"-check", "-contract", contractPath, "-out", targetPath}, io.Discard); err != nil {
		t.Fatalf("run -check: %v", err)
	}
}

func TestRunRejectsInvalidInvocations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"-nope"}, want: "invalid flags"},
		{name: "unexpected argument", args: []string{"extra"}, want: `unexpected argument "extra"`},
		{name: "missing contract", args: []string{"-contract", filepath.Join(t.TempDir(), "absent.json"), "-out", filepath.Join(t.TempDir(), "out.ts")}, want: "no such file"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := run(testCase.args, io.Discard)
			if err == nil {
				t.Fatal("run succeeded, want failure")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %v does not mention %q", err, testCase.want)
			}
		})
	}
}
