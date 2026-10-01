// Package main is the P31-T08 gate, extended in P46-T01 to recognize
// the seasonal addendum: it reads only versioned documents and
// refuses any financial implementation while a critical business
// decision is unresolved. Six rules, all static, no environment
// input: critical-pending, price-unapproved, ambiguous-time,
// threat-without-control, prohibited-offer and addendum-bypass. A
// pending critical decision blocks P32; it is never resolved by
// inference, and a seasonal addendum never ratifies one by
// implication.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding is one violated rule with the evidence that proves it.
type Finding struct {
	Rule   string
	Detail string
}

// Documents judged by the gate, relative to the repository root.
const (
	decisionsPath = "docs/reino/DECISOES_VIGENTES.md"
	pricingPath   = "docs/reino/PRECIFICACAO.md"
	tempoPath     = "docs/reino/TEMPO_ECONOMICO.md"
	threatPath    = "docs/THREAT_MODEL.md"
	addendumPath  = "docs/reino/TEMPORADAS_SUCESSAO.md"
)

// criticalQs are the decisions P32 needs before any financial code:
// custody, third-party rights, Genesis, unit, quote, treasury,
// tithe, crumbs, settlement and chargeback.
var criticalQs = []string{
	"Q08", "Q11", "Q20", "Q21", "Q22",
	"Q23", "Q24", "Q25", "Q28", "Q30",
}

// deferredQs must stay non-offered: titles and markets are concepts,
// never products, until per-country analysis and a new approval.
var deferredQs = []string{"Q26", "Q29"}

// statePattern reads the ratification state of one decision block.
var statePattern = regexp.MustCompile(`\*\*Estado:\*\*\s*` + "`" + `([A-Z]+)` + "`")

// threatPattern finds one threat row of the model.
var threatPattern = regexp.MustCompile(`\*\*(THR-ECON-[0-9]+)\*\*`)

// Audit judges the versioned documents at root and returns every
// finding in stable rule order. An unreadable document is itself a
// finding, never a pass.
func Audit(root string) []Finding {
	decisions, decisionsErr := readDoc(root, decisionsPath)
	pricing, pricingErr := readDoc(root, pricingPath)
	tempo, tempoErr := readDoc(root, tempoPath)
	threat, threatErr := readDoc(root, threatPath)
	addendum, addendumErr := readDoc(root, addendumPath)

	var findings []Finding
	findings = append(findings, checkCritical(decisions, decisionsErr)...)
	findings = append(findings, checkPrice(pricing, pricingErr)...)
	findings = append(findings, checkTime(tempo, tempoErr)...)
	findings = append(findings, checkThreat(threat, threatErr)...)
	findings = append(findings, checkOffer(decisions, decisionsErr)...)
	findings = append(findings, checkAddendum(decisions, decisionsErr, pricing, pricingErr, addendum, addendumErr)...)
	return findings
}

func readDoc(root, path string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// blockOf returns the decision block of one Q identifier.
func blockOf(document, q string) string {
	heading := "### " + q
	start := strings.Index(document, heading)
	if start < 0 {
		return ""
	}
	rest := document[start:]
	next := strings.Index(rest[len(heading):], "\n### ")
	if next < 0 {
		return rest
	}
	return rest[:len(heading)+next]
}

func stateOf(block string) string {
	match := statePattern.FindStringSubmatch(block)
	if match == nil {
		return ""
	}
	return match[1]
}

// checkCritical refuses every critical Q that is not expressly approved.
// Approval lives only in the versioned register, never in a flag.
func checkCritical(document string, readErr error) []Finding {
	if readErr != nil {
		return []Finding{{Rule: "critical-pending", Detail: decisionsPath + " is unreadable"}}
	}
	var findings []Finding
	for _, q := range criticalQs {
		block := blockOf(document, q)
		if block == "" {
			findings = append(findings, Finding{Rule: "critical-pending", Detail: q + " has no register block"})
			continue
		}
		if stateOf(block) != "APROVADA" {
			findings = append(findings, Finding{Rule: "critical-pending", Detail: q + " is not ratified"})
		}
	}
	return findings
}

// checkPrice refuses a sale without an approved price table. A suggested
// future table never inherits vigour by omission.
func checkPrice(document string, readErr error) []Finding {
	if readErr != nil {
		return []Finding{{Rule: "price-unapproved", Detail: pricingPath + " is unreadable"}}
	}
	if strings.Contains(document, "**Tabela aprovada:**") &&
		strings.Contains(document, "tabela v") &&
		strings.Contains(document, "APROVADA") {
		return nil
	}
	return []Finding{{Rule: "price-unapproved", Detail: "no approved price table in " + pricingPath}}
}

// checkTime refuses an ambiguous accounting temporality: without the
// canonical instants, the half-open interval and the quote expiry,
// identical events could settle in two windows.
func checkTime(document string, readErr error) []Finding {
	if readErr != nil {
		return []Finding{{Rule: "ambiguous-time", Detail: tempoPath + " is unreadable"}}
	}
	compact := strings.ReplaceAll(document, " ", "")
	hasInterval := strings.Contains(compact, "[início,fim)") ||
		strings.Contains(compact, "[inicio,fim)")
	if strings.Contains(document, "accepted_at") &&
		strings.Contains(document, "expires_at") &&
		hasInterval &&
		strings.Contains(document, "Proibi") {
		return nil
	}
	return []Finding{{Rule: "ambiguous-time", Detail: "canonical instants, interval or prohibitions missing in " + tempoPath}}
}

// checkThreat refuses a critical or high economic threat without its
// four artifacts on the same row: preventive control, detector,
// breaking test and runbook.
func checkThreat(document string, readErr error) []Finding {
	if readErr != nil {
		return []Finding{{Rule: "threat-without-control", Detail: threatPath + " is unreadable"}}
	}
	ids := threatPattern.FindAllStringSubmatch(document, -1)
	if len(ids) == 0 {
		return []Finding{{Rule: "threat-without-control", Detail: "no THR-ECON row in " + threatPath}}
	}
	seen := map[string]bool{}
	var findings []Finding
	for _, line := range strings.Split(document, "\n") {
		match := threatPattern.FindStringSubmatch(line)
		if match == nil || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		hasRunbook := strings.Contains(line, "docs/RUNBOOKS.md")
		hasBreak := strings.Contains(line, ".go") || strings.Contains(line, "planejado P3")
		if !hasRunbook || !hasBreak {
			findings = append(findings, Finding{
				Rule:   "threat-without-control",
				Detail: fmt.Sprintf("%s lacks control, detector, breaking test or runbook", match[1]),
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Detail < findings[j].Detail })
	return findings
}

// checkAddendum recognizes the seasonal addendum without letting it
// bypass a pending decision: an approval claim on the same line as a
// critical Q whose register block is not approved, or an approved
// price table the pricing register denies, refuses as
// addendum-bypass. Intent statements without ratification language
// pass: the addendum is read, never obeyed by implication.
func checkAddendum(decisions string, decisionsErr error, pricing string, pricingErr error, addendum string, addendumErr error) []Finding {
	if addendumErr != nil {
		return []Finding{{Rule: "addendum-bypass", Detail: addendumPath + " is unreadable"}}
	}
	if decisionsErr != nil {
		return []Finding{{Rule: "addendum-bypass", Detail: decisionsPath + " is unreadable"}}
	}
	var findings []Finding
	for _, line := range strings.Split(addendum, "\n") {
		if !strings.Contains(line, "APROVADA") {
			continue
		}
		for _, q := range criticalQs {
			if strings.Contains(line, q) && stateOf(blockOf(decisions, q)) != "APROVADA" {
				findings = append(findings, Finding{
					Rule:   "addendum-bypass",
					Detail: q + " approved by addendum while register pending",
				})
			}
		}
	}
	if pricingErr != nil {
		return append(findings, Finding{Rule: "addendum-bypass", Detail: pricingPath + " is unreadable"})
	}
	if strings.Contains(addendum, "**Tabela aprovada:**") && strings.Contains(addendum, "APROVADA") &&
		!(strings.Contains(pricing, "**Tabela aprovada:**") && strings.Contains(pricing, "tabela v") && strings.Contains(pricing, "APROVADA")) {
		findings = append(findings, Finding{
			Rule:   "addendum-bypass",
			Detail: "approved price table claimed by addendum while register pending",
		})
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Detail < findings[j].Detail })
	return findings
}

// checkOffer refuses a legally prohibited offer: titles and markets stay
// concepts until per-country analysis and a new express approval.
func checkOffer(document string, readErr error) []Finding {
	if readErr != nil {
		return []Finding{{Rule: "prohibited-offer", Detail: decisionsPath + " is unreadable"}}
	}
	var findings []Finding
	for _, q := range deferredQs {
		block := blockOf(document, q)
		if block == "" {
			findings = append(findings, Finding{Rule: "prohibited-offer", Detail: q + " has no register block"})
			continue
		}
		if stateOf(block) == "APROVADA" {
			findings = append(findings, Finding{Rule: "prohibited-offer", Detail: q + " offered without juridical package"})
		}
	}
	return findings
}
