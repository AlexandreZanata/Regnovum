package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// GenesisCommand carries the creation event key and its season book.
// The amount is never a parameter: Genesis always credits exactly S
// to the Treasury, so a caller cannot ask for a partial or larger
// supply. The season is mandatory: callers without a book are
// refused before anything is read.
type GenesisCommand struct {
	Key    string
	Season string
}

// GenesisRequest is a validated creation event for the repository port.
type GenesisRequest struct {
	Key    domain.GenesisKey
	Season domain.SeasonKey
}

// GenesisResult is the outcome of the creation event: the Treasury
// custody that holds S, and whether the call replayed an earlier event
// (no new row or leg was written).
type GenesisResult struct {
	TreasuryCustodyID string
	Amount            domain.MilliInk
	Replayed          bool
}

// GenesisRepository persists the single creation event per book.
// Implementations run every step in one transaction: custody,
// partition, attestation and the credit leg commit together or not
// at all.
type GenesisRepository interface {
	// RunGenesis records the creation event under its key in its
	// book. The same key resolves to the original attestation
	// untouched; a different key after Genesis in the same book
	// fails with domain.ErrGenesisAlreadyExists. Another book keeps
	// its own Genesis.
	RunGenesis(ctx context.Context, request GenesisRequest) (*GenesisResult, error)
}

// GenesisUseCase validates and records the guarded creation event in
// a prepared book. It is an internal initializer: nothing in
// production startup or any public surface calls it, and activation
// stays a separate step.
type GenesisUseCase struct {
	genesis GenesisRepository
	books   SeasonBooks
}

// NewGenesisUseCase creates an instance of GenesisUseCase.
func NewGenesisUseCase(genesis GenesisRepository, books SeasonBooks) *GenesisUseCase {
	return &GenesisUseCase{genesis: genesis, books: books}
}

// Execute validates the event key and its book and records Genesis
// exactly once per book.
func (uc *GenesisUseCase) Execute(ctx context.Context, cmd GenesisCommand) (*GenesisResult, error) {
	key, err := domain.ParseGenesisKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	if err := uc.books.RequirePrepared(ctx, season); err != nil {
		return nil, err
	}
	return uc.genesis.RunGenesis(ctx, GenesisRequest{Key: key, Season: season})
}
