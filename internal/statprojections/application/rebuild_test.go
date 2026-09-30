package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/statprojections/application"
)

type fakeRepository struct {
	calls []string
}

func (f *fakeRepository) RebuildArenaBatch(_ context.Context, cursor string, limit int) (application.ArenaRebuildBatch, error) {
	f.calls = append(f.calls, cursor)
	if cursor == "" {
		return application.ArenaRebuildBatch{Processed: limit, NextCursor: "11111111-1111-1111-1111-111111111111"}, nil
	}
	return application.ArenaRebuildBatch{Processed: 1}, nil
}

func (f *fakeRepository) RebuildTransparency(context.Context, time.Time, time.Time) error { return nil }

func TestRebuildArenaBatchResumesFromCursor(t *testing.T) {
	repo := &fakeRepository{}
	uc, err := application.NewRebuildUseCase(repo, 10)
	if err != nil {
		t.Fatalf("NewRebuildUseCase: %v", err)
	}
	first, err := uc.RebuildArenaBatch(context.Background(), "")
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first.Processed != 10 || first.NextCursor == "" {
		t.Fatalf("first batch = %+v, want a resumable cursor", first)
	}
	second, err := uc.RebuildArenaBatch(context.Background(), first.NextCursor)
	if err != nil {
		t.Fatalf("resumed batch: %v", err)
	}
	if second.Processed != 1 || second.NextCursor != "" {
		t.Fatalf("resumed batch = %+v, want terminal batch", second)
	}
	if len(repo.calls) != 2 || repo.calls[1] != first.NextCursor {
		t.Fatalf("repository cursors = %v, want the persisted cursor on resume", repo.calls)
	}
}

func TestRebuildUseCaseRejectsInvalidConfiguration(t *testing.T) {
	if _, err := application.NewRebuildUseCase(nil, 1); err == nil {
		t.Fatal("nil repository must be rejected")
	}
	repo := &fakeRepository{}
	for _, size := range []int{-1, application.MaxBatchSize + 1} {
		if _, err := application.NewRebuildUseCase(repo, size); err == nil {
			t.Fatalf("batch size %d must be rejected", size)
		}
	}
	uc, err := application.NewRebuildUseCase(repo, 1)
	if err != nil {
		t.Fatalf("NewRebuildUseCase: %v", err)
	}
	if _, err := uc.RebuildArenaBatch(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("invalid cursor must be rejected before the adapter")
	}
	if err := uc.RebuildTransparency(context.Background(), time.Time{}, time.Now()); err == nil {
		t.Fatal("zero period must be rejected")
	}
}
