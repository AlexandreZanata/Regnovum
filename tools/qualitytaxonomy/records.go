package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CatalogPath is the catalog the classes are read from. The taxonomy does not
// restate a rule's risk: it reads it, so that a rule whose class changes does
// not leave a second, quieter copy of the old class behind.
const CatalogPath = "quality/catalog.json"

// ReadRecords builds what the register resolves against. A missing catalog is a
// refusal: a taxonomy compared against nothing would pass because it had
// nothing to compare with, and every row would read as classified.
func ReadRecords(root string) (Records, Violations) {
	records := Records{Rules: map[string]string{}, Root: root}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CatalogPath)))
	if err != nil {
		return records, Violations{{
			Row:    "records",
			Code:   codeSourceUnknown,
			Detail: fmt.Sprintf("`%s` is unreadable: without the catalog's own classes a rule cannot be held to one: %v", CatalogPath, err),
		}}
	}
	var catalog struct {
		Rules []struct {
			ID   string `json:"id"`
			Risk string `json:"risk"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return records, Violations{{
			Row:    "records",
			Code:   codeSourceUnknown,
			Detail: fmt.Sprintf("`%s` does not decode as a catalog: %v", CatalogPath, err),
		}}
	}
	if len(catalog.Rules) == 0 {
		return records, Violations{{
			Row:    "records",
			Code:   codeSourceUnknown,
			Detail: fmt.Sprintf("`%s` declares no rule: evidence for a rule nobody declared classifies nothing", CatalogPath),
		}}
	}
	for _, rule := range catalog.Rules {
		records.Rules[rule.ID] = rule.Risk
	}
	return records, nil
}
