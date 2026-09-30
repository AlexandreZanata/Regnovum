package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func disguiseEvidence(seed string) string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

func TestParseDisguiseReasonClosesVocabulary(t *testing.T) {
	for _, raw := range []string{"contract", "announcement", "delivery", "recurrence"} {
		if _, err := ParseDisguiseReason(raw); err != nil {
			t.Fatalf("ParseDisguiseReason(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", " ", "Contract", "CONTRACT", " contract", "contract ", "gift", "trade", "contract/announcement"} {
		if _, err := ParseDisguiseReason(raw); !errors.Is(err, ErrInvalidDisguise) {
			t.Fatalf("ParseDisguiseReason(%q) = nil, want ErrInvalidDisguise", raw)
		}
	}
	if len(AllDisguiseReasons()) != 4 {
		t.Fatalf("vocabulary = %d reasons, want the closed four", len(AllDisguiseReasons()))
	}
}

func TestFlagDisguiseSealsWithoutMoving(t *testing.T) {
	flag, err := FlagDisguise(FlagRequest{
		ReviewKey: "review-01", TransferKey: "gift-01",
		Payer: "payer-a", Payee: "payee-b", AmountMill: 20000,
		Reason: DisguiseContract, EvidenceHash: disguiseEvidence("a"), Reporter: "watcher",
	})
	if err != nil {
		t.Fatalf("FlagDisguise: %v", err)
	}
	if flag.Status != DisguiseFlagged {
		t.Fatalf("status = %q, want flagged", flag.Status)
	}
	if err := flag.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
	mutated := flag
	mutated.Reason = DisguiseDelivery
	if err := mutated.VerifyHash(); !errors.Is(err, ErrInvalidDisguise) {
		t.Fatalf("reclassified seal = %v, want ErrInvalidDisguise (seal breaks first)", err)
	}
	cases := []struct {
		name string
		mute func(*FlagRequest)
	}{
		{"empty review key", func(r *FlagRequest) { r.ReviewKey = "" }},
		{"blank reporter", func(r *FlagRequest) { r.Reporter = " " }},
		{"self transfer", func(r *FlagRequest) { r.Payee = r.Payer }},
		{"zero amount", func(r *FlagRequest) { r.AmountMill = 0 }},
		{"unknown reason", func(r *FlagRequest) { r.Reason = DisguiseReason("gift") }},
		{"short hash", func(r *FlagRequest) { r.EvidenceHash = "abc" }},
		{"upper hash", func(r *FlagRequest) { r.EvidenceHash = strings.ToUpper(disguiseEvidence("a")) }},
		{"empty transfer key", func(r *FlagRequest) { r.TransferKey = "" }},
	}
	for _, tc := range cases {
		req := FlagRequest{
			ReviewKey: "review-02", TransferKey: "gift-02",
			Payer: "payer-a", Payee: "payee-b", AmountMill: 1000,
			Reason: DisguiseRecurrence, EvidenceHash: disguiseEvidence("a"), Reporter: "watcher",
		}
		tc.mute(&req)
		if _, err := FlagDisguise(req); !errors.Is(err, ErrInvalidDisguise) {
			t.Fatalf("%s: error = %v, want ErrInvalidDisguise", tc.name, err)
		}
	}
}

func TestDisguiseContestNeedsPartyAndFlagged(t *testing.T) {
	flag, err := FlagDisguise(FlagRequest{
		ReviewKey: "review-10", TransferKey: "gift-10",
		Payer: "payer-a", Payee: "payee-b", AmountMill: 5000,
		Reason: DisguiseAnnouncement, EvidenceHash: disguiseEvidence("a"), Reporter: "watcher",
	})
	if err != nil {
		t.Fatalf("FlagDisguise: %v", err)
	}
	for _, by := range []string{"payer-a", "payee-b"} {
		contested, err := flag.Contest(by)
		if err != nil {
			t.Fatalf("Contest(%q): %v", by, err)
		}
		if contested.Status != DisguiseContested {
			t.Fatalf("Contest(%q) status = %q, want contested", by, contested.Status)
		}
	}
	if _, err := flag.Contest("stranger"); !errors.Is(err, ErrDisguiseNotParty) {
		t.Fatalf("stranger contest = %v, want ErrDisguiseNotParty", err)
	}
	contested, err := flag.Contest("payer-a")
	if err != nil {
		t.Fatalf("Contest: %v", err)
	}
	if _, err := contested.Contest("payee-b"); !errors.Is(err, ErrDisguiseState) {
		t.Fatalf("second contest = %v, want ErrDisguiseState", err)
	}
}

func TestDisguiseResolveDismissesAndConfirmsOnce(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	newFlag := func(key string) DisguiseFlag {
		t.Helper()
		flag, err := FlagDisguise(FlagRequest{
			ReviewKey: key, TransferKey: "gift-" + key,
			Payer: "payer-a", Payee: "payee-b", AmountMill: 20000,
			Reason: DisguiseDelivery, EvidenceHash: disguiseEvidence("a"), Reporter: "watcher",
		})
		if err != nil {
			t.Fatalf("FlagDisguise: %v", err)
		}
		return flag
	}
	dismissed, err := newFlag("resolve-01").Resolve(ResolveRequest{
		Decision: DisguiseDismiss, Now: now, AppealUntil: now.Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if dismissed.Status != DisguiseDismissed {
		t.Fatalf("dismiss status = %q, want dismissed", dismissed.Status)
	}
	if _, err := dismissed.Resolve(ResolveRequest{
		Decision: DisguiseDismiss, Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); !errors.Is(err, ErrDisguiseState) {
		t.Fatalf("second resolve = %v, want ErrDisguiseState (terminal never reopens)", err)
	}
	contested, err := newFlag("resolve-02").Contest("payee-b")
	if err != nil {
		t.Fatalf("contest: %v", err)
	}
	confirmed, err := contested.Resolve(ResolveRequest{
		Decision: DisguiseConfirm, Basis: "contrato de serviço nº 7",
		TrailHash: disguiseEvidence("b"), Now: now, AppealUntil: now.Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatalf("confirm after contest: %v", err)
	}
	if confirmed.Status != DisguiseConfirmed {
		t.Fatalf("confirm status = %q, want confirmed", confirmed.Status)
	}
	if _, err := newFlag("resolve-03").Resolve(ResolveRequest{
		Decision: DisguiseConfirm, Basis: "", TrailHash: disguiseEvidence("b"),
		Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); !errors.Is(err, ErrInvalidDisguise) {
		t.Fatalf("confirm without basis = %v, want ErrInvalidDisguise", err)
	}
	if _, err := newFlag("resolve-04").Resolve(ResolveRequest{
		Decision: DisguiseConfirm, Basis: "contrato", TrailHash: "short",
		Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); !errors.Is(err, ErrInvalidDisguise) {
		t.Fatalf("confirm with short trail = %v, want ErrInvalidDisguise", err)
	}
	if _, err := newFlag("resolve-05").Resolve(ResolveRequest{
		Decision: DisguiseDismiss, Basis: "extra", Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); !errors.Is(err, ErrInvalidDisguise) {
		t.Fatalf("dismiss with basis = %v, want ErrInvalidDisguise (false positive carries no act)", err)
	}
	if _, err := newFlag("resolve-06").Resolve(ResolveRequest{
		Decision: DisguiseDismiss, Now: now, AppealUntil: now,
	}); !errors.Is(err, ErrInvalidDisguise) {
		t.Fatalf("dismiss without future appeal = %v, want ErrInvalidDisguise (right to resource)", err)
	}
}

func TestDisguiseChargeIsFloorTenth(t *testing.T) {
	cases := []struct{ amount, charge int64 }{
		{1, 0}, {9, 0}, {10, 1}, {11, 1}, {19, 1}, {20, 2}, {1000, 100}, {20000, 2000},
	}
	for _, tc := range cases {
		charge, err := DisguiseCharge(tc.amount)
		if err != nil {
			t.Fatalf("DisguiseCharge(%d): %v", tc.amount, err)
		}
		if charge != tc.charge {
			t.Fatalf("DisguiseCharge(%d) = %d, want %d", tc.amount, charge, tc.charge)
		}
	}
	for _, amount := range []int64{0, -1, -100} {
		if _, err := DisguiseCharge(amount); !errors.Is(err, ErrInvalidDisguise) {
			t.Fatalf("DisguiseCharge(%d) = nil, want ErrInvalidDisguise", amount)
		}
	}
}
