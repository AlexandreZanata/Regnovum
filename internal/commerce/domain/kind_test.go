package domain

import (
	"errors"
	"testing"
)

func TestParseTransferKindRefusesAbsentAndAmbiguous(t *testing.T) {
	for _, raw := range []string{"", " ", "Gift", "GIFT", " gift", "gift ", "trade/gift", "gift,trade", "presente", "pagamento", "tithe"} {
		if _, err := ParseTransferKind(raw); !errors.Is(err, ErrInvalidTransferKind) {
			t.Errorf("ParseTransferKind(%q) = %v, want ErrInvalidTransferKind", raw, err)
		}
	}
	for _, raw := range []string{"gift", "trade", "refund", "treasury"} {
		kind, err := ParseTransferKind(raw)
		if err != nil {
			t.Errorf("ParseTransferKind(%q) = %v, want nil", raw, err)
		} else if kind.String() != raw || !kind.IsValid() {
			t.Errorf("round-trip %q failed", raw)
		}
	}
	if len(AllTransferKinds()) != 4 {
		t.Fatalf("vocabulary = %d kinds, want the closed four", len(AllTransferKinds()))
	}
}

func TestKindRoutesTitheWithoutPricing(t *testing.T) {
	if !TransferTrade.BearsTithe() {
		t.Error("formal trade must route to tithe settlement")
	}
	for _, kind := range []TransferKind{TransferGift, TransferRefund, TransferTreasury} {
		if kind.BearsTithe() {
			t.Errorf("%q must never route to tithe", kind)
		}
	}
}

func TestAcceptIntentionSealsKind(t *testing.T) {
	intention, err := AcceptIntention(IntentionRequest{
		Key: "intent-01", Kind: TransferTrade, Payer: "payer-a", Payee: "payee-b", AmountMilli: 10000,
	})
	if err != nil {
		t.Fatalf("AcceptIntention: %v", err)
	}
	if err := intention.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
	reclassified := intention
	reclassified.Kind = TransferGift
	if err := intention.Matches(reclassified); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("reclassified seal = %v, want ErrInvalidTerms (seal breaks first)", err)
	}
	forged := intention
	forged.Kind = TransferGift
	forged.Hash = sealIntention(forged)
	if err := intention.Matches(forged); !errors.Is(err, ErrIntentionConflict) {
		t.Fatalf("same key other kind = %v, want ErrIntentionConflict, never a silent type change", err)
	}
	if err := intention.Matches(intention); err != nil {
		t.Fatalf("self match: %v", err)
	}
}

func TestAcceptIntentionRefusals(t *testing.T) {
	base := IntentionRequest{Key: "k", Kind: TransferGift, Payer: "a", Payee: "b", AmountMilli: 100}
	cases := []struct {
		name string
		mute func(*IntentionRequest)
		want error
	}{
		{name: "empty key", mute: func(r *IntentionRequest) { r.Key = "" }, want: ErrInvalidIntention},
		{name: "blank payer", mute: func(r *IntentionRequest) { r.Payer = " " }, want: ErrInvalidIntention},
		{name: "blank payee", mute: func(r *IntentionRequest) { r.Payee = "" }, want: ErrInvalidIntention},
		{name: "unknown kind", mute: func(r *IntentionRequest) { r.Kind = TransferKind("tithe") }, want: ErrInvalidTransferKind},
		{name: "empty kind", mute: func(r *IntentionRequest) { r.Kind = "" }, want: ErrInvalidTransferKind},
		{name: "zero amount", mute: func(r *IntentionRequest) { r.AmountMilli = 0 }, want: ErrInvalidIntention},
		{name: "negative amount", mute: func(r *IntentionRequest) { r.AmountMilli = -5 }, want: ErrInvalidIntention},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mute(&req)
			if _, err := AcceptIntention(req); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVisibleTermsReachBothParties(t *testing.T) {
	intention, err := AcceptIntention(IntentionRequest{
		Key: "intent-02", Kind: TransferTrade, Payer: "payer-a", Payee: "payee-b", AmountMilli: 10000,
	})
	if err != nil {
		t.Fatalf("AcceptIntention: %v", err)
	}
	payerView, err := intention.DescribeFor("payer-a")
	if err != nil {
		t.Fatalf("payer view: %v", err)
	}
	payeeView, err := intention.DescribeFor("payee-b")
	if err != nil {
		t.Fatalf("payee view: %v", err)
	}
	if payerView != payeeView {
		t.Fatalf("payer sees %+v, payee sees %+v: both sides read identical terms", payerView, payeeView)
	}
	if payerView.Kind != TransferTrade || payerView.AmountMilli != 10000 {
		t.Fatalf("terms = %+v, want sealed kind and amount", payerView)
	}
	if _, err := intention.DescribeFor("stranger"); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("stranger view = %v, want ErrInvalidTerms", err)
	}
}
