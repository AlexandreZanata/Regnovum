package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Where a waiver resolves against. The catalog is what states how serious a
// rule is, and the audits are what state that a finding exists to be accepted:
// a waiver that named neither would be a sentence with an id.
const (
	CatalogPath = "quality/catalog.json"

	// AuditDocuments are the registers a waiver's finding may come from. They
	// are listed because "recorded by an audit" has to mean a file, not a
	// feeling — and because the register is what a reader will open when the
	// waiver is reviewed.
	AuditDocuments = "docs/SECURITY_AUDIT.md,docs/PRIVACY_AUDIT.md,docs/I18N_AUDIT.md"
)

// jsonBlock is the machine-readable half of an audit document: the last JSON
// fence in it. The audits keep their findings there, in prose for the reader
// and in JSON for the tool, which is the same arrangement the security and
// privacy gates already read.
var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

// ReadRecords builds the records a waiver is judged against. A missing register
// is a refusal: the judgement would otherwise pass because it had nothing to
// compare with, and a gate that cannot see the rules accepts every waiver.
func ReadRecords(root string) (Records, []Violation) {
	records := Records{Rules: map[string]string{}, Findings: map[string]string{}, Root: root}
	var violations []Violation

	raw, err := readFile(filepath.Join(root, filepath.FromSlash(CatalogPath)))
	if err != nil {
		violations = append(violations, Violation{
			Waiver: "records",
			Code:   codeSourceUnknown,
			Detail: fmt.Sprintf("`%s` is unreadable: without the catalog's own classification a waiver cannot be held to it: %v", CatalogPath, err),
		})
	} else {
		var catalog struct {
			Rules []struct {
				ID   string `json:"id"`
				Risk string `json:"risk"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			violations = append(violations, Violation{
				Waiver: "records",
				Code:   codeSourceUnknown,
				Detail: fmt.Sprintf("`%s` does not decode as a catalog: %v", CatalogPath, err),
			})
		}
		for _, rule := range catalog.Rules {
			records.Rules[rule.ID] = rule.Risk
		}
	}

	for _, path := range strings.Split(AuditDocuments, ",") {
		document, err := readFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			violations = append(violations, Violation{
				Waiver: "records",
				Code:   codeSourceUnknown,
				Detail: fmt.Sprintf("`%s` is unreadable: the findings a waiver may accept are the ones an audit recorded: %v", path, err),
			})
			continue
		}
		blocks := jsonBlock.FindAllSubmatch(document, -1)
		if len(blocks) == 0 {
			violations = append(violations, Violation{
				Waiver: "records",
				Code:   codeSourceUnknown,
				Detail: fmt.Sprintf("`%s` carries no machine-readable block: the register exists to be read by a tool as well as by a person", path),
			})
			continue
		}
		var audit struct {
			Findings []struct {
				ID string `json:"id"`
			} `json:"findings"`
		}
		if err := json.Unmarshal(blocks[len(blocks)-1][1], &audit); err != nil {
			violations = append(violations, Violation{
				Waiver: "records",
				Code:   codeSourceUnknown,
				Detail: fmt.Sprintf("`%s` does not decode as an audit: %v", path, err),
			})
			continue
		}
		for _, finding := range audit.Findings {
			records.Findings[finding.ID] = path
		}
	}
	sortViolations(violations)
	return records, violations
}

// readFile reads a file the caller already resolved. It exists so that "the
// document is not there" is one answer in one place.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// safeJoin turns a repository-relative path into an absolute one, refusing the
// two ways a document can leave the tree it is judged against: an absolute path
// and a parent segment. A compensation that points at ../secrets is not a
// compensation this tool will follow.
func safeJoin(root, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return filepath.Join(root, filepath.FromSlash(clean)), true
}
