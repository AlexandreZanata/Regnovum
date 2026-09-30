// Package qualitydecide is the fail-closed release certification
// decisor of P30-T05: it reads one verified evidence bundle and emits
// PASS or FAIL with stable reasons, and nothing else.
//
// The decisor reads only the bundle directory: the manifest, its seal,
// every listed artifact and the run-result evidence files. Tiers come
// from the bundled tiers declaration, waivers from the bundled waiver
// register. No manual input exists, no environment variable or flag can
// flip the verdict, and no log text is ever parsed — the flags name the
// bundle, never the decision.
//
// A FAIL names every reason in a fixed vocabulary: bundle-corrupt (the
// seal or an artifact hash does not hold), missing-field (the bundle
// does not describe itself), dirty-tree (the manifest records an
// unclean tree), evidence-mismatch (an evidence file does not belong to
// the manifest commit or is malformed), q0-missing and q0-failed (a
// required gate has no passing verdict), waiver-forbidden (a critical
// waiver in one of the eight areas the phase refuses to trade) and
// waiver-expired. Only an empty reason set is a PASS.
//
// The register's deeper validation (unknown rules, understated risk,
// unknown findings) belongs to the waiver gate, which runs long before
// any bundle is built: the decisor judges the declared risk and scope
// at face value. Test waivers (skipped tests) belong to the test-quality
// gate, not to this register.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Decision vocabulary. PASS and FAIL are the only verdicts.
const (
	DecisionPass = "PASS"
	DecisionFail = "FAIL"
)

// Reason codes are the stable vocabulary of a FAIL, in the order the
// decisor reports them.
const (
	ReasonBundleCorrupt    = "bundle-corrupt"
	ReasonMissingField     = "missing-field"
	ReasonDirtyTree        = "dirty-tree"
	ReasonEvidenceMismatch = "evidence-mismatch"
	ReasonQ0Missing        = "q0-missing"
	ReasonQ0Failed         = "q0-failed"
	ReasonWaiverForbidden  = "waiver-forbidden"
	ReasonWaiverExpired    = "waiver-expired"
)

// Decision is the machine-readable verdict.
type Decision struct {
	Decision string   `json:"decision"`
	Commit   string   `json:"commit"`
	Reasons  []string `json:"reasons"`
}

// ForbiddenCategories are the areas where a critical (Q0) waiver blocks
// certification, mirroring the waiver policy: the eight areas the phase
// refuses to trade, whatever the compensation.
var ForbiddenCategories = []string{
	"authorization",
	"auditability",
	"data-loss",
	"ink",
	"money",
	"privacy",
	"security",
	"webhook",
}

// manifest mirrors the bundle descriptor written by tools/qualitymanifest.
type manifest struct {
	Schema     int               `json:"schema"`
	Commit     string            `json:"commit"`
	Clean      bool              `json:"clean"`
	Toolchains map[string]string `json:"toolchains"`
	Artifacts  []artifact        `json:"artifacts"`
	Evidence   []artifact        `json:"evidence"`
}

type artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// evidenceVerdict is one run result: which gate it judges, whether it
// passed, and which commit it measured.
type evidenceVerdict struct {
	Gate    string `json:"gate"`
	Verdict string `json:"verdict"`
	Commit  string `json:"commit"`
}

// waiver mirrors one entry of the waiver register: the declared risk,
// scope and expiry, which is what certification judges.
type waiver struct {
	ID         string   `json:"id"`
	Risk       string   `json:"risk"`
	Categories []string `json:"categories"`
	Expires    string   `json:"expires"`
}

// Decide judges the bundle at bundleDir on the instant now and returns
// the verdict. now travels as a parameter so expiry is testable without
// freezing the decision to any clock: production passes the wall clock,
// tests pass fixed instants.
func Decide(bundleDir string, now time.Time) Decision {
	decision := Decision{Commit: "", Reasons: []string{}}
	fail := func(reason string) {
		decision.Reasons = append(decision.Reasons, reason)
	}

	manifest, ok := loadVerifiedManifest(bundleDir, &decision, fail)
	if !ok {
		return finalize(decision)
	}
	decision.Commit = manifest.Commit

	tiers := artifactBytes(bundleDir, manifest, "tiers")
	required := requiredLeaves(tiers, fail)
	verdicts := collectVerdicts(bundleDir, manifest, fail)
	for _, gate := range required {
		verdict, ok := verdicts[gate]
		if !ok {
			fail(ReasonQ0Missing + ":" + gate)
			continue
		}
		if verdict != "pass" {
			fail(ReasonQ0Failed + ":" + gate)
		}
	}

	judgeWaivers(artifactBytes(bundleDir, manifest, "waivers"), now, fail)
	return finalize(decision)
}

func finalize(decision Decision) Decision {
	sort.Strings(decision.Reasons)
	if len(decision.Reasons) == 0 {
		decision.Decision = DecisionPass
		decision.Reasons = []string{}
	} else {
		decision.Decision = DecisionFail
	}
	return decision
}

// loadVerifiedManifest reads the manifest only when its seal holds and
// the envelope is complete. Anything else fails closed: an unverified
// bundle contributes no evidence.
func loadVerifiedManifest(bundleDir string, decision *Decision, fail func(string)) (manifest, bool) {
	var empty manifest
	encoded, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		fail(ReasonBundleCorrupt + ":manifest-unreadable")
		return empty, false
	}
	sealed, err := os.ReadFile(filepath.Join(bundleDir, "manifest.sha256"))
	if err != nil {
		fail(ReasonBundleCorrupt + ":seal-unreadable")
		return empty, false
	}
	sum := sha256.Sum256(encoded)
	if strings.TrimSpace(string(sealed)) != hex.EncodeToString(sum[:]) {
		fail(ReasonBundleCorrupt + ":seal-mismatch")
		return empty, false
	}
	var described manifest
	if err := json.Unmarshal(encoded, &described); err != nil {
		fail(ReasonBundleCorrupt + ":manifest-malformed")
		return empty, false
	}
	if described.Schema != 1 {
		fail(ReasonMissingField + ":schema")
		return empty, false
	}
	if strings.TrimSpace(described.Commit) == "" {
		fail(ReasonMissingField + ":commit")
		return empty, false
	}
	if !described.Clean {
		fail(ReasonDirtyTree)
		return empty, false
	}
	decision.Commit = described.Commit
	for _, entry := range append(append([]artifact{}, described.Artifacts...), described.Evidence...) {
		content, err := os.ReadFile(filepath.Join(bundleDir, entry.Path))
		if err != nil {
			fail(ReasonEvidenceMismatch + ":unreadable:" + entry.Path)
			return empty, false
		}
		actual := sha256.Sum256(content)
		if hex.EncodeToString(actual[:]) != entry.SHA256 {
			fail(ReasonEvidenceMismatch + ":changed:" + entry.Path)
			return empty, false
		}
	}
	return described, true
}

// artifactBytes returns the verified bytes of a named manifest artifact,
// or nil when the manifest does not list it.
func artifactBytes(bundleDir string, described manifest, name string) []byte {
	for _, entry := range append(append([]artifact{}, described.Artifacts...), described.Evidence...) {
		if entry.Name == name {
			content, err := os.ReadFile(filepath.Join(bundleDir, entry.Path))
			if err != nil {
				return nil
			}
			return content
		}
	}
	return nil
}

// tiersDocument mirrors the tier declaration for required-gate expansion.
type tiersDocument struct {
	Schema int `json:"schema"`
	Tiers  map[string]struct {
		Gates []string `json:"gates"`
	} `json:"tiers"`
}

// requiredLeaves expands the tier compositions to the leaf gates the
// evidence must cover: tier references resolve transitively, every other
// name is a leaf the bundle must carry a verdict for.
func requiredLeaves(raw []byte, fail func(string)) []string {
	var document tiersDocument
	if err := json.Unmarshal(raw, &document); err != nil || document.Schema != 1 {
		fail(ReasonMissingField + ":tiers")
		return nil
	}
	leaves := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		// Tier references carry the quality- prefix of the Makefile
		// targets while tier keys do not; anything else is a leaf gate
		// the evidence must cover directly.
		tier, isTier := document.Tiers[strings.TrimPrefix(name, "quality-")]
		if !isTier {
			leaves[name] = true
			return
		}
		for _, gate := range tier.Gates {
			visit(gate)
		}
	}
	for name := range document.Tiers {
		visit(name)
	}
	ordered := make([]string, 0, len(leaves))
	for leaf := range leaves {
		ordered = append(ordered, leaf)
	}
	sort.Strings(ordered)
	return ordered
}

// collectVerdicts reads every evidence file: each must be a well-formed
// verdict for the manifest commit. Unknown gates are inert — the decisor
// judges required gates, and extra evidence changes no verdict — but a
// malformed file or a foreign commit fails the bundle it came in.
func collectVerdicts(bundleDir string, described manifest, fail func(string)) map[string]string {
	verdicts := map[string]string{}
	for _, entry := range described.Evidence {
		content, err := os.ReadFile(filepath.Join(bundleDir, entry.Path))
		if err != nil {
			fail(ReasonEvidenceMismatch + ":unreadable:" + entry.Path)
			continue
		}
		var verdict evidenceVerdict
		if err := json.Unmarshal(content, &verdict); err != nil || strings.TrimSpace(verdict.Gate) == "" {
			fail(ReasonEvidenceMismatch + ":malformed:" + entry.Path)
			continue
		}
		if verdict.Commit != described.Commit {
			fail(ReasonEvidenceMismatch + ":foreign-commit:" + entry.Path)
			continue
		}
		if verdict.Verdict != "pass" && verdict.Verdict != "fail" {
			fail(ReasonEvidenceMismatch + ":malformed:" + entry.Path)
			continue
		}
		if _, seen := verdicts[verdict.Gate]; !seen {
			verdicts[verdict.Gate] = verdict.Verdict
		}
	}
	return verdicts
}

// judgeWaivers refuses critical waivers in forbidden areas and expired
// waivers by the calendar date: an exception nobody can read, or one
// whose day passed, cannot certify anything.
func judgeWaivers(raw []byte, now time.Time, fail func(string)) {
	if raw == nil {
		fail(ReasonMissingField + ":waivers")
		return
	}
	var register struct {
		Waivers []waiver `json:"waivers"`
	}
	if err := json.Unmarshal(raw, &register); err != nil {
		fail(ReasonMissingField + ":waivers-malformed")
		return
	}
	today := now.UTC().Format("2006-01-02")
	for _, entry := range register.Waivers {
		judgeWaiver(entry, today, fail)
	}
}

// judgeWaiver judges one register entry: readable fields, a valid date,
// no critical waiver in a forbidden area, and no passed expiry.
func judgeWaiver(entry waiver, today string, fail func(string)) {
	if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Risk) == "" || strings.TrimSpace(entry.Expires) == "" {
		fail(ReasonMissingField + ":waiver-malformed")
		return
	}
	if _, err := time.Parse("2006-01-02", entry.Expires); err != nil {
		fail(ReasonMissingField + ":waiver-date:" + entry.ID)
		return
	}
	if entry.Risk == "Q0" && tradesForbiddenArea(entry.Categories) {
		fail(ReasonWaiverForbidden + ":" + entry.ID)
	}
	if entry.Expires < today {
		fail(ReasonWaiverExpired + ":" + entry.ID)
	}
}

// tradesForbiddenArea reports whether any declared scope is one of the
// eight areas the phase refuses to trade at the critical class.
func tradesForbiddenArea(categories []string) bool {
	for _, category := range categories {
		for _, forbidden := range ForbiddenCategories {
			if category == forbidden {
				return true
			}
		}
	}
	return false
}
