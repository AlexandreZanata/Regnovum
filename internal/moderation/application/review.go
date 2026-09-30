// Review implements the auditable case decision path (P13-T04): a
// moderator claims a case under a bounded lease, then records one explicit
// decision with rule, scope, duration and justification. The measure stays
// the moderator's choice: the use cases never infer severity from content,
// ideology or volume. Reporter context and justifications never enter
// errors.
package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// CaseStatus is the triage lifecycle the repository reports.
type CaseStatus string

const (
	CaseOpen        CaseStatus = "open"
	CaseUnderReview CaseStatus = "under_review"
	CaseDecided     CaseStatus = "decided"
	CaseClosed      CaseStatus = "closed"
)

// CaseRecord is one moderation case as read back from persistence.
type CaseRecord struct {
	ID             string
	Target         domain.TargetType
	TargetID       string
	TargetOwner    domain.AccountID
	Status         CaseStatus
	ClaimedBy      domain.AccountID
	LeaseExpiresAt *time.Time
}

// CaseRepository persists review claims and decisions. Claim and Decide
// are conditional: concurrent writers serialize on the row, and only one
// wins a live lease.
type CaseRepository interface {
	// GetCase resolves the case with its target owner for conflict checks.
	GetCase(ctx context.Context, caseID string) (*CaseRecord, error)
	// ClaimCase moves an open case (or an expired lease) under the actor
	// with a fresh lease. A live lease held by another moderator refuses
	// with ErrCaseAlreadyClaimed.
	ClaimCase(ctx context.Context, request ClaimCaseRequest) (*CaseRecord, error)
	// DecideCase records one action and moves the case to decided while
	// clearing the claim, atomically. A lapsed lease refuses with
	// ErrLeaseExpired; a case outside under_review refuses with
	// ErrInvalidCaseTransition.
	DecideCase(ctx context.Context, request DecideCaseRequest) (*DecisionRecord, error)
}

// ClaimCaseRequest is one conditional claim.
type ClaimCaseRequest struct {
	CaseID    string
	Actor     domain.AccountID
	ClaimedAt time.Time
	LeaseDays time.Duration
}

// DecideCaseRequest is one auditable decision.
type DecideCaseRequest struct {
	CaseID        string
	Actor         domain.AccountID
	Action        domain.Action
	Rule          string
	Justification string
	ExpiresAt     *time.Time
	DecidedAt     time.Time
}

// DecisionRecord is the stored decision as read back.
type DecisionRecord struct {
	ActionID string
	CaseID   string
	Actor    domain.AccountID
	Action   domain.Action
}

// ClaimCaseCommand names the case, the claimant and the session freshness
// for step-up evaluation of the implicit triageClaim. Claiming itself
// authorizes as a low-impact triage step: any active role may claim, but a
// conflict still denies.
type ClaimCaseCommand struct {
	CaseID     string
	Actor      string
	SessionAge time.Duration
}

// DecideCaseCommand names the case, the decider, the explicit measure with
// its rule, scope duration and justification, and the session freshness.
// The action travels untouched from moderator to ledger: nothing here
// upgrades, downgrades or derives it from content.
type DecideCaseCommand struct {
	CaseID        string
	Actor         string
	Action        string
	Rule          string
	Justification string
	ExpiresAt     *time.Time
	SessionAge    time.Duration
}

// DecideCaseResult is the explicit outcome.
type DecideCaseResult struct {
	ActionID string
	CaseID   string
	Action   domain.Action
	Replayed bool
}

// ReviewDependencies groups everything the review use cases need.
type ReviewDependencies struct {
	Cases      CaseRepository
	Authorizer *Authorizer
	Clock      Clock
}

// ClaimCaseUseCase claims one open case under a bounded lease.
type ClaimCaseUseCase struct {
	cases      CaseRepository
	authorizer *Authorizer
	clock      Clock
}

// NewClaimCaseUseCase builds the use case, refusing incomplete composition.
func NewClaimCaseUseCase(deps ReviewDependencies) (*ClaimCaseUseCase, error) {
	if deps.Cases == nil || deps.Authorizer == nil || deps.Clock == nil {
		return nil, ErrInvalidDecisionConfig
	}
	return &ClaimCaseUseCase{cases: deps.Cases, authorizer: deps.Authorizer, clock: deps.Clock}, nil
}

// Execute claims the case for the actor.
func (uc *ClaimCaseUseCase) Execute(ctx context.Context, cmd ClaimCaseCommand) (*CaseRecord, error) {
	actor := domain.AccountID(cmd.Actor)
	if actor.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if cmd.CaseID == "" {
		return nil, ErrCaseNotFound
	}
	if cmd.SessionAge < 0 {
		return nil, domain.ErrInvalidSessionAge
	}

	stored, err := uc.cases.GetCase(ctx, cmd.CaseID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, ErrCaseNotFound
	}

	if _, err := uc.authorizer.EnsureAuthorized(ctx, AuthorizeCommand{
		Actor:       actor,
		Action:      domain.ActionNoAction,
		TargetOwner: stored.TargetOwner,
		SessionAge:  cmd.SessionAge,
	}); err != nil {
		return nil, err
	}

	now := uc.clock.Now()
	return uc.cases.ClaimCase(ctx, ClaimCaseRequest{
		CaseID:    cmd.CaseID,
		Actor:     actor,
		ClaimedAt: now,
		LeaseDays: domain.ClaimLease,
	})
}

// DecideCaseUseCase records one explicit decision.
type DecideCaseUseCase struct {
	cases      CaseRepository
	authorizer *Authorizer
	clock      Clock
}

// NewDecideCaseUseCase builds the use case, refusing incomplete composition.
func NewDecideCaseUseCase(deps ReviewDependencies) (*DecideCaseUseCase, error) {
	if deps.Cases == nil || deps.Authorizer == nil || deps.Clock == nil {
		return nil, ErrInvalidDecisionConfig
	}
	return &DecideCaseUseCase{cases: deps.Cases, authorizer: deps.Authorizer, clock: deps.Clock}, nil
}

// Execute validates the explicit measure and records it with the case
// transition, atomically.
func (uc *DecideCaseUseCase) Execute(ctx context.Context, cmd DecideCaseCommand) (*DecideCaseResult, error) {
	actor := domain.AccountID(cmd.Actor)
	if actor.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if cmd.CaseID == "" {
		return nil, ErrCaseNotFound
	}
	action, err := domain.ParseAction(cmd.Action)
	if err != nil {
		return nil, err
	}
	rule, err := domain.ParseRule(cmd.Rule)
	if err != nil {
		return nil, err
	}
	justification, err := domain.ParseJustification(cmd.Justification)
	if err != nil {
		return nil, err
	}
	if cmd.SessionAge < 0 {
		return nil, domain.ErrInvalidSessionAge
	}

	stored, err := uc.cases.GetCase(ctx, cmd.CaseID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, ErrCaseNotFound
	}

	// The measure is the moderator's: authorize exactly what was asked,
	// never a derived severity.
	if _, err := uc.authorizer.EnsureAuthorized(ctx, AuthorizeCommand{
		Actor:       actor,
		Action:      action,
		TargetOwner: stored.TargetOwner,
		SessionAge:  cmd.SessionAge,
	}); err != nil {
		return nil, err
	}

	// The action must fit the target: an admin can suspend profiles but
	// never close an argument, whatever the role allows.
	if !domain.SanctionAllowed(stored.Target, action) {
		return nil, fmt.Errorf("%w: %s cannot sanction %s", domain.ErrTargetActionMismatch, action, stored.Target)
	}

	now := uc.clock.Now()
	expiry, err := domain.DecisionExpiry(action, cmd.ExpiresAt, now)
	if err != nil {
		return nil, err
	}

	record, err := uc.cases.DecideCase(ctx, DecideCaseRequest{
		CaseID:        cmd.CaseID,
		Actor:         actor,
		Action:        action,
		Rule:          rule,
		Justification: justification,
		ExpiresAt:     expiry,
		DecidedAt:     now,
	})
	if err != nil {
		return nil, err
	}

	return &DecideCaseResult{ActionID: record.ActionID, CaseID: record.CaseID, Action: record.Action}, nil
}
