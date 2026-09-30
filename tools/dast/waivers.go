package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Waiver triages one finding without deleting it: the rule and path it
// names, who owns the risk, why the answer is correct, and when the
// triage expires. An expired, ownerless, reasonless or unknown-rule
// waiver refuses the whole run before any request leaves: a waiver that
// excuses nothing is how a finding gets buried, and the tool refuses to
// be the shovel. A waiver that matched nothing is reported stale after
// the scan instead, because only the findings tell whether the risk
// moved.
type Waiver struct {
	Rule    string `json:"rule"`
	Path    string `json:"path"`
	Reason  string `json:"reason"`
	Owner   string `json:"owner"`
	Expires string `json:"expires"`
}

// knownRules is the closed rule set a waiver may name: it mirrors the
// probe constants, so a waiver for a rule nobody checks fails loudly.
var knownRules = map[string]bool{
	RuleNo5xx: true, RuleNoReflection: true, RuleAuthBypass: true,
	RuleUnexpectedWrite: true, RuleOpenRedirect: true,
	RuleSecurityHeaders: true, RuleCookieFlags: true,
}

// loadWaivers reads the register. A missing file means no waivers, which
// is the default: an empty register excuses nothing.
func loadWaivers(path string) ([]Waiver, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read waivers: %w", err)
	}
	var waivers []Waiver
	if err := json.Unmarshal(raw, &waivers); err != nil {
		return nil, fmt.Errorf("decode waivers: %w", err)
	}
	now := time.Now().UTC()
	for index, waiver := range waivers {
		if !knownRules[waiver.Rule] {
			return nil, fmt.Errorf("waiver %d names unknown rule %q", index, waiver.Rule)
		}
		if waiver.Path == "" || waiver.Reason == "" || waiver.Owner == "" {
			return nil, fmt.Errorf("waiver %d (%s %s) needs path, reason and owner", index, waiver.Rule, waiver.Path)
		}
		expires, err := time.Parse("2006-01-02", waiver.Expires)
		if err != nil {
			return nil, fmt.Errorf("waiver %d (%s %s) has no parseable expiry YYYY-MM-DD: %q", index, waiver.Rule, waiver.Path, waiver.Expires)
		}
		if !expires.After(now) {
			return nil, fmt.Errorf("waiver %d (%s %s) expired on %s", index, waiver.Rule, waiver.Path, waiver.Expires)
		}
	}
	return waivers, nil
}

// applyWaivers marks the findings a valid waiver names and reports the
// register itself: a waiver that matched nothing is stale, and stale is
// a violation, because the finding it used to excuse is either fixed
// (remove the waiver) or renamed (rename the waiver).
func applyWaivers(report *Report, waivers []Waiver) {
	used := make([]bool, len(waivers))
	for index := range report.Findings {
		finding := &report.Findings[index]
		for waiverIndex, waiver := range waivers {
			if waiver.Rule == finding.Rule && waiver.Path == finding.Path {
				finding.Waived = true
				used[waiverIndex] = true
			}
		}
	}
	for waiverIndex, waiver := range waivers {
		if !used[waiverIndex] {
			report.Findings = append(report.Findings, Finding{
				Severity: SeverityMedium,
				Rule:     "stale-waiver",
				Path:     waiver.Path,
				Detail:   fmt.Sprintf("waiver for rule %q matched no finding: remove it or rename it", waiver.Rule),
			})
		}
	}
}
