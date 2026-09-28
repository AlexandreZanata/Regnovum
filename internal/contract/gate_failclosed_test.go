package contract_test

// P30-T04 — every quality gate fails closed: quality/gate-controls.json
// inventories one positive and one negative control per violation class,
// and these metatests prove the inventory holds plus execute the negative
// controls that run without PostgreSQL, long runs or mutants.
//
// An executed control runs as a subprocess `go test <package> -run
// <test>`: exit zero with a PASS line means the artificial defect did not
// pass. If a gate ever stops refusing, its control fails here first. The
// inventory-only classes (database, long or mutant-bound) prove file,
// symbol and fixture wiring; their efficacy is what their own suites
// prove in CI.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type gateControl struct {
	File string `json:"file"`
	Test string `json:"test"`
}

type gateEntry struct {
	Class    string      `json:"class"`
	Gate     string      `json:"gate"`
	Mode     string      `json:"mode"`
	Package  string      `json:"package"`
	Test     string      `json:"test"`
	Positive gateControl `json:"positive"`
	Negative gateControl `json:"negative"`
	Fixture  string      `json:"fixture"`
}

type gateControls struct {
	Schema int         `json:"schema"`
	Gates  []gateEntry `json:"gates"`
}

func loadGateControls(t *testing.T) (string, gateControls) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "quality", "gate-controls.json"))
	if err != nil {
		t.Fatalf("read gate controls: %v", err)
	}
	var controls gateControls
	if err := json.Unmarshal(raw, &controls); err != nil {
		t.Fatalf("gate controls are not JSON: %v", err)
	}
	return root, controls
}

func testSymbol(t *testing.T, root, file, symbol string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatalf("%s unreadable: %v", file, err)
	}
	if !strings.Contains(string(raw), "func "+symbol+"(") {
		t.Fatalf("%s holds no test %s", file, symbol)
	}
}

// TestGateControlsInventory proves every violation class names a gate
// with a positive and a negative control that exist, and every fixture
// stays test-only: under testdata/, inside a _test.go file, or absent
// (inline tables, which are test-only by location). It also proves no
// production file references a fixture directory, so fixtures can never
// ship in the binary or runtime.
func TestGateControlsInventory(t *testing.T) {
	t.Parallel()

	root, controls := loadGateControls(t)
	if controls.Schema != 1 {
		t.Fatalf("gate controls schema = %d, want 1", controls.Schema)
	}
	wantClasses := []string{"architectural", "business-rule", "sql-drift", "auth", "secret", "vulnerability", "flake", "mutant", "contract", "performance", "truncated-evidence"}
	if len(controls.Gates) != len(wantClasses) {
		t.Fatalf("gates = %d, want the %d violation classes", len(controls.Gates), len(wantClasses))
	}
	seen := map[string]bool{}
	for _, entry := range controls.Gates {
		seen[entry.Class] = true
		if entry.Mode != "execute" && entry.Mode != "inventory" {
			t.Errorf("%s mode = %q, want execute or inventory", entry.Class, entry.Mode)
		}
		if strings.TrimSpace(entry.Gate) == "" {
			t.Errorf("%s names no gate", entry.Class)
		}
		testSymbol(t, root, entry.Positive.File, entry.Positive.Test)
		testSymbol(t, root, entry.Negative.File, entry.Negative.Test)
		if entry.Fixture != "" {
			if !strings.Contains(entry.Fixture, "testdata") {
				t.Errorf("%s fixture %q is not test-only", entry.Class, entry.Fixture)
			}
			info, err := os.Stat(filepath.Join(root, entry.Fixture))
			if err != nil || !info.IsDir() {
				t.Errorf("%s fixture %q does not exist", entry.Class, entry.Fixture)
			}
		}
	}
	for _, class := range wantClasses {
		if !seen[class] {
			t.Errorf("violation class %q missing from the inventory", class)
		}
	}

	// Fixture directories must be invisible to production code, with two
	// reviewed exceptions: the compat baseline is a versioned contract
	// snapshot that production reads by design, and the dependency gate
	// names its proof directory to prove its own rules at runtime. Both
	// are data paths, never defect fixtures, and any new reference fails
	// here for review — the same shape as the architecture gate's
	// adapterImportExceptions.
	allowedReferences := map[string]string{
		"internal/contract/compat.go:internal/contract/testdata":       "versioned contract snapshot read by CheckBaselinePin",
		"tools/dependencyaudit/main.go:tools/dependencyaudit/testdata": "gate self-proof directory named by the gate itself",
	}
	fixtureDirs := map[string]bool{}
	for _, entry := range controls.Gates {
		if entry.Fixture != "" {
			fixtureDirs[entry.Fixture] = true
		}
	}
	if len(fixtureDirs) == 0 {
		t.Fatal("no fixture directories declared; the test-only proof is vacuous")
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for dir := range fixtureDirs {
			if !strings.Contains(string(raw), dir) {
				continue
			}
			if reason, allowed := allowedReferences[rel+":"+dir]; allowed {
				t.Logf("allowed reference %s (%s)", rel+":"+dir, reason)
				continue
			}
			t.Errorf("production file %s references fixture dir %s", rel, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// runNegativeControl executes one negative control as a subprocess and
// reports whether the artificial defect failed to pass: exit zero plus a
// PASS line for the named test. Anything else — including a filter that
// matched nothing — is a failure of the metatest, never a pass.
func runNegativeControl(root, pkg, test string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", pkg, "-run", test, "-count=1", "-v")
	command.Dir = root
	out, err := command.CombinedOutput()
	output := string(out)
	if err != nil {
		return output, false
	}
	if strings.Contains(output, "no tests to run") || !strings.Contains(output, "PASS: "+test) {
		return output, false
	}
	return output, true
}

// TestArtificialDefectsFail executes every negative control the inventory
// marks executable: each artificial defect must fail to pass, or the
// metatest names the gate that went blind.
func TestArtificialDefectsFail(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	_, controls := loadGateControls(t)

	if _, err := exec.LookPath("go"); err != nil {
		t.Fatal("go toolchain not found: negative controls cannot execute")
	}
	for _, entry := range controls.Gates {
		if entry.Mode != "execute" {
			continue
		}
		t.Run(entry.Class, func(t *testing.T) {
			t.Parallel()

			output, ok := runNegativeControl(root, entry.Package, entry.Test)
			if !ok {
				t.Fatalf("gate %q let the defect pass (or never ran it):\n%s", entry.Gate, output)
			}
		})
	}
}

// TestNegativeControlDetectorFailsClosed proves the detector itself: a
// filter matching nothing is reported as a failure, so a renamed control
// can never pass silently.
func TestNegativeControlDetectorFailsClosed(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatal("go toolchain not found: negative controls cannot execute")
	}
	if output, ok := runNegativeControl(root, "./tools/evidence/", "TestNoSuchControlZZZ"); ok {
		t.Fatalf("detector passed a filter matching nothing:\n%s", output)
	}
}
