package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubGenesisRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: key validation happens before it is
// ever called, and one canned attestation proves the success mapping.
type stubGenesisRepository struct {
	called int
	result *application.GenesisResult
	err    error
}

func (s *stubGenesisRepository) RunGenesis(_ context.Context, _ application.GenesisRequest) (*application.GenesisResult, error) {
	s.called++
	return s.result, s.err
}

// stubSeasonBooks permits every book: book gating itself is proven
// against PostgreSQL, where lifecycle rows exist.
type stubSeasonBooks struct{}

func (stubSeasonBooks) RequirePrepared(context.Context, domain.SeasonKey) error { return nil }
func (stubSeasonBooks) RequireActive(context.Context, domain.SeasonKey) error   { return nil }

func TestGenesisUseCaseRefusesInvalidKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"", "   ", strings.Repeat("k", 129), "gen\x00esis", "gen\tes\u200bis"} {
		stub := &stubGenesisRepository{}
		useCase := application.NewGenesisUseCase(stub, stubSeasonBooks{})
		if _, err := useCase.Execute(context.Background(), application.GenesisCommand{Key: key, Season: domain.CompatSeasonKey}); !errors.Is(err, domain.ErrInvalidGenesisKey) {
			t.Errorf("Execute(%q) = %v, want ErrInvalidGenesisKey", key, err)
		}
		if stub.called != 0 {
			t.Errorf("Execute(%q) reached the repository: invalid keys never touch storage", key)
		}
	}
}

func TestGenesisUseCaseRefusesMissingSeason(t *testing.T) {
	t.Parallel()

	stub := &stubGenesisRepository{}
	useCase := application.NewGenesisUseCase(stub, stubSeasonBooks{})
	if _, err := useCase.Execute(context.Background(), application.GenesisCommand{Key: "genesis-key"}); !errors.Is(err, domain.ErrMissingSeason) {
		t.Errorf("Execute without season = %v, want ErrMissingSeason", err)
	}
	if stub.called != 0 {
		t.Errorf("seasonless command reached the repository: missing books never touch storage")
	}
}

func TestGenesisUseCaseMapsRepositoryOutcome(t *testing.T) {
	t.Parallel()

	want := &application.GenesisResult{TreasuryCustodyID: "treasury-id", Amount: domain.GenesisSupply()}
	stub := &stubGenesisRepository{result: want}
	useCase := application.NewGenesisUseCase(stub, stubSeasonBooks{})
	got, err := useCase.Execute(context.Background(), application.GenesisCommand{Key: "genesis-key", Season: domain.CompatSeasonKey})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the recorded attestation")
	}

	stub = &stubGenesisRepository{err: domain.ErrGenesisAlreadyExists}
	useCase = application.NewGenesisUseCase(stub, stubSeasonBooks{})
	if _, err := useCase.Execute(context.Background(), application.GenesisCommand{Key: "other-key", Season: domain.CompatSeasonKey}); !errors.Is(err, domain.ErrGenesisAlreadyExists) {
		t.Fatalf("Execute with a second key = %v, want ErrGenesisAlreadyExists", err)
	}
}
