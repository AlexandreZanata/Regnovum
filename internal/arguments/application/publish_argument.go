package application

import (
	"context"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

// SourceCommand is one requested source of a publication.
type SourceCommand struct {
	URL         string
	Description string
}

// PublishArgumentCommand holds one authenticated publication attempt: the
// content, its relation to the statement, optional sources, an optional
// parent for replies and the attempt idempotency key.
type PublishArgumentCommand struct {
	AccountID      string
	ArenaID        string
	ParentID       string
	Relation       string
	Content        string
	Sources        []SourceCommand
	IdempotencyKey string
}

// PublishArgumentResult is the outcome: the stored argument and whether the
// call resolved an earlier attempt instead of writing.
type PublishArgumentResult struct {
	Argument PublishedArgument
	Replayed bool
}

// PublishArgumentUseCase publishes one argument atomically: it validates the
// content with the injected UAX #29 counter (ADR-013), verifies eligibility
// and the parent, and then debits the INK cost, inserts the argument and its
// sources in the same transaction. The attempt idempotency key makes retries
// resolve the recorded argument without debiting again; no external call
// happens inside the transaction.
type PublishArgumentUseCase struct {
	arguments ArgumentRepository
	accounts  AccountEligibility
	arenas    ArenaEligibility
	wallet    InkDebit
	uow       UnitOfWork
	counter   domain.GraphemeCounter
	replies   domain.ReplyPolicy
	clock     Clock
}

// NewPublishArgumentUseCase creates an instance of PublishArgumentUseCase.
func NewPublishArgumentUseCase(
	arguments ArgumentRepository,
	accounts AccountEligibility,
	arenas ArenaEligibility,
	wallet InkDebit,
	uow UnitOfWork,
	counter domain.GraphemeCounter,
	replies domain.ReplyPolicy,
	clock Clock,
) *PublishArgumentUseCase {
	return &PublishArgumentUseCase{
		arguments: arguments,
		accounts:  accounts,
		arenas:    arenas,
		wallet:    wallet,
		uow:       uow,
		counter:   counter,
		replies:   replies,
		clock:     clock,
	}
}

// Execute publishes the argument.
func (uc *PublishArgumentUseCase) Execute(ctx context.Context, cmd PublishArgumentCommand) (*PublishArgumentResult, error) {
	if !uc.replies.IsValid() {
		return nil, domain.ErrInvalidPolicy
	}

	authorID, err := domain.ParseAccountID(cmd.AccountID)
	if err != nil {
		return nil, err
	}
	arenaID, err := domain.ParseArenaID(cmd.ArenaID)
	if err != nil {
		return nil, err
	}
	relation, err := domain.ParseRelation(cmd.Relation)
	if err != nil {
		return nil, err
	}
	content, err := domain.ParseContent(cmd.Content, uc.counter)
	if err != nil {
		return nil, err
	}
	key, err := domain.ParseIdempotencyKey(cmd.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	sources, err := parseSources(cmd.Sources)
	if err != nil {
		return nil, err
	}

	parentID := domain.ArgumentID{}
	if cmd.ParentID != "" {
		parentID, err = domain.ParseArgumentID(cmd.ParentID)
		if err != nil {
			return nil, err
		}
	}

	// Idempotent retry first: a recorded attempt is the result, so retries
	// never charge again and never re-check eligibility.
	if existing, err := uc.arguments.GetByAuthorAndIdempotencyKey(ctx, authorID, key); err == nil {
		return &PublishArgumentResult{Argument: *existing, Replayed: true}, nil
	} else if !errors.Is(err, ErrArgumentNotFound) {
		return nil, err
	}

	if err := uc.accounts.EnsureEligible(ctx, authorID); err != nil {
		return nil, err
	}
	if err := uc.arenas.EnsureAcceptsArguments(ctx, arenaID); err != nil {
		return nil, err
	}
	if !parentID.IsZero() {
		if err := uc.ensureReplyableParent(ctx, arenaID, parentID); err != nil {
			return nil, err
		}
	}

	at := uc.clock.Now()
	var created *PublishedArgument
	err = uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := uc.wallet.Debit(txCtx, InkDebitRequest{
			AccountID:      authorID.String(),
			Amount:         int64(content.GraphemeCost()),
			Reference:      key.WalletReference(),
			IdempotencyKey: key.String(),
		}); err != nil {
			return err
		}

		argument, inserted, err := uc.arguments.CreateArgument(txCtx, CreateArgumentRequest{
			ArenaID:        arenaID,
			AuthorID:       authorID,
			ParentID:       parentID,
			Relation:       relation,
			Content:        content,
			IdempotencyKey: key,
			CreatedAt:      at,
		})
		if err != nil {
			return err
		}
		if !inserted {
			// A concurrent attempt with the same key won: roll this
			// transaction back and resolve the replay outside.
			return ErrDuplicateIdempotencyKey
		}
		for _, source := range sources {
			if err := uc.arguments.CreateArgumentSource(txCtx, argument.ID, source, at); err != nil {
				return err
			}
		}
		created = argument
		return nil
	})
	if errors.Is(err, ErrDuplicateIdempotencyKey) {
		existing, readErr := uc.arguments.GetByAuthorAndIdempotencyKey(ctx, authorID, key)
		if readErr != nil {
			return nil, readErr
		}
		return &PublishArgumentResult{Argument: *existing, Replayed: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return &PublishArgumentResult{Argument: *created}, nil
}

// ensureReplyableParent validates that the parent belongs to the same Arena,
// is still published and accepts one more reply level under the domain depth
// policy. The rule lives here once: replies reuse this same use case.
func (uc *PublishArgumentUseCase) ensureReplyableParent(ctx context.Context, arenaID domain.ArenaID, parentID domain.ArgumentID) error {
	parent, depth, err := uc.arguments.GetParent(ctx, parentID)
	if err != nil {
		if errors.Is(err, ErrArgumentNotFound) {
			return ErrParentNotFound
		}
		return err
	}
	if !parent.ArenaID.Equals(arenaID) || parent.Status != statusPublished {
		return ErrParentNotAvailable
	}
	if !uc.replies.AllowsChildDepth(depth + 1) {
		return ErrReplyDepthExceeded
	}
	return nil
}

// Argument statuses owned by the use cases; the schema enforces the closed
// vocabulary and the immutability trigger protects the historical content.
const (
	statusPublished = "published"
	statusWithdrawn = "withdrawn"
	statusRemoved   = "removed"
)

// parseSources validates every requested source, preserving order.
func parseSources(commands []SourceCommand) ([]domain.Source, error) {
	if len(commands) == 0 {
		return nil, nil
	}
	sources := make([]domain.Source, 0, len(commands))
	for _, command := range commands {
		source, err := domain.ParseSource(command.URL, command.Description)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}
