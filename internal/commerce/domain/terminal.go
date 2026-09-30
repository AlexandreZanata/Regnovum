package domain

// TerminalOutcome is the pure classification of one escrow after the
// barrier and drainage of P46-T09, per section 5.1 of
// docs/reino/TEMPORADAS_SUCESSAO.md. T07 delivers the
// classification and its blocking result; the worker, the global
// barrier, the terminalization in CLOSING, the seal and the
// successor belong to T09/T10.
type TerminalOutcome string

const (
	// TerminalNoEffect preserves the receipt with no new terminal
	// effect: released, refunded and resolved contracts never move
	// again. A later authorized refund is a separate linked event,
	// not a reopening of the escrow.
	TerminalNoEffect TerminalOutcome = "no-effect"
	// TerminalRelease pays the provider under the accepted clause
	// with the standing tithe and rounding, creating no rate.
	TerminalRelease TerminalOutcome = "release"
	// TerminalRefund cancels the seasonal obligation under the
	// previously accepted clause and returns the retained
	// principal to the buyer. Escrow deposit and return are not
	// settled performance and create no tithe.
	TerminalRefund TerminalOutcome = "refund"
	// TerminalBlocked preserves custody and returns BLOCKED: the
	// evidence is missing, pending, conflicting or otherwise
	// undecidable. Unknown or unavailable state also blocks;
	// absence of data never proves absence of litigation, and no
	// new judgment is inferred here. T09 refuses the seal while
	// any BLOCKED remains; T10 opens no successor without a seal.
	TerminalBlocked TerminalOutcome = "blocked"
)

// String returns the stored outcome value.
func (o TerminalOutcome) String() string { return string(o) }

// TerminalEvidence carries the authoritative facts the classifier
// may read: whether the buyer acceptance was admitted before the
// cutoff, whether the escrow is proven uncontested, whether a
// competent final persisted resolution exists with its direction,
// whether the terms and accepts are complete, whether the stored
// integrity verifies, and whether the evidence source itself was
// available. Litigation and resolution arrive through an
// authoritative port, never as a free client boolean: the caller
// proves each flag from procedure P39 or a valid party agreement,
// and unknown or unavailable resolves to blocked here.
type TerminalEvidence struct {
	TermsComplete        bool
	AcceptedBeforeCutoff bool
	Uncontested          bool
	EvidenceAvailable    bool
	IntegrityOK          bool
	ResolutionFinal      bool
	Resolution           ResolveDecision
}

// ClassifyTerminal maps one escrow status with its authoritative
// evidence to its terminal outcome:
//
//   - released, refunded, resolved: no new effect, receipt preserved.
//   - accepted: release only with complete terms, acceptance
//     admitted before the cutoff, proven absence of litigation or
//     impediment, available evidence and verified integrity.
//   - funded without accepted delivery and proven uncontested:
//     refund of the retained principal under the accepted clause.
//   - expired: only the competent final persisted resolution
//     decides release or refund; expiry alone never chooses.
//   - pending litigation or timely appeal, conflicting or non-final
//     decision, uncertain ownership, missing terms or accepts,
//     divergent integrity, unknown state or unavailable source:
//     BLOCKED with custody preserved.
//
// Payment and return use only the balance actually retained in the
// original book, without duplicating settled parcels and without
// partial settlement the model does not support: principal,
// custody or receipt divergence blocks. There is no carry-over,
// mint, confiscation to the Crown, elimination of litigation or
// cancellation of external obligation. Legacy contracts without
// this clause keep their rules and are never terminalized
// automatically.
func ClassifyTerminal(status ContractStatus, evidence TerminalEvidence) TerminalOutcome {
	if status == ContractReleased || status == ContractRefunded || status == ContractResolved {
		return TerminalNoEffect
	}
	if !evidence.EvidenceAvailable || !evidence.IntegrityOK || !evidence.TermsComplete {
		return TerminalBlocked
	}
	switch status {
	case ContractAccepted:
		if !evidence.AcceptedBeforeCutoff || !evidence.Uncontested {
			return TerminalBlocked
		}
		return TerminalRelease
	case ContractFunded:
		if !evidence.Uncontested {
			return TerminalBlocked
		}
		return TerminalRefund
	case ContractExpired:
		if !evidence.ResolutionFinal {
			return TerminalBlocked
		}
		switch evidence.Resolution {
		case ResolveRelease:
			return TerminalRelease
		case ResolveRefund:
			return TerminalRefund
		default:
			return TerminalBlocked
		}
	default:
		return TerminalBlocked
	}
}
