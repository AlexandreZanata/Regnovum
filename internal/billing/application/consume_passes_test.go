package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

type fakeConsumer struct {
	requests []application.ConsumePassRequest
	result   *application.ConsumePassResult
	err      error
}

func (r *fakeConsumer) ConsumeArenaPass(_ context.Context, request application.ConsumePassRequest) (*application.ConsumePassResult, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return nil, r.err
	}
	return r.result, nil
}

const testArenaID = "018f6b2a-0000-7000-8000-000000000020"

func TestConsumeArenaPassUseCaseValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*application.ConsumeArenaPassCommand)
		want   error
	}{
		{name: "empty account", mutate: func(cmd *application.ConsumeArenaPassCommand) { cmd.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "empty arena", mutate: func(cmd *application.ConsumeArenaPassCommand) { cmd.ArenaID = "" }, want: domain.ErrEmptyArenaID},
		{name: "invalid arena", mutate: func(cmd *application.ConsumeArenaPassCommand) { cmd.ArenaID = "arena with space" }, want: domain.ErrInvalidArenaID},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			consumer := &fakeConsumer{}
			useCase := application.NewConsumeArenaPassUseCase(consumer, &fakeClock{now: testClockInstant})

			command := application.ConsumeArenaPassCommand{AccountID: testAccountID, ArenaID: testArenaID}
			tc.mutate(&command)

			result, err := useCase.Execute(context.Background(), command)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.want)
			}
			if result != nil {
				t.Fatal("validation failure must return no result")
			}
			if len(consumer.requests) != 0 {
				t.Fatalf("consumer was called %d times on validation error", len(consumer.requests))
			}
		})
	}
}

func TestConsumeArenaPassUseCaseBuildsRequestAndPropagatesOutcome(t *testing.T) {
	quantity, _ := domain.NewQuantity(1)
	reference, _ := domain.ParseReference("stripe:evt_consume")
	lot, err := domain.ReconstitutePassLot(
		domain.LotID("lot-consume"),
		domain.AccountID(testAccountID),
		domain.OriginPurchase,
		quantity,
		0,
		nil,
		reference,
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot: %v", err)
	}

	consumer := &fakeConsumer{result: &application.ConsumePassResult{Lot: *lot}}
	useCase := application.NewConsumeArenaPassUseCase(consumer, &fakeClock{now: testClockInstant})

	result, err := useCase.Execute(context.Background(), application.ConsumeArenaPassCommand{
		AccountID: testAccountID,
		ArenaID:   testArenaID,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Lot.ID().String() != "lot-consume" {
		t.Fatalf("result = %+v", result)
	}
	if len(consumer.requests) != 1 {
		t.Fatalf("consumer calls = %d, want 1", len(consumer.requests))
	}
	request := consumer.requests[0]
	if request.AccountID.String() != testAccountID || request.ArenaID.String() != testArenaID {
		t.Errorf("request = %s/%s", request.AccountID, request.ArenaID)
	}
	if !request.ConsumedAt.Equal(testClockInstant) {
		t.Errorf("ConsumedAt = %v, want %v", request.ConsumedAt, testClockInstant)
	}
}

func TestConsumeArenaPassUseCasePropagatesReplayAndErrors(t *testing.T) {
	consumer := &fakeConsumer{}
	useCase := application.NewConsumeArenaPassUseCase(consumer, &fakeClock{now: testClockInstant})

	consumer.err = domain.ErrNoPassAvailable
	if _, err := useCase.Execute(context.Background(), application.ConsumeArenaPassCommand{
		AccountID: testAccountID,
		ArenaID:   testArenaID,
	}); !errors.Is(err, domain.ErrNoPassAvailable) {
		t.Fatalf("no-pass error not propagated: %v", err)
	}

	consumer.err = application.ErrArenaAlreadyConsumed
	if _, err := useCase.Execute(context.Background(), application.ConsumeArenaPassCommand{
		AccountID: testAccountID,
		ArenaID:   testArenaID,
	}); !errors.Is(err, application.ErrArenaAlreadyConsumed) {
		t.Fatalf("cross-account error not propagated: %v", err)
	}
}
