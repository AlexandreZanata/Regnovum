package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

type disguiseFake struct {
	view *DisguiseReviewView
	err  error
}

func (f *disguiseFake) FlagDisguise(context.Context, FlagDisguiseRequest) (*DisguiseReviewView, error) {
	return f.view, f.err
}

func (f *disguiseFake) ContestDisguise(context.Context, ContestDisguiseRequest) (*DisguiseReviewView, error) {
	return f.view, f.err
}

func (f *disguiseFake) ResolveDisguise(context.Context, ResolveDisguiseRequest) (*DisguiseReviewView, error) {
	return f.view, f.err
}

func disguiseNow() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func TestFlagDisguiseUseCaseValidatesEnvelope(t *testing.T) {
	fake := &disguiseFake{view: &DisguiseReviewView{}}
	uc, err := NewFlagDisguiseUseCase(fake)
	if err != nil {
		t.Fatalf("NewFlagDisguiseUseCase: %v", err)
	}
	view, err := uc.Execute(context.Background(), FlagDisguiseCommand{
		ReviewKey: "review-01", TransferKey: "gift-01", Payer: "payer-a",
		Reason: "contract", EvidenceHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Reporter: "watcher",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if view != fake.view {
		t.Fatal("use case must return the repository outcome untouched")
	}
	if _, err := uc.Execute(context.Background(), FlagDisguiseCommand{
		ReviewKey: "review-02", TransferKey: "gift-02", Payer: "payer-a",
		Reason: "gift", EvidenceHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Reporter: "watcher",
	}); !errors.Is(err, domain.ErrInvalidDisguise) {
		t.Fatalf("unknown reason = %v, want ErrInvalidDisguise before any store", err)
	}
	if _, err := NewFlagDisguiseUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidTransferConfig", err)
	}
}

func TestContestDisguiseUseCaseValidatesEnvelope(t *testing.T) {
	uc, _ := NewContestDisguiseUseCase(&disguiseFake{view: &DisguiseReviewView{}})
	if _, err := uc.Execute(context.Background(), ContestDisguiseCommand{
		ReviewKey: "review-01", TransferKey: "gift-01", Payer: "payer-a", By: "payer-a",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := uc.Execute(context.Background(), ContestDisguiseCommand{
		ReviewKey: "review-01", TransferKey: "gift-01", Payer: "payer-a", By: "",
	}); !errors.Is(err, domain.ErrDisguiseNotParty) {
		t.Fatalf("blank challenger = %v, want ErrDisguiseNotParty", err)
	}
	if _, err := NewContestDisguiseUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidTransferConfig", err)
	}
}

func TestResolveDisguiseUseCaseValidatesEnvelope(t *testing.T) {
	uc, _ := NewResolveDisguiseUseCase(&disguiseFake{view: &DisguiseReviewView{}})
	now := disguiseNow()
	if _, err := uc.Execute(context.Background(), ResolveDisguiseCommand{
		ReviewKey: "review-01", TransferKey: "gift-01", Payer: "payer-a",
		Decision: "dismiss", Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); err != nil {
		t.Fatalf("Execute dismiss: %v", err)
	}
	if _, err := uc.Execute(context.Background(), ResolveDisguiseCommand{
		ReviewKey: "review-01", TransferKey: "gift-01", Payer: "payer-a",
		Decision: "seize", Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); !errors.Is(err, domain.ErrInvalidDisguise) {
		t.Fatalf("unknown decision = %v, want ErrInvalidDisguise", err)
	}
	if _, err := uc.Execute(context.Background(), ResolveDisguiseCommand{
		ReviewKey: "review-01", TransferKey: "gift-01", Payer: "payer-a",
		Decision: "dismiss", Now: now, AppealUntil: now,
	}); !errors.Is(err, domain.ErrInvalidDisguise) {
		t.Fatalf("appeal without future = %v, want ErrInvalidDisguise", err)
	}
	if _, err := NewResolveDisguiseUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidTransferConfig", err)
	}
}
