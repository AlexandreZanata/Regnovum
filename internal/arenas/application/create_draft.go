package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// CreateArenaDraftCommand holds the parameters for a new Arena draft. The
// context is optional; statement, category and language are required.
type CreateArenaDraftCommand struct {
	AccountID string
	Statement string
	Context   string
	Category  string
	Language  string
}

// CreateArenaDraftUseCase validates the formulation and stores a private
// draft. Drafts never consume an Arena Pass and are never publicly visible.
type CreateArenaDraftUseCase struct {
	arenas ArenaRepository
	policy domain.StatementPolicy
}

// NewCreateArenaDraftUseCase creates an instance of
// CreateArenaDraftUseCase with the versioned statement policy.
func NewCreateArenaDraftUseCase(arenas ArenaRepository, policy domain.StatementPolicy) *CreateArenaDraftUseCase {
	return &CreateArenaDraftUseCase{arenas: arenas, policy: policy}
}

// Execute validates and persists the draft.
func (uc *CreateArenaDraftUseCase) Execute(ctx context.Context, cmd CreateArenaDraftCommand) (*domain.Arena, error) {
	creatorID := domain.CreatorID(cmd.AccountID)
	if creatorID.IsZero() {
		return nil, domain.ErrEmptyCreatorID
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

	return uc.arenas.CreateArena(ctx, CreateArenaRequest{
		CreatorID: creatorID,
		Statement: statement,
		Context:   context,
		Category:  category,
		Language:  language,
	})
}
