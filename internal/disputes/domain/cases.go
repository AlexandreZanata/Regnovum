package domain

import "time"

// CaseKind is the closed competence vocabulary of one case entry:
// a voluntary challenge, a bilateral agreement, a private
// arbitration over bound terms, or an institutional safety report.
// The first three are consent-based private cases; the last one is
// an institutional notice that follows a separate rite and never
// attributes private credit nor defeat.
type CaseKind string

const (
	// CaseChallenge names a voluntary challenge between the two
	// named parties: refusal declines it, never sentences it.
	CaseChallenge CaseKind = "challenge"
	// CaseAgreement names a bilateral agreement case over bound
	// terms.
	CaseAgreement CaseKind = "agreement"
	// CaseArbitration names a private arbitration over bound terms.
	CaseArbitration CaseKind = "arbitration"
	// CaseSafetyReport names an institutional safety notice: it is
	// received, never opened as a private case.
	CaseSafetyReport CaseKind = "safety-report"
)

// ParseCaseKind validates a kind token. Matching is exact: no
// trimming, no case folding.
func ParseCaseKind(raw string) (CaseKind, error) {
	kind := CaseKind(raw)
	switch kind {
	case CaseChallenge, CaseAgreement, CaseArbitration, CaseSafetyReport:
		return kind, nil
	default:
		return "", ErrInvalidCase
	}
}

// ConsentBased reports whether the kind needs bilateral terms: the
// three private kinds do, the safety report never does.
func (k CaseKind) ConsentBased() bool {
	switch k {
	case CaseChallenge, CaseAgreement, CaseArbitration:
		return true
	default:
		return false
	}
}

// Private reports whether the kind is a private case. Safety reports
// are institutional notices: they are never private, carry no
// parties and decide no private sentence.
func (k CaseKind) Private() bool { return k.ConsentBased() }

// String returns the stored kind value.
func (k CaseKind) String() string { return string(k) }

// CaseStatus is the closed state vocabulary of one entry: opened
// private cases, declined challenges and received safety notices.
// No status carries a sentence: decisions arrive in later tasks.
type CaseStatus string

const (
	// StatusOpen names a private case opened over bound terms.
	StatusOpen CaseStatus = "open"
	// StatusDeclined names a challenge its own party refused: the
	// refusal closes the invitation without any sentence.
	StatusDeclined CaseStatus = "declined"
	// StatusReceived names a safety report taken in by the
	// institutional rite: it is tracked, never tried as a private
	// case.
	StatusReceived CaseStatus = "received"
)

// Case is one competence entry: the kind, the negotiation or report
// key, the two bound parties for private cases, who moved it, when
// it was recorded in UTC and its status. Safety reports carry no
// parties and no value: the institutional rite never attributes
// private credit nor defeat by refusal.
type Case struct {
	Kind       CaseKind
	Key        string
	Claimant   string
	Respondent string
	Reporter   string
	OpenedAt   time.Time
	Status     CaseStatus
}

// IsPrivate reports whether the entry is a private case. Safety
// reports always answer false, even when their key text collides
// with a negotiation key.
func (c Case) IsPrivate() bool { return c.Kind.Private() }

// HasSentence reports whether the entry carries a private sentence.
// Opening, refusal and receipt never sentence: the decision rite
// lives in later tasks, and refusal or safety receipt never become
// one by implication.
func (c Case) HasSentence() bool { return false }

// OpenConsentCase opens one private case over bilateral terms. The
// proposal must seal and bind: one-sided or silent relations refuse
// with ErrCaseNeedsConsent, strangers refuse with ErrCaseNotParty,
// and the safety kind routed here refuses with ErrInvalidCase. The
// opening instant arrives per call — the domain never reads the wall
// clock.
func OpenConsentCase(kind CaseKind, proposal Proposal, by string, at time.Time) (Case, error) {
	if !kind.ConsentBased() {
		return Case{}, ErrInvalidCase
	}
	if err := proposal.VerifyTermsHash(); err != nil {
		return Case{}, err
	}
	if !proposal.Bound() {
		return Case{}, ErrCaseNeedsConsent
	}
	if by != proposal.Claimant && by != proposal.Respondent {
		return Case{}, ErrCaseNotParty
	}
	if at.IsZero() {
		return Case{}, ErrInvalidCase
	}
	return Case{
		Kind: kind, Key: proposal.Key,
		Claimant: proposal.Claimant, Respondent: proposal.Respondent,
		Reporter: by, OpenedAt: at.UTC(), Status: StatusOpen,
	}, nil
}

// OpenSafetyReport receives one institutional safety notice under
// its own key. It never reads a proposal: an unbound, missing or
// colliding negotiation never helps nor harms it, and it never
// carries private parties, value or sentence. Moderation of risk
// follows this rite, never the private one.
func OpenSafetyReport(key, reporter, reason string, at time.Time) (Case, error) {
	boundKey, err := parseTermsToken(key)
	if err != nil {
		return Case{}, ErrInvalidCase
	}
	boundReporter, err := parseTermsToken(reporter)
	if err != nil {
		return Case{}, ErrInvalidCase
	}
	if _, err := parseTermsToken(reason); err != nil {
		return Case{}, ErrInvalidCase
	}
	if at.IsZero() {
		return Case{}, ErrInvalidCase
	}
	return Case{
		Kind: CaseSafetyReport, Key: boundKey,
		Reporter: boundReporter, OpenedAt: at.UTC(), Status: StatusReceived,
	}, nil
}

// DeclineChallenge records one named party refusing its own
// unbound challenge. The refusal closes the invitation with no
// sentence: declining never attributes credit, defeat or verdict.
// Bound proposals already passed the invitation, so declining them
// refuses; strangers refuse as well.
func DeclineChallenge(proposal Proposal, by string, at time.Time) (Case, error) {
	if err := proposal.VerifyTermsHash(); err != nil {
		return Case{}, err
	}
	if proposal.Bound() {
		return Case{}, ErrInvalidCase
	}
	if by != proposal.Claimant && by != proposal.Respondent {
		return Case{}, ErrCaseNotParty
	}
	if at.IsZero() {
		return Case{}, ErrInvalidCase
	}
	return Case{
		Kind: CaseChallenge, Key: proposal.Key,
		Claimant: proposal.Claimant, Respondent: proposal.Respondent,
		Reporter: by, OpenedAt: at.UTC(), Status: StatusDeclined,
	}, nil
}
