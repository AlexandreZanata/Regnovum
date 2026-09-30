package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
)

// transactionalStore is a deterministic model of the shared transaction:
// writes are staged while a transaction is open and only become visible on
// commit; a rollback discards the whole stage.
type transactionalStore struct {
	pending   []application.PassConsumption
	committed []application.PassConsumption
}

type fakeUnitOfWork struct {
	store *transactionalStore
}

func (u *fakeUnitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	u.store.pending = nil
	if err := fn(ctx); err != nil {
		u.store.pending = nil
		return err
	}
	u.store.committed = append(u.store.committed, u.store.pending...)
	u.store.pending = nil
	return nil
}

type fakePassConsumer struct {
	store *transactionalStore
}

func (c *fakePassConsumer) ConsumeArenaPass(_ context.Context, request application.PassConsumption) (*application.ConsumedPass, error) {
	c.store.pending = append(c.store.pending, request)
	return &application.ConsumedPass{LotID: "lot-1", Remaining: 0}, nil
}

func TestPublicationCommitsPassConsumption(t *testing.T) {
	store := &transactionalStore{}
	uow := &fakeUnitOfWork{store: store}
	passes := &fakePassConsumer{store: store}

	err := uow.WithinTransaction(context.Background(), func(ctx context.Context) error {
		if _, err := passes.ConsumeArenaPass(ctx, application.PassConsumption{AccountID: "account-1", ArenaID: "arena-1"}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTransaction() error = %v", err)
	}
	if len(store.committed) != 1 || store.committed[0].ArenaID != "arena-1" {
		t.Fatalf("committed = %+v, want the pass consumption of arena-1", store.committed)
	}
	if len(store.pending) != 0 {
		t.Fatalf("pending = %+v, want empty after commit", store.pending)
	}
}

// TestPublicationRollsBackConsumptionWhenPublicationFails is the P07-T05
// acceptance probe: the consumption and the publication share the same unit
// of work, so a failed publication leaves no consumed pass behind.
func TestPublicationRollsBackConsumptionWhenPublicationFails(t *testing.T) {
	store := &transactionalStore{}
	uow := &fakeUnitOfWork{store: store}
	passes := &fakePassConsumer{store: store}
	publicationErr := errors.New("arena insertion failed")

	err := uow.WithinTransaction(context.Background(), func(ctx context.Context) error {
		if _, err := passes.ConsumeArenaPass(ctx, application.PassConsumption{AccountID: "account-1", ArenaID: "arena-2"}); err != nil {
			return err
		}
		// The publication write fails after the pass was consumed.
		return publicationErr
	})
	if !errors.Is(err, publicationErr) {
		t.Fatalf("WithinTransaction() error = %v, want the publication failure", err)
	}
	if len(store.committed) != 0 {
		t.Fatalf("committed = %+v, want nothing committed after a failed publication", store.committed)
	}
	if len(store.pending) != 0 {
		t.Fatalf("pending = %+v, want the consumption discarded", store.pending)
	}
}

func TestPublicationPropagatesConsumptionFailure(t *testing.T) {
	store := &transactionalStore{}
	uow := &fakeUnitOfWork{store: store}
	consumptionErr := errors.New("no pass available")

	err := uow.WithinTransaction(context.Background(), func(ctx context.Context) error {
		_ = consumptionErr
		return consumptionErr
	})
	if !errors.Is(err, consumptionErr) {
		t.Fatalf("WithinTransaction() error = %v, want the consumption failure", err)
	}
	if len(store.committed) != 0 {
		t.Fatal("a failed consumption must not commit anything")
	}
}
