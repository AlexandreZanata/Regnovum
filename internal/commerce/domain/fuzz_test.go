package domain

import (
	"testing"
	"time"
)

// FuzzTitheSplitExact proves the settlement split never panics on any
// int64, refuses non-positive amounts and always prices the same
// floor: tithe == amount/10 with truncation and tithe+net == amount.
// The oracle is integer division itself.
func FuzzTitheSplitExact(f *testing.F) {
	seeds := []int64{-1, 0, 1, 9, 10, 11, 19, 20, 100, 1000, 20000, 9223372036854775807}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, amount int64) {
		tithe, net, err := SplitTithe(amount)
		if amount <= 0 {
			if err == nil {
				t.Fatalf("SplitTithe(%d) accepted a non-positive amount", amount)
			}
			return
		}
		if err != nil {
			t.Fatalf("SplitTithe(%d): %v", amount, err)
		}
		if tithe != amount/10 {
			t.Fatalf("SplitTithe(%d) tithe = %d, want floor %d", amount, tithe, amount/10)
		}
		if tithe+net != amount {
			t.Fatalf("SplitTithe(%d) outputs %d+%d != amount", amount, tithe, net)
		}
		again, netAgain, err := SplitTithe(amount)
		if err != nil || again != tithe || netAgain != net {
			t.Fatalf("SplitTithe(%d) unstable: (%d,%d) vs (%d,%d)", amount, tithe, net, again, netAgain)
		}
		retithe, reprovider, err := SplitServiceRefund(amount)
		if err != nil {
			t.Fatalf("SplitServiceRefund(%d): %v", amount, err)
		}
		if retithe != tithe || reprovider != net {
			t.Fatalf("refund split (%d,%d) diverges from settlement (%d,%d) for %d",
				retithe, reprovider, tithe, net, amount)
		}
	})
}

// FuzzIntentionSealTamper proves the transfer seal binds every term:
// flipping any canonical field refuses verification.
func FuzzIntentionSealTamper(f *testing.F) {
	f.Add("intent-01", "gift", "payer-a", "payee-b", int64(20000))
	f.Fuzz(func(t *testing.T, key, kind, payer, payee string, amount int64) {
		parsed, err := ParseTransferKind(kind)
		if err != nil {
			return
		}
		intention, err := AcceptIntention(IntentionRequest{
			Key: key, Kind: parsed, Payer: payer, Payee: payee, AmountMilli: amount,
		})
		if err != nil {
			return
		}
		if err := intention.VerifyHash(); err != nil {
			t.Fatalf("fresh seal rejected: %v", err)
		}
		tampered := intention
		tampered.AmountMilli++
		if tampered.VerifyHash() == nil {
			t.Fatal("incremented amount kept the seal")
		}
		tampered = intention
		tampered.Payer += "x"
		if tampered.VerifyHash() == nil {
			t.Fatal("extended payer kept the seal")
		}
		tampered = intention
		tampered.Kind = TransferTrade
		if tampered.Kind == intention.Kind {
			return
		}
		if tampered.VerifyHash() == nil {
			t.Fatal("reclassified kind kept the seal")
		}
	})
}

// FuzzContractSealTamper proves the escrow seal binds every term:
// flipping object, parties, amount or expiry refuses verification.
func FuzzContractSealTamper(f *testing.F) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	f.Add("contract-01", "serviço com recibo", "buyer-a", "provider-b", int64(20000))
	f.Fuzz(func(t *testing.T, key, object, buyer, provider string, amount int64) {
		contract, err := FundContract(ContractRequest{
			Key: key, Object: object, Buyer: buyer, Provider: provider,
			AmountMill: amount, ExpiresAt: now.Add(time.Hour), Now: now,
		})
		if err != nil {
			return
		}
		if err := contract.VerifyHash(); err != nil {
			t.Fatalf("fresh seal rejected: %v", err)
		}
		tampered := contract
		tampered.AmountMill++
		if tampered.VerifyHash() == nil {
			t.Fatal("incremented amount kept the seal")
		}
		tampered = contract
		tampered.Object += "x"
		if tampered.VerifyHash() == nil {
			t.Fatal("extended object kept the seal")
		}
	})
}
