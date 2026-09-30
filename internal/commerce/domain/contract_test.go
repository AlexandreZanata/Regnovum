package domain

import (
	"errors"
	"testing"
	"time"
)

func fundedContract(t *testing.T) TradeContract {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	contract, err := FundContract(ContractRequest{
		Key: "contract-01", Object: "revisão de contrato", Buyer: "buyer-a", Provider: "provider-b",
		AmountMill: 20000, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil {
		t.Fatalf("FundContract: %v", err)
	}
	return contract
}

func TestFundContractSealsTerms(t *testing.T) {
	contract := fundedContract(t)
	if contract.Status != ContractFunded {
		t.Fatalf("status = %q, want funded", contract.Status)
	}
	if err := contract.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
	if contract.Status.Terminal() {
		t.Fatal("funded contracts must not read terminal")
	}
	for _, status := range []ContractStatus{ContractAccepted, ContractReleased, ContractRefunded, ContractExpired, ContractResolved} {
		if !status.IsValid() {
			t.Errorf("status %q must be valid", status)
		}
	}
	if _, err := ParseContractStatus("delivered"); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("unknown status = %v, want ErrInvalidContract", err)
	}
}

func TestFundContractRefusals(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	base := ContractRequest{
		Key: "k", Object: "obj", Buyer: "a", Provider: "b",
		AmountMill: 100, ExpiresAt: now.Add(time.Hour), Now: now,
	}
	cases := []struct {
		name string
		mute func(*ContractRequest)
	}{
		{name: "empty key", mute: func(r *ContractRequest) { r.Key = "" }},
		{name: "empty object", mute: func(r *ContractRequest) { r.Object = "  " }},
		{name: "blank buyer", mute: func(r *ContractRequest) { r.Buyer = "" }},
		{name: "blank provider", mute: func(r *ContractRequest) { r.Provider = "" }},
		{name: "same parties", mute: func(r *ContractRequest) { r.Provider = "a" }},
		{name: "zero amount", mute: func(r *ContractRequest) { r.AmountMill = 0 }},
		{name: "past expiry", mute: func(r *ContractRequest) { r.ExpiresAt = now.Add(-time.Minute) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mute(&req)
			if _, err := FundContract(req); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("error = %v, want ErrInvalidContract", err)
			}
		})
	}
}

func TestEscrowMachineAcceptRelease(t *testing.T) {
	funded := fundedContract(t)
	if _, err := funded.Release(); !errors.Is(err, ErrContractState) {
		t.Fatalf("release before acceptance = %v, want ErrContractState: no unilateral release", err)
	}
	if _, err := funded.Accept("provider-b"); !errors.Is(err, ErrUnauthorizedRelease) {
		t.Fatalf("provider self-acceptance = %v, want ErrUnauthorizedRelease", err)
	}
	accepted, err := funded.Accept("buyer-a")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.Status != ContractAccepted {
		t.Fatalf("status = %q, want accepted", accepted.Status)
	}
	if _, err := accepted.Cancel(); !errors.Is(err, ErrContractState) {
		t.Fatalf("cancel after acceptance = %v, want ErrContractState", err)
	}
	released, err := accepted.Release()
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !released.Status.Terminal() || released.Status != ContractReleased {
		t.Fatalf("status = %q, want terminal released", released.Status)
	}
	if _, err := released.Release(); !errors.Is(err, ErrContractState) {
		t.Fatalf("second release = %v, want ErrContractState: never twice", err)
	}
}

func TestEscrowMachineCancelRefund(t *testing.T) {
	funded := fundedContract(t)
	refunded, err := funded.Cancel()
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !refunded.Status.Terminal() || refunded.Status != ContractRefunded {
		t.Fatalf("status = %q, want terminal refunded", refunded.Status)
	}
}

func TestEscrowExpiryNeverDelivers(t *testing.T) {
	funded := fundedContract(t)
	early := funded.ExpiresAt.Add(-time.Minute)
	if _, err := funded.Expire(early); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("early expiry = %v, want ErrInvalidContract", err)
	}
	lapsed := funded.ExpiresAt.Add(time.Minute)
	expired, err := funded.Expire(lapsed)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if expired.Status.Terminal() {
		t.Fatal("expired contracts wait for a decision, never read terminal")
	}
	if _, err := expired.Release(); !errors.Is(err, ErrContractState) {
		t.Fatalf("release after expiry = %v, want ErrContractState", err)
	}
	resolved, err := expired.Resolve(ResolveRelease)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !resolved.Status.Terminal() || resolved.Status != ContractResolved {
		t.Fatalf("status = %q, want terminal resolved", resolved.Status)
	}
	if _, err := expired.Resolve("burn"); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("bad decision = %v, want ErrInvalidContract", err)
	}
	if _, err := funded.Resolve(ResolveRefund); !errors.Is(err, ErrContractState) {
		t.Fatalf("resolve before expiry = %v, want ErrContractState", err)
	}
}
