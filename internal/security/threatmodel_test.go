package security_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var threatIDPattern = regexp.MustCompile(`\*\*(THR-[A-Z0-9-]+)\*\*`)
var matrixRowPattern = regexp.MustCompile(`^\| (THR-[A-Z0-9-]+) \| (Crítica|Alta|Média) \| (test|procedure): `)

func TestThreatModelHasExecutableEvidenceForEveryThreat(t *testing.T) {
	modelIDs := readThreatIDs(t, "../../docs/THREAT_MODEL.md")
	matrixIDs, evidence := readMatrix(t, "../../docs/THREAT_MODEL_TEST_MATRIX.md")

	if got, want := strings.Join(modelIDs, "\n"), strings.Join(matrixIDs, "\n"); got != want {
		t.Fatalf("threat IDs differ between model and matrix\nmodel:\n%s\nmatrix:\n%s", got, want)
	}

	for id, entry := range evidence {
		if entry.severity == "Crítica" && entry.kind != "test" {
			t.Errorf("critical threat %s must have automated test evidence, got %s", id, entry.kind)
		}
		if entry.kind == "test" {
			if len(entry.targets) == 0 {
				t.Errorf("threat %s declares test evidence with no target", id)
			}
			for _, target := range entry.targets {
				assertEvidenceTarget(t, id, target)
			}
			continue
		}
		// A procedure is evidence only with a review date in the future:
		// an unautomatable risk blocks certification once its triage
		// expires, and a procedure nobody re-checks is a rumor.
		if entry.reviewBy == "" {
			t.Errorf("threat %s has procedure evidence without review-by YYYY-MM-DD", id)
			continue
		}
		review, err := time.Parse("2006-01-02", entry.reviewBy)
		if err != nil {
			t.Errorf("threat %s has unparseable review-by %q", id, entry.reviewBy)
			continue
		}
		if !review.After(time.Now()) {
			t.Errorf("threat %s procedure review expired on %s", id, entry.reviewBy)
		}
	}
}

func readThreatIDs(t *testing.T, path string) []string {
	t.Helper()
	contents := readFile(t, path)
	seen := make(map[string]bool)
	var ids []string
	for _, match := range threatIDPattern.FindAllStringSubmatch(contents, -1) {
		id := match[1]
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

type matrixEvidence struct {
	severity string
	kind     string
	targets  []string
	reviewBy string
}

var evidenceTargetPattern = regexp.MustCompile("`([^`]+)`")
var reviewByPattern = regexp.MustCompile(`review-by (\d{4}-\d{2}-\d{2})`)

func readMatrix(t *testing.T, path string) ([]string, map[string]matrixEvidence) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open threat matrix: %v", err)
	}
	defer file.Close()

	seen := make(map[string]bool)
	evidence := make(map[string]matrixEvidence)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		matches := matrixRowPattern.FindStringSubmatch(scanner.Text())
		if len(matches) == 0 {
			continue
		}
		id := matches[1]
		if seen[id] {
			t.Fatalf("threat %s appears more than once in the matrix", id)
		}
		seen[id] = true
		entry := matrixEvidence{severity: matches[2], kind: matches[3]}
		for _, target := range evidenceTargetPattern.FindAllStringSubmatch(scanner.Text(), -1) {
			// Only path-shaped spans resolve: prose code spans are not
			// evidence references.
			if strings.Contains(target[1], "/") {
				entry.targets = append(entry.targets, target[1])
			}
		}
		if review := reviewByPattern.FindStringSubmatch(scanner.Text()); review != nil {
			entry.reviewBy = review[1]
		}
		evidence[id] = entry
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read threat matrix: %v", err)
	}

	ids := make([]string, 0, len(evidence))
	for id := range evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, evidence
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

// assertEvidenceTarget proves the evidence resolves: test targets name a
// file or package directory that exists, so adding a THR with a dangling
// reference fails the gate instead of passing in silence. Paths resolve
// from the repository root, like the matrix documents them.
func assertEvidenceTarget(t *testing.T, id, target string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join("..", "..", target)); err != nil {
		t.Errorf("threat %s names missing evidence %q", id, target)
	}
}
