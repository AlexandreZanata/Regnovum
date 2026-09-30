package application

import (
	"context"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// PublishArenaCommand holds the parameters for publishing a draft. The
// Arena id doubles as the idempotency key: the pass consumption is unique
// per Arena, so retries never consume twice.
type PublishArenaCommand struct {
	AccountID string
	ArenaID   string
}

// PublishArenaResult is the outcome of a publication: the published Arena
// and whether the call resolved an already published Arena instead of
// publishing again.
type PublishArenaResult struct {
	Arena    domain.Arena
	Replayed bool
}

// PublishArenaUseCase publishes a draft and consumes exactly one Arena Pass
// in the same transaction. Either both writes commit or neither does: there
// is never a published Arena without a consumption nor an orphan
// consumption.
type PublishArenaUseCase struct {
	arenas ArenaRepository
	passes ArenaPassConsumer
	uow    UnitOfWork
	clock  Clock
}

// NewPublishArenaUseCase creates an instance of PublishArenaUseCase.
func NewPublishArenaUseCase(arenas ArenaRepository, passes ArenaPassConsumer, uow UnitOfWork, clock Clock) *PublishArenaUseCase {
	return &PublishArenaUseCase{arenas: arenas, passes: passes, uow: uow, clock: clock}
}

// Execute publishes the draft owned by the creator.
func (uc *PublishArenaUseCase) Execute(ctx context.Context, cmd PublishArenaCommand) (*PublishArenaResult, error) {
	creatorID := domain.CreatorID(cmd.AccountID)
	if creatorID.IsZero() {
		return nil, domain.ErrEmptyCreatorID
	}
	arenaID := domain.ArenaID(cmd.ArenaID)
	if arenaID.IsZero() {
		return nil, domain.ErrEmptyArenaID
	}

	var result *PublishArenaResult
	err := uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		arena, err := uc.arenas.GetArenaForCreator(txCtx, arenaID, creatorID)
		if err != nil {
			return err
		}

		switch arena.Status() {
		case domain.ArenaStatusPublished:
			// Idempotent retry: the Arena is already public and the pass was
			// already consumed.
			result = &PublishArenaResult{Arena: *arena, Replayed: true}
			return nil
		case domain.ArenaStatusDraft:
			// Proceed with the publication.
		default:
			return domain.ErrInvalidStatusChange
		}

		slug, err := DeriveSlug(*arena)
		if err != nil {
			return err
		}
		expectedVersion := arena.Version()
		if err := arena.Publish(slug, uc.clock.Now()); err != nil {
			return err
		}

		// The consumption joins this transaction (P07-T05): if the
		// publication fails afterwards, the consumed pass rolls back with it.
		if _, err := uc.passes.ConsumeArenaPass(txCtx, PassConsumption{
			AccountID: creatorID.String(),
			ArenaID:   arenaID.String(),
		}); err != nil {
			return err
		}

		published, err := uc.arenas.PublishArenaDraft(txCtx, arenaID, creatorID, slug, *arena.PublishedAt(), expectedVersion)
		if err != nil {
			return err
		}
		result = &PublishArenaResult{Arena: *published}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DeriveSlug derives the stable public address of an Arena: the slugified
// statement base plus the first eight hex characters of the Arena id.
// Retries of the same Arena always produce the same slug, and the id suffix
// keeps different Arenas apart even when their statements collide.
func DeriveSlug(arena domain.Arena) (domain.Slug, error) {
	suffix := shortArenaID(arena.ID())
	base := domain.Slugify(arena.Statement().String())

	maxBase := domain.SlugMaxLength - len(suffix) - 1
	if maxBase < domain.SlugMinLength {
		maxBase = domain.SlugMinLength
	}
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	if len(base) < domain.SlugMinLength {
		base = "arena"
	}

	return domain.ParseSlug(base + "-" + suffix)
}

// shortArenaID compacts an Arena identifier into its first eight lowercase
// hex characters.
func shortArenaID(id domain.ArenaID) string {
	compact := strings.ReplaceAll(strings.ToLower(id.String()), "-", "")
	if len(compact) < 8 {
		return compact
	}
	return compact[:8]
}
