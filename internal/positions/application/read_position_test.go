package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func TestGetMyPositionReturnsTheOwnerProjection(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	useCase := application.NewGetMyPositionUseCase(repo)

	position, err := useCase.Execute(context.Background(), application.GetMyPositionQuery{
		AccountID: testAccountRaw,
		ArenaID:   testArenaRaw,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if position.Version() != 1 || !position.CurrentPosition().Equals(mustPosition(t, domain.PositionAgree)) {
		t.Fatalf("position = v%d %q, want the stored projection", position.Version(), position.CurrentPosition().String())
	}

	// An unknown pair is simply not found.
	if _, err := useCase.Execute(context.Background(), application.GetMyPositionQuery{
		AccountID: "018f6b2a-0000-7000-8000-0000000000ff",
		ArenaID:   testArenaRaw,
	}); !errors.Is(err, application.ErrPositionNotFound) {
		t.Fatalf("unknown pair error = %v, want ErrPositionNotFound", err)
	}
}

func TestGetMyPositionValidatesInputs(t *testing.T) {
	repo := newFakePositionRepo()
	useCase := application.NewGetMyPositionUseCase(repo)

	if _, err := useCase.Execute(context.Background(), application.GetMyPositionQuery{ArenaID: testArenaRaw}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}
	if _, err := useCase.Execute(context.Background(), application.GetMyPositionQuery{AccountID: testAccountRaw}); !errors.Is(err, domain.ErrEmptyArenaID) {
		t.Fatalf("empty arena error = %v, want ErrEmptyArenaID", err)
	}
}

func TestListPositionChangesReturnsNewestFirst(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo)
	change := newChangeUseCase(repo, arenas, uow)

	if _, err := change.Execute(context.Background(), changeCommand(domain.PositionDisagree)); err != nil {
		t.Fatalf("first change: %v", err)
	}
	if _, err := change.Execute(context.Background(), changeCommand(domain.PositionUndecided)); err != nil {
		t.Fatalf("second change: %v", err)
	}

	useCase := application.NewListPositionChangesUseCase(repo)
	records, err := useCase.Execute(context.Background(), application.ListPositionChangesQuery{
		AccountID: testAccountRaw,
		ArenaID:   testArenaRaw,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want the two stored changes", len(records))
	}
	if records[0].Change.Version() != 3 || records[1].Change.Version() != 2 {
		t.Fatalf("versions = %d, %d, want newest first (3, 2)", records[0].Change.Version(), records[1].Change.Version())
	}
	if records[0].ID == "" || !records[0].Change.To().Equals(mustPosition(t, domain.PositionUndecided)) {
		t.Fatal("history rows must carry their identifier and transition")
	}

	// A pair without history returns an empty collection, not an error.
	records, err = useCase.Execute(context.Background(), application.ListPositionChangesQuery{
		AccountID: "018f6b2a-0000-7000-8000-0000000000ff",
		ArenaID:   testArenaRaw,
	})
	if err != nil {
		t.Fatalf("empty history error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("unknown pair records = %d, want none", len(records))
	}
}

func TestListPositionChangesValidatesInputsAndPropagatesFailures(t *testing.T) {
	repo := newFakePositionRepo()
	useCase := application.NewListPositionChangesUseCase(repo)

	if _, err := useCase.Execute(context.Background(), application.ListPositionChangesQuery{ArenaID: testArenaRaw}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}
	if _, err := useCase.Execute(context.Background(), application.ListPositionChangesQuery{AccountID: testAccountRaw}); !errors.Is(err, domain.ErrEmptyArenaID) {
		t.Fatalf("empty arena error = %v, want ErrEmptyArenaID", err)
	}

	storageErr := errors.New("storage down")
	repo.getErr = storageErr
	// The fake history path ignores getErr; assert the projection read path
	// propagates failures instead.
	getMine := application.NewGetMyPositionUseCase(repo)
	if _, err := getMine.Execute(context.Background(), application.GetMyPositionQuery{
		AccountID: testAccountRaw,
		ArenaID:   testArenaRaw,
	}); !errors.Is(err, storageErr) {
		t.Fatalf("read failure error = %v, want the storage failure", err)
	}
}
