package application

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

type serviceRefundFake struct {
	result *ServiceRefundResult
	err    error
}

func (f *serviceRefundFake) RefundService(context.Context, ServiceRefundRequest) (*ServiceRefundResult, error) {
	return f.result, f.err
}

func TestRefundServiceUseCaseValidatesEnvelope(t *testing.T) {
	fake := &serviceRefundFake{result: &ServiceRefundResult{}}
	uc, err := NewRefundServiceUseCase(fake)
	if err != nil {
		t.Fatalf("NewRefundServiceUseCase: %v", err)
	}
	cmd := ServiceRefundCommand{
		ContractKey: "trade-01", Buyer: "buyer-a", RefundKey: "refund-01", AmountMill: 20000,
	}
	result, err := uc.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != fake.result {
		t.Fatal("use case must return the repository outcome untouched")
	}
	for _, bad := range []ServiceRefundCommand{
		{ContractKey: "", Buyer: "buyer-a", RefundKey: "refund-02", AmountMill: 100},
		{ContractKey: "trade-01", Buyer: "", RefundKey: "refund-02", AmountMill: 100},
		{ContractKey: "trade-01", Buyer: "buyer-a", RefundKey: "", AmountMill: 100},
		{ContractKey: "trade-01", Buyer: "buyer-a", RefundKey: "refund-02", AmountMill: 0},
		{ContractKey: "trade-01", Buyer: "buyer-a", RefundKey: "refund-02", AmountMill: -5},
	} {
		if _, err := uc.Execute(context.Background(), bad); !errors.Is(err, domain.ErrInvalidContract) {
			t.Fatalf("bad %+v = %v, want ErrInvalidContract before any store", bad, err)
		}
	}
	if _, err := NewRefundServiceUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidTransferConfig", err)
	}
}
