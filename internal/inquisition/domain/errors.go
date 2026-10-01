package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for
// inquisition rule violations.
type ErrorCode string

const (
	// CodeInvalidReport names a malformed filing: a blank or
	// abusive token, a self-report, a missing reason or a missing
	// instant. Shape refuses before jurisdiction is judged.
	CodeInvalidReport ErrorCode = "INQUISITION_INVALID_REPORT"
	// CodeUnknownViolation names a violation outside the closed
	// safety vocabulary: only threat, fraud, harassment, data
	// abuse and malware authorize institutional action, never by
	// inference.
	CodeUnknownViolation ErrorCode = "INQUISITION_UNKNOWN_VIOLATION"
	// CodePrivateMatter names a private debt or private challenge
	// offered as a safety case: bilateral matters belong to
	// private dispute, never to institutional sanction.
	CodePrivateMatter ErrorCode = "INQUISITION_PRIVATE_MATTER"
	// CodeUnprovenAccusation names a containment over an
	// unevidenced report: a private accusation without proof
	// activates no sanction.
	CodeUnprovenAccusation ErrorCode = "INQUISITION_UNPROVEN_ACCUSATION"
	// CodeUngroundedContainment names a containment without
	// recorded authority, without motive, or without a bounded
	// temporary window: risk reports contain only with all three
	// on record.
	CodeUngroundedContainment ErrorCode = "INQUISITION_UNGROUNDED_CONTAINMENT"
	// CodeAmbiguousCharter names a report without an explicit
	// charter version: facts bind to one published vN, never to
	// "current" or "latest" by inference.
	CodeAmbiguousCharter ErrorCode = "INQUISITION_AMBIGUOUS_CHARTER"
	// CodeInterestedProsecution names an opening by an interested
	// inquisitor or a panel with a financial or relational
	// conflict: the opener is never the reporter or the accused
	// and never belongs to the interested set, and the arbiter
	// and the auditor never belong to it either.
	CodeInterestedProsecution ErrorCode = "INQUISITION_INTERESTED_PROSECUTION"
	// CodeSelfReview names a proceeding where one holder reviews
	// its own act: inquisitor, arbiter and auditor are three
	// distinct holders, and a candidate never reviews its own
	// succession eligibility.
	CodeSelfReview ErrorCode = "INQUISITION_SELF_REVIEW"
	// CodeDuplicateCase names a second opening of the same case
	// or of the same report: one report opens at most one severe
	// case, replay authorizes nothing new.
	CodeDuplicateCase ErrorCode = "INQUISITION_DUPLICATE_CASE"
	// CodeExpiredMandate names an opening outside the opener
	// mandate: the mandate holder opens only inside its issued
	// window, never before it and never at or past its end.
	CodeExpiredMandate ErrorCode = "INQUISITION_EXPIRED_MANDATE"
	// CodeConflictedThrone names a King with an interest in the
	// throne dispute deciding or reviewing it: an interested
	// King neither seats the severe panel nor reviews a rival
	// eligibility.
	CodeConflictedThrone ErrorCode = "INQUISITION_CONFLICTED_THRONE"
	// CodeUnpublishedProceeding names an opening without a
	// published competence and deadline: competence arrives as
	// stated text and the deadline arrives after the opening
	// inside the allowed cap.
	CodeUnpublishedProceeding ErrorCode = "INQUISITION_UNPUBLISHED_PROCEEDING"
	// CodeTamperedEvidence names a disclosure whose digest no
	// longer matches the seal: evidence swapped after sealing
	// discloses nothing.
	CodeTamperedEvidence ErrorCode = "INQUISITION_TAMPERED_EVIDENCE"
	// CodeUnnotifiedDefense names a disclosure without a prior
	// notice to the defense: nobody meets sealed evidence
	// without being notified first, and waivers without a
	// stated motive open no unsealed phase.
	CodeUnnotifiedDefense ErrorCode = "INQUISITION_UNNOTIFIED_DEFENSE"
	// CodeUnsafeEvidence names a dangerous file offered as
	// evidence: executables and scripts never circulate as
	// proof, even sealed.
	CodeUnsafeEvidence ErrorCode = "INQUISITION_UNSAFE_EVIDENCE"
	// CodeExposedVictim names a disclosure leaking victim or
	// minor identity: evidence carrying victim or minor data
	// discloses only redacted, and the disclosed view never
	// carries the raw payload.
	CodeExposedVictim ErrorCode = "INQUISITION_EXPOSED_VICTIM"
	// CodeUnsealedPhase names a severe case without its
	// mandatory sealed phase: the case carries a sealed
	// envelope or a motivated waiver, never neither.
	CodeUnsealedPhase ErrorCode = "INQUISITION_UNSEALED_PHASE"
)

// DomainError represents an invariant or rule failure in the
// inquisition domain.
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
	// ErrInvalidReport refuses a malformed filing before anything
	// is read.
	ErrInvalidReport = DomainError{
		Code:    CodeInvalidReport,
		Message: "the filing is malformed: whole identifiers, two distinct subjects, a stated reason and a live instant are all required",
	}
	// ErrUnknownViolation refuses a kind outside the closed safety
	// vocabulary.
	ErrUnknownViolation = DomainError{
		Code:    CodeUnknownViolation,
		Message: "unknown safety violation: only ameaca, fraude, assedio, dados and malware authorize institutional action",
	}
	// ErrPrivateMatter refuses a private debt or challenge as a
	// safety case.
	ErrPrivateMatter = DomainError{
		Code:    CodePrivateMatter,
		Message: "the matter is private: debt and private challenge belong to bilateral dispute, never to institutional sanction",
	}
	// ErrUnprovenAccusation refuses to contain on an unevidenced
	// report.
	ErrUnprovenAccusation = DomainError{
		Code:    CodeUnprovenAccusation,
		Message: "the accusation carries no proof: reports without sealed evidence activate no sanction",
	}
	// ErrUngroundedContainment refuses a containment without
	// recorded authority, motive or bounded window.
	ErrUngroundedContainment = DomainError{
		Code:    CodeUngroundedContainment,
		Message: "the containment is ungrounded: recorded authority, motive and a bounded temporary window are all required",
	}
	// ErrAmbiguousCharter refuses a report without an explicit
	// version.
	ErrAmbiguousCharter = DomainError{
		Code:    CodeAmbiguousCharter,
		Message: "the charter version is ambiguous: reports bind to one published vN, never to current or latest by inference",
	}
	// ErrInterestedProsecution refuses an opening by an
	// interested holder or a panel with a conflict.
	ErrInterestedProsecution = DomainError{
		Code:    CodeInterestedProsecution,
		Message: "the prosecution is interested: the opener is never the reporter or the accused and the panel never carries a financial or relational conflict",
	}
	// ErrSelfReview refuses one holder reviewing its own act.
	ErrSelfReview = DomainError{
		Code:    CodeSelfReview,
		Message: "the review is not independent: inquisitor, arbiter and auditor are three distinct holders and nobody reviews its own eligibility",
	}
	// ErrDuplicateCase refuses a second opening of the same case
	// or report.
	ErrDuplicateCase = DomainError{
		Code:    CodeDuplicateCase,
		Message: "the case already exists: one report opens at most one severe case, replay authorizes nothing new",
	}
	// ErrExpiredMandate refuses an opening outside the opener
	// mandate window.
	ErrExpiredMandate = DomainError{
		Code:    CodeExpiredMandate,
		Message: "the mandate expired: the holder opens only inside its issued window, never before it and never at or past its end",
	}
	// ErrConflictedThrone refuses an interested King deciding or
	// reviewing the throne dispute.
	ErrConflictedThrone = DomainError{
		Code:    CodeConflictedThrone,
		Message: "the throne is conflicted: an interested King neither seats the severe panel nor reviews a rival eligibility",
	}
	// ErrUnpublishedProceeding refuses an opening without a
	// published competence and deadline.
	ErrUnpublishedProceeding = DomainError{
		Code:    CodeUnpublishedProceeding,
		Message: "the proceeding is unpublished: competence arrives as stated text and the deadline arrives after the opening inside the allowed cap",
	}
	// ErrTamperedEvidence refuses a disclosure whose digest no
	// longer matches the seal.
	ErrTamperedEvidence = DomainError{
		Code:    CodeTamperedEvidence,
		Message: "the evidence was swapped after the seal: disclosure binds the sealed digest or discloses nothing",
	}
	// ErrUnnotifiedDefense refuses a disclosure without prior
	// notice to the defense.
	ErrUnnotifiedDefense = DomainError{
		Code:    CodeUnnotifiedDefense,
		Message: "the defense was not notified: sealed evidence discloses only to the notified holder after the notice",
	}
	// ErrUnsafeEvidence refuses a dangerous file as evidence.
	ErrUnsafeEvidence = DomainError{
		Code:    CodeUnsafeEvidence,
		Message: "the file is unsafe: executables and scripts never circulate as evidence, even sealed",
	}
	// ErrExposedVictim refuses a disclosure leaking victim or
	// minor identity.
	ErrExposedVictim = DomainError{
		Code:    CodeExposedVictim,
		Message: "the disclosure leaks the victim: evidence carrying victim or minor data discloses only redacted and never carries the raw payload",
	}
	// ErrUnsealedPhase refuses a severe case without a sealed
	// envelope or a motivated waiver.
	ErrUnsealedPhase = DomainError{
		Code:    CodeUnsealedPhase,
		Message: "the sealed phase is missing: a severe case carries a sealed envelope bound to the case or a motivated waiver, never neither",
	}
)
