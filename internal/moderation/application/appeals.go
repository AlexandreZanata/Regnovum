// Appeals implement the sanction contest path (P13-T06): the affected
// owner files exactly one appeal per eligible decision inside the appeal
// window; a different reviewer claims it and records uphold, modify or
// reverse with reason. Reversals restore projections from the original
// action record without editing or deleting it. Appeal contexts and
// decision reasons never enter errors.
package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// AppealStatus is the review lifecycle the repository reports.
type AppealStatus string

const (
	AppealOpen        AppealStatus = "open"
	AppealUnderReview AppealStatus = "under_review"
	AppealUpheld      AppealStatus = "upheld"
	AppealModified    AppealStatus = "modified"
	AppealReversed    AppealStatus = "reversed"
)

// SanctionedAction is one contestable decision with its case target and
// owner, as read back from persistence.
type SanctionedAction struct {
	ActionID    string
	Action      domain.Action
	Actor       domain.AccountID
	Rule        string
	CreatedAt   time.Time
	CaseID      string
	Target      domain.TargetType
	TargetID    string
	TargetOwner domain.AccountID
}

// AppealRecord is one stored appeal as read back from persistence.
type AppealRecord struct {
	ID        string
	ActionID  string
	Appellant domain.AccountID
	Status    AppealStatus
	Reviewer  domain.AccountID
	DecidedAt *time.Time
}

// AppealStore persists appeals with singularity anchored on the action.
// Claim and Decide are conditional: concurrent reviewers serialize, and a
// reversal restores the sanctioned projection in the same transaction as
// the outcome.
type AppealStore interface {
	// ActionForAppeal resolves the sanction with its case target and
	// owner, or nil when no action carries the identifier.
	ActionForAppeal(ctx context.Context, actionID string) (*SanctionedAction, error)
	// AppealByAction resolves the appeal contesting the action, or nil
	// when the action carries none yet.
	AppealByAction(ctx context.Context, actionID string) (*AppealRecord, error)
	// AppealByID resolves one appeal, or nil when unknown.
	AppealByID(ctx context.Context, appealID string) (*AppealRecord, error)
	// InsertAppeal files one fresh appeal in open state.
	InsertAppeal(ctx context.Context, request InsertAppealRequest) (*AppealRecord, error)
	// ClaimAppeal moves an open appeal under the reviewer. A concurrent
	// claim refuses with ErrAppealAlreadyClaimed.
	ClaimAppeal(ctx context.Context, request ClaimAppealRequest) (*AppealRecord, error)
	// DecideAppeal records the outcome with reason and, on reversal,
	// restores the sanctioned projection from the original action record,
	// atomically. The original action row is never edited or deleted.
	DecideAppeal(ctx context.Context, request DecideAppealRequest) (*AppealRecord, error)
}

// InsertAppealRequest is one fresh appeal to persist.
type InsertAppealRequest struct {
	ActionID  string
	Appellant domain.AccountID
	Context   string
}

// ClaimAppealRequest is one conditional review claim. The claim moves the
// appeal to review without naming the reviewer in storage: the review
// triple (reviewer, reason, decided instant) is written once at decision,
// so any authorized reviewer distinct from the deciding moderator may
// finish a claimed appeal.
type ClaimAppealRequest struct {
	AppealID string
}

// DecideAppealRequest is one review outcome with reason.
type DecideAppealRequest struct {
	AppealID  string
	Reviewer  domain.AccountID
	Outcome   domain.Outcome
	Reason    string
	DecidedAt time.Time
}

// FileAppealCommand names the contested action, the appellant and the
// appellant context.
type FileAppealCommand struct {
	ActionID  string
	Appellant string
	Context   string
}

// FileAppealResult is the explicit outcome.
type FileAppealResult struct {
	AppealID string
	ActionID string
	Replayed bool
}

// ClaimAppealCommand names the appeal, the reviewer and the session
// freshness.
type ClaimAppealCommand struct {
	AppealID   string
	Reviewer   string
	SessionAge time.Duration
}

// DecideAppealCommand names the appeal, the claiming reviewer, the outcome
// with reason and the session freshness.
type DecideAppealCommand struct {
	AppealID   string
	Reviewer   string
	Outcome    string
	Reason     string
	SessionAge time.Duration
}

// DecideAppealResult is the explicit outcome.
type DecideAppealResult struct {
	AppealID string
	ActionID string
	Outcome  domain.Outcome
}

// AppealDependencies groups everything the appeal use cases need.
type AppealDependencies struct {
	Appeals    AppealStore
	Authorizer *Authorizer
	Clock      Clock
}

// FileAppealUseCase files one appeal for the sanctioned owner.
type FileAppealUseCase struct {
	appeals AppealStore
	clock   Clock
}

// NewFileAppealUseCase builds the use case, refusing incomplete composition.
func NewFileAppealUseCase(deps AppealDependencies) (*FileAppealUseCase, error) {
	if deps.Appeals == nil || deps.Clock == nil {
		return nil, ErrInvalidAppealConfig
	}
	return &FileAppealUseCase{appeals: deps.Appeals, clock: deps.Clock}, nil
}

// Execute validates owner, deadline and singularity, then files the appeal
// in open state.
func (uc *FileAppealUseCase) Execute(ctx context.Context, cmd FileAppealCommand) (*FileAppealResult, error) {
	appellant := domain.AccountID(cmd.Appellant)
	if appellant.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if cmd.ActionID == "" {
		return nil, ErrActionNotFound
	}
	appealContext, err := domain.ParseReportContext(cmd.Context)
	if err != nil || appealContext == "" {
		return nil, domain.ErrInvalidContext
	}

	action, err := uc.appeals.ActionForAppeal(ctx, cmd.ActionID)
	if err != nil {
		return nil, err
	}
	if action == nil {
		return nil, ErrActionNotFound
	}
	if !domain.Appealable(action.Action) {
		return nil, ErrActionNotAppealable
	}
	if appellant != action.TargetOwner {
		return nil, ErrNotAppealOwner
	}

	now := uc.clock.Now()
	if domain.AppealExpired(action.CreatedAt, now) {
		return nil, ErrAppealExpired
	}

	existing, err := uc.appeals.AppealByAction(ctx, cmd.ActionID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &FileAppealResult{AppealID: existing.ID, ActionID: cmd.ActionID, Replayed: true}, nil
	}

	stored, err := uc.appeals.InsertAppeal(ctx, InsertAppealRequest{
		ActionID:  cmd.ActionID,
		Appellant: appellant,
		Context:   appealContext,
	})
	if err != nil {
		return nil, err
	}
	return &FileAppealResult{AppealID: stored.ID, ActionID: cmd.ActionID}, nil
}

// ClaimAppealUseCase claims one open appeal for review by a different
// reviewer than the deciding moderator.
type ClaimAppealUseCase struct {
	appeals    AppealStore
	authorizer *Authorizer
	clock      Clock
}

// NewClaimAppealUseCase builds the use case, refusing incomplete composition.
func NewClaimAppealUseCase(deps AppealDependencies) (*ClaimAppealUseCase, error) {
	if deps.Appeals == nil || deps.Authorizer == nil || deps.Clock == nil {
		return nil, ErrInvalidAppealConfig
	}
	return &ClaimAppealUseCase{appeals: deps.Appeals, authorizer: deps.Authorizer, clock: deps.Clock}, nil
}

// Execute authorizes the reviewer and claims the appeal.
func (uc *ClaimAppealUseCase) Execute(ctx context.Context, cmd ClaimAppealCommand) (*AppealRecord, error) {
	reviewer := domain.AccountID(cmd.Reviewer)
	if reviewer.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if cmd.AppealID == "" {
		return nil, ErrAppealNotFound
	}
	if cmd.SessionAge < 0 {
		return nil, domain.ErrInvalidSessionAge
	}

	appeal, err := uc.appeals.AppealByID(ctx, cmd.AppealID)
	if err != nil {
		return nil, err
	}
	if appeal == nil {
		return nil, ErrAppealNotFound
	}
	action, err := uc.appeals.ActionForAppeal(ctx, appeal.ActionID)
	if err != nil {
		return nil, err
	}
	if action == nil {
		return nil, ErrActionNotFound
	}
	if reviewer == action.Actor {
		return nil, ErrSameReviewer
	}

	if _, err := uc.authorizer.EnsureAuthorized(ctx, AuthorizeCommand{
		Actor:       reviewer,
		Action:      domain.ActionNoAction,
		TargetOwner: action.TargetOwner,
		SessionAge:  cmd.SessionAge,
	}); err != nil {
		return nil, err
	}

	return uc.appeals.ClaimAppeal(ctx, ClaimAppealRequest{
		AppealID: cmd.AppealID,
	})
}

// DecideAppealUseCase records one review outcome with reason.
type DecideAppealUseCase struct {
	appeals    AppealStore
	authorizer *Authorizer
	clock      Clock
}

// NewDecideAppealUseCase builds the use case, refusing incomplete composition.
func NewDecideAppealUseCase(deps AppealDependencies) (*DecideAppealUseCase, error) {
	if deps.Appeals == nil || deps.Authorizer == nil || deps.Clock == nil {
		return nil, ErrInvalidAppealConfig
	}
	return &DecideAppealUseCase{appeals: deps.Appeals, authorizer: deps.Authorizer, clock: deps.Clock}, nil
}

// Execute validates the claiming reviewer, authorizes, and records the
// outcome with reason, restoring on reversal.
func (uc *DecideAppealUseCase) Execute(ctx context.Context, cmd DecideAppealCommand) (*DecideAppealResult, error) {
	reviewer := domain.AccountID(cmd.Reviewer)
	if reviewer.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if cmd.AppealID == "" {
		return nil, ErrAppealNotFound
	}
	outcome, err := domain.ParseOutcome(cmd.Outcome)
	if err != nil {
		return nil, err
	}
	reason, err := domain.ParseJustification(cmd.Reason)
	if err != nil {
		return nil, err
	}
	if cmd.SessionAge < 0 {
		return nil, domain.ErrInvalidSessionAge
	}

	appeal, err := uc.appeals.AppealByID(ctx, cmd.AppealID)
	if err != nil {
		return nil, err
	}
	if appeal == nil {
		return nil, ErrAppealNotFound
	}
	action, err := uc.appeals.ActionForAppeal(ctx, appeal.ActionID)
	if err != nil {
		return nil, err
	}
	if action == nil {
		return nil, ErrActionNotFound
	}
	// The deciding moderator never reviews: handoff between distinct
	// reviewers stays allowed.
	if reviewer == action.Actor {
		return nil, ErrSameReviewer
	}

	if _, err := uc.authorizer.EnsureAuthorized(ctx, AuthorizeCommand{
		Actor:       reviewer,
		Action:      domain.ActionNoAction,
		TargetOwner: action.TargetOwner,
		SessionAge:  cmd.SessionAge,
	}); err != nil {
		return nil, err
	}

	record, err := uc.appeals.DecideAppeal(ctx, DecideAppealRequest{
		AppealID:  cmd.AppealID,
		Reviewer:  reviewer,
		Outcome:   outcome,
		Reason:    reason,
		DecidedAt: uc.clock.Now(),
	})
	if err != nil {
		return nil, err
	}
	return &DecideAppealResult{AppealID: record.ID, ActionID: record.ActionID, Outcome: outcome}, nil
}
