package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxKingdomExportRunes bounds opaque kingdom export tokens: long
// enough for accounts, sections and fact links, short enough to
// stay out of log abuse.
const maxKingdomExportRunes = 128

// parseKingdomExportToken validates one opaque token: exact
// match, bounded, never blank, no control characters.
func parseKingdomExportToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidKingdomExport
	}
	if utf8.RuneCountInString(raw) > maxKingdomExportRunes {
		return "", ErrInvalidKingdomExport
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidKingdomExport
		}
	}
	return raw, nil
}

// ExportSection is the allowlist of what a kingdom export may
// carry: the holder's contracts, receipts and acts. Anything
// else — another holder's proof, balances, raw sealed bytes —
// is absent here on purpose, so an export never mixes holders
// or leaks foreign proof by inference.
type ExportSection string

const (
	// ExportSectionContracts carries the holder's contracts.
	ExportSectionContracts ExportSection = "contratos"
	// ExportSectionReceipts carries the holder's receipts.
	ExportSectionReceipts ExportSection = "recibos"
	// ExportSectionHolderActs carries the holder's own acts.
	ExportSectionHolderActs ExportSection = "atos-do-titular"
)

// ParseExportSection resolves an export section value. Matching
// is exact: no trimming, no case folding.
func ParseExportSection(value string) (ExportSection, bool) {
	switch ExportSection(value) {
	case ExportSectionContracts, ExportSectionReceipts, ExportSectionHolderActs:
		return ExportSection(value), true
	default:
		return "", false
	}
}

// ExportItem is one candidate export line: its identity, whose
// holder it belongs to, and whether it proves someone else's
// fact. Foreign proof never boards any export.
type ExportItem struct {
	ID           string
	Owner        string
	ForeignProof bool
}

// ExportRequest carries one kingdom export: whose account, who
// asks, which allowlisted sections, whether the holder
// consented, whether the account is dead (then the requester
// is the heir) or reactivated, and when.
type ExportRequest struct {
	Account     string
	Requester   string
	Sections    []string
	Consent     bool
	Dead        bool
	Heir        string
	Reactivated bool
	At          time.Time
}

// ExportGrant is one authorized kingdom export: the single
// account it covers, the allowlisted sections, and when.
type ExportGrant struct {
	Account  string
	Sections []ExportSection
	At       time.Time
}

// checkExportIdentity validates the export identity: whole
// account and requester, a live instant, and — for a dead
// account — a named heir asking. The dead stay reachable to
// their heir, never to strangers.
func checkExportIdentity(req ExportRequest) error {
	if _, err := parseKingdomExportToken(req.Account); err != nil {
		return err
	}
	if _, err := parseKingdomExportToken(req.Requester); err != nil {
		return err
	}
	if req.At.IsZero() {
		return ErrInvalidKingdomExport
	}
	if req.Dead {
		if _, err := parseKingdomExportToken(req.Heir); err != nil {
			return ErrDeadAccountAccess
		}
		if req.Requester != req.Heir {
			return ErrDeadAccountAccess
		}
	}
	return nil
}

// checkExportSections binds the export to the allowlist: every
// section known, none twice. An empty or foreign section stops
// here, before anything is read.
func checkExportSections(req ExportRequest) ([]ExportSection, error) {
	if len(req.Sections) == 0 {
		return nil, ErrUnknownExportSection
	}
	sections := make([]ExportSection, 0, len(req.Sections))
	seen := make(map[ExportSection]bool, len(req.Sections))
	for _, raw := range req.Sections {
		section, known := ParseExportSection(raw)
		if !known {
			return nil, ErrUnknownExportSection
		}
		if seen[section] {
			return nil, ErrInvalidKingdomExport
		}
		seen[section] = true
		sections = append(sections, section)
	}
	return sections, nil
}

// AuthorizeExport authorizes one kingdom export for exactly one
// holder. Refused consent, a dead account without its heir, a
// foreign section and a duplicated section all stop here with
// nothing read and nothing mixed.
func AuthorizeExport(req ExportRequest) (ExportGrant, error) {
	if !req.Consent {
		return ExportGrant{}, ErrConsentRefused
	}
	if err := checkExportIdentity(req); err != nil {
		return ExportGrant{}, err
	}
	sections, err := checkExportSections(req)
	if err != nil {
		return ExportGrant{}, err
	}
	return ExportGrant{Account: req.Account, Sections: sections, At: req.At.UTC()}, nil
}

// CheckExportItems refuses an export that mixes holders or leaks
// foreign proof: every line belongs to the granted account and
// no line proves someone else's fact.
func CheckExportItems(grant ExportGrant, items []ExportItem) error {
	for _, item := range items {
		if _, err := parseKingdomExportToken(item.ID); err != nil {
			return err
		}
		if _, err := parseKingdomExportToken(item.Owner); err != nil {
			return err
		}
		if item.Owner != grant.Account {
			return ErrCrossAccountExport
		}
		if item.ForeignProof {
			return ErrCrossAccountExport
		}
	}
	return nil
}

// MinimizableFact is one deletion candidate: its identity,
// whether a legal basis keeps it, and whether it belongs to a
// pending appeal.
type MinimizableFact struct {
	ID            string
	LegalBasis    bool
	AppealRelated bool
}

// MinimizeRequest carries one deletion: whose account, whether
// an appeal is pending, whether a legal hold keeps everything,
// and the candidate facts.
type MinimizeRequest struct {
	Account       string
	AppealPending bool
	LegalHold     bool
	Facts         []MinimizableFact
}

// MinimizeResult is what deletion decided: the kept fact
// identities and the anonymized ones, in request order.
type MinimizeResult struct {
	Kept       []string
	Anonymized []string
}

// Minimize deletes lawfully: a legal hold keeps everything,
// otherwise only facts with a legal basis — and appeal facts
// while an appeal is pending — stay, and the rest is
// anonymized. Death changes nothing: the dead keep their legal
// facts and their right to appeal exactly like the living.
func Minimize(req MinimizeRequest) (MinimizeResult, error) {
	if _, err := parseKingdomExportToken(req.Account); err != nil {
		return MinimizeResult{}, err
	}
	result := MinimizeResult{Kept: []string{}, Anonymized: []string{}}
	for _, fact := range req.Facts {
		if _, err := parseKingdomExportToken(fact.ID); err != nil {
			return MinimizeResult{}, err
		}
		if req.LegalHold || fact.LegalBasis || (fact.AppealRelated && req.AppealPending) {
			result.Kept = append(result.Kept, fact.ID)
			continue
		}
		result.Anonymized = append(result.Anonymized, fact.ID)
	}
	return result, nil
}
