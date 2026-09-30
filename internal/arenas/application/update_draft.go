package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// UpdateArenaDraftCommand holds the parameters for replacing a draft. The
// expected version comes from the last read: a mismatch fails with
// ErrVersionConflict instead of overwriting a newer draft.
type UpdateArenaDraftCommand struct {
	AccountID       string
	ArenaID         string
	Statement       string
	Context         string
	Category        string
	Language        string
	ExpectedVersion int32
}

// UpdateArenaDraftUseCase applies the draft transition under the optimistic
// version check. Only the creator reaches the Arena at all.
type UpdateArenaDraftUseCase struct {
	arenas ArenaRepository
	policy domain.StatementPolicy
}

// NewUpdateArenaDraftUseCase creates an instance of
// UpdateArenaDraftUseCase with the versioned statement policy.
func NewUpdateArenaDraftUseCase(arenas ArenaRepository, policy domain.StatementPolicy) *UpdateArenaDraftUseCase {
	return &UpdateArenaDraftUseCase{arenas: arenas, policy: policy}
}

// Execute validates the replacement, verifies the version and persists it.
func (uc *UpdateArenaDraftUseCase) Execute(ctx context.Context, cmd UpdateArenaDraftCommand) (*domain.Arena, error) {
	creatorID := domain.CreatorID(cmd.AccountID)
	if creatorID.IsZero() {
		return nil, domain.ErrEmptyCreatorID
	}
	arenaID := domain.ArenaID(cmd.ArenaID)
	if arenaID.IsZero() {
		return nil, domain.ErrEmptyArenaID
	}
	if cmd.ExpectedVersion < 1 {
		return nil, domain.ErrInvalidVersion
	}

	statement, err := domain.ParseStatement(cmd.Statement, uc.policy)
	if err != nil {
		return nil, err
	}
	context, err := domain.ParseContext(cmd.Context, uc.policy)
	if err != nil {
		return nil, err
	}
	category, err := domain.ParseCategory(cmd.Category)
	if err != nil {
		return nil, err
	}
	language, err := domain.ParseLanguage(cmd.Language)
	if err != nil {
		return nil, err
	}

	arena, err := uc.arenas.GetArenaForCreator(ctx, arenaID, creatorID)
	if err != nil {
		return nil, err
	}
	if arena.Version() != cmd.ExpectedVersion {
		return nil, ErrVersionConflict
	}

	// The entity enforces the draft-only transition; the repository repeats
	// the version check atomically to close the read-modify-write race.
	if err := arena.UpdateDraft(&statement, &context, &category, &language); err != nil {
		return nil, err
	}

	return uc.arenas.UpdateArenaDraft(ctx, arenaID, creatorID, DraftUpdate{
		Statement:       statement,
		Context:         context,
		Category:        category,
		Language:        language,
		ExpectedVersion: cmd.ExpectedVersion,
	})
}
