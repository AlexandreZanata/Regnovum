package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for crown rule violations.
type ErrorCode string

const (
	// CodeInvalidAuthority names a malformed or partial credential:
	// a blank or abusive token, a zero reign, a missing instant or
	// a session window no policy could have issued.
	CodeInvalidAuthority ErrorCode = "CROWN_INVALID_AUTHORITY"
	// CodeOperatorNotSovereign names an operator credential offered
	// as a game office: the technical administrator never becomes
	// King by alias.
	CodeOperatorNotSovereign ErrorCode = "CROWN_OPERATOR_NOT_SOVEREIGN"
	// CodeSeasonMismatch names a claim outside the current book: the
	// authority of one season never decides in another.
	CodeSeasonMismatch ErrorCode = "CROWN_SEASON_MISMATCH"
	// CodeSeasonClosed names a claim outside the open window: before
	// the start, at or past the exclusive end, or while suspended.
	CodeSeasonClosed ErrorCode = "CROWN_SEASON_CLOSED"
	// CodeStaleReign names a claim from a superseded reign: a new
	// investiture revokes the previous holder without touching any
	// account.
	CodeStaleReign ErrorCode = "CROWN_STALE_REIGN"
	// CodeFutureReign names a claim from a reign nobody invested:
	// authority only moves forward through investiture.
	CodeFutureReign ErrorCode = "CROWN_FUTURE_REIGN"
	// CodeNotHolder names a claimant the current reign does not
	// invest: strangers and superseded holders decide nothing,
	// with or without a delegation they cannot chain.
	CodeNotHolder ErrorCode = "CROWN_NOT_HOLDER"
	// CodeInvalidSession names a session that cannot authenticate:
	// missing step-up, time travel, or a window longer than the
	// short session the call allows.
	CodeInvalidSession ErrorCode = "CROWN_INVALID_SESSION"
	// CodeSessionExpired names a live-shaped session whose window
	// already passed: replaying it authorizes nothing new.
	CodeSessionExpired ErrorCode = "CROWN_SESSION_EXPIRED"
	// CodeInvalidDelegation names a delegation that cannot chain:
	// wrong book, wrong reign, widened competence, self-theater, or
	// a delegator who no longer holds the office.
	CodeInvalidDelegation ErrorCode = "CROWN_INVALID_DELEGATION"
	// CodeDelegationExpired names a well-chained delegation past its
	// expiry: operational power ends where its window ends.
	CodeDelegationExpired ErrorCode = "CROWN_DELEGATION_EXPIRED"
	// CodeGrantExpired names a reused grant past its session: a
	// recorded authorization never outlives the session that made it.
	CodeGrantExpired ErrorCode = "CROWN_GRANT_EXPIRED"
	// CodeUnknownAct names a decree kind outside the closed royal
	// vocabulary: season alterations of deadline, Genesis or wealth
	// criteria never become a kind by inference.
	CodeUnknownAct ErrorCode = "CROWN_UNKNOWN_ACT"
	// CodeIncompleteAct names a decree missing a required field or
	// carrying an economic payload outside the economic kind: the
	// shape refuses before any effect.
	CodeIncompleteAct ErrorCode = "CROWN_INCOMPLETE_ACT"
	// CodeRetroactiveAct names a decree whose vigour starts before
	// its date: harmful backdating refuses, even when the text
	// claims a correction (corrections arrive as new prospective
	// acts linked to the original).
	CodeRetroactiveAct ErrorCode = "CROWN_RETROACTIVE_ACT"
	// CodeUnknownOrigin names an economic decree without a known
	// funding origin: value never moves from nowhere.
	CodeUnknownOrigin ErrorCode = "CROWN_UNKNOWN_ORIGIN"
	// CodeAmbiguousCharter names a decree without an explicit
	// charter version: facts bind to one published vN, never to
	// "current" or "latest" by inference.
	CodeAmbiguousCharter ErrorCode = "CROWN_AMBIGUOUS_CHARTER"
	// CodeSelfApproval names a check by its own author: the author
	// never reviews its own irreversible act.
	CodeSelfApproval ErrorCode = "CROWN_SELF_APPROVAL"
	// CodeDuplicateApproval names two checks that are not
	// independent: same identifier or same checker replayed.
	CodeDuplicateApproval ErrorCode = "CROWN_DUPLICATE_APPROVAL"
	// CodeTamperedAct names a check bound to another payload: any
	// change of identity, vigour, target, effect, value or charter
	// breaks both checks first.
	CodeTamperedAct ErrorCode = "CROWN_TAMPERED_ACT"
	// CodeCheckExpired names a check outside its live window:
	// spent approvals authorize nothing new.
	CodeCheckExpired ErrorCode = "CROWN_CHECK_EXPIRED"
	// CodeEmergencyWithoutReview names an urgent containment
	// without scheduled review: urgency only contains temporarily
	// with review, never executes finally alone.
	CodeEmergencyWithoutReview ErrorCode = "CROWN_EMERGENCY_WITHOUT_REVIEW"
	// CodeNonMonetaryAct names an execution request over a decree
	// whose kind never moves value: only the economic kind carries
	// origin and amount, so any other kind stops before custody.
	CodeNonMonetaryAct ErrorCode = "CROWN_NON_MONETARY_ACT"
	// CodeForbiddenOrigin names an execution funded outside the
	// free treasury: escrow, contract, title, principal alheio and
	// confiscation without an independent procedure never fund a
	// crown move.
	CodeForbiddenOrigin ErrorCode = "CROWN_FORBIDDEN_ORIGIN"
	// CodeSelfGrant names a decree paying its own author: the King
	// never grants treasury to itself, directly or by alias.
	CodeSelfGrant ErrorCode = "CROWN_SELF_GRANT"
	// CodeInsufficientTreasury names an execution beyond the free
	// treasury availability: conserved custody moves at most what
	// is available, never by mint.
	CodeInsufficientTreasury ErrorCode = "CROWN_INSUFFICIENT_TREASURY"
	// CodeExecutionFrozen names an execution while the book is
	// frozen: a security freeze stops every movement, never
	// extends the calendar and never picks a beneficiary.
	CodeExecutionFrozen ErrorCode = "CROWN_EXECUTION_FROZEN"
	// CodeConflictedBenefit names a benefit with an unresolved
	// conflict and no independent review: legitimate reparation
	// passes an independent procedure, never the beneficiary.
	// CodeAnonymousRecord names a book entry without authorship:
	// every royal fact names who issued it, or it is not recorded.
	CodeAnonymousRecord ErrorCode = "CROWN_ANONYMOUS_RECORD"
	// CodeDetachedCorrection names a correction that links no
	// recorded original: a correction never floats alone, and the
	// original stays visible beside it.
	CodeDetachedCorrection ErrorCode = "CROWN_DETACHED_CORRECTION"
	// CodeUnrecordedAct names an executed act missing from the
	// book: hidden decrees decide nothing, every act is published.
	CodeUnrecordedAct ErrorCode = "CROWN_UNRECORDED_ACT"
	// CodeImproperPetitioner names a petition outside the affected
	// channel: the act author appeals nothing, corrections of own
	// errors arrive as new linked acts, never as petitions.
	CodeImproperPetitioner ErrorCode = "CROWN_IMPROPER_PETITIONER"
	// CodeInterestedAuditor names a review by an interested party:
	// the author, the petitioner and the responsible never audit
	// their own action.
	CodeInterestedAuditor ErrorCode = "CROWN_INTERESTED_AUDITOR"
	// CodeUnreviewedRepair names a correction without a repair
	// verdict: errors are corrected by new acts under independent
	// review, never by solitary rewrite.
	CodeUnreviewedRepair ErrorCode = "CROWN_UNREVIEWED_REPAIR"
	// CodeTreasuryFundedRestitution names a restitution charged to
	// the free treasury: the responsible funds the repair from own
	// custody, never the public vault.
	CodeTreasuryFundedRestitution ErrorCode = "CROWN_TREASURY_FUNDED_RESTITUTION"
	CodeConflictedBenefit         ErrorCode = "CROWN_CONFLICTED_BENEFIT"
	// CodeUnknownOffice names an office outside the closed game
	// vocabulary: inquisitor, justice, defender, witness, arbiter,
	// auditor, executor and certifier only, never by inference.
	CodeUnknownOffice ErrorCode = "CROWN_UNKNOWN_OFFICE"
	// CodeOfficeExpired names a mandate past its window: game
	// offices never survive the season end without a fresh valid
	// designation.
	CodeOfficeExpired ErrorCode = "CROWN_OFFICE_EXPIRED"
	// CodeConflictedOffice names an accuser judging, an interested
	// witness or executor, or the King judging a rival dispute in
	// own cause: conflicts decide nothing.
	CodeConflictedOffice ErrorCode = "CROWN_CONFLICTED_OFFICE"
	// CodeOutcomePay names a fee conditioned on conviction: game
	// offices are paid per service, never per result.
	CodeOutcomePay ErrorCode = "CROWN_OUTCOME_PAY"
	// CodeInvalidBlessing names a blessing without a new act, for a
	// third party, by the beneficiary, with the sanction erased, or
	// without the required charter acceptance: reset alone removes
	// no sanction and a new act reopens only authentication.
	CodeInvalidBlessing ErrorCode = "CROWN_INVALID_BLESSING"
	// CodeMintedBlessing names a return carrying value or sealed
	// wealth: revival never mints, never restores balance, office,
	// contract or patent, and sealed-season wealth never reappears.
	CodeMintedBlessing ErrorCode = "CROWN_MINTED_BLESSING"
	// CodeUnknownWealthPolicy names an unratified wealth policy version:
	// only ratified versions qualify.
	CodeUnknownWealthPolicy ErrorCode = "CROWN_UNKNOWN_WEALTH_POLICY"
	// CodeAmbiguousFixture names a wealth fixture missing required
	// mappings, carrying contradictory tags or mixing domains.
	CodeAmbiguousFixture ErrorCode = "CROWN_AMBIGUOUS_FIXTURE"
	// CodeUnmappedBeneficiary names an asset holding without an explicit
	// owner: every custody must name an explicit beneficiary.
	CodeUnmappedBeneficiary ErrorCode = "CROWN_UNMAPPED_BENEFICIARY"
	// CodeUnmappedObligation names a liability without a responsible
	// debtor: every obligation must name an explicit debtor.
	CodeUnmappedObligation ErrorCode = "CROWN_UNMAPPED_OBLIGATION"
	// CodeInvalidWealthAmount names a negative or malformed wealth amount.
	CodeInvalidWealthAmount ErrorCode = "CROWN_INVALID_WEALTH_AMOUNT"
	// CodeWealthOverflow names an amount or sum exceeding supply bounds
	// or integer arithmetic limits.
	CodeWealthOverflow ErrorCode = "CROWN_WEALTH_OVERFLOW"
	// CodeUnknownAssetKind names an asset classification outside the
	// closed vocabulary of seasonal holdings.
	CodeUnknownAssetKind ErrorCode = "CROWN_UNKNOWN_ASSET_KIND"
	// CodeUnknownObligationKind names an obligation outside the closed
	// vocabulary of seasonal liabilities.
	CodeUnknownObligationKind ErrorCode = "CROWN_UNKNOWN_OBLIGATION_KIND"
	// CodeDuplicateCustody names the same custody evaluated twice for
	// a beneficiary.
	CodeDuplicateCustody ErrorCode = "CROWN_DUPLICATE_CUSTODY"
	// CodeDuplicateObligation names the same obligation evaluated twice.
	CodeDuplicateObligation ErrorCode = "CROWN_DUPLICATE_OBLIGATION"
	// CodeStaleRevision names an evaluation against a superseded revision.
	CodeStaleRevision ErrorCode = "CROWN_STALE_REVISION"
	// CodeRevisionGap names a detected gap in linearizable revisions.
	CodeRevisionGap ErrorCode = "CROWN_REVISION_GAP"
	// CodeProjectionFrozen names a frozen wealth projection.
	CodeProjectionFrozen ErrorCode = "CROWN_PROJECTION_FROZEN"
	// CodeProjectionMismatch names a divergence between projection and journal oracle.
	CodeProjectionMismatch ErrorCode = "CROWN_PROJECTION_MISMATCH"
	// CodeNoQualifiedSuccessor names a succession where no candidate qualifies and no incumbent or regent exists.
	CodeNoQualifiedSuccessor ErrorCode = "CROWN_NO_QUALIFIED_SUCCESSOR"
	// CodeDuplicateCandidate names the same subject appearing more than once in the succession candidate list.
	CodeDuplicateCandidate ErrorCode = "CROWN_DUPLICATE_CANDIDATE"
	// CodeWorkerLeaseBusy names an evaluator lease currently held by another worker.
	CodeWorkerLeaseBusy ErrorCode = "CROWN_WORKER_LEASE_BUSY"
	// CodeStaleWorkerLease names an evaluation attempt with an expired or stolen worker lease.
	CodeStaleWorkerLease ErrorCode = "CROWN_STALE_WORKER_LEASE"
	// CodeBacklogUnevaluated names economic events confirmed in the journal that have not yet been evaluated by the succession engine.
	CodeBacklogUnevaluated ErrorCode = "CROWN_BACKLOG_UNEVALUATED"
	// CodeActiveReignConflict names multiple active reigns detected for a season.
	CodeActiveReignConflict ErrorCode = "CROWN_ACTIVE_REIGN_CONFLICT"
)

// DomainError represents an invariant or rule failure in the crown domain.
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
	// ErrInvalidAuthority refuses a malformed or partial credential
	// before anything is read: every token, reign and instant must
	// arrive whole.
	ErrInvalidAuthority = DomainError{
		Code:    CodeInvalidAuthority,
		Message: "sovereign authority needs a season, a holder, a reign from 1, a competence and whole instants",
	}
	// ErrOperatorNotSovereign refuses the operator as King: the
	// technical credential never converts into the game office.
	ErrOperatorNotSovereign = DomainError{
		Code:    CodeOperatorNotSovereign,
		Message: "the operator credential is separate: administrators are never invested as King by alias",
	}
	// ErrSeasonMismatch refuses a claim outside the current book.
	ErrSeasonMismatch = DomainError{
		Code:    CodeSeasonMismatch,
		Message: "one season never decides in another: the claim names the current book or stops",
	}
	// ErrSeasonClosed refuses a claim outside the open window.
	ErrSeasonClosed = DomainError{
		Code:    CodeSeasonClosed,
		Message: "the season is not open: before the start, at or past the exclusive end, or suspended",
	}
	// ErrStaleReign refuses a superseded reign: investiture moves
	// power without deleting accounts.
	ErrStaleReign = DomainError{
		Code:    CodeStaleReign,
		Message: "the reign moved on: a new investiture revokes the previous holder",
	}
	// ErrFutureReign refuses a reign nobody invested.
	ErrFutureReign = DomainError{
		Code:    CodeFutureReign,
		Message: "no such reign was invested: authority only moves forward through investiture",
	}
	// ErrNotHolder refuses a claimant the reign does not invest.
	ErrNotHolder = DomainError{
		Code:    CodeNotHolder,
		Message: "the claimant does not hold the office in this reign: strangers decide nothing",
	}
	// ErrInvalidSession refuses a session that cannot authenticate.
	ErrInvalidSession = DomainError{
		Code:    CodeInvalidSession,
		Message: "the session cannot authenticate: step-up, ordered instants and a short window are all required",
	}
	// ErrSessionExpired refuses a spent session: replay authorizes
	// nothing new.
	ErrSessionExpired = DomainError{
		Code:    CodeSessionExpired,
		Message: "the session window passed: replaying it authorizes nothing new",
	}
	// ErrInvalidDelegation refuses a delegation that cannot chain to
	// the current holder.
	ErrInvalidDelegation = DomainError{
		Code:    CodeInvalidDelegation,
		Message: "the delegation cannot chain: same book, same reign, same competence, current holder as delegator",
	}
	// ErrDelegationExpired refuses a spent delegation.
	ErrDelegationExpired = DomainError{
		Code:    CodeDelegationExpired,
		Message: "the delegation window passed: operational power ends where its window ends",
	}
	// ErrGrantExpired refuses a reused grant past its session.
	ErrGrantExpired = DomainError{
		Code:    CodeGrantExpired,
		Message: "the grant outlived its session: recorded authorization never outlives the session that made it",
	}
	// ErrUnknownAct refuses a kind outside the closed vocabulary.
	ErrUnknownAct = DomainError{
		Code:    CodeUnknownAct,
		Message: "unknown royal act: the kind is not normative, office, process, economic, pardon or blessing, and season alterations never qualify",
	}
	// ErrIncompleteAct refuses a decree missing a required field.
	ErrIncompleteAct = DomainError{
		Code:    CodeIncompleteAct,
		Message: "the decree is incomplete: identity, authority, reason, target, effect, vigencia and charter version all arrive whole, and only the economic kind carries value",
	}
	// ErrRetroactiveAct refuses harmful backdating.
	ErrRetroactiveAct = DomainError{
		Code:    CodeRetroactiveAct,
		Message: "the decree backdates its vigour: effects start at or after the decree date, corrections arrive as new prospective acts",
	}
	// ErrUnknownOrigin refuses an economic decree without origin.
	ErrUnknownOrigin = DomainError{
		Code:    CodeUnknownOrigin,
		Message: "the economic decree names no known origin: value never moves from nowhere",
	}
	// ErrAmbiguousCharter refuses a decree without explicit version.
	ErrAmbiguousCharter = DomainError{
		Code:    CodeAmbiguousCharter,
		Message: "the charter version is ambiguous: decrees bind to one published vN, never to current or latest by inference",
	}
	// ErrSelfApproval refuses the author reviewing its own act.
	ErrSelfApproval = DomainError{
		Code:    CodeSelfApproval,
		Message: "the author cannot check its own act: irreversible acts need a distinct independent checker",
	}
	// ErrDuplicateApproval refuses checks that are not independent.
	ErrDuplicateApproval = DomainError{
		Code:    CodeDuplicateApproval,
		Message: "the checks are not independent: identifiers and checkers both differ, replay authorizes nothing new",
	}
	// ErrTamperedAct refuses a check bound to another payload.
	ErrTamperedAct = DomainError{
		Code:    CodeTamperedAct,
		Message: "the payload changed after the check: identity, vigour, target, effect, value and charter all bind the digest",
	}
	// ErrCheckExpired refuses a spent check.
	ErrCheckExpired = DomainError{
		Code:    CodeCheckExpired,
		Message: "the check window passed: spent approvals authorize nothing new",
	}
	// ErrEmergencyWithoutReview refuses urgency without review.
	ErrEmergencyWithoutReview = DomainError{
		Code:    CodeEmergencyWithoutReview,
		Message: "urgency without review executes nothing finally: emergency only contains temporarily with scheduled review",
	}
	// ErrNonMonetaryAct refuses to move value for a decree whose
	// kind never carries it.
	ErrNonMonetaryAct = DomainError{
		Code:    CodeNonMonetaryAct,
		Message: "the decree never moves value: only the economic kind carries origin and amount",
	}
	// ErrForbiddenOrigin refuses funding outside the free treasury.
	ErrForbiddenOrigin = DomainError{
		Code:    CodeForbiddenOrigin,
		Message: "the origin cannot fund a crown move: escrow, contract, title, principal alheio and confiscation stay where they are",
	}
	// ErrSelfGrant refuses to pay the decree author.
	ErrSelfGrant = DomainError{
		Code:    CodeSelfGrant,
		Message: "the author cannot grant to itself: the King never takes the treasury as personal wealth",
	}
	// ErrInsufficientTreasury refuses to move beyond availability.
	ErrInsufficientTreasury = DomainError{
		Code:    CodeInsufficientTreasury,
		Message: "the free treasury cannot cover the decree: conserved custody moves at most what is available, never by mint",
	}
	// ErrExecutionFrozen refuses every movement under freeze.
	ErrExecutionFrozen = DomainError{
		Code:    CodeExecutionFrozen,
		Message: "the book is frozen: no decree moves value while the freeze holds",
	}
	// ErrConflictedBenefit refuses a benefit with unresolved conflict.
	ErrConflictedBenefit = DomainError{
		Code:    CodeConflictedBenefit,
		Message: "the benefit has an unresolved conflict: legitimate reparation passes an independent procedure",
	}
	// ErrAnonymousRecord refuses a book entry without authorship.
	ErrAnonymousRecord = DomainError{
		Code:    CodeAnonymousRecord,
		Message: "the record names no author: every royal fact is attributable or it is not recorded",
	}
	// ErrDetachedCorrection refuses a correction without a recorded original.
	ErrDetachedCorrection = DomainError{
		Code:    CodeDetachedCorrection,
		Message: "the correction links no recorded original: corrections arrive linked and the original stays visible",
	}
	// ErrUnrecordedAct refuses to leave an executed act unpublished.
	ErrUnrecordedAct = DomainError{
		Code:    CodeUnrecordedAct,
		Message: "the act is missing from the book: hidden decrees decide nothing, every act is published",
	}
	// ErrImproperPetitioner refuses a petition by the act author.
	ErrImproperPetitioner = DomainError{
		Code:    CodeImproperPetitioner,
		Message: "the petitioner cannot appeal its own act: the channel belongs to the affected holder, authors correct by new linked acts",
	}
	// ErrInterestedAuditor refuses a review by an interested party.
	ErrInterestedAuditor = DomainError{
		Code:    CodeInterestedAuditor,
		Message: "the auditor cannot review its own action: author, petitioner and responsible never audit the case",
	}
	// ErrUnreviewedRepair refuses a correction without a repair verdict.
	ErrUnreviewedRepair = DomainError{
		Code:    CodeUnreviewedRepair,
		Message: "the correction carries no repair verdict: errors are corrected by new acts under independent review",
	}
	// ErrTreasuryFundedRestitution refuses to charge the free treasury.
	ErrTreasuryFundedRestitution = DomainError{
		Code:    CodeTreasuryFundedRestitution,
		Message: "the free treasury funds no restitution: the responsible repairs from own custody",
	}
	// ErrUnknownOffice refuses an office outside the closed vocabulary.
	ErrUnknownOffice = DomainError{
		Code:    CodeUnknownOffice,
		Message: "unknown game office: only inquisidor, justiceiro, defensor, testemunha, arbitro, auditor, carrasco and certificador qualify",
	}
	// ErrOfficeExpired refuses a mandate past its window.
	ErrOfficeExpired = DomainError{
		Code:    CodeOfficeExpired,
		Message: "the mandate expired: game offices never survive the season end without a fresh designation",
	}
	// ErrConflictedOffice refuses an accuser judging, an interested
	// witness or executor, or the King judging a rival in own cause.
	ErrConflictedOffice = DomainError{
		Code:    CodeConflictedOffice,
		Message: "the office is conflicted: accuser never judges, interested never witness or execute, the King never judges a rival in own cause",
	}
	// ErrOutcomePay refuses a fee conditioned on conviction.
	ErrOutcomePay = DomainError{
		Code:    CodeOutcomePay,
		Message: "the fee follows the result: game offices are paid per service, never per conviction",
	}
	// ErrInvalidBlessing refuses a blessing without a new act for
	// the account itself.
	ErrInvalidBlessing = DomainError{
		Code:    CodeInvalidBlessing,
		Message: "the blessing is invalid: a new blessing act for the account, sanction kept visible, no third party, no self-blessing and charter acceptance when the version changes",
	}
	// ErrMintedBlessing refuses a return carrying value.
	ErrMintedBlessing = DomainError{
		Code:    CodeMintedBlessing,
		Message: "the return mints: revival never recreates balance, office, contract or patent and sealed-season wealth never reappears",
	}
	// ErrUnknownWealthPolicy refuses an unratified wealth policy.
	ErrUnknownWealthPolicy = DomainError{
		Code:    CodeUnknownWealthPolicy,
		Message: "unknown wealth policy: only ratified versions qualify",
	}
	// ErrAmbiguousFixture refuses a fixture with contradictory tags,
	// missing fields or cross-domain violations.
	ErrAmbiguousFixture = DomainError{
		Code:    CodeAmbiguousFixture,
		Message: "ambiguous wealth fixture: inputs must be uniquely and completely mapped",
	}
	// ErrUnmappedBeneficiary refuses an asset holding without an explicit owner.
	ErrUnmappedBeneficiary = DomainError{
		Code:    CodeUnmappedBeneficiary,
		Message: "unmapped asset beneficiary: every custody must name an explicit owner",
	}
	// ErrUnmappedObligation refuses an obligation without an explicit debtor.
	ErrUnmappedObligation = DomainError{
		Code:    CodeUnmappedObligation,
		Message: "unmapped obligation debtor: every liability must name an explicit responsible debtor",
	}
	// ErrInvalidWealthAmount refuses a negative or malformed amount.
	ErrInvalidWealthAmount = DomainError{
		Code:    CodeInvalidWealthAmount,
		Message: "invalid wealth amount: amounts must be non-negative integers within bounds",
	}
	// ErrWealthOverflow refuses an amount or sum beyond limits.
	ErrWealthOverflow = DomainError{
		Code:    CodeWealthOverflow,
		Message: "wealth amount overflows fixed limits or integer bounds",
	}
	// ErrUnknownAssetKind refuses an asset kind outside the closed vocabulary.
	ErrUnknownAssetKind = DomainError{
		Code:    CodeUnknownAssetKind,
		Message: "unknown asset kind: classification must belong to the closed vocabulary",
	}
	// ErrUnknownObligationKind refuses an obligation kind outside the closed vocabulary.
	ErrUnknownObligationKind = DomainError{
		Code:    CodeUnknownObligationKind,
		Message: "unknown obligation kind: obligation must belong to the closed vocabulary",
	}
	// ErrDuplicateCustody refuses duplicate custody in an evaluation.
	ErrDuplicateCustody = DomainError{
		Code:    CodeDuplicateCustody,
		Message: "duplicate custody entry: each custody must be uniquely mapped per evaluation",
	}
	// ErrDuplicateObligation refuses duplicate obligation in an evaluation.
	ErrDuplicateObligation = DomainError{
		Code:    CodeDuplicateObligation,
		Message: "duplicate obligation entry: each obligation must be uniquely mapped per evaluation",
	}
	// ErrStaleRevision refuses an operation based on a superseded revision.
	ErrStaleRevision = DomainError{
		Code:    CodeStaleRevision,
		Message: "stale revision: wealth projection is behind current committed checkpoint",
	}
	// ErrRevisionGap refuses an operation when a revision gap is detected.
	ErrRevisionGap = DomainError{
		Code:    CodeRevisionGap,
		Message: "revision gap: expected continuous monotonic revision without gaps",
	}
	// ErrProjectionFrozen refuses acts when the wealth projection is frozen.
	ErrProjectionFrozen = DomainError{
		Code:    CodeProjectionFrozen,
		Message: "wealth projection is frozen: no sovereign decisions can be derived while frozen",
	}
	// ErrProjectionMismatch refuses when incremental projection drifts from the rebuild oracle.
	ErrProjectionMismatch = DomainError{
		Code:    CodeProjectionMismatch,
		Message: "projection mismatch: incremental index diverged from authoritative journal",
	}
	// ErrNoQualifiedSuccessor refuses when no eligible candidate exceeded C and no incumbent or regent is available.
	ErrNoQualifiedSuccessor = DomainError{
		Code:    CodeNoQualifiedSuccessor,
		Message: "no qualified successor: no candidate exceeded institutional threshold C, and no incumbent or regent is available",
	}
	// ErrDuplicateCandidate refuses when a subject appears multiple times in the candidate pool.
	ErrDuplicateCandidate = DomainError{
		Code:    CodeDuplicateCandidate,
		Message: "duplicate candidate: each subject must appear at most once in candidate list",
	}
	// ErrWorkerLeaseBusy refuses when an evaluator lease is currently held by another active worker.
	ErrWorkerLeaseBusy = DomainError{
		Code:    CodeWorkerLeaseBusy,
		Message: "worker lease busy: another evaluator holds an active lease for this season",
	}
	// ErrStaleWorkerLease refuses an evaluation attempt when the worker's lease has expired or was superseded.
	ErrStaleWorkerLease = DomainError{
		Code:    CodeStaleWorkerLease,
		Message: "stale worker lease: worker lease expired or is not owned by this evaluator",
	}
	// ErrBacklogUnevaluated halts royal acts until all confirmed economic events are evaluated by succession.
	ErrBacklogUnevaluated = DomainError{
		Code:    CodeBacklogUnevaluated,
		Message: "unevaluated backlog: confirmed economic events must be evaluated before executing royal acts",
	}
	// ErrActiveReignConflict refuses when multiple active reigns are detected for a season.
	ErrActiveReignConflict = DomainError{
		Code:    CodeActiveReignConflict,
		Message: "active reign conflict: multiple active reigns exist for this season",
	}
)
