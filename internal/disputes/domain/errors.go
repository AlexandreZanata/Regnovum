package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	// CodeInvalidTerms names malformed arbitration terms: blank or
	// abusive tokens, parties that are not two distinct accounts, a
	// negative value, a missing deadline or an unknown costs rule.
	CodeInvalidTerms ErrorCode = "DISPUTES_INVALID_TERMS"
	// CodeTermsExpired names an acceptance past the proposal
	// deadline: expired terms never bind, and dead terms are never
	// proposed.
	CodeTermsExpired ErrorCode = "DISPUTES_TERMS_EXPIRED"
	// CodeTermsNotParty names a stranger speaking for a proposal:
	// only the two named parties accept their own terms.
	CodeTermsNotParty ErrorCode = "DISPUTES_TERMS_NOT_PARTY"
	// CodeInvalidCase names a malformed case entry: an unknown kind,
	// a blank or abusive report key, reporter or reason, a missing
	// opening instant or a consent entry routed to the safety rite.
	CodeInvalidCase ErrorCode = "DISPUTES_INVALID_CASE"
	// CodeCaseNeedsConsent names a consent-based case without both
	// acceptances: one-sided or silent relations never open a
	// private case.
	CodeCaseNeedsConsent ErrorCode = "DISPUTES_CASE_NEEDS_CONSENT"
	// CodeCaseNotParty names a stranger opening or declining someone
	// else's private case: only a named party moves its own case.
	CodeCaseNotParty ErrorCode = "DISPUTES_CASE_NOT_PARTY"
	// CodeInvalidRuling names a malformed ruling entry: a blank or
	// abusive arbiter, digest, grounds or reason, an unknown
	// verdict, a negative award, a missing instant or a ruling by
	// someone other than the designated arbiter.
	CodeInvalidRuling ErrorCode = "DISPUTES_INVALID_RULING"
	// CodeRulingConflict names an arbiter with an interest in the
	// case: a party ruling its own dispute, or a party named as
	// arbiter.
	CodeRulingConflict ErrorCode = "DISPUTES_RULING_CONFLICT"
	// CodeLateEvidence names an exhibit filed past the evidence
	// deadline: late proof never joins the record.
	CodeLateEvidence ErrorCode = "DISPUTES_LATE_EVIDENCE"
	// CodeMissingDefense names a ruling without a defense from both
	// sides: each named party files at least one exhibit before the
	// arbiter rules.
	CodeMissingDefense ErrorCode = "DISPUTES_MISSING_DEFENSE"
	// CodeBeyondContract names a ruling beyond the accepted terms:
	// the award never exceeds the declared value.
	CodeBeyondContract ErrorCode = "DISPUTES_BEYOND_CONTRACT"
	// CodeDuplicateAppeal names a second appeal over one ruling:
	// the previste recurso opens once.
	CodeDuplicateAppeal ErrorCode = "DISPUTES_DUPLICATE_APPEAL"
)

// DomainError represents an invariant or rule failure in the disputes domain.
type DomainError struct {
	Code    ErrorCode
	Message string
}

func (e DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e DomainError) Is(target error) bool {
	t, ok := target.(DomainError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

var (
	// ErrInvalidTerms refuses arbitration terms that cannot name an
	// obligation: the proposal needs a key, a revision, an object,
	// two distinct parties, a rite, evidence rules, a costs rule, a
	// deadline, execution terms and a non-negative value.
	ErrInvalidTerms = DomainError{
		Code:    CodeInvalidTerms,
		Message: "arbitration terms need a key, a revision, an object, two distinct parties, a rite, evidence rules, a costs rule, a deadline and execution terms",
	}
	// ErrTermsExpired refuses acceptances past the deadline:
	// expired proposals never open a case.
	ErrTermsExpired = DomainError{
		Code:    CodeTermsExpired,
		Message: "expired proposal terms never bind: acceptance needs a live deadline",
	}
	// ErrTermsNotParty refuses a stranger accepting someone else's
	// proposal: only a named party accepts its own terms.
	ErrTermsNotParty = DomainError{
		Code:    CodeTermsNotParty,
		Message: "only a named party accepts its own proposal terms: strangers never bind a case",
	}
	// ErrInvalidCase refuses a case entry that cannot name a
	// competence: the entry needs a known kind, a report key with a
	// reporter and a reason for safety reports, and a live opening
	// instant; consent entries never travel the safety rite.
	ErrInvalidCase = DomainError{
		Code:    CodeInvalidCase,
		Message: "case entry needs a known kind, a sealed bilateral proposal for consent cases or a keyed safety report, and a live opening instant",
	}
	// ErrCaseNeedsConsent refuses a consent-based case without both
	// acceptances: challenge, agreement and arbitration open only
	// over bound terms.
	ErrCaseNeedsConsent = DomainError{
		Code:    CodeCaseNeedsConsent,
		Message: "consent-based cases open only over bilateral terms: one-sided relations never open a private case",
	}
	// ErrCaseNotParty refuses a stranger opening or declining
	// someone else's private case: only a named party moves its own
	// case.
	ErrCaseNotParty = DomainError{
		Code:    CodeCaseNotParty,
		Message: "only a named party opens or declines its own private case: strangers never move a case",
	}
	// ErrInvalidRuling refuses a ruling entry that cannot name its
	// rite: the entry needs a known verdict, an impartial arbiter,
	// a non-negative award, explicit grounds and live instants.
	ErrInvalidRuling = DomainError{
		Code:    CodeInvalidRuling,
		Message: "ruling entry needs a known verdict, the designated arbiter, explicit grounds and live instants",
	}
	// ErrRulingConflict refuses an arbiter with an interest in the
	// case: parties never rule their own dispute.
	ErrRulingConflict = DomainError{
		Code:    CodeRulingConflict,
		Message: "impartial arbiter only: a party never rules its own dispute",
	}
	// ErrLateEvidence refuses an exhibit past the evidence deadline:
	// late proof never joins the record.
	ErrLateEvidence = DomainError{
		Code:    CodeLateEvidence,
		Message: "late proof never joins the record: exhibits arrive before the evidence deadline",
	}
	// ErrMissingDefense refuses a ruling without both defenses:
	// each named party files at least one exhibit first.
	ErrMissingDefense = DomainError{
		Code:    CodeMissingDefense,
		Message: "no ruling without both defenses: each named party files at least one exhibit first",
	}
	// ErrBeyondContract refuses a ruling beyond the accepted terms:
	// the award never exceeds the declared value.
	ErrBeyondContract = DomainError{
		Code:    CodeBeyondContract,
		Message: "the ruling applies the accepted terms only: the award never exceeds the declared value",
	}
	// ErrDuplicateAppeal refuses a second appeal over one ruling:
	// the previsto recurso opens once.
	ErrDuplicateAppeal = DomainError{
		Code:    CodeDuplicateAppeal,
		Message: "one appeal per ruling: a second appeal over the same ruling refuses",
	}
)
