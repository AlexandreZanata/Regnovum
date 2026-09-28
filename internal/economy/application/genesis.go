package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// GenesisCommand carries the creation event key. The amount is never a
// parameter: Genesis always credits exactly S to the Treasury, so a
// caller cannot ask for a partial or larger supply.
type GenesisCommand struct {
	Key string
}

// GenesisRequest is a validated creation event for the repository port.
type GenesisRequest struct {
	Key domain.GenesisKey
}

// GenesisResult is the outcome of the creation event: the Treasury
// custody that holds S, and whether the call replayed an earlier event
// (no new row or leg was written).
type GenesisResult struct {
	TreasuryCustodyID string
	Amount            domain.MilliInk
	Replayed          bool
}

// GenesisRepository persists the single creation event. Implementations
// run every step in one transaction: custody, partition, attestation and
// the credit leg commit together or not at all.
type GenesisRepository interface {
	// RunGenesis records the creation event under its key. The same key
	// resolves to the original attestation untouched; a different key
	// after Genesis fails with domain.ErrGenesisAlreadyExists.
	RunGenesis(ctx context.Context, request GenesisRequest) (*GenesisResult, error)
}

// GenesisUseCase validates and records the guarded creation event. It is
// an internal initializer: nothing in production startup or any public
// surface calls it.
type GenesisUseCase struct {
	genesis GenesisRepository
}

// NewGenesisUseCase creates an instance of GenesisUseCase.
func NewGenesisUseCase(genesis GenesisRepository) *GenesisUseCase {
	return &GenesisUseCase{genesis: genesis}
}

// Execute validates the event key and records Genesis exactly once.
func (uc *GenesisUseCase) Execute(ctx context.Context, cmd GenesisCommand) (*GenesisResult, error) {
	key, err := domain.ParseGenesisKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	return uc.genesis.RunGenesis(ctx, GenesisRequest{Key: key})
}
