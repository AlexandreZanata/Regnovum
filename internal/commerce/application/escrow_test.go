package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

type escrowFake struct {
	view *ContractView
	err  error
}

func (f *escrowFake) FundContract(context.Context, FundRequest) (*ContractView, error) {
	return f.view, f.err
}

func (f *escrowFake) AcceptDelivery(context.Context, string, string) (*ContractView, error) {
	return f.view, f.err
}

func (f *escrowFake) ReleaseContract(context.Context, string, string) (*ContractView, error) {
	return f.view, f.err
}

func (f *escrowFake) CancelContract(context.Context, string, string) (*ContractView, error) {
	return f.view, f.err
}

func (f *escrowFake) ExpireContract(context.Context, string, string, time.Time) (*ContractView, error) {
	return f.view, f.err
}

func (f *escrowFake) ResolveContract(context.Context, string, string, string, domain.ResolveDecision) (*ContractView, error) {
	return f.view, f.err
}

func escrowNow() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func TestFundContractUseCaseValidatesEnvelope(t *testing.T) {
	fake := &escrowFake{view: &ContractView{}}
	uc, err := NewFundContractUseCase(fake)
	if err != nil {
		t.Fatalf("NewFundContractUseCase: %v", err)
	}
	now := escrowNow()
	view, err := uc.Execute(context.Background(), FundCommand{
		Key: "escrow-01", Object: "revisão", Buyer: "buyer-a", Provider: "provider-b",
		AmountMill: 20000, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if view != fake.view {
		t.Fatal("use case must return the repository outcome untouched")
	}
	if _, err := uc.Execute(context.Background(), FundCommand{
		Key: "escrow-02", Object: "revisão", Buyer: "buyer-a", Provider: "provider-b",
		AmountMill: 0, ExpiresAt: now.Add(time.Hour), Now: now,
	}); !errors.Is(err, domain.ErrInvalidContract) {
		t.Fatalf("zero amount = %v, want ErrInvalidContract before any store", err)
	}
	if _, err := NewFundContractUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidTransferConfig", err)
	}
}

func TestEscrowTransitionsValidateEnvelopes(t *testing.T) {
	now := escrowNow()
	t.Run("accept", func(t *testing.T) {
		uc, _ := NewAcceptDeliveryUseCase(&escrowFake{view: &ContractView{}})
		if _, err := uc.Execute(context.Background(), "k", "buyer-a"); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := uc.Execute(context.Background(), "", "buyer-a"); !errors.Is(err, domain.ErrInvalidContract) {
			t.Fatalf("empty key = %v, want ErrInvalidContract", err)
		}
		if _, err := NewAcceptDeliveryUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
			t.Fatalf("nil = %v, want ErrInvalidTransferConfig", err)
		}
	})
	t.Run("release", func(t *testing.T) {
		uc, _ := NewReleaseContractUseCase(&escrowFake{view: &ContractView{}})
		if _, err := uc.Execute(context.Background(), "k", "buyer-a"); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := NewReleaseContractUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
			t.Fatalf("nil = %v, want ErrInvalidTransferConfig", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		uc, _ := NewCancelContractUseCase(&escrowFake{view: &ContractView{}})
		if _, err := uc.Execute(context.Background(), "k", ""); !errors.Is(err, domain.ErrInvalidContract) {
			t.Fatalf("empty buyer = %v, want ErrInvalidContract", err)
		}
		if _, err := NewCancelContractUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
			t.Fatalf("nil = %v, want ErrInvalidTransferConfig", err)
		}
	})
	t.Run("expire", func(t *testing.T) {
		uc, _ := NewExpireContractUseCase(&escrowFake{view: &ContractView{}})
		if _, err := uc.Execute(context.Background(), "k", "buyer-a", now); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := NewExpireContractUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
			t.Fatalf("nil = %v, want ErrInvalidTransferConfig", err)
		}
	})
	t.Run("resolve", func(t *testing.T) {
		uc, _ := NewResolveContractUseCase(&escrowFake{view: &ContractView{}})
		if _, err := uc.Execute(context.Background(), "k", "buyer-a", "arbiter-1", "release"); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := uc.Execute(context.Background(), "k", "buyer-a", "arbiter-1", "burn"); !errors.Is(err, domain.ErrInvalidContract) {
			t.Fatalf("bad decision = %v, want ErrInvalidContract", err)
		}
		if _, err := NewResolveContractUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
			t.Fatalf("nil = %v, want ErrInvalidTransferConfig", err)
		}
	})
}
