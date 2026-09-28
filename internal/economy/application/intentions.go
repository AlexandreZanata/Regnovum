package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// IdempotentTransferCommand moves existing INK under one business
// intention: the triple (key, actor, operation) settles at most once, no
// matter how often an uncertain caller retries it.
type IdempotentTransferCommand struct {
	Key       string
	Actor     string
	Operation string
	FromKind  string
	FromLabel string
	ToKind    string
	ToLabel   string
	Millis    int64
}

// IdempotentTransferRequest is a validated intention for the port,
// carrying the payload hash that tells a replay from a conflict.
type IdempotentTransferRequest struct {
	Key         domain.IntentionKey
	Actor       domain.IntentionActor
	Operation   domain.IntentionOperation
	PayloadHash string
	FromKind    domain.CustodyKind
	FromLabel   string
	ToKind      domain.CustodyKind
	ToLabel     string
	Amount      domain.MilliInk
}

// IdempotentTransferResult is the settled outcome: the transfer that the
// intention owns, and whether the call replayed it (no new leg written).
type IdempotentTransferResult struct {
	TransferID string
	Debited    domain.MilliInk
	Credited   domain.MilliInk
	Replayed   bool
}

// IdempotentTransferRepository settles monetary intentions exactly once.
// Implementations persist the response in the same transaction as the
// legs, so a retry after a post-commit timeout reads the stored outcome.
type IdempotentTransferRepository interface {
	// TransferIdempotent settles the intention or resolves its stored
	// response. The same triple with the same payload replays untouched;
	// the same triple with another payload fails with
	// domain.ErrIntentionConflict.
	TransferIdempotent(ctx context.Context, request IdempotentTransferRequest) (*IdempotentTransferResult, error)
}

// IdempotentTransferUseCase validates and settles one business intention
// at most once. It is an internal operation: no public surface calls it.
type IdempotentTransferUseCase struct {
	intentions IdempotentTransferRepository
}

// NewIdempotentTransferUseCase creates an instance of
// IdempotentTransferUseCase.
func NewIdempotentTransferUseCase(intentions IdempotentTransferRepository) *IdempotentTransferUseCase {
	return &IdempotentTransferUseCase{intentions: intentions}
}

// payloadHash binds the transfer parameters to the intention: any
// difference in endpoints or amount produces another hash, so reuse of a
// key with changed terms can never pass as a replay.
func payloadHash(fromKind, fromLabel, toKind, toLabel string, millis int64) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", fromKind, fromLabel, toKind, toLabel, millis)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// Execute validates the intention and settles it exactly once.
func (uc *IdempotentTransferUseCase) Execute(ctx context.Context, cmd IdempotentTransferCommand) (*IdempotentTransferResult, error) {
	key, err := domain.ParseIntentionKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	actor, err := domain.ParseIntentionActor(cmd.Actor)
	if err != nil {
		return nil, err
	}
	operation, err := domain.ParseIntentionOperation(cmd.Operation)
	if err != nil {
		return nil, err
	}
	ends, err := validateTransferEnds(cmd.FromKind, cmd.FromLabel, cmd.ToKind, cmd.ToLabel, cmd.Millis)
	if err != nil {
		return nil, err
	}
	return uc.intentions.TransferIdempotent(ctx, IdempotentTransferRequest{
		Key:         key,
		Actor:       actor,
		Operation:   operation,
		PayloadHash: payloadHash(cmd.FromKind, cmd.FromLabel, cmd.ToKind, cmd.ToLabel, cmd.Millis),
		FromKind:    ends.fromKind,
		FromLabel:   ends.fromLabel,
		ToKind:      ends.toKind,
		ToLabel:     ends.toLabel,
		Amount:      ends.amount,
	})
}
