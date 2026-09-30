package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

type refundFake struct {
	calls  int
	result *RefundResult
	err    error
	last   RefundRequest
}

func (f *refundFake) Refund(ctx context.Context, request RefundRequest) (*RefundResult, error) {
	f.calls++
	f.last = request
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestRefundUseCaseSettlesValidatedCause(t *testing.T) {
	fake := &refundFake{result: &RefundResult{RefundID: "rf-1", TransferID: "tx-9", AmountMilli: 2750}}
	uc, err := NewRefundUseCase(fake)
	if err != nil {
		t.Fatalf("NewRefundUseCase() error = %v", err)
	}
	result, err := uc.Execute(context.Background(), RefundCommand{
		Key: "refund-01", Account: "acct-01", OriginalKey: "pub-01", Reason: "publication-error",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != fake.result {
		t.Fatal("use case must return the repository outcome untouched")
	}
	if fake.calls != 1 || fake.last.Reason != "publication-error" {
		t.Fatal("validated request must reach the store once with its cause")
	}
	if _, err := NewRefundUseCase(nil); !errors.Is(err, ErrInvalidPublishConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidPublishConfig", err)
	}
}

func TestRefundUseCaseRefusesBeforeAnyWrite(t *testing.T) {
	cases := []struct {
		name string
		cmd  RefundCommand
		want error
	}{
		{name: "empty key", cmd: RefundCommand{Key: "", Account: "acct-01", OriginalKey: "pub-01", Reason: "r"}, want: domain.ErrInvalidPublishKey},
		{name: "blank account", cmd: RefundCommand{Key: "k", Account: " ", OriginalKey: "pub-01", Reason: "r"}, want: domain.ErrInvalidQuote},
		{name: "empty original", cmd: RefundCommand{Key: "k", Account: "acct-01", OriginalKey: "", Reason: "r"}, want: domain.ErrInvalidPublishKey},
		{name: "empty reason", cmd: RefundCommand{Key: "k", Account: "acct-01", OriginalKey: "pub-01", Reason: ""}, want: domain.ErrInvalidQuote},
		{name: "blank reason", cmd: RefundCommand{Key: "k", Account: "acct-01", OriginalKey: "pub-01", Reason: "  "}, want: domain.ErrInvalidQuote},
		{name: "control reason", cmd: RefundCommand{Key: "k", Account: "acct-01", OriginalKey: "pub-01", Reason: "a\x00b"}, want: domain.ErrInvalidQuote},
		{name: "long reason", cmd: RefundCommand{Key: "k", Account: "acct-01", OriginalKey: "pub-01", Reason: strings.Repeat("r", 281)}, want: domain.ErrInvalidQuote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &refundFake{result: &RefundResult{}}
			uc, _ := NewRefundUseCase(fake)
			if _, err := uc.Execute(context.Background(), tc.cmd); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if fake.calls != 0 {
				t.Fatal("refused envelope must never reach the store")
			}
		})
	}
}
