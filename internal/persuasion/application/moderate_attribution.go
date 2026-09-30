package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// ModerateAttributionCommand holds the parameters of an attribution
// moderation action: the acting moderator, the target attribution and the
// mandatory reason.
type ModerateAttributionCommand struct {
	ActorAccountID string
	AttributionID  string
	Reason         string
}

// InvalidateAttributionUseCase marks one attribution as fraudulent or
// collusive (P11-T04, REQ-PERS-08). It is moderator-only, the reason is
// mandatory, nothing is deleted and the decision is recorded with its audit
// event. Reapplying the same decision resolves the recorded state instead of
// writing again.
type InvalidateAttributionUseCase struct {
	attributions AttributionModerationRepository
	authorizer   ModerationAuthorizer
	audit        AttributionAuditRecorder
	clock        Clock
	uow          UnitOfWork
}

// NewInvalidateAttributionUseCase creates an instance of
// InvalidateAttributionUseCase.
func NewInvalidateAttributionUseCase(attributions AttributionModerationRepository, authorizer ModerationAuthorizer, audit AttributionAuditRecorder, clock Clock, uow UnitOfWork) *InvalidateAttributionUseCase {
	return &InvalidateAttributionUseCase{
		attributions: attributions,
		authorizer:   authorizer,
		audit:        audit,
		clock:        clock,
		uow:          uow,
	}
}

// Execute invalidates the attribution under a recorded decision.
func (uc *InvalidateAttributionUseCase) Execute(ctx context.Context, cmd ModerateAttributionCommand) (*AttributionModerationResult, error) {
	return moderateAttribution(ctx, uc.attributions, uc.authorizer, uc.audit, uc.clock, uc.uow, cmd, domain.ModerationActionInvalidate)
}

// RestoreAttributionUseCase reverses one invalidation. It shares every rule
// with the invalidation path: moderator-only, mandatory reason, no deletion
// and a recorded audit event.
type RestoreAttributionUseCase struct {
	attributions AttributionModerationRepository
	authorizer   ModerationAuthorizer
	audit        AttributionAuditRecorder
	clock        Clock
	uow          UnitOfWork
}

// NewRestoreAttributionUseCase creates an instance of
// RestoreAttributionUseCase.
func NewRestoreAttributionUseCase(attributions AttributionModerationRepository, authorizer ModerationAuthorizer, audit AttributionAuditRecorder, clock Clock, uow UnitOfWork) *RestoreAttributionUseCase {
	return &RestoreAttributionUseCase{
		attributions: attributions,
		authorizer:   authorizer,
		audit:        audit,
		clock:        clock,
		uow:          uow,
	}
}

// Execute restores the attribution under a recorded decision.
func (uc *RestoreAttributionUseCase) Execute(ctx context.Context, cmd ModerateAttributionCommand) (*AttributionModerationResult, error) {
	return moderateAttribution(ctx, uc.attributions, uc.authorizer, uc.audit, uc.clock, uc.uow, cmd, domain.ModerationActionRestore)
}

// moderateAttribution is the shared path of invalidation and restoration:
// validation and authorization happen before any state is read, the row is
// locked for update inside one transaction, an already applied decision
// resolves as a replay, and an applied decision is recorded together with
// its audit event.
func moderateAttribution(
	ctx context.Context,
	attributions AttributionModerationRepository,
	authorizer ModerationAuthorizer,
	audit AttributionAuditRecorder,
	clock Clock,
	uow UnitOfWork,
	cmd ModerateAttributionCommand,
	action domain.ModerationAction,
) (*AttributionModerationResult, error) {
	decision, attributionID, err := validateAttributionModeration(ctx, authorizer, clock, cmd, action)
	if err != nil {
		return nil, err
	}

	var result *AttributionModerationResult
	err = uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := attributions.LockAttributionForModeration(txCtx, attributionID)
		if err != nil {
			return err
		}

		// A retry of a decision already applied resolves the recorded state:
		// no write, no second audit event, and the original decision facts
		// stay untouched.
		if current.Status == decision.TargetStatus() {
			result = &AttributionModerationResult{Attribution: *current, Replayed: true}
			return nil
		}

		target, err := transition(*current, decision)
		if err != nil {
			return err
		}
		applied, err := attributions.ApplyModerationDecision(txCtx, attributionID, target.Decision)
		if err != nil {
			return err
		}
		if err := audit.RecordAttributionModeration(txCtx, AttributionModerationEvent{
			ActorAccountID: decision.Actor,
			AttributionID:  applied.ID,
			ArgumentID:     applied.ArgumentID,
			Action:         decision.Action,
			Reason:         decision.Reason,
			OccurredAt:     decision.DecidedAt,
		}); err != nil {
			return err
		}

		result = &AttributionModerationResult{Attribution: *applied}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// transition applies the domain rule of the requested action to the current
// attribution.
func transition(current domain.Attribution, decision domain.ModerationDecision) (domain.Attribution, error) {
	switch decision.Action {
	case domain.ModerationActionInvalidate:
		return current.Invalidate(decision)
	case domain.ModerationActionRestore:
		return current.Restore(decision)
	default:
		return domain.Attribution{}, domain.ErrInvalidModerationAction
	}
}

// validateAttributionModeration parses and authorizes a moderation command
// before any attribution state is read.
func validateAttributionModeration(
	ctx context.Context,
	authorizer ModerationAuthorizer,
	clock Clock,
	cmd ModerateAttributionCommand,
	action domain.ModerationAction,
) (domain.ModerationDecision, domain.AttributionID, error) {
	if !action.IsValid() {
		return domain.ModerationDecision{}, domain.AttributionID{}, domain.ErrInvalidModerationAction
	}

	actor, err := domain.ParseModeratorID(cmd.ActorAccountID)
	if err != nil {
		return domain.ModerationDecision{}, domain.AttributionID{}, err
	}
	attributionID, err := domain.ParseAttributionID(cmd.AttributionID)
	if err != nil {
		return domain.ModerationDecision{}, domain.AttributionID{}, err
	}
	reason, err := domain.ParseReason(cmd.Reason)
	if err != nil {
		return domain.ModerationDecision{}, domain.AttributionID{}, err
	}
	if err := authorizer.EnsureModerator(ctx, actor); err != nil {
		return domain.ModerationDecision{}, domain.AttributionID{}, err
	}

	decision := domain.ModerationDecision{
		Action:    action,
		Actor:     actor,
		Reason:    reason,
		DecidedAt: clock.Now().UTC(),
	}
	if err := decision.Validate(); err != nil {
		return domain.ModerationDecision{}, domain.AttributionID{}, err
	}
	return decision, attributionID, nil
}
