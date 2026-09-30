package application

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

type transferFake struct {
	calls  int
	result *TransferResult
	err    error
	last   TransferRequest
}

func (f *transferFake) Transfer(ctx context.Context, request TransferRequest) (*TransferResult, error) {
	f.calls++
	f.last = request
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestTransferUseCaseSettlesValidatedEnvelope(t *testing.T) {
	fake := &transferFake{result: &TransferResult{TransferRowID: "row-1", TransferID: "tx-1", AmountMilli: 5000}}
	uc, err := NewTransferUseCase(fake)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	result, err := uc.Execute(context.Background(), TransferCommand{
		Key: "transfer-01", Kind: "gift", Payer: "payer-a", Payee: "payee-b",
		AmountMilli: 5000, ConsentRef: "consent-01",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != fake.result {
		t.Fatal("use case must return the repository outcome untouched")
	}
	if fake.calls != 1 || fake.last.Kind != domain.TransferGift || fake.last.PayloadHash == "" {
		t.Fatalf("validated request = %+v, want sealed gift", fake.last)
	}
	if _, err := NewTransferUseCase(nil); !errors.Is(err, ErrInvalidTransferConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidTransferConfig", err)
	}
}

func TestTransferUseCaseRefusesBeforeAnyWrite(t *testing.T) {
	base := TransferCommand{
		Key: "k", Kind: "trade", Payer: "a", Payee: "b", AmountMilli: 100, ConsentRef: "consent-01",
	}
	cases := []struct {
		name string
		mute func(*TransferCommand)
		want error
	}{
		{name: "empty key", mute: func(c *TransferCommand) { c.Key = "" }, want: domain.ErrInvalidIntention},
		{name: "unknown kind", mute: func(c *TransferCommand) { c.Kind = "tithe" }, want: domain.ErrInvalidTransferKind},
		{name: "empty kind", mute: func(c *TransferCommand) { c.Kind = "" }, want: domain.ErrInvalidTransferKind},
		{name: "blank payer", mute: func(c *TransferCommand) { c.Payer = " " }, want: domain.ErrInvalidIntention},
		{name: "blank payee", mute: func(c *TransferCommand) { c.Payee = "" }, want: domain.ErrInvalidIntention},
		{name: "self payment", mute: func(c *TransferCommand) { c.Payee = "a" }, want: domain.ErrSelfTransfer},
		{name: "zero amount", mute: func(c *TransferCommand) { c.AmountMilli = 0 }, want: domain.ErrInvalidIntention},
		{name: "negative amount", mute: func(c *TransferCommand) { c.AmountMilli = -3 }, want: domain.ErrInvalidIntention},
		{name: "missing consent", mute: func(c *TransferCommand) { c.ConsentRef = "" }, want: domain.ErrConsentRequired},
		{name: "blank consent", mute: func(c *TransferCommand) { c.ConsentRef = "  " }, want: domain.ErrConsentRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			muted := base
			tc.mute(&muted)
			fake := &transferFake{result: &TransferResult{}}
			uc, _ := NewTransferUseCase(fake)
			if _, err := uc.Execute(context.Background(), muted); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if fake.calls != 0 {
				t.Fatal("refused envelope must never reach the store")
			}
		})
	}
}
