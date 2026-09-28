package contract_test

// P30-T01 — the canonical autonomous quality commands mirror their
// manifest: quality/tiers.json declares the contents and cadence of each
// quality-* Makefile tier, and this test proves the targets implement
// exactly that document. A tier naming a missing gate fails here instead
// of failing silently at run time, and the fast tier can never stand in
// for certification because its gates are a strict subset of certify's.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

type qualityTier struct {
	Cadence string   `json:"cadence"`
	Enabled bool     `json:"enabled"`
	Gates   []string `json:"gates"`
}

type qualityTiers struct {
	Schema int                    `json:"schema"`
	Tiers  map[string]qualityTier `json:"tiers"`
}

var (
	tierTargetPattern = regexp.MustCompile(`(?m)^quality-(fast|main|nightly|weekly|certify):\s*(.*)$`)
	makeTargetPattern = regexp.MustCompile(`(?m)^([a-z][\w-]*):\s*(.*)$`)
)

func loadTiers(t *testing.T) (string, qualityTiers) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))

	raw, err := os.ReadFile(filepath.Join(root, "quality", "tiers.json"))
	if err != nil {
		t.Fatalf("read tiers manifest: %v", err)
	}
	var manifest qualityTiers
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("tiers manifest is not JSON: %v", err)
	}
	return root, manifest
}

func makeTargets(t *testing.T, root string) (map[string][]string, map[string]bool, map[string][]string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	tierPrerequisites := map[string][]string{}
	for _, match := range tierTargetPattern.FindAllStringSubmatch(string(raw), -1) {
		tierPrerequisites[match[1]] = strings.Fields(match[2])
	}
	prerequisites := map[string][]string{}
	existing := map[string]bool{}
	for _, match := range makeTargetPattern.FindAllStringSubmatch(string(raw), -1) {
		existing[match[1]] = true
		if match[2] != "" {
			prerequisites[match[1]] = strings.Fields(match[2])
		}
	}
	for tier, gates := range tierPrerequisites {
		prerequisites["quality-"+tier] = gates
	}
	return tierPrerequisites, existing, prerequisites
}

// TestQualityTiersMirrorTheirManifest proves the five canonical targets
// implement exactly the manifest: same tiers, same gates, every gate a
// real target, every tier with a cadence.
func TestQualityTiersMirrorTheirManifest(t *testing.T) {
	t.Parallel()

	root, manifest := loadTiers(t)
	if manifest.Schema != 1 {
		t.Fatalf("tiers schema = %d, want 1", manifest.Schema)
	}
	prerequisites, existing, _ := makeTargets(t, root)

	for _, tier := range []string{"fast", "main", "nightly", "weekly", "certify"} {
		declared, ok := manifest.Tiers[tier]
		if !ok {
			t.Fatalf("tier %q missing from the manifest", tier)
		}
		if strings.TrimSpace(declared.Cadence) == "" {
			t.Errorf("tier %q declares no cadence", tier)
		}
		got, ok := prerequisites[tier]
		if !ok {
			t.Fatalf("Makefile has no quality-%s target", tier)
		}
		if len(got) != len(declared.Gates) {
			t.Fatalf("quality-%s prerequisites = %v, manifest gates = %v", tier, got, declared.Gates)
		}
		want := map[string]bool{}
		for _, gate := range declared.Gates {
			want[gate] = true
		}
		for _, gate := range got {
			if !want[gate] {
				t.Errorf("quality-%s runs %q, missing from the manifest", tier, gate)
			}
			if existing[gate] {
				continue
			}
			if _, isTier := manifest.Tiers[strings.TrimPrefix(gate, "quality-")]; !isTier {
				t.Errorf("quality-%s names %q, which implements nothing", tier, gate)
			}
		}
	}
}

// TestFastRunNeverSubstitutesCertification proves the cadence promise by
// closure: every fast gate must also run under certify, and certify must
// run strictly more.
func TestFastRunNeverSubstitutesCertification(t *testing.T) {
	t.Parallel()

	root, manifest := loadTiers(t)
	_, _, prerequisites := makeTargets(t, root)

	// closure expands the Makefile graph, not just the manifest tiers:
	// verify and quick-verify are opaque gate names in tiers.json but
	// real compositions in the Makefile, and the promise is about every
	// gate that actually runs.
	closure := func(tier string) map[string]bool {
		seen := map[string]bool{}
		var visit func(name string)
		visit = func(name string) {
			children, composed := prerequisites[name]
			if name == "quality-"+tier {
				if tierDocument, ok := manifest.Tiers[tier]; ok {
					children, composed = tierDocument.Gates, true
				}
			}
			if !composed {
				seen[name] = true
				return
			}
			for _, gate := range children {
				if seen[gate] {
					continue
				}
				seen[gate] = true
				visit(gate)
			}
		}
		visit("quality-" + tier)
		return seen
	}

	fast, certify := closure("fast"), closure("certify")
	for gate := range fast {
		if !certify[gate] {
			t.Errorf("fast gate %q escapes certification", gate)
		}
	}
	if len(certify) <= len(fast) {
		t.Fatalf("certify runs %d gates vs fast %d: certification must be strictly broader", len(certify), len(fast))
	}
}
