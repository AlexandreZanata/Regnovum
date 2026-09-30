package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxTermsRunes bounds opaque terms tokens: long enough for operation
// tokens and custody labels, short enough to stay out of log abuse.
const maxTermsRunes = 128

// maxTermsTextRunes bounds free terms prose: the object and the
// execution terms. Long enough to name the dispute, short enough to
// stay out of log abuse.
const maxTermsTextRunes = 280

// CostsBearer is the closed allocation of the procedure costs the
// parties agree on. The rule travels explicitly with the proposal:
// no default lives here, and later tasks never infer one.
type CostsBearer string

const (
	// CostsSplit divides the procedure costs between the parties.
	CostsSplit CostsBearer = "split"
	// CostsClaimant loads the procedure costs on the claiming party.
	CostsClaimant CostsBearer = "claimant"
	// CostsRespondent loads the procedure costs on the answering party.
	CostsRespondent CostsBearer = "respondent"
)

// ParseCostsBearer validates a costs rule against the closed
// vocabulary. Matching is exact: no trimming, no case folding.
func ParseCostsBearer(raw string) (CostsBearer, error) {
	bearer := CostsBearer(raw)
	switch bearer {
	case CostsSplit, CostsClaimant, CostsRespondent:
		return bearer, nil
	default:
		return "", ErrInvalidTerms
	}
}

// String returns the stored costs rule value.
func (b CostsBearer) String() string { return string(b) }

// Proposal is one versioned bilateral arbitration proposal: the
// negotiation key, the revision number, the dispute object, the two
// named parties in claimant order, the declared value in milliINK
// with the declared escrow reference (empty when no escrow is
// pledged), the agreed rite and evidence rules by reference, the
// costs rule, the acceptance deadline in UTC and the execution
// terms, sealed by hash. Nothing here obliges anyone: the case opens
// in later tasks, only after both parties accept these exact terms.
type Proposal struct {
	Key        string
	Version    int
	Object     string
	Claimant   string
	Respondent string
	ValueMilli int64
	EscrowRef  string
	Rite       string
	Evidence   string
	Costs      CostsBearer
	ExpiresAt  time.Time
	Execution  string
	Accepted   [2]bool
	Hash       string
}

// ProposalRequest carries one proposal. Every value arrives from the
// caller: no ratified object, rite, cost or deadline lives here.
type ProposalRequest struct {
	Key        string
	Version    int
	Object     string
	Claimant   string
	Respondent string
	ValueMilli int64
	EscrowRef  string
	Rite       string
	Evidence   string
	Costs      CostsBearer
	ExpiresAt  time.Time
	Execution  string
}

// parseTermsToken validates one opaque terms token: exact match, no
// control characters, bounded length.
func parseTermsToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidTerms
	}
	if utf8.RuneCountInString(raw) > maxTermsRunes {
		return "", ErrInvalidTerms
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidTerms
		}
	}
	return raw, nil
}

// parseTermsText validates one prose term: exact match, bounded like
// a contract object, never blank.
func parseTermsText(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidTerms
	}
	if utf8.RuneCountInString(raw) > maxTermsTextRunes {
		return "", ErrInvalidTerms
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidTerms
		}
	}
	return raw, nil
}

// sealTerms binds key, revision, object, parties, value, escrow,
// rite, evidence, costs, deadline and execution: any term changed
// after a first acceptance breaks the seal path, so acceptances
// never travel to amended terms.
func sealTerms(proposal Proposal) string {
	canonical := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		proposal.Key, proposal.Version, proposal.Object,
		proposal.Claimant, proposal.Respondent, proposal.ValueMilli,
		proposal.EscrowRef, proposal.Rite, proposal.Evidence,
		proposal.Costs.String(),
		proposal.ExpiresAt.UTC().Format(time.RFC3339Nano), proposal.Execution)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// validateParties checks the bilateral standing: two named, distinct,
// well-formed parties. One missing party never opens a case.
func validateParties(claimant, respondent string) (string, string, error) {
	first, err := parseTermsToken(claimant)
	if err != nil {
		return "", "", err
	}
	second, err := parseTermsToken(respondent)
	if err != nil {
		return "", "", err
	}
	if first == second {
		return "", "", ErrInvalidTerms
	}
	return first, second, nil
}

// validateTermsBody checks object, rite, evidence, costs, execution
// and value: every term explicit, amounts never negative.
func validateTermsBody(req ProposalRequest) error {
	if _, err := parseTermsText(req.Object); err != nil {
		return err
	}
	if _, err := parseTermsToken(req.Rite); err != nil {
		return err
	}
	if _, err := parseTermsToken(req.Evidence); err != nil {
		return err
	}
	if _, err := ParseCostsBearer(string(req.Costs)); err != nil {
		return err
	}
	if _, err := parseTermsText(req.Execution); err != nil {
		return err
	}
	if req.ValueMilli < 0 {
		return ErrInvalidTerms
	}
	if req.EscrowRef != "" {
		if _, err := parseTermsToken(req.EscrowRef); err != nil {
			return err
		}
	}
	return nil
}

// Propose seals one bilateral arbitration proposal. A key names the
// negotiation, a revision numbers the amendment: an amendment after
// a first acceptance is a new revision whose acceptances start over,
// so no acceptance ever travels to terms its party did not see. Terms
// born past their deadline seal but can never bind: Accept refuses
// them, so dead terms open no case.
func Propose(req ProposalRequest) (Proposal, error) {
	key, err := parseTermsToken(req.Key)
	if err != nil {
		return Proposal{}, err
	}
	if req.Version < 1 {
		return Proposal{}, ErrInvalidTerms
	}
	claimant, respondent, err := validateParties(req.Claimant, req.Respondent)
	if err != nil {
		return Proposal{}, err
	}
	if err := validateTermsBody(req); err != nil {
		return Proposal{}, err
	}
	if req.ExpiresAt.IsZero() {
		return Proposal{}, ErrInvalidTerms
	}
	proposal := Proposal{
		Key: key, Version: req.Version, Object: req.Object,
		Claimant: claimant, Respondent: respondent, ValueMilli: req.ValueMilli,
		EscrowRef: req.EscrowRef, Rite: req.Rite, Evidence: req.Evidence,
		Costs: req.Costs, ExpiresAt: req.ExpiresAt.UTC(), Execution: req.Execution,
	}
	proposal.Hash = sealTerms(proposal)
	return proposal, nil
}

// VerifyTermsHash recomputes the seal and refuses terms whose values
// no longer agree.
func (p Proposal) VerifyTermsHash() error {
	if p.Hash == "" || sealTerms(p) != p.Hash {
		return ErrInvalidTerms
	}
	return nil
}

// partyIndex resolves one account to its party slot, or refuses
// strangers. Only named parties bind a case.
func (p Proposal) partyIndex(by string) (int, error) {
	switch by {
	case p.Claimant:
		return 0, nil
	case p.Respondent:
		return 1, nil
	default:
		return 0, ErrTermsNotParty
	}
}

// Bound reports whether both named parties accepted these exact
// terms: the only state in which later tasks may open a case. A
// private relation without both acceptances never binds.
func (p Proposal) Bound() bool {
	return p.Accepted[0] && p.Accepted[1]
}

// Accept records one party's acceptance of these exact terms at one
// instant: the first acceptance counts, a replay of the same party
// returns the proposal unchanged, and the second distinct party
// binds it. Expired terms refuse, strangers refuse, and the now
// instant arrives per call — the domain never reads the wall clock.
func (p Proposal) Accept(by string, now time.Time) (Proposal, error) {
	if err := p.VerifyTermsHash(); err != nil {
		return Proposal{}, err
	}
	index, err := p.partyIndex(by)
	if err != nil {
		return Proposal{}, err
	}
	if now.IsZero() || !now.UTC().Before(p.ExpiresAt) {
		return Proposal{}, ErrTermsExpired
	}
	if p.Accepted[index] {
		return p, nil
	}
	next := p
	next.Accepted[index] = true
	return next, nil
}
