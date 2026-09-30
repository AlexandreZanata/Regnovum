package main

import (
	"encoding/json"
	"os"
	"strings"
)

// catalogRisks indexes the rule catalog by module, so target risks are
// checked against the catalog instead of restated beside it. The lookup
// answers the same question as tools/mutationaudit with the phase's wider
// vocabulary (Q2 joined Q0/Q1 here).
func catalogRisks(root string) (map[string]map[string]bool, []string) {
	byModule := map[string]map[string]bool{}
	data, readErr := os.ReadFile(root + "/" + CatalogPath)
	if readErr != nil {
		return byModule, []string{"catalog-unreadable: " + CatalogPath + ": " + readErr.Error()}
	}
	var catalog struct {
		Rules []struct {
			Modules []string `json:"modules"`
			Risk    string   `json:"risk"`
		} `json:"rules"`
	}
	if decodeErr := json.Unmarshal(data, &catalog); decodeErr != nil {
		return byModule, []string{"catalog-malformed: " + CatalogPath + ": " + decodeErr.Error()}
	}
	for _, rule := range catalog.Rules {
		for _, module := range rule.Modules {
			held, ok := byModule[module]
			if !ok {
				held = map[string]bool{}
				byModule[module] = held
			}
			held[rule.Risk] = true
		}
	}
	return byModule, nil
}

// packageModule maps internal/<module>/{domain,application} to its module.
// Anything else is outside the judged population.
func packageModule(packageName string) string {
	fields := strings.Split(packageName, "/")
	if len(fields) != 3 {
		return ""
	}
	if fields[0] != "internal" {
		return ""
	}
	if fields[2] != "domain" && fields[2] != "application" {
		return ""
	}
	return fields[0] + "/" + fields[1]
}

// hasGoFiles tells whether a directory ships delivered Go sources
// (test files alone do not count as delivery).
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		return true
	}
	return false
}

// maxThreshold answers the highest nominal floor across a target's risks:
// a Q0/Q1 package owes the Q0 bar, a Q1/Q2 package the Q1 bar.
func maxThreshold(risks []string, thresholds map[string]float64) float64 {
	peak := 0.0
	for _, risk := range risks {
		value, ok := thresholds[risk]
		if !ok {
			continue
		}
		if value > peak {
			peak = value
		}
	}
	return peak
}
