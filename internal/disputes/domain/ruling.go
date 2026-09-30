package domain

import "time"

// Verdict is the closed outcome vocabulary of one private ruling:
// which side the rite upholds, in whole or in part. It applies the
// accepted terms to the filed evidence only: the winning side wins
// the rite, never a universal truth, and common debates outside the
// case keep their semantics untouched.
type Verdict string

const (
	// VerdictUpholdClaimant upholds the claiming party within the
	// accepted terms.
	VerdictUpholdClaimant Verdict = "uphold-claimant"
	// VerdictUpholdRespondent upholds the answering party within
	// the accepted terms.
	VerdictUpholdRespondent Verdict = "uphold-respondent"
	// VerdictPartial upholds each side in part within the accepted
	// terms.
	VerdictPartial Verdict = "partial"
)

// ParseVerdict validates an outcome token. Matching is exact: no
// trimming, no case folding.
func ParseVerdict(raw string) (Verdict, error) {
	verdict := Verdict(raw)
	switch verdict {
	case VerdictUpholdClaimant, VerdictUpholdRespondent, VerdictPartial:
		return verdict, nil
	default:
		return "", ErrInvalidRuling
	}
}

// String returns the stored verdict value.
func (v Verdict) String() string { return string(v) }

// parseRulingToken validates one opaque ruling token (arbiter,
// evidence digest) and reports it as a ruling refusal: malformed
// ruling entries never borrow the terms error.
func parseRulingToken(raw string) (string, error) {
	if bound, err := parseTermsToken(raw); err != nil {
		return "", ErrInvalidRuling
	} else {
		return bound, nil
	}
}

// parseRulingText validates one ruling prose (grounds, appeal
// reason) and reports it as a ruling refusal.
func parseRulingText(raw string) (string, error) {
	if bound, err := parseTermsText(raw); err != nil {
		return "", ErrInvalidRuling
	} else {
		return bound, nil
	}
}

// EvidenceItem is one proportional-access exhibit: who filed it,
// which content digest it points to and when it arrived in UTC. Only
// the two named parties file: strangers never reach the record, and
// the arbiter never files.
type EvidenceItem struct {
	By     string
	Digest string
	At     time.Time
}

// Hearing is one instructed private case: the opened entry bound to
// its sealed bilateral terms, the designated impartial arbiter, the
// evidence deadline in UTC and the filed exhibits. The terms travel
// inside, so the later ruling cannot drift beyond the contract its
// parties accepted.
type Hearing struct {
	Kind          CaseKind
	Key           string
	Claimant      string
	Respondent    string
	Arbiter       string
	Terms         Proposal
	OpenedAt      time.Time
	EvidenceDueAt time.Time
	Evidence      []EvidenceItem
}

// defended reports whether both named parties filed at least one
// exhibit: a ruling without a defense from either side refuses.
func (h Hearing) defended() bool {
	var claimant, respondent bool
	for _, item := range h.Evidence {
		switch item.By {
		case h.Claimant:
			claimant = true
		case h.Respondent:
			respondent = true
		}
	}
	return claimant && respondent
}

// OpenHearing instructs one opened private case under its sealed
// bilateral terms with a designated arbiter and an evidence
// deadline. The arbiter must be a stranger to the dispute: naming a
// party refuses with ErrRulingConflict. Safety notices and declined
// invitations never instruct: they refuse with ErrInvalidCase, and
// one-sided relations refuse with ErrCaseNeedsConsent. Every instant
// arrives per call — the domain never reads the wall clock.
func OpenHearing(entry Case, proposal Proposal, arbiter string, evidenceDueAt time.Time) (Hearing, error) {
	if !entry.IsPrivate() || entry.Status != StatusOpen {
		return Hearing{}, ErrInvalidCase
	}
	if err := proposal.VerifyTermsHash(); err != nil {
		return Hearing{}, err
	}
	if !proposal.Bound() {
		return Hearing{}, ErrCaseNeedsConsent
	}
	if entry.Key != proposal.Key || entry.Claimant != proposal.Claimant || entry.Respondent != proposal.Respondent {
		return Hearing{}, ErrInvalidCase
	}
	boundArbiter, err := parseRulingToken(arbiter)
	if err != nil {
		return Hearing{}, err
	}
	if boundArbiter == proposal.Claimant || boundArbiter == proposal.Respondent {
		return Hearing{}, ErrRulingConflict
	}
	if evidenceDueAt.IsZero() || !entry.OpenedAt.Before(evidenceDueAt) {
		return Hearing{}, ErrInvalidRuling
	}
	return Hearing{
		Kind: entry.Kind, Key: entry.Key,
		Claimant: proposal.Claimant, Respondent: proposal.Respondent,
		Arbiter: boundArbiter, Terms: proposal,
		OpenedAt: entry.OpenedAt.UTC(), EvidenceDueAt: evidenceDueAt.UTC(),
	}, nil
}

// SubmitEvidence files one exhibit under proportional access: only a
// named party files its own exhibit before the evidence deadline.
// Strangers refuse with ErrCaseNotParty, late exhibits refuse with
// ErrLateEvidence, and the deadline is exclusive, like the
// acceptance deadline of the terms.
func (h Hearing) SubmitEvidence(by, digest string, at time.Time) (Hearing, error) {
	if by != h.Claimant && by != h.Respondent {
		return Hearing{}, ErrCaseNotParty
	}
	boundDigest, err := parseRulingToken(digest)
	if err != nil {
		return Hearing{}, err
	}
	if at.IsZero() || at.UTC().Before(h.OpenedAt) {
		return Hearing{}, ErrInvalidRuling
	}
	if !at.UTC().Before(h.EvidenceDueAt) {
		return Hearing{}, ErrLateEvidence
	}
	next := h
	next.Evidence = append(append([]EvidenceItem{}, h.Evidence...), EvidenceItem{
		By: by, Digest: boundDigest, At: at.UTC(),
	})
	return next, nil
}

// Decision is one reasoned private ruling: the verdict within the
// accepted terms, the award capped by the declared value, the
// grounds citing the sealed terms, and the appeal window in UTC.
// The award never exceeds the declared value and no external
// obligation is executed here: custody moves in a later task, once,
// and never by automatic confiscation.
type Decision struct {
	Key          string
	Kind         CaseKind
	Claimant     string
	Respondent   string
	Arbiter      string
	Verdict      Verdict
	AwardMilli   int64
	Rationale    string
	TermsHash    string
	DecidedAt    time.Time
	AppealDueAt  time.Time
	Appealed     bool
	AppealBy     string
	AppealReason string
	AppealAt     time.Time
}

// Decide rules one instructed hearing. Only the designated arbiter
// rules: a party ruling its own case refuses with
// ErrRulingConflict, any other stranger with ErrInvalidRuling. The
// ruling needs a defense from both sides (ErrMissingDefense), an
// award within the declared value (beyond it, ErrBeyondContract)
// and explicit grounds (ErrInvalidRuling without them). The appeal
// window opens here; the appeal itself arrives via Appeal.
func (h Hearing) Decide(by string, verdict Verdict, awardMilli int64, rationale string, decidedAt, appealDueAt time.Time) (Decision, error) {
	if err := h.Terms.VerifyTermsHash(); err != nil {
		return Decision{}, err
	}
	if by != h.Arbiter {
		if by == h.Claimant || by == h.Respondent {
			return Decision{}, ErrRulingConflict
		}
		return Decision{}, ErrInvalidRuling
	}
	switch verdict {
	case VerdictUpholdClaimant, VerdictUpholdRespondent, VerdictPartial:
	default:
		return Decision{}, ErrInvalidRuling
	}
	if awardMilli < 0 {
		return Decision{}, ErrInvalidRuling
	}
	if awardMilli > h.Terms.ValueMilli {
		return Decision{}, ErrBeyondContract
	}
	boundRationale, err := parseRulingText(rationale)
	if err != nil {
		return Decision{}, err
	}
	if decidedAt.IsZero() || decidedAt.UTC().Before(h.OpenedAt) {
		return Decision{}, ErrInvalidRuling
	}
	if appealDueAt.IsZero() || !decidedAt.UTC().Before(appealDueAt.UTC()) {
		return Decision{}, ErrInvalidRuling
	}
	if !h.defended() {
		return Decision{}, ErrMissingDefense
	}
	return Decision{
		Key: h.Key, Kind: h.Kind,
		Claimant: h.Claimant, Respondent: h.Respondent,
		Arbiter: h.Arbiter, Verdict: verdict, AwardMilli: awardMilli,
		Rationale: boundRationale, TermsHash: h.Terms.Hash,
		DecidedAt: decidedAt.UTC(), AppealDueAt: appealDueAt.UTC(),
	}, nil
}

// Appeal contests one ruling once, by a named party within the
// appeal window and with an explicit reason. A second appeal over
// the same ruling refuses with ErrDuplicateAppeal, strangers refuse
// with ErrCaseNotParty, and the window is exclusive, like every
// deadline of this module.
func (d Decision) Appeal(by, reason string, at time.Time) (Decision, error) {
	if d.Appealed {
		return Decision{}, ErrDuplicateAppeal
	}
	if by != d.Claimant && by != d.Respondent {
		return Decision{}, ErrCaseNotParty
	}
	boundReason, err := parseRulingText(reason)
	if err != nil {
		return Decision{}, err
	}
	if at.IsZero() || at.UTC().Before(d.DecidedAt) || !at.UTC().Before(d.AppealDueAt) {
		return Decision{}, ErrInvalidRuling
	}
	next := d
	next.Appealed = true
	next.AppealBy = by
	next.AppealReason = boundReason
	next.AppealAt = at.UTC()
	return next, nil
}
