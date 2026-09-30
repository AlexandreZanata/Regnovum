package domain

import "time"

// SettlementOrigin is the closed source vocabulary of one escrow
// release: a bilateral agreement or an authorized final ruling. No
// external obligation opens a release: custody moves only from these
// two origins, never by automatic confiscation.
type SettlementOrigin string

const (
	// SettlementAgreement releases escrow from a bilateral
	// agreement: both named parties approve, one of them is paid.
	SettlementAgreement SettlementOrigin = "agreement"
	// SettlementRuling releases escrow from a final ruling: the
	// appeal window lapsed with no appeal, and the payee follows
	// the verdict.
	SettlementRuling SettlementOrigin = "ruling"
)

// ParseSettlementOrigin validates an origin token. Matching is
// exact: no trimming, no case folding.
func ParseSettlementOrigin(raw string) (SettlementOrigin, error) {
	origin := SettlementOrigin(raw)
	switch origin {
	case SettlementAgreement, SettlementRuling:
		return origin, nil
	default:
		return "", ErrInvalidSettlement
	}
}

// String returns the stored origin value.
func (o SettlementOrigin) String() string { return string(o) }

// parseSettlementToken validates one opaque settlement token (claim
// key) and reports it as a settlement refusal.
func parseSettlementToken(raw string) (string, error) {
	if bound, err := parseTermsToken(raw); err != nil {
		return "", ErrInvalidSettlement
	} else {
		return bound, nil
	}
}

// ReleaseClaim is one recorded escrow release: the idempotency key,
// the origin, who was paid, how much in milliINK and when in UTC.
// The key makes crash recovery safe by construction: replaying the
// same key returns the custody unchanged, while a divergent claim
// under a recorded key conflicts instead of paying twice.
type ReleaseClaim struct {
	ClaimKey    string
	Origin      SettlementOrigin
	Payee       string
	AmountMilli int64
	At          time.Time
}

// Escrow is one private custody: the negotiation key, the two named
// parties, the pledged escrow reference, the declared value, the
// sealed terms hash, the remaining balance and the recorded claims.
// Nothing here moves value: this is the release authorization the
// future adapter executes once, and third parties never appear in
// it — custody of non-participants is preserved by construction.
type Escrow struct {
	Key          string
	Claimant     string
	Respondent   string
	EscrowRef    string
	ValueMilli   int64
	TermsHash    string
	BalanceMilli int64
	Claims       []ReleaseClaim
}

// Closed reports whether the custody holds nothing left to release.
func (e Escrow) Closed() bool { return e.BalanceMilli == 0 }

// ReleasedTotal reports the sum of the recorded releases.
func (e Escrow) ReleasedTotal() int64 {
	var total int64
	for _, claim := range e.Claims {
		total += claim.AmountMilli
	}
	return total
}

// OpenEscrow opens the custody of one bound proposal. Proposals
// without a pledged escrow refuse with ErrNoEscrow: there is nothing
// to release and no external account is ever debited instead.
func OpenEscrow(proposal Proposal) (Escrow, error) {
	if err := proposal.VerifyTermsHash(); err != nil {
		return Escrow{}, err
	}
	if !proposal.Bound() {
		return Escrow{}, ErrCaseNeedsConsent
	}
	if proposal.EscrowRef == "" {
		return Escrow{}, ErrNoEscrow
	}
	if proposal.ValueMilli <= 0 {
		return Escrow{}, ErrInvalidSettlement
	}
	return Escrow{
		Key:      proposal.Key,
		Claimant: proposal.Claimant, Respondent: proposal.Respondent,
		EscrowRef: proposal.EscrowRef, ValueMilli: proposal.ValueMilli,
		TermsHash: proposal.Hash, BalanceMilli: proposal.ValueMilli,
	}, nil
}

// replayClaim returns the custody unchanged when the claim key is
// already recorded with identical terms, or refuses a divergent
// claim under a recorded key. It reports whether the caller is a
// replay.
func (e Escrow) replayClaim(claimKey string, origin SettlementOrigin, payee string, amountMilli int64) (Escrow, bool, error) {
	for _, recorded := range e.Claims {
		if recorded.ClaimKey != claimKey {
			continue
		}
		if recorded.Origin == origin && recorded.Payee == payee && recorded.AmountMilli == amountMilli {
			return e, true, nil
		}
		return Escrow{}, false, ErrSettlementConflict
	}
	return e, false, nil
}

// checkPayee refuses releases to strangers: only a named party is
// ever paid, so third-party custody is never touched.
func (e Escrow) checkPayee(payee string) error {
	if payee != e.Claimant && payee != e.Respondent {
		return ErrCaseNotParty
	}
	return nil
}

// checkAmount refuses non-positive amounts and amounts beyond the
// remaining balance. A drained custody refuses new keys with
// ErrAlreadyReleased: the escrow releases once, never twice.
func (e Escrow) checkAmount(amountMilli int64) error {
	if amountMilli <= 0 {
		return ErrInvalidSettlement
	}
	if e.Closed() {
		return ErrAlreadyReleased
	}
	if amountMilli > e.BalanceMilli {
		return ErrBeyondContract
	}
	return nil
}

// record appends one release claim and moves the balance. Callers
// run replayClaim, checkPayee and checkAmount first.
func (e Escrow) record(claimKey string, origin SettlementOrigin, payee string, amountMilli int64, at time.Time) Escrow {
	next := e
	next.Claims = append(append([]ReleaseClaim{}, e.Claims...), ReleaseClaim{
		ClaimKey: claimKey, Origin: origin, Payee: payee,
		AmountMilli: amountMilli, At: at.UTC(),
	})
	next.BalanceMilli -= amountMilli
	return next
}

// ReleaseByAgreement releases escrow from a bilateral agreement: the
// two named parties approve together (any order) and one of them is
// paid. Partial agreements are welcome: any positive amount up to
// the balance releases, and the remainder stays in custody. Every
// instant arrives per call — the domain never reads the wall clock.
func (e Escrow) ReleaseByAgreement(claimKey, payee string, amountMilli int64, agreedBy [2]string, at time.Time) (Escrow, error) {
	boundKey, err := parseSettlementToken(claimKey)
	if err != nil {
		return Escrow{}, err
	}
	if next, replay, err := e.replayClaim(boundKey, SettlementAgreement, payee, amountMilli); err != nil || replay {
		return next, err
	}
	if agreedBy[0] == agreedBy[1] {
		return Escrow{}, ErrCaseNeedsConsent
	}
	for _, approver := range agreedBy {
		if approver != e.Claimant && approver != e.Respondent {
			return Escrow{}, ErrCaseNotParty
		}
	}
	if !((agreedBy[0] == e.Claimant && agreedBy[1] == e.Respondent) ||
		(agreedBy[0] == e.Respondent && agreedBy[1] == e.Claimant)) {
		return Escrow{}, ErrCaseNeedsConsent
	}
	if err := e.checkPayee(payee); err != nil {
		return Escrow{}, err
	}
	if err := e.checkAmount(amountMilli); err != nil {
		return Escrow{}, err
	}
	if at.IsZero() {
		return Escrow{}, ErrInvalidSettlement
	}
	return e.record(boundKey, SettlementAgreement, payee, amountMilli, at), nil
}

// ReleaseByRuling releases escrow from an authorized final ruling:
// the appeal window lapsed with no appeal pending. The payee follows
// the verdict: the upheld side is paid, and a partial verdict pays
// either named party. Rulings under appeal, inside the appeal window
// or bound to other terms refuse; strangers are never paid.
func (e Escrow) ReleaseByRuling(claimKey string, decision Decision, payee string, at time.Time) (Escrow, error) {
	boundKey, err := parseSettlementToken(claimKey)
	if err != nil {
		return Escrow{}, err
	}
	if next, replay, err := e.replayClaim(boundKey, SettlementRuling, payee, decision.AwardMilli); err != nil || replay {
		return next, err
	}
	if decision.Key != e.Key || decision.Claimant != e.Claimant || decision.Respondent != e.Respondent || decision.TermsHash != e.TermsHash {
		return Escrow{}, ErrInvalidSettlement
	}
	if at.IsZero() || at.UTC().Before(decision.AppealDueAt) {
		return Escrow{}, ErrNotFinal
	}
	if decision.Appealed {
		return Escrow{}, ErrNotFinal
	}
	switch decision.Verdict {
	case VerdictUpholdClaimant:
		if payee != e.Claimant {
			return Escrow{}, ErrInvalidSettlement
		}
	case VerdictUpholdRespondent:
		if payee != e.Respondent {
			return Escrow{}, ErrInvalidSettlement
		}
	case VerdictPartial:
		if err := e.checkPayee(payee); err != nil {
			return Escrow{}, err
		}
	default:
		return Escrow{}, ErrInvalidRuling
	}
	if err := e.checkAmount(decision.AwardMilli); err != nil {
		return Escrow{}, err
	}
	return e.record(boundKey, SettlementRuling, payee, decision.AwardMilli, at), nil
}
