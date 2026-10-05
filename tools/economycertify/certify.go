// Package main is the fail-closed financial certification gate:
// P44-T12 built it and P45-T03 wired the final independent
// decision. It judges one JSON bundle file and emits PASS or FAIL
// with stable reasons, and nothing else.
//
// The bundle is the only input: SHA, decisions, season manifest,
// toolchains, coverage, mutation, capacity, threat findings,
// jurisdiction, per-book reconciliation, merge evidence, credential
// age, product state, oracle scale, resets, late webhook, tied
// takes, ex-King refusal, purchase expiry, archive retention, the
// independent opinion and per-country legal approval all come from
// inside it. No environment variable or flag can flip the verdict —
// the flags name the bundle, never the decision. Exit 0 is PASS, 1
// is FAIL with the reasons listed, 2 is a usage error.
//
// P44 exercises the decisor only with fixtures, without a real
// certificate or activation: every Q19–Q31 stays PENDENTE in the
// versioned register, so a bundle built from the live tree fails
// here by design until the post-merge matrix of P45 fills a real
// bundle from testdata/final-template.json.
package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Decision vocabulary. PASS and FAIL are the only verdicts.
const (
	DecisionPass = "PASS"
	DecisionFail = "FAIL"
)

// Reason codes are the stable vocabulary of a FAIL, in the order
// the decisor reports them after sorting.
const (
	ReasonFileMissing      = "file-missing"
	ReasonMissingField     = "missing-field"
	ReasonSHAMismatch      = "sha-mismatch"
	ReasonDirtyTree        = "dirty-tree"
	ReasonDecisionsPending = "decisions-pending"
	ReasonSeasonMismatch   = "season-manifest-mismatch"
	ReasonToolchainDrift   = "toolchain-drift"
	ReasonCoverageGap      = "coverage-gap"
	ReasonMutationSurvivor = "mutation-survivor"
	ReasonCapacityReorg    = "capacity-regression"
	ReasonThreatOpen       = "threat-open"
	ReasonJurisPending     = "jurisdiction-pending"
	ReasonMilliDrift       = "milli-drift"
	ReasonMergeMissing     = "evidence-merge-missing"
	ReasonStaleCredential  = "stale-credential"
	ReasonProductActive    = "product-active"
	ReasonOracleScale      = "oracle-scale-missing"
	ReasonResetsShort      = "resets-incomplete"
	ReasonWebhookUnproven  = "webhook-unproven"
	ReasonTiesUnjudged     = "ties-unjudged"
	ReasonExKingUnrefused  = "exking-unrefused"
	ReasonPurchaseExpiry   = "purchase-expiry-missing"
	ReasonArchiveUnkept    = "archive-unretained"
	ReasonOpinionMissing   = "opinion-missing"
	ReasonOpinionOpen      = "opinion-open"
	ReasonLegalPending     = "legal-pending"
)

// Decision is the machine-readable verdict.
type Decision struct {
	Decision string   `json:"decision"`
	Commit   string   `json:"commit"`
	Reasons  []string `json:"reasons"`
}

// BookDrift is one per-book reconciliation outcome in milliINK:
// zero drift holds, any nonzero drift fails the bundle.
type BookDrift struct {
	Book       string `json:"book"`
	DriftMilli int64  `json:"driftMilli"`
}

// OracleScale is the at-scale differential oracle execution: how
// many deterministic sequences ran against the independent model.
type OracleScale struct {
	Executed     bool `json:"executed"`
	Sequences    int  `json:"sequences"`
	MinSequences int  `json:"minSequences"`
}

// Resets counts the rehearsed season resets: three, with history
// intact and no carry-over, or the bundle fails.
type Resets struct {
	Count    int `json:"count"`
	Required int `json:"required"`
}

// BehaviorProof is one witnessed behavior of the final execution:
// the late webhook replayed without duplication, the tied takes
// judged, the ex-King refused without moving value.
type BehaviorProof struct {
	Proven bool `json:"proven"`
}

// PurchaseExpiry is the purchase contract term: sales expire after
// TermDays, and a missing term refuses the bundle.
type PurchaseExpiry struct {
	TermDays int `json:"termDays"`
}

// ArchiveRetention seals the retained archive: sealed history that
// no reset rewrites.
type ArchiveRetention struct {
	Retained bool `json:"retained"`
}

// Opinion is the real independent opinion: its IREV reference plus
// its open critical/high findings, which must both be zero.
type Opinion struct {
	Reference string `json:"reference"`
	Critical  int    `json:"critical"`
	High      int    `json:"high"`
}

// LegalApproval is the per-country juridical/tributary approval:
// every required country approved, none inferred.
type LegalApproval struct {
	Required  []string        `json:"required"`
	Approvals map[string]bool `json:"approvals"`
}

// Bundle is the single JSON document the decisor judges. Every
// field is required: an absent check is a FAIL, never a pass.
type Bundle struct {
	Schema             int               `json:"schema"`
	Commit             string            `json:"commit"`
	TreeClean          bool              `json:"treeClean"`
	RequiredDecisions  []string          `json:"requiredDecisions"`
	Decisions          map[string]string `json:"decisions"`
	SeasonDigest       string            `json:"seasonDigest"`
	ExpectedSeason     string            `json:"expectedSeason"`
	Toolchain          map[string]string `json:"toolchain"`
	ExpectedToolchain  map[string]string `json:"expectedToolchain"`
	AxesCovered        int               `json:"axesCovered"`
	AxesRequired       int               `json:"axesRequired"`
	MutationSurvivors  int               `json:"mutationSurvivors"`
	CapacityRegression bool              `json:"capacityRegression"`
	CriticalFindings   int               `json:"criticalFindings"`
	HighFindings       int               `json:"highFindings"`
	PendingCountries   []string          `json:"pendingCountries"`
	Books              []BookDrift       `json:"books"`
	MergeP46           bool              `json:"mergeP46"`
	MergeP47           bool              `json:"mergeP47"`
	RealCredentialDays int               `json:"realCredentialDays"`
	MaxCredentialDays  int               `json:"maxCredentialDays"`
	ProhibitedActive   bool              `json:"prohibitedActive"`
	Oracle             OracleScale       `json:"oracle"`
	SeasonResets       Resets            `json:"resets"`
	LateWebhook        BehaviorProof     `json:"lateWebhook"`
	TiedTakes          BehaviorProof     `json:"tiedTakes"`
	ExKing             BehaviorProof     `json:"exKing"`
	Purchase           PurchaseExpiry    `json:"purchase"`
	Archive            ArchiveRetention  `json:"archive"`
	IndependentOpinion Opinion           `json:"opinion"`
	Legal              LegalApproval     `json:"legal"`
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Decide judges the bundle file at path, optionally holding its
// commit against expectedCommit. An empty expectedCommit skips the
// hold; a set one refuses any alteration. An unreadable bundle is
// itself a FAIL with the commit unknown.
func Decide(path, expectedCommit string) Decision {
	decision := Decision{Decision: DecisionFail, Reasons: []string{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		decision.Reasons = []string{ReasonFileMissing}
		return decision
	}
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		decision.Reasons = []string{ReasonMissingField + ":malformed"}
		return decision
	}
	decision.Commit = bundle.Commit
	reasons := judgeBundle(bundle, expectedCommit)
	sort.Strings(reasons)
	if reasons == nil {
		reasons = []string{}
	}
	decision.Reasons = reasons
	if len(reasons) == 0 {
		decision.Decision = DecisionPass
	}
	return decision
}

func judgeBundle(bundle Bundle, expectedCommit string) []string {
	var reasons []string
	reasons = append(reasons, judgeIdentity(bundle, expectedCommit)...)
	reasons = append(reasons, judgeDecisions(bundle)...)
	reasons = append(reasons, judgeBuild(bundle)...)
	reasons = append(reasons, judgeEvidence(bundle)...)
	reasons = append(reasons, judgeSafety(bundle)...)
	reasons = append(reasons, judgeFinal(bundle)...)
	return reasons
}

func judgeIdentity(bundle Bundle, expectedCommit string) []string {
	var reasons []string
	if bundle.Schema != 1 {
		reasons = append(reasons, ReasonMissingField+":schema")
	}
	if !shaPattern.MatchString(bundle.Commit) {
		reasons = append(reasons, ReasonMissingField+":commit")
	}
	if expectedCommit != "" && bundle.Commit != expectedCommit {
		reasons = append(reasons, ReasonSHAMismatch)
	}
	if !bundle.TreeClean {
		reasons = append(reasons, ReasonDirtyTree)
	}
	return reasons
}

func judgeDecisions(bundle Bundle) []string {
	var reasons []string
	if len(bundle.RequiredDecisions) == 0 {
		reasons = append(reasons, ReasonMissingField+":requiredDecisions")
		return reasons
	}
	for _, q := range bundle.RequiredDecisions {
		if strings.TrimSpace(q) == "" {
			reasons = append(reasons, ReasonMissingField+":requiredDecisions")
			continue
		}
		if bundle.Decisions[q] != "APROVADA" {
			reasons = append(reasons, ReasonDecisionsPending+":"+q)
		}
	}
	return reasons
}

func judgeBuild(bundle Bundle) []string {
	var reasons []string
	if strings.TrimSpace(bundle.SeasonDigest) == "" || strings.TrimSpace(bundle.ExpectedSeason) == "" {
		reasons = append(reasons, ReasonMissingField+":seasonDigest")
	} else if bundle.SeasonDigest != bundle.ExpectedSeason {
		reasons = append(reasons, ReasonSeasonMismatch)
	}
	reasons = append(reasons, judgeToolchain(bundle)...)
	return reasons
}

func judgeToolchain(bundle Bundle) []string {
	var reasons []string
	if len(bundle.ExpectedToolchain) == 0 {
		return []string{ReasonMissingField + ":expectedToolchain"}
	}
	for name, want := range bundle.ExpectedToolchain {
		if bundle.Toolchain[name] != want {
			reasons = append(reasons, ReasonToolchainDrift+":"+name)
		}
	}
	return reasons
}

func judgeEvidence(bundle Bundle) []string {
	var reasons []string
	if bundle.AxesRequired <= 0 {
		reasons = append(reasons, ReasonMissingField+":axesRequired")
	} else if bundle.AxesCovered < bundle.AxesRequired {
		reasons = append(reasons, ReasonCoverageGap)
	}
	if bundle.MutationSurvivors != 0 {
		reasons = append(reasons, ReasonMutationSurvivor)
	}
	if bundle.CapacityRegression {
		reasons = append(reasons, ReasonCapacityReorg)
	}
	if bundle.CriticalFindings != 0 || bundle.HighFindings != 0 {
		reasons = append(reasons, ReasonThreatOpen)
	}
	return reasons
}

func judgeSafety(bundle Bundle) []string {
	var reasons []string
	for _, country := range bundle.PendingCountries {
		if strings.TrimSpace(country) != "" {
			reasons = append(reasons, ReasonJurisPending+":"+country)
		}
	}
	reasons = append(reasons, judgeBooks(bundle)...)
	reasons = append(reasons, judgeRelease(bundle)...)
	return reasons
}

func judgeBooks(bundle Bundle) []string {
	var reasons []string
	if len(bundle.Books) == 0 {
		return []string{ReasonMissingField + ":books"}
	}
	for _, book := range bundle.Books {
		if strings.TrimSpace(book.Book) == "" {
			reasons = append(reasons, ReasonMissingField+":books")
			continue
		}
		if book.DriftMilli != 0 {
			reasons = append(reasons, ReasonMilliDrift+":"+book.Book)
		}
	}
	return reasons
}

func judgeFinal(bundle Bundle) []string {
	var reasons []string
	reasons = append(reasons, judgeOracleResets(bundle)...)
	reasons = append(reasons, judgeBehaviors(bundle)...)
	reasons = append(reasons, judgeContracts(bundle)...)
	reasons = append(reasons, judgeAssurance(bundle)...)
	return reasons
}

func judgeOracleResets(bundle Bundle) []string {
	var reasons []string
	if bundle.Oracle.MinSequences <= 0 {
		reasons = append(reasons, ReasonMissingField+":oracle")
	} else if !bundle.Oracle.Executed || bundle.Oracle.Sequences < bundle.Oracle.MinSequences {
		reasons = append(reasons, ReasonOracleScale)
	}
	if bundle.SeasonResets.Required <= 0 {
		reasons = append(reasons, ReasonMissingField+":resets")
	} else if bundle.SeasonResets.Count < bundle.SeasonResets.Required {
		reasons = append(reasons, ReasonResetsShort)
	}
	return reasons
}

func judgeBehaviors(bundle Bundle) []string {
	var reasons []string
	if !bundle.LateWebhook.Proven {
		reasons = append(reasons, ReasonWebhookUnproven)
	}
	if !bundle.TiedTakes.Proven {
		reasons = append(reasons, ReasonTiesUnjudged)
	}
	if !bundle.ExKing.Proven {
		reasons = append(reasons, ReasonExKingUnrefused)
	}
	return reasons
}

func judgeContracts(bundle Bundle) []string {
	var reasons []string
	if bundle.Purchase.TermDays <= 0 {
		reasons = append(reasons, ReasonPurchaseExpiry)
	}
	if !bundle.Archive.Retained {
		reasons = append(reasons, ReasonArchiveUnkept)
	}
	return reasons
}

func judgeAssurance(bundle Bundle) []string {
	var reasons []string
	if strings.TrimSpace(bundle.IndependentOpinion.Reference) == "" {
		reasons = append(reasons, ReasonOpinionMissing)
	} else if bundle.IndependentOpinion.Critical != 0 || bundle.IndependentOpinion.High != 0 {
		reasons = append(reasons, ReasonOpinionOpen)
	}
	reasons = append(reasons, judgeLegal(bundle)...)
	return reasons
}

func judgeLegal(bundle Bundle) []string {
	var reasons []string
	if len(bundle.Legal.Required) == 0 {
		return []string{ReasonMissingField + ":legal"}
	}
	for _, country := range bundle.Legal.Required {
		if strings.TrimSpace(country) == "" {
			reasons = append(reasons, ReasonMissingField+":legal")
			continue
		}
		if !bundle.Legal.Approvals[country] {
			reasons = append(reasons, ReasonLegalPending+":"+country)
		}
	}
	return reasons
}

func judgeRelease(bundle Bundle) []string {
	var reasons []string
	if !bundle.MergeP46 {
		reasons = append(reasons, ReasonMergeMissing+":P46")
	}
	if !bundle.MergeP47 {
		reasons = append(reasons, ReasonMergeMissing+":P47")
	}
	if bundle.MaxCredentialDays <= 0 {
		reasons = append(reasons, ReasonMissingField+":maxCredentialDays")
	} else if bundle.RealCredentialDays > bundle.MaxCredentialDays {
		reasons = append(reasons, ReasonStaleCredential)
	}
	if bundle.ProhibitedActive {
		reasons = append(reasons, ReasonProductActive)
	}
	return reasons
}
