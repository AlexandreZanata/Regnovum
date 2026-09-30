package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// ModerateArenaCommand holds the parameters of a moderation action: the
// acting moderator, the target Arena and the mandatory reason.
type ModerateArenaCommand struct {
	ActorAccountID string
	ArenaID        string
	Reason         string
}

// RestrictArenaUseCase applies the restriction decision. Only authorized
// moderators reach the Arena at all, the reason is mandatory and the action
// is audited.
type RestrictArenaUseCase struct {
	arenas     ArenaRepository
	authorizer ModerationAuthorizer
	audit      ModerationAuditRecorder
	clock      Clock
}

// NewRestrictArenaUseCase creates an instance of RestrictArenaUseCase.
func NewRestrictArenaUseCase(arenas ArenaRepository, authorizer ModerationAuthorizer, audit ModerationAuditRecorder, clock Clock) *RestrictArenaUseCase {
	return &RestrictArenaUseCase{arenas: arenas, authorizer: authorizer, audit: audit, clock: clock}
}

// Execute restricts the Arena under a recorded decision.
func (uc *RestrictArenaUseCase) Execute(ctx context.Context, cmd ModerateArenaCommand) (*ModerationResult, error) {
	actor, reason, arenaID, err := validateModeration(ctx, uc.authorizer, cmd)
	if err != nil {
		return nil, err
	}

	arena, err := uc.arenas.GetArenaByID(ctx, arenaID)
	if err != nil {
		return nil, err
	}

	switch arena.Status() {
	case domain.ArenaStatusPublished, domain.ArenaStatusClosed:
		expectedVersion := arena.Version()
		if err := arena.Restrict(); err != nil {
			return nil, err
		}
		restricted, err := uc.arenas.RestrictArena(ctx, arenaID, expectedVersion)
		if err != nil {
			return nil, err
		}
		return recordModeration(ctx, uc.audit, uc.clock, actor, arenaID, ModerationRestrict, reason, restricted, false)
	case domain.ArenaStatusRestricted:
		// Idempotent retry: the Arena is already restricted.
		return recordModeration(ctx, uc.audit, uc.clock, actor, arenaID, ModerationRestrict, reason, arena, true)
	default:
		return nil, domain.ErrInvalidStatusChange
	}
}

// RemoveArenaUseCase applies the removal decision: removed is terminal and
// every decision is authorized and audited.
type RemoveArenaUseCase struct {
	arenas     ArenaRepository
	authorizer ModerationAuthorizer
	audit      ModerationAuditRecorder
	clock      Clock
}

// NewRemoveArenaUseCase creates an instance of RemoveArenaUseCase.
func NewRemoveArenaUseCase(arenas ArenaRepository, authorizer ModerationAuthorizer, audit ModerationAuditRecorder, clock Clock) *RemoveArenaUseCase {
	return &RemoveArenaUseCase{arenas: arenas, authorizer: authorizer, audit: audit, clock: clock}
}

// Execute removes the Arena under a recorded decision.
func (uc *RemoveArenaUseCase) Execute(ctx context.Context, cmd ModerateArenaCommand) (*ModerationResult, error) {
	actor, reason, arenaID, err := validateModeration(ctx, uc.authorizer, cmd)
	if err != nil {
		return nil, err
	}

	arena, err := uc.arenas.GetArenaByID(ctx, arenaID)
	if err != nil {
		return nil, err
	}

	switch arena.Status() {
	case domain.ArenaStatusPublished, domain.ArenaStatusClosed, domain.ArenaStatusRestricted:
		expectedVersion := arena.Version()
		if err := arena.Remove(); err != nil {
			return nil, err
		}
		removed, err := uc.arenas.RemoveArena(ctx, arenaID, expectedVersion)
		if err != nil {
			return nil, err
		}
		return recordModeration(ctx, uc.audit, uc.clock, actor, arenaID, ModerationRemove, reason, removed, false)
	case domain.ArenaStatusRemoved:
		// Idempotent retry: the Arena is already removed.
		return recordModeration(ctx, uc.audit, uc.clock, actor, arenaID, ModerationRemove, reason, arena, true)
	default:
		return nil, domain.ErrInvalidStatusChange
	}
}

// validateModeration parses and authorizes a moderation command before any
// Arena state is touched.
func validateModeration(ctx context.Context, authorizer ModerationAuthorizer, cmd ModerateArenaCommand) (domain.ModeratorID, domain.Reason, domain.ArenaID, error) {
	actor := domain.ModeratorID(cmd.ActorAccountID)
	if actor.IsZero() {
		return "", domain.Reason{}, "", domain.ErrActorRequired
	}
	arenaID := domain.ArenaID(cmd.ArenaID)
	if arenaID.IsZero() {
		return "", domain.Reason{}, "", domain.ErrEmptyArenaID
	}
	reason, err := domain.ParseReason(cmd.Reason)
	if err != nil {
		return "", domain.Reason{}, "", err
	}
	if err := authorizer.EnsureModerator(ctx, actor); err != nil {
		return "", domain.Reason{}, "", err
	}
	return actor, reason, arenaID, nil
}

// recordModeration emits the audit event for both fresh and replayed
// outcomes; the recorder deduplicates by (Arena, Action), so a retry can
// repair a lost record without duplicating it.
func recordModeration(
	ctx context.Context,
	audit ModerationAuditRecorder,
	clock Clock,
	actor domain.ModeratorID,
	arenaID domain.ArenaID,
	action ModerationAction,
	reason domain.Reason,
	arena *domain.Arena,
	replayed bool,
) (*ModerationResult, error) {
	event := ModerationEvent{
		ActorAccountID: actor,
		ArenaID:        arenaID,
		Action:         action,
		Reason:         reason,
		Replayed:       replayed,
		OccurredAt:     clock.Now(),
	}
	if err := audit.RecordArenaModeration(ctx, event); err != nil {
		return nil, err
	}
	return &ModerationResult{Arena: *arena, Replayed: replayed}, nil
}
